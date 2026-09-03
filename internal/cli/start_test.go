package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// start leaves a viewer running after it returns, records where it is, and
// stop ends that viewer and forgets it; status tells the two states apart.
func TestStartStopAndStatusManageABackgroundViewer(t *testing.T) {
	root := home(t)
	bin := stubProviders(t, agentrecName)
	restore := sessionExecutable
	t.Cleanup(func() { sessionExecutable = restore })
	sessionExecutable = func() (string, error) { return filepath.Join(bin, agentrecName), nil }
	stateDir := filepath.Join(filepath.Dir(root), viewerDirName)
	// Whatever the assertions find, the viewer must not outlive the test.
	t.Cleanup(func() { run(t, "stop") })

	code, stdout, stderr := run(t, "status")
	if code != 0 || !strings.Contains(stdout, "viewer    not running") || !strings.Contains(stdout, "runs      0 recorded") {
		t.Fatalf("status before start: exit %d\n%s%s", code, stdout, stderr)
	}

	code, stdout, stderr = run(t, "start", "--no-open", "--listen", "127.0.0.1:0", "--allow-run")
	if code != 0 {
		t.Fatalf("start exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	url := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(stdout, " (pid", 2)[0], "agentrec viewer: "))
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("start did not report a loopback URL: %q", stdout)
	}
	pidRaw, err := os.ReadFile(filepath.Join(stateDir, viewerPIDFile))
	if err != nil {
		t.Fatalf("pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if err != nil || !processAlive(pid) {
		t.Fatalf("pid file holds %q, want a live process", pidRaw)
	}
	// Outside tests the start process exits and init reaps the viewer; here
	// the test process is its parent, so it must reap it itself or a stopped
	// viewer would linger as a zombie that kill(0) still reports alive.
	go func() {
		var ws syscall.WaitStatus
		syscall.Wait4(pid, &ws, 0, nil)
	}()
	if urlRaw, err := os.ReadFile(filepath.Join(stateDir, viewerURLFile)); err != nil || strings.TrimSpace(string(urlRaw)) != url {
		t.Errorf("url file = %q, %v; want %q", urlRaw, err, url)
	}
	expectedExecutable, err := filepath.EvalSymlinks(filepath.Join(bin, agentrecName))
	if err != nil {
		t.Fatal(err)
	}
	if executableRaw, err := os.ReadFile(filepath.Join(stateDir, viewerExecutableFile)); err != nil || strings.TrimSpace(string(executableRaw)) != expectedExecutable {
		t.Errorf("executable file = %q, %v; want %q", executableRaw, err, expectedExecutable)
	}
	// An empty store is a state the viewer shows, not a reason to refuse.
	resp, err := http.Get(url + "api/runs")
	if err != nil {
		t.Fatalf("GET /api/runs: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/runs = %d, want 200", resp.StatusCode)
	}

	// A second start finds the first.
	code, stdout, _ = run(t, "start", "--no-open")
	if code != 0 || !strings.Contains(stdout, "already running: "+url) {
		t.Errorf("second start exit %d, stdout %q; want the running viewer named", code, stdout)
	}
	code, stdout, _ = run(t, "status")
	if code != 0 || !strings.Contains(stdout, "viewer    running at "+url+" (pid "+strconv.Itoa(pid)+", comparisons allowed)") || !strings.Contains(stdout, "hooks not installed (agentrec setup)") {
		t.Errorf("status while running: exit %d\n%s", code, stdout)
	}
	// The page can say so too.
	resp, err = http.Get(url + "api/shadow")
	if err != nil {
		t.Fatalf("GET /api/shadow: %v", err)
	}
	var overview struct{ AllowRun bool }
	if err := json.NewDecoder(resp.Body).Decode(&overview); err != nil || !overview.AllowRun {
		t.Errorf("viewer started with --allow-run reports allowRun=%v (%v)", overview.AllowRun, err)
	}
	resp.Body.Close()

	code, stdout, stderr = run(t, "stop")
	if code != 0 || !strings.Contains(stdout, "agentrec viewer stopped (pid "+strconv.Itoa(pid)+")") {
		t.Fatalf("stop exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if processAlive(pid) {
		t.Errorf("viewer pid %d is still alive after stop", pid)
	}
	for _, name := range []string{viewerPIDFile, viewerURLFile} {
		if _, err := os.Stat(filepath.Join(stateDir, name)); !os.IsNotExist(err) {
			t.Errorf("%s still present after stop: %v", name, err)
		}
	}
	if code, stdout, _ := run(t, "stop"); code != 0 || !strings.Contains(stdout, "not running") {
		t.Errorf("second stop exit %d, stdout %q", code, stdout)
	}
}

// A pid file left behind by a viewer that died is stale, not running.
func TestStatusForgetsAStaleViewerPID(t *testing.T) {
	root := home(t)
	stateDir := filepath.Join(filepath.Dir(root), viewerDirName)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A pid no process can have on any supported platform.
	if err := os.WriteFile(filepath.Join(stateDir, viewerPIDFile), []byte("2147483646\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := run(t, "status")
	if code != 0 || !strings.Contains(stdout, "viewer    not running") {
		t.Errorf("status with a stale pid: exit %d\n%s", code, stdout)
	}
	if _, err := os.Stat(filepath.Join(stateDir, viewerPIDFile)); !os.IsNotExist(err) {
		t.Errorf("stale pid file was kept: %v", err)
	}
}

// A live process at a stale viewer PID is not the viewer. The identity served
// at the recorded loopback URL must match before stop sends it any signal.
func TestReadViewerStatePreservesLiveLegacyViewerMetadata(t *testing.T) {
	root := home(t)
	stateDir := filepath.Join(filepath.Dir(root), viewerDirName)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		viewerPIDFile:  fmt.Sprintf("%d\n", os.Getpid()),
		viewerURLFile:  "http://127.0.0.1:43191\n",
		viewerModeFile: "allow-run\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(stateDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	_, running, err := readViewerState()
	if err == nil || !strings.Contains(err.Error(), "legacy viewer") {
		t.Fatalf("readViewerState error = %v, want legacy viewer recovery error", err)
	}
	if running {
		t.Fatal("legacy viewer state reported manageable")
	}
	for name := range files {
		if _, err := os.Stat(filepath.Join(stateDir, name)); err != nil {
			t.Errorf("legacy state %s was removed: %v", name, err)
		}
	}
}

func TestStopPreservesViewerStateWhenAuthenticatedStopFails(t *testing.T) {
	root := home(t)
	const token = "viewer-stop-retry-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(viewerIdentityHeader) != token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/viewer-identity":
			fmt.Fprintf(w, `{"pid":%d}`, os.Getpid())
		case r.Method == http.MethodPost && r.URL.Path == "/api/viewer-stop":
			http.Error(w, "temporary", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	stateDir := filepath.Join(filepath.Dir(root), viewerDirName)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		viewerPIDFile:   strconv.Itoa(os.Getpid()),
		viewerURLFile:   server.URL + "/",
		viewerTokenFile: token,
		viewerModeFile:  "read-only",
	} {
		if err := os.WriteFile(filepath.Join(stateDir, name), []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := runStop(nil, &stdout, &stderr); code != exitFailure {
		t.Fatalf("runStop exit = %d, want %d", code, exitFailure)
	}
	for _, name := range []string{viewerPIDFile, viewerURLFile, viewerTokenFile, viewerModeFile} {
		if _, err := os.Stat(filepath.Join(stateDir, name)); err != nil {
			t.Fatalf("state %s was removed after retryable stop failure: %v", name, err)
		}
	}
}

func TestReadViewerStatePreservesIdentityOnTransportFailure(t *testing.T) {
	root := home(t)
	stateDir := filepath.Join(filepath.Dir(root), viewerDirName)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + listener.Addr().String() + "/"
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		viewerPIDFile:   strconv.Itoa(os.Getpid()) + "\n",
		viewerURLFile:   url + "\n",
		viewerTokenFile: "expected-token\n",
	} {
		if err := os.WriteFile(filepath.Join(stateDir, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := readViewerState(); err == nil {
		t.Fatal("read viewer state accepted an unreachable identity endpoint")
	}
	for _, name := range []string{viewerPIDFile, viewerURLFile, viewerTokenFile} {
		if _, err := os.Stat(filepath.Join(stateDir, name)); err != nil {
			t.Fatalf("state file %s was removed after a transport failure: %v", name, err)
		}
	}
}

func TestTakeViewerIdentityTokenRemovesItFromChildEnvironment(t *testing.T) {
	t.Setenv(viewerIdentityEnv, "expected-token")
	if got := takeViewerIdentityToken(); got != "expected-token" {
		t.Fatalf("token = %q", got)
	}
	if _, ok := os.LookupEnv(viewerIdentityEnv); ok {
		t.Fatal("viewer identity token remained in the process environment")
	}
}

func TestViewerIdentityRejectsTrailingJSON(t *testing.T) {
	state := viewerState{pid: 1234, token: "expected-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(viewerIdentityHeader) != state.token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		fmt.Fprintf(w, `{"pid":%d} {}`, state.pid)
	}))
	defer server.Close()
	state.url = server.URL + "/"
	if viewerIdentityMatches(state) {
		t.Fatal("viewer identity accepted trailing JSON")
	}
}

func TestViewerIdentityRequiresChallengeAndStopsItself(t *testing.T) {
	root := home(t)
	handler := newViewHandlerWithIdentity(root, "", false, "expected-token")
	t.Cleanup(func() { _ = handler.Close() })

	unauthenticated := httptest.NewRequest(http.MethodGet, "/api/viewer-identity", nil)
	unauthenticated.Host = "127.0.0.1"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, unauthenticated)
	if response.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated identity status = %d, want 403", response.Code)
	}

	authenticated := httptest.NewRequest(http.MethodGet, "/api/viewer-identity", nil)
	authenticated.Host = "127.0.0.1"
	authenticated.Header.Set(viewerIdentityHeader, "expected-token")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, authenticated)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "expected-token") {
		t.Fatalf("authenticated identity status = %d, body = %q", response.Code, response.Body.String())
	}

	stop := httptest.NewRequest(http.MethodPost, "/api/viewer-stop", nil)
	stop.Host = "127.0.0.1"
	stop.Header.Set(viewerIdentityHeader, "expected-token")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, stop)
	if response.Code != http.StatusAccepted {
		t.Fatalf("authenticated stop status = %d, want 202", response.Code)
	}
	select {
	case <-handler.shutdown:
	default:
		t.Fatal("authenticated stop did not request viewer shutdown")
	}
}

func TestStopDoesNotSignalAProcessThatDoesNotOwnTheViewerIdentity(t *testing.T) {
	root := home(t)
	stateDir := filepath.Join(filepath.Dir(root), viewerDirName)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/viewer-identity" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"pid": cmd.Process.Pid + 1})
	}))
	t.Cleanup(server.Close)
	for name, value := range map[string]string{
		viewerPIDFile:   strconv.Itoa(cmd.Process.Pid) + "\n",
		viewerURLFile:   server.URL + "/\n",
		viewerTokenFile: "expected-instance\n",
	} {
		if err := os.WriteFile(filepath.Join(stateDir, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	code, stdout, stderr := run(t, "stop")
	if code != 0 || !strings.Contains(stdout, "not running") || stderr != "" {
		t.Fatalf("stop stale identity: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if !processAlive(cmd.Process.Pid) {
		t.Fatal("stop signalled the unrelated process named by a stale PID")
	}
}

func TestStartRejectsMisuse(t *testing.T) {
	home(t)
	for _, args := range [][]string{
		{"start", "--listen"},
		{"start", "--listen", "0.0.0.0:7788"},
		{"start", "--bogus"},
		{"stop", "now"},
		{"status", "--json"},
	} {
		if code, _, _ := run(t, args...); code != exitUsage {
			t.Errorf("%v exit code = %d, want %d", args, code, exitUsage)
		}
	}
}
