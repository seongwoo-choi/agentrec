//go:build darwin || linux

package cli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/runner"
)

// A comparison runs two agents in checkouts of agentrec's own making, taken out
// of the operator's repository. An interrupt in the middle of one is the moment
// where that matters most: a recorder killed where it stands leaves a copy of
// the repository on disk, an administration entry for it in the repository
// itself, and a run that never says how it ended — and the next agent launched
// anyway.
//
// agentrec is signalled here as the operating system would signal it, so what is
// exercised is the disposition of the signal itself. Both signals are covered
// because a comparison is stopped by a scheduler's SIGTERM as often as by an
// operator's Ctrl-C, and each is delivered during a different leg, so neither
// the first nor the second position is the only one that cleans up.
func TestShadowCleansUpWhenItIsInterrupted(t *testing.T) {
	for _, tc := range []struct {
		name    string
		signal  syscall.Signal
		runners []string
		// stopped is the runner whose provider is signalled, which is the first
		// one given; skipped is the one that must never be launched.
		stopped, skipped string
	}{
		{
			name:    "interrupted during the first leg",
			signal:  syscall.SIGINT,
			runners: []string{"claude", "codex"},
			stopped: "claude",
			skipped: "codex",
		},
		{
			name:    "terminated during the leg the operator asked for first",
			signal:  syscall.SIGTERM,
			runners: []string{"codex", "claude"},
			stopped: "codex",
			skipped: "claude",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := home(t)
			repo := cleanRepo(t)
			dir := stubProviders(t, "claude", "codex", verifyHelperName, agentrecName)
			commitVerifyConfig(t, repo, verifyHelperName, "pass")
			pidFile := filepath.Join(t.TempDir(), "provider.pid")
			t.Setenv(lingerEnv, pidFile)
			task := writeTask(t, "change the README\n")
			before := snapshotSource(t, repo)

			args := []string{"shadow", "run", task}
			for _, name := range tc.runners {
				args = append(args, "--runner", name)
			}
			var stdout, stderr bytes.Buffer
			rec := exec.Command(filepath.Join(dir, agentrecName), args...)
			rec.Stdout, rec.Stderr = &stdout, &stderr
			if err := rec.Start(); err != nil {
				t.Fatalf("start the recorder: %v", err)
			}
			// Signalled only once the first agent is really running: a comparison
			// stopped before it has a run to hold is not the one this is about.
			if !waitForFile(pidFile, 60*time.Second) {
				rec.Process.Kill()
				rec.Wait()
				t.Fatalf("the first provider was never launched (stderr %q)", stderr.String())
			}
			group := readPID(t, pidFile)
			// However this test ends, nothing it launched outlives it.
			t.Cleanup(func() { syscall.Kill(-group, syscall.SIGKILL) })

			if err := rec.Process.Signal(tc.signal); err != nil {
				t.Fatalf("signal the recorder: %v", err)
			}
			rec.Wait()

			if code := rec.ProcessState.ExitCode(); code != exitInterrupted {
				t.Fatalf("exit code = %d, want %d (stderr %q)", code, exitInterrupted, stderr.String())
			}
			// The provider's whole group went with the recorder rather than being
			// left behind in a checkout that is about to be deleted.
			if err := syscall.Kill(-group, syscall.Signal(0)); !errors.Is(err, syscall.ESRCH) {
				t.Errorf("signalling provider group %d = %v, want the group reaped", group, err)
			}

			// The leg that started is recorded to the end, and the leg that had not
			// started was never launched: an interrupt is not permission to run
			// another agent against the operator's repository.
			bundles := shadowBundles(t, root)
			if len(bundles) != 1 || bundles[tc.stopped] == "" {
				t.Fatalf("recorded runs = %v, want only the interrupted %s leg", bundles, tc.stopped)
			}
			runID := bundles[tc.stopped]
			manifest, err := readManifest(filepath.Join(root, runID))
			if err != nil {
				t.Fatalf("read manifest: %v", err)
			}
			if manifest.ExitReason != runner.ReasonInterrupted || manifest.EndedAt == nil {
				t.Errorf("manifest exit reason = %q, ended = %v, want a run recorded as interrupted", manifest.ExitReason, manifest.EndedAt)
			}
			wantGitArtifacts(t, filepath.Join(root, runID))

			// The comparison still says what each side came to, including that one
			// of them never ran.
			if !strings.Contains(stdout.String(), comparisonHeader) || !strings.Contains(stdout.String(), runID) {
				t.Errorf("stdout =\n%s\nwant the comparison naming run %s", stdout.String(), runID)
			}
			if !strings.Contains(stdout.String(), tc.skipped+"\n  "+notRun) {
				t.Errorf("stdout =\n%s\nwant %s reported as %s", stdout.String(), tc.skipped, notRun)
			}

			// Nothing agentrec prepared outlived the interrupt, and the repository
			// it was all recorded from is exactly as it was found: same HEAD, same
			// status, same refs, same worktree list, same tracked bytes.
			wantNoWorkspace(t, root)
			wantSourceUnchanged(t, repo, before)
		})
	}
}

// Holding a signal is a promise to finish writing down the run that was stopped
// and to take the checkouts back out — it is not a claim on the operator's
// terminal. A comparison has more left to do after an interrupt than a single
// run does: two agents, two checkouts, and the cleanup of both. An operator who
// asks a second time has decided that wait is over, so the first signal is the
// last one this command holds, whichever part of the recording took it.
func TestShadowStopsHoldingSignalsAfterTheFirstOne(t *testing.T) {
	home(t)
	repo := cleanRepo(t)
	dir := stubProviders(t, "claude", "codex", verifyHelperName, agentrecName)
	commitVerifyConfig(t, repo, verifyHelperName, "pass")
	pidFile := filepath.Join(t.TempDir(), "provider.pid")
	t.Setenv(lingerEnv, pidFile)
	// The first leg's repository measurement is held open, so the second signal
	// arrives while the recorder is still finishing the leg the first one
	// stopped, rather than at a moment left to chance.
	measuring, resume := pauseRepositoryMeasurement(t, dir)
	task := writeTask(t, "change the README\n")

	var stdout, stderr bytes.Buffer
	rec := exec.Command(filepath.Join(dir, agentrecName), "shadow", "run", task, "--runner", "claude", "--runner", "codex")
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
		t.Fatalf("the first provider was never launched (stderr %q)", stderr.String())
	}
	group := readPID(t, pidFile)
	t.Cleanup(func() { syscall.Kill(-group, syscall.SIGKILL) })

	if err := rec.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal the recorder: %v", err)
	}
	if !waitForFile(measuring, 60*time.Second) {
		t.Fatalf("the stopped leg was never measured (stderr %q)", stderr.String())
	}
	if err := rec.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal the recorder again: %v", err)
	}
	// Released so that a recorder which wrongly held the second signal finishes
	// and reports an ending, rather than this test reading a timeout as a pass.
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
