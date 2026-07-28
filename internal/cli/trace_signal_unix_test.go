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

// A run does not end when its provider does: the repository still has to be
// measured, the checks still have to run and the report still has to be filed,
// and all of that happens on the recorder's own time. A signal arriving in that
// window is the operator asking for the run to stop, not permission to abandon
// the evidence half-written — so it is held until the run has been recorded and
// then reported as the interruption it was.
func TestTraceHoldsASignalUntilTheRepositoryHasBeenMeasured(t *testing.T) {
	root := home(t)
	cleanRepo(t)
	dir := stubProviders(t, "claude", agentrecName)
	measuring, resume := pauseRepositoryMeasurement(t, dir)

	var stdout, stderr bytes.Buffer
	rec := exec.Command(filepath.Join(dir, agentrecName), "trace", "claude", "--", "-p", "read the README")
	rec.Stdout, rec.Stderr = &stdout, &stderr
	if err := rec.Start(); err != nil {
		t.Fatalf("start the recorder: %v", err)
	}
	t.Cleanup(func() {
		os.WriteFile(resume, []byte("go\n"), 0o600)
		rec.Process.Kill()
		rec.Wait()
	})

	// Signalled only once the provider has ended and the recorder is inside the
	// measurement it makes afterwards: that is the window this is about.
	if !waitForFile(measuring, 60*time.Second) {
		t.Fatalf("the repository was never measured (stderr %q)", stderr.String())
	}
	if err := rec.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("terminate the recorder: %v", err)
	}
	// The signal is delivered by the operating system rather than by this
	// process, so the measurement is released a moment later: releasing it in
	// the same instant would leave which of the two happened first to chance.
	time.Sleep(200 * time.Millisecond)
	if err := os.WriteFile(resume, []byte("go\n"), 0o600); err != nil {
		t.Fatalf("release the measurement: %v", err)
	}
	rec.Wait()

	if code := rec.ProcessState.ExitCode(); code != exitInterrupted {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, exitInterrupted, stderr.String())
	}
	// The run was recorded to the end: the repository evidence answers its own
	// pending document, and the report was filed beside it.
	runDir := filepath.Join(root, traceRunID(t, stdout.String()))
	wantGitArtifacts(t, runDir)
	if _, err := os.Stat(filepath.Join(runDir, reportFile)); err != nil {
		t.Errorf("recorded report: %v", err)
	}
}

// Holding a signal is a promise to finish writing the run down, not a claim on
// the operator's terminal: an operator who asks a second time has decided the
// wait is over. The first signal is the last one the recorder holds, so the
// second one reaches this process with the disposition the operating system
// gives it and ends it where it stands.
func TestTraceStopsHoldingSignalsDuringTheProviderAfterTheFirstOne(t *testing.T) {
	home(t)
	cleanRepo(t)
	dir := stubProviders(t, "claude", agentrecName)
	measuring, resume := pauseRepositoryMeasurement(t, dir)
	pidFile := filepath.Join(t.TempDir(), "provider.pid")
	t.Setenv(lingerEnv, pidFile)

	var stdout, stderr bytes.Buffer
	rec := exec.Command(filepath.Join(dir, agentrecName), "trace", "claude", "--", "-p", "read the README")
	rec.Stdout, rec.Stderr = &stdout, &stderr
	if err := rec.Start(); err != nil {
		t.Fatalf("start the recorder: %v", err)
	}
	t.Cleanup(func() {
		os.WriteFile(resume, []byte("go\n"), 0o600)
		rec.Process.Kill()
		rec.Wait()
	})

	if !waitForFile(pidFile, 60*time.Second) {
		t.Fatalf("the provider was never launched (stderr %q)", stderr.String())
	}
	if err := rec.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("terminate the provider run: %v", err)
	}
	if !waitForFile(measuring, 60*time.Second) {
		t.Fatalf("the stopped run was never measured (stderr %q)", stderr.String())
	}
	if err := rec.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("terminate the recorder again: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := os.WriteFile(resume, []byte("go\n"), 0o600); err != nil {
		t.Fatalf("release the measurement: %v", err)
	}
	rec.Wait()

	status, ok := rec.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("wait status = %T, want a unix wait status", rec.ProcessState.Sys())
	}
	if !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Fatalf("recorder ended with exit code %d, want it terminated by %v (stderr %q)",
			rec.ProcessState.ExitCode(), syscall.SIGTERM, stderr.String())
	}
}

func TestTraceStopsHoldingSignalsAfterTheFirstOne(t *testing.T) {
	home(t)
	cleanRepo(t)
	dir := stubProviders(t, "claude", agentrecName)
	measuring, resume := pauseRepositoryMeasurement(t, dir)

	var stdout, stderr bytes.Buffer
	rec := exec.Command(filepath.Join(dir, agentrecName), "trace", "claude", "--", "-p", "read the README")
	rec.Stdout, rec.Stderr = &stdout, &stderr
	if err := rec.Start(); err != nil {
		t.Fatalf("start the recorder: %v", err)
	}
	t.Cleanup(func() {
		os.WriteFile(resume, []byte("go\n"), 0o600)
		rec.Process.Kill()
		rec.Wait()
	})

	if !waitForFile(measuring, 60*time.Second) {
		t.Fatalf("the repository was never measured (stderr %q)", stderr.String())
	}
	for range 2 {
		if err := rec.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatalf("terminate the recorder: %v", err)
		}
		// The two signals are separated so that the second one is the second
		// ask rather than a repetition the recorder never had a chance to
		// distinguish: the first has to have been taken before it arrives.
		time.Sleep(500 * time.Millisecond)
	}
	// Released so that a recorder which wrongly held the second signal finishes
	// and reports an ending, rather than this test reading a timeout as a pass.
	if err := os.WriteFile(resume, []byte("go\n"), 0o600); err != nil {
		t.Fatalf("release the measurement: %v", err)
	}
	rec.Wait()

	status, ok := rec.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("wait status = %T, want a unix wait status", rec.ProcessState.Sys())
	}
	if !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Fatalf("recorder ended with exit code %d, want it terminated by %v (stderr %q)",
			rec.ProcessState.ExitCode(), syscall.SIGTERM, stderr.String())
	}
}

// pauseRepositoryMeasurement puts the git stand-in on the recorder's PATH in
// place of the real one and reports the file it creates when it has been asked
// for the diff from the pinned baseline, together with the file that releases
// it again.
func pauseRepositoryMeasurement(t *testing.T, dir string) (measuring, resume string) {
	t.Helper()
	stub := filepath.Join(dir, gitHelperName)
	real, err := filepath.EvalSymlinks(stub)
	if err != nil {
		t.Fatalf("resolve git: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	if err := os.Remove(stub); err != nil {
		t.Fatalf("replace git: %v", err)
	}
	if err := os.Symlink(exe, stub); err != nil {
		t.Fatalf("stub git: %v", err)
	}

	signals := t.TempDir()
	measuring, resume = filepath.Join(signals, "measuring"), filepath.Join(signals, "resume")
	t.Setenv(gitRealEnv, real)
	t.Setenv(gitPauseEnv, measuring)
	t.Setenv(gitResumeEnv, resume)
	return measuring, resume
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
