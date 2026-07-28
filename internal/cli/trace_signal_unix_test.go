//go:build darwin || linux

package cli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/runner"
)

// A run is stopped with SIGTERM at least as often as with a Ctrl-C: a parent
// runner, a scheduler and a container all ask a process to stop that way. The
// recorder has to end the run it is holding rather than be killed where it
// stands, or it leaves a bundle that says a run is still going long after it
// stopped and a provider process group still running with the run's pipes open.
//
// agentrec is signalled here as the operating system would signal it, so what is
// exercised is the disposition of the signal itself rather than a channel a test
// wrote into: the test binary stands in for the recorder the same way it stands
// in for a provider, and the run is stopped once the provider is really running.
func TestTraceFinalizesTheRunWhenItIsTerminated(t *testing.T) {
	root := home(t)
	cleanRepo(t)
	dir := stubProviders(t, "claude", agentrecName)
	pidFile := filepath.Join(t.TempDir(), "provider.pid")
	t.Setenv(lingerEnv, pidFile)

	var stdout, stderr bytes.Buffer
	rec := exec.Command(filepath.Join(dir, agentrecName), "trace", "claude", "--", "-p", "wait to be stopped")
	rec.Stdout, rec.Stderr = &stdout, &stderr
	if err := rec.Start(); err != nil {
		t.Fatalf("start the recorder: %v", err)
	}
	// Signalled only once the provider is running: a recorder stopped before it
	// has a run to hold is not the recorder this is about.
	if !waitForFile(pidFile, 30*time.Second) {
		rec.Process.Kill()
		rec.Wait()
		t.Fatalf("the provider was never launched (stderr %q)", stderr.String())
	}
	group := readPID(t, pidFile)
	// However this test ends, nothing it launched outlives it.
	t.Cleanup(func() { syscall.Kill(-group, syscall.SIGKILL) })

	if err := rec.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("terminate the recorder: %v", err)
	}
	rec.Wait()

	if code := rec.ProcessState.ExitCode(); code != exitInterrupted {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, exitInterrupted, stderr.String())
	}
	// The provider's whole group went with the recorder rather than being left
	// behind: an empty group is one no signal can reach.
	if err := syscall.Kill(-group, syscall.Signal(0)); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("signalling provider group %d = %v, want the group reaped", group, err)
	}

	// The bundle says how the run ended rather than reading as one still going,
	// and the repository evidence was measured rather than left pending.
	runDir := filepath.Join(root, traceRunID(t, stdout.String()))
	manifest, err := readManifest(runDir)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if manifest.ExitReason != runner.ReasonInterrupted || manifest.EndedAt == nil {
		t.Errorf("manifest exit reason = %q, ended = %v, want a run recorded as interrupted", manifest.ExitReason, manifest.EndedAt)
	}
	wantGitArtifacts(t, runDir)
}

func waitForFile(path string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return pid
}
