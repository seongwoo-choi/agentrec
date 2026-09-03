package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/evidence"
	"github.com/seongwoo-choi/agentrec/internal/storage"
)

// finishedRunIn records a run that ended two hours ago in repo, closed out.
func finishedRunIn(t *testing.T, root, id, repo string) string {
	t.Helper()
	started := time.Now().Add(-2 * time.Hour)
	b, err := storage.Create(root, id, storage.Manifest{Provider: "claude", Argv: []string{"claude"}, CWD: repo, RepoRoot: repo, StartedAt: started})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProcessResult(processResultJSON(t, started, "completed")); err != nil {
		t.Fatal(err)
	}
	if err := b.Finalize(storage.Finalization{EndedAt: started.Add(time.Minute), ExitReason: "completed"}); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, id)
}

func TestVerifyLaterFilesAResultBesideTheRunAndSaysWhenHeadMoved(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	commitVerifyConfig(t, repo, "sh", "-c", "echo verified-later")
	dir := finishedRunIn(t, root, "run-later", repo)

	result, meta, err := verifyRunLater(context.Background(), root, "run-later")
	if err != nil {
		t.Fatalf("verify later: %v", err)
	}
	if result.Status != "passed" || result.Attribution != posthocAttribution || len(result.Checks) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if meta.MeasuredAt.IsZero() || meta.HeadNow == "" || meta.HeadMovedSince != nil {
		t.Fatalf("meta = %+v, want a time, a head, and no verdict on movement without a baseline", meta)
	}
	for _, name := range []string{verifyResults, verifyPosthocMeta} {
		if _, err := os.Stat(filepath.Join(dir, verifyPosthocDir, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, verifyDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the run-end verification directory appeared: %v", err)
	}

	code, stdout, stderr := run(t, "show", "run-later")
	if code != 0 {
		t.Fatalf("show exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Verified later") || !strings.Contains(stdout, "PASS at "+meta.MeasuredAt.Format(time.RFC3339)) || !strings.Contains(stdout, "not the tree the run left behind") || !strings.Contains(stdout, posthocAttribution) {
		t.Fatalf("show said:\n%s", stdout)
	}
	// A run never verified at its end says so, so that the later verdict
	// standing alone in the section is not read as the run's own.
	if !strings.Contains(stdout, verificationNotRun+" (this run was not verified when it ended)") {
		t.Fatalf("show does not state the run's own verification:\n%s", stdout)
	}

	// A baseline lets the later run say whether HEAD moved; a second later
	// verification replaces the first.
	gitIn(t, repo, "commit", "--allow-empty", "-m", "moved on")
	if err := os.MkdirAll(filepath.Join(dir, gitDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, gitDir, resultFile), []byte(`{"status":"unavailable","reason":"a fixture baseline","attribution":"`+evidence.Attribution+`","baseline":"0000000000000000000000000000000000000000"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, meta, err = verifyRunLater(context.Background(), root, "run-later"); err != nil {
		t.Fatal(err)
	}
	if meta.HeadMovedSince == nil || !*meta.HeadMovedSince || meta.HeadAtRun == "" {
		t.Fatalf("meta = %+v, want HEAD reported as moved", meta)
	}
	code, stdout, _ = run(t, "show", "run-later")
	if code != 0 || strings.Count(stdout, "Verified later") != 1 || !strings.Contains(stdout, "HEAD has moved since the run") {
		t.Fatalf("show after the second later verification:\n%s", stdout)
	}

	code, stdout, stderr = run(t, "verify", "latest")
	if code != 0 || !strings.Contains(stdout, "run-later verified later at ") || !strings.Contains(stdout, ": PASS") || !strings.Contains(stdout, "note: measured later") {
		t.Fatalf("verify exited %d:\n%s\n%s", code, stdout, stderr)
	}
}

func TestVerifyLaterReportsAFailedVerdictAndKeepsTheEarlierResult(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	commitVerifyConfig(t, repo, "sh", "-c", "echo first-run")
	dir := finishedRunIn(t, root, "run-keep", repo)
	if _, _, err := verifyRunLater(context.Background(), root, "run-keep"); err != nil {
		t.Fatal(err)
	}
	first := mustReadFile(t, filepath.Join(dir, verifyPosthocDir, verifyResults))

	// A re-run that cannot even be pinned leaves the measurement already
	// filed exactly as it was: a failed attempt destroys no evidence.
	writeVerifyConfig(t, repo, "version: 9\nverify: []\n")
	gitIn(t, repo, "commit", "-am", "an unreadable config")
	if _, _, err := verifyRunLater(context.Background(), root, "run-keep"); err == nil {
		t.Fatal("an unreadable config was accepted")
	}
	if got := mustReadFile(t, filepath.Join(dir, verifyPosthocDir, verifyResults)); !bytes.Equal(got, first) {
		t.Fatalf("the earlier later verification changed:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, verifyPosthocPending)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unfinished later verification was left behind: %v", err)
	}

	// A verification that ran and failed is filed, and reaches the shell as
	// a failure the way a traced run's does.
	commitVerifyConfig(t, repo, "sh", "-c", "exit 7")
	code, stdout, stderr := run(t, "verify", "run-keep")
	if code != exitFailure {
		t.Fatalf("verify exited %d on a failed verification (stdout %q, stderr %q)", code, stdout, stderr)
	}
	if !strings.Contains(stdout, ": FAIL") {
		t.Fatalf("verify said:\n%s", stdout)
	}
	if got := mustReadFile(t, filepath.Join(dir, verifyPosthocDir, verifyResults)); bytes.Equal(got, first) {
		t.Fatal("the failed verification did not replace the earlier one")
	}
}

func TestVerifyLaterRefusesAConfigThatDiffersFromHeadEvenWhenGitIsToldToIgnoreIt(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	commitVerifyConfig(t, repo, "sh", "-c", "exit 0")
	finishedRunIn(t, root, "run-swapped", repo)

	// An index entry marked assume-unchanged makes git report a clean tree
	// whatever the file on disk now says, so what would be executed is
	// compared with the bytes HEAD holds instead of with git's opinion.
	gitIn(t, repo, "update-index", "--assume-unchanged", verifyConfigFile)
	marker := filepath.Join(t.TempDir(), "ran")
	writeVerifyConfig(t, repo, "version: 1\nverify:\n  - name: \"swapped\"\n    timeout: \"30s\"\n    command:\n      - \"sh\"\n      - \"-c\"\n      - "+strconv.Quote("touch "+marker)+"\n")
	if out := gitIn(t, repo, "status", "--porcelain"); strings.TrimSpace(out) != "" {
		t.Fatalf("git still reports the swap, so this test proves nothing: %q", out)
	}
	_, _, err := verifyRunLater(context.Background(), root, "run-swapped")
	if !errors.Is(err, errVerifyNoConfig) || !strings.Contains(err.Error(), "differs from HEAD") {
		t.Fatalf("the swapped config was accepted: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the swapped config ran")
	}
}

func TestPosthocEvidenceStaysInItsOwnLayerAndIsRedacted(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	const secret = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	commitVerifyConfig(t, repo, "sh", "-c", "echo "+secret)
	dir := finishedRunIn(t, root, "run-layers", repo)

	result, _, err := verifyRunLater(context.Background(), root, "run-layers")
	if err != nil {
		t.Fatal(err)
	}
	// What a check printed goes through a redactor before it is filed: the
	// later measurement is evidence like any other in the bundle.
	filed := mustReadFile(t, filepath.Join(dir, verifyPosthocDir, verifyResults))
	if bytes.Contains(filed, []byte(secret)) {
		t.Fatalf("the later verification filed a secret verbatim:\n%s", filed)
	}
	if len(result.Checks) != 1 || strings.Contains(result.Checks[0].Stdout, secret) {
		t.Fatalf("the returned document carries the secret: %+v", result.Checks)
	}
	if !bytes.Contains(filed, []byte("REDACTED")) {
		t.Fatalf("nothing marks what was taken out:\n%s", filed)
	}

	// The two verification layers are told apart by what each document
	// claims, not by where it sits: a run-end result dropped into the later
	// directory is refused rather than read as a later measurement.
	swapped := bytes.Replace(filed, []byte(posthocAttribution), []byte(evidence.VerificationAttribution), 1)
	if bytes.Equal(swapped, filed) {
		t.Fatal("the filed document does not carry the later attribution")
	}
	if err := os.WriteFile(filepath.Join(dir, verifyPosthocDir, verifyResults), swapped, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readPosthocVerification(dir); err == nil {
		t.Fatal("a run-end document was read as a later measurement")
	}
	code, _, stderr := run(t, "show", "run-layers")
	if code == 0 || !strings.Contains(stderr, "want the recorded attribution") {
		t.Fatalf("show exited %d on a mislabelled document: %s", code, stderr)
	}
	handler := newViewHandler(root, "", false)
	defer handler.Close()
	rec := viewMutate(t, handler, http.MethodGet, "/api/runs/run-layers", "", nil)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "want the recorded attribution") {
		t.Fatalf("the viewer served a mislabelled document: %d %s", rec.Code, rec.Body.String())
	}
}

func TestViewRefusesASecondVerificationWhileOneRuns(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	barrier := filepath.Join(t.TempDir(), "started")
	commitVerifyConfig(t, repo, "sh", "-c", "touch "+barrier+"; sleep 2")
	finishedRunIn(t, root, "run-slow", repo)

	handler := newViewHandler(root, "", true)
	defer handler.Close()
	token := viewToken(t, handler)
	done := make(chan int, 1)
	go func() {
		done <- viewMutate(t, handler, http.MethodPost, "/api/runs/run-slow/verify", token, nil).Code
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(barrier); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first verification never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	rec := viewMutate(t, handler, http.MethodPost, "/api/runs/run-slow/verify", token, nil)
	first := <-done
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "another verification is running") {
		t.Fatalf("a second verification was accepted while the first ended %d: %d %s", first, rec.Code, rec.Body.String())
	}
	if first != http.StatusOK {
		t.Fatalf("the first verification ended %d", first)
	}
}

func TestVerifyLaterAsksGitWithoutLettingTheRepositoryRunAnything(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	finishedRunIn(t, root, "run-config", repo)

	// core.fsmonitor names a program git runs whenever it refreshes the
	// index. It lives in .git/config, which is never committed and is not
	// what the tracked-and-matches-HEAD check covers, so the questions
	// asked before the pinned checks must not refresh the index at all.
	marker := filepath.Join(t.TempDir(), "fsmonitor-ran")
	script := filepath.Join(t.TempDir(), "fsmonitor.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "config", "core.fsmonitor", script)

	// Refused for want of a config, and accepted with one: neither answer
	// may come from running the repository's program.
	if _, _, err := verifyRunLater(context.Background(), root, "run-config"); !errors.Is(err, errVerifyNoConfig) {
		t.Fatalf("without a config: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the repository's fsmonitor program ran on the refused path")
	}
	commitVerifyConfig(t, repo, "sh", "-c", "exit 0")
	// The commit above is this test's own git usage, not agentrec's.
	if err := os.RemoveAll(marker); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifyRunLater(context.Background(), root, "run-config"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the repository's fsmonitor program ran while the config was being read")
	}
}

func TestStatusReportsTheViewerCache(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-cached", "claude", time.Now().Add(-2*time.Hour), "completed")
	handler := newViewHandler(root, "", false)
	defer handler.Close()
	viewJSONRequest(t, handler, "/api/runs/run-cached", &struct{}{})

	var stdout, stderr bytes.Buffer
	if code := runStatus(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("status exited %d: %s", code, stderr.String())
	}
	cache := filepath.Join(filepath.Dir(root), viewCacheDirName)
	size := storeBytes(cache)
	if size <= 0 {
		t.Fatalf("the viewer cached nothing: %d bytes under %s", size, cache)
	}
	if out := stdout.String(); !strings.Contains(out, "cache     "+humanBytes(size)+" in "+cache) {
		t.Fatalf("status does not report the viewer's copies:\n%s", out)
	}
}

func TestVerifyLaterRefusesOpenRunsAndRepositoriesWithoutAConfig(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	finishedRunIn(t, root, "run-noconfig", repo)
	if _, _, err := verifyRunLater(context.Background(), root, "run-noconfig"); !errors.Is(err, errVerifyNoConfig) {
		t.Fatalf("without a config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, verifyConfigFile), []byte("version: 1\nverify: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifyRunLater(context.Background(), root, "run-noconfig"); !errors.Is(err, errVerifyNoConfig) {
		t.Fatalf("with an untracked config: %v", err)
	}
	commitVerifyConfig(t, repo, "sh", "-c", "exit 0")
	writeRun(t, root, "run-open", "claude", time.Now(), "")
	if _, _, err := verifyRunLater(context.Background(), root, "run-open"); !errors.Is(err, errRunClosing) && !errors.Is(err, errRunOpen) {
		t.Fatalf("open run: %v", err)
	}
	if _, _, err := verifyRunLater(context.Background(), root, "run-missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing run: %v", err)
	}
	if _, _, err := verifyRunLater(context.Background(), root, "../escape"); err == nil {
		t.Fatal("a bad run id was accepted")
	}
	code, _, stderr := run(t, "verify")
	if code != exitUsage || !strings.Contains(stderr, "usage: agentrec verify") {
		t.Fatalf("verify without a run exited %d: %s", code, stderr)
	}
}

func TestViewVerifiesARunLaterOnlyWhenAllowed(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	commitVerifyConfig(t, repo, "sh", "-c", "echo from-the-page")
	finishedRunIn(t, root, "run-page", repo)

	locked := newViewHandler(root, "", false)
	defer locked.Close()
	if rec := viewMutate(t, locked, http.MethodPost, "/api/runs/run-page/verify", viewToken(t, locked), nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "--allow-run") {
		t.Fatalf("without --allow-run: %d %s", rec.Code, rec.Body.String())
	}
	if rec := viewMutate(t, locked, http.MethodPost, "/api/runs/run-page/verify", "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("without a token: %d", rec.Code)
	}

	handler := newViewHandler(root, "", true)
	defer handler.Close()
	token := viewToken(t, handler)
	rec := viewMutate(t, handler, http.MethodPost, "/api/runs/run-page/verify", token, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"PASS"`) || !strings.Contains(rec.Body.String(), `"caveat":"measured later`) {
		t.Fatalf("verify from the page: %d %s", rec.Code, rec.Body.String())
	}
	var detail struct {
		Evidence struct {
			PosthocVerification *struct {
				OwnRan         bool        `json:"ownRan"`
				Status         string      `json:"status"`
				Caveat         string      `json:"caveat"`
				HeadMovedSince *bool       `json:"headMovedSince"`
				MeasuredAt     time.Time   `json:"measuredAt"`
				Fields         []viewField `json:"fields"`
			} `json:"posthocVerification"`
		} `json:"evidence"`
	}
	viewJSONRequest(t, handler, "/api/runs/run-page", &detail)
	later := detail.Evidence.PosthocVerification
	if later == nil || later.Status != "PASS" || later.MeasuredAt.IsZero() || later.HeadMovedSince != nil || len(later.Fields) == 0 {
		t.Fatalf("snapshot later verification = %+v", later)
	}
	if later.OwnRan {
		t.Fatal("the snapshot claims the run verified itself; it never did")
	}

	writeRun(t, root, "run-open", "claude", time.Now(), "")
	for _, bad := range []struct {
		id   string
		code int
	}{{"run-missing", http.StatusNotFound}, {"run-open", http.StatusConflict}} {
		if rec := viewMutate(t, handler, http.MethodPost, "/api/runs/"+bad.id+"/verify", token, nil); rec.Code != bad.code {
			t.Fatalf("%s: %d %s, want %d", bad.id, rec.Code, rec.Body.String(), bad.code)
		}
	}
	// A run with a baseline of its own says whether HEAD moved, all the way
	// through the snapshot the page reads.
	if err := os.MkdirAll(filepath.Join(root, "run-page", gitDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "run-page", gitDir, resultFile), []byte(`{"status":"unavailable","reason":"a fixture baseline","attribution":"`+evidence.Attribution+`","baseline":"0000000000000000000000000000000000000000"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := viewMutate(t, handler, http.MethodPost, "/api/runs/run-page/verify", token, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"headMovedSince":true`) {
		t.Fatalf("the page was not told HEAD moved: %d %s", rec.Code, rec.Body.String())
	}
	viewJSONRequest(t, handler, "/api/runs/run-page", &detail)
	if later = detail.Evidence.PosthocVerification; later == nil || later.HeadMovedSince == nil || !*later.HeadMovedSince {
		t.Fatalf("the snapshot does not carry the moved HEAD: %+v", later)
	}
	if !strings.Contains(later.Caveat, "HEAD has moved since the run") {
		t.Fatalf("caveat = %q", later.Caveat)
	}

	if err := os.Remove(filepath.Join(repo, verifyConfigFile)); err != nil {
		t.Fatal(err)
	}
	if rec := viewMutate(t, handler, http.MethodPost, "/api/runs/run-page/verify", token, nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("without a config: %d %s", rec.Code, rec.Body.String())
	}
}
