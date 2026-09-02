package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func viewPost(t *testing.T, handler http.Handler, target, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	request.Host = "127.0.0.1:7788"
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("X-Agentrec-Token", token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// A comparison launched from the viewer is the shadow run command as a
// child: off unless the viewer was started with --allow-run, validated
// before anything runs, one at a time, its output readable as it comes, and
// the runs it recorded named when it ends.
func TestViewRunsAComparisonOnlyWhenAllowed(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	bin := stubProviders(t, "claude", "codex", verifyHelperName, agentrecName)
	// A shadow run verifies both legs against the committed configuration.
	commitVerifyConfig(t, repo, verifyHelperName, "pass")
	restore := sessionExecutable
	t.Cleanup(func() { sessionExecutable = restore })
	sessionExecutable = func() (string, error) { return filepath.Join(bin, agentrecName), nil }
	body := `{"cwd":` + strconvQuote(repo) + `,"task":"change the README\n","runners":["claude","codex"]}`

	off := newViewHandler(root, "latest", false)
	t.Cleanup(func() { off.Close() })
	var overview shadowOverview
	viewJSONRequest(t, off, "/api/shadow", &overview)
	if overview.AllowRun || len(overview.Runners) != 2 || !overview.Runners[0].Available || !overview.Runners[1].Available {
		t.Errorf("overview without --allow-run = %+v", overview)
	}
	if res := viewPost(t, off, "/api/shadow/jobs", viewToken(t, off), body); res.Code != http.StatusForbidden || !strings.Contains(res.Body.String(), "--allow-run") {
		t.Errorf("POST without --allow-run = %d %s, want 403 naming the flag", res.Code, res.Body.String())
	}

	on := newViewHandler(root, "latest", true)
	t.Cleanup(func() { on.Close() })
	token := viewToken(t, on)
	if res := viewPost(t, on, "/api/shadow/jobs", "", body); res.Code != http.StatusForbidden {
		t.Errorf("POST without a token = %d, want 403", res.Code)
	}
	for _, bad := range []string{
		`{"cwd":"relative/path","task":"x","runners":["claude"]}`,
		`{"cwd":` + strconvQuote(repo) + `,"task":"","runners":["claude"]}`,
		`{"cwd":` + strconvQuote(repo) + `,"task":"x","runners":["gemini"]}`,
		`{"cwd":` + strconvQuote(repo) + `,"task":"x","runners":["claude","claude"]}`,
		`{"cwd":` + strconvQuote(repo) + `,"task":"x","runners":[]}`,
		`not json`,
	} {
		if res := viewPost(t, on, "/api/shadow/jobs", token, bad); res.Code != http.StatusBadRequest {
			t.Errorf("POST %s = %d, want 400", bad, res.Code)
		}
	}

	res := viewPost(t, on, "/api/shadow/jobs", token, body)
	if res.Code != http.StatusAccepted {
		t.Fatalf("POST = %d %s, want 202", res.Code, res.Body.String())
	}
	var started struct{ ID string }
	if err := json.Unmarshal(res.Body.Bytes(), &started); err != nil || started.ID == "" {
		t.Fatalf("job id: %v (%s)", err, res.Body.String())
	}
	var job shadowJobView
	deadline := time.Now().Add(90 * time.Second)
	var log strings.Builder
	offset := int64(0)
	for {
		viewJSONRequest(t, on, "/api/shadow/jobs/"+started.ID+"?since="+itoa64(offset), &job)
		log.WriteString(job.Chunk)
		offset = job.Offset
		if job.Status != "running" || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if job.Status != "completed" || job.ExitCode == nil || *job.ExitCode != 0 {
		t.Fatalf("job = %+v\nlog:\n%s", job, log.String())
	}
	if !strings.Contains(log.String(), "claude") || !strings.Contains(log.String(), "codex") {
		t.Errorf("comparison output does not name both runners:\n%s", log.String())
	}
	if len(job.RunIDs) != 2 {
		t.Errorf("runIds = %v, want the two recorded runs", job.RunIDs)
	}
	if got := viewRunIDs(t, on); len(got) != 2 {
		t.Errorf("runs after the comparison = %v, want 2", got)
	}
	if res := viewMutate(t, on, http.MethodPost, "/api/shadow/jobs/"+started.ID+"/cancel", token, nil); res.Code != http.StatusConflict {
		t.Errorf("cancel of a finished job = %d, want 409", res.Code)
	}
	if res := viewMutate(t, on, http.MethodPost, "/api/shadow/jobs/nope/cancel", token, nil); res.Code != http.StatusNotFound {
		t.Errorf("cancel of an unknown job = %d, want 404", res.Code)
	}
	viewJSONRequest(t, on, "/api/shadow", &overview)
	if !overview.AllowRun || len(overview.Jobs) != 1 || overview.Jobs[0].ID != started.ID || overview.Jobs[0].Chunk != "" {
		t.Errorf("overview after the job = %+v", overview)
	}
}

// A second comparison is refused while one runs; the output kept in memory
// is bounded and says when it was cut.
func TestShadowJobsRefuseASecondWhileOneRunsAndBoundTheirOutput(t *testing.T) {
	root := home(t)
	jobs := newShadowJobs(root, true)
	jobs.running = &shadowJob{info: shadowJobInfo{ID: "busy"}}
	if _, err := jobs.start(shadowJobRequest{CWD: t.TempDir(), Task: "x", Runners: []string{"claude", "codex"}}); err != errShadowBusy {
		t.Errorf("start while busy = %v, want errShadowBusy", err)
	}
	job := &shadowJob{info: shadowJobInfo{ID: "big"}}
	job.read(strings.NewReader(strings.Repeat("line of output\n", shadowJobOutputCap/10)))
	view := job.view(0)
	if !view.Truncated || len(view.Chunk) > shadowJobOutputCap || view.Offset != int64(len("line of output\n")*(shadowJobOutputCap/10)) {
		t.Errorf("bounded output: truncated %v, chunk %d bytes, offset %d", view.Truncated, len(view.Chunk), view.Offset)
	}
	if tail := job.view(view.Offset - 5); tail.Chunk != "tput\n" || tail.Truncated {
		t.Errorf("tail from an offset = %q (truncated %v)", tail.Chunk, tail.Truncated)
	}
}

func TestViewAndStartAcceptAllowRun(t *testing.T) {
	if _, _, _, allowRun, ok := parseViewArgs([]string{"--allow-run", "--no-open"}); !ok || !allowRun {
		t.Errorf("--allow-run was not accepted (%v %v)", allowRun, ok)
	}
	if _, _, _, _, ok := parseViewArgs([]string{"--allow-run", "--allow-run"}); ok {
		t.Error("--allow-run twice was accepted")
	}
}

func strconvQuote(s string) string { return strconv.Quote(s) }

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// A character still arriving is held back rather than sent as two halves.
func TestShadowJobViewNeverSplitsACharacter(t *testing.T) {
	job := &shadowJob{info: shadowJobInfo{ID: "utf8"}}
	job.read(strings.NewReader("ab\xe2\x82"))
	first := job.view(0)
	if first.Chunk != "ab" || first.Offset != 2 {
		t.Errorf("partial character: chunk %q offset %d, want \"ab\" and 2", first.Chunk, first.Offset)
	}
	job.read(strings.NewReader("\xac!"))
	if next := job.view(first.Offset); next.Chunk != "€!" || next.Offset != 6 {
		t.Errorf("completed character: chunk %q offset %d, want \"€!\" and 6", next.Chunk, next.Offset)
	}
}

// After Close nothing starts, and a job that ended cleanly is completed even
// if a cancel was asked for on its way out.
func TestShadowJobsRefuseToStartAfterClose(t *testing.T) {
	jobs := newShadowJobs(home(t), true)
	jobs.Close()
	if _, err := jobs.start(shadowJobRequest{CWD: t.TempDir(), Task: "x", Runners: []string{"claude", "codex"}}); err != errShadowClosing {
		t.Errorf("start after Close = %v, want errShadowClosing", err)
	}
}

func waitForPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no provider pid appeared in %s", path)
	return 0
}

// A comparison that is running can be cancelled — only from the page — and
// ends with its provider; runs that were there before it, or that others
// record meanwhile, are not attributed to it; the task file is private; and
// a viewer that closes takes its running comparison with it.
func TestViewCancelsARunningComparisonAndCloseEndsIt(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	bin := stubProviders(t, "claude", "codex", verifyHelperName, agentrecName)
	commitVerifyConfig(t, repo, verifyHelperName, "pass")
	restore := sessionExecutable
	t.Cleanup(func() { sessionExecutable = restore })
	sessionExecutable = func() (string, error) { return filepath.Join(bin, agentrecName), nil }
	writeRun(t, root, "run-before", "claude", time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC), "completed")
	pidFile := filepath.Join(t.TempDir(), "linger.pid")
	t.Setenv(lingerEnv, pidFile)

	handler := newViewHandler(root, "latest", true)
	t.Cleanup(func() { handler.Close() })
	token := viewToken(t, handler)
	body := `{"cwd":` + strconvQuote(repo) + `,"task":"change the README\n","runners":["claude","codex"]}`
	if res := viewPost(t, handler, "/api/shadow/jobs", token, `{"cwd":`+strconvQuote(repo)+`,"task":"`+strings.Repeat("x", shadowTaskLimit+1)+`","runners":["claude","codex"]}`); res.Code != http.StatusBadRequest {
		t.Errorf("oversized task = %d, want 400", res.Code)
	}
	if res := viewPost(t, handler, "/api/shadow/jobs", token, `{"cwd":`+strconvQuote(repo)+`,"task":"x","runners":["claude"]}`); res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "both runners") {
		t.Errorf("one runner = %d %s, want 400 asking for both", res.Code, res.Body.String())
	}
	res := viewPost(t, handler, "/api/shadow/jobs", token, body)
	if res.Code != http.StatusAccepted {
		t.Fatalf("POST = %d %s", res.Code, res.Body.String())
	}
	var started struct{ ID string }
	if err := json.Unmarshal(res.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	pid := waitForPID(t, pidFile)

	taskPath := filepath.Join(filepath.Dir(root), shadowTaskDirName, started.ID+".md")
	if info, err := os.Stat(taskPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("task file %s: %v, mode %v; want a private file", taskPath, err, info)
	}
	var job shadowJobView
	viewJSONRequest(t, handler, "/api/shadow/jobs/"+started.ID+"?since=0", &job)
	if job.Status != "running" || slices.Contains(job.RunIDs, "run-before") {
		t.Errorf("running job = status %q runIds %v; want running without the pre-existing run", job.Status, job.RunIDs)
	}
	for _, target := range []string{"/api/shadow/jobs/" + started.ID + "?since=-1", "/api/shadow/jobs/" + started.ID + "?since=abc"} {
		if res := viewMutate(t, handler, http.MethodGet, target, "", nil); res.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", target, res.Code)
		}
	}
	cancelTarget := "/api/shadow/jobs/" + started.ID + "/cancel"
	if res := viewMutate(t, handler, http.MethodPost, cancelTarget, "", nil); res.Code != http.StatusForbidden {
		t.Errorf("cancel without a token = %d, want 403", res.Code)
	}
	if res := viewMutate(t, handler, http.MethodPost, cancelTarget, token, map[string]string{"Sec-Fetch-Site": "cross-site"}); res.Code != http.StatusForbidden {
		t.Errorf("cross-site cancel = %d, want 403", res.Code)
	}
	if !processAlive(pid) {
		t.Fatalf("provider %d is not running before the cancel", pid)
	}
	if res := viewMutate(t, handler, http.MethodPost, cancelTarget, token, nil); res.Code != http.StatusAccepted {
		t.Fatalf("cancel = %d %s, want 202", res.Code, res.Body.String())
	}
	deadline := time.Now().Add(30 * time.Second)
	for job.Status == "running" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		viewJSONRequest(t, handler, "/api/shadow/jobs/"+started.ID+"?since=0", &job)
	}
	if job.Status != "cancelled" && job.Status != "completed" {
		t.Errorf("job after cancel = %q, want it ended", job.Status)
	}
	if processAlive(pid) {
		t.Errorf("provider %d survived the cancel", pid)
	}
	if slices.Contains(job.RunIDs, "run-before") {
		t.Errorf("runIds %v attribute the pre-existing run", job.RunIDs)
	}
	if _, err := os.Stat(taskPath); !os.IsNotExist(err) {
		t.Errorf("task file kept after the job: %v", err)
	}

	// A second comparison, still running when the viewer closes, goes with it.
	pidFile2 := filepath.Join(t.TempDir(), "linger2.pid")
	t.Setenv(lingerEnv, pidFile2)
	if res := viewPost(t, handler, "/api/shadow/jobs", token, body); res.Code != http.StatusAccepted {
		t.Fatalf("second POST = %d %s", res.Code, res.Body.String())
	}
	pid2 := waitForPID(t, pidFile2)
	handler.Close()
	if processAlive(pid2) {
		t.Errorf("provider %d survived the viewer's close", pid2)
	}
}
