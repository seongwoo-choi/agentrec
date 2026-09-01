package cli

import (
	"net/http"
	"os"
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

	code, stdout, stderr = run(t, "start", "--no-open", "--listen", "127.0.0.1:0")
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
	if code != 0 || !strings.Contains(stdout, "viewer    running at "+url+" (pid "+strconv.Itoa(pid)+")") || !strings.Contains(stdout, "hooks not installed (agentrec setup)") {
		t.Errorf("status while running: exit %d\n%s", code, stdout)
	}

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
