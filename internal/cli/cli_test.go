package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/action"
	"github.com/seongwoo-choi/agentrec/internal/evidence"
	"github.com/seongwoo-choi/agentrec/internal/lock"
	"github.com/seongwoo-choi/agentrec/internal/report"
	"github.com/seongwoo-choi/agentrec/internal/runner"
	"github.com/seongwoo-choi/agentrec/internal/storage"
)

// Fixed instants keep every rendered timestamp deterministic, and UTC keeps it
// independent of the machine the tests run on.
var (
	early = time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	late  = time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
)

// home points the CLI at a private data directory and returns the runs root
// underneath it, which is where the fixtures below write their bundles.
func home(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AGENTREC_HOME", dir)
	return filepath.Join(dir, "runs")
}

// writeRun records one bundle: a manifest, one action, and, unless exitReason
// is empty, a process result and the finalization that ends the run.
func writeRun(t *testing.T, root, id, provider string, startedAt time.Time, exitReason string) {
	t.Helper()

	b, err := storage.Create(root, id, storage.Manifest{
		Provider:  provider,
		Argv:      []string{provider, "-p", "hello"},
		CWD:       "/tmp",
		StartedAt: startedAt,
	})
	if err != nil {
		t.Fatalf("create run %s: %v", id, err)
	}
	if err := b.WriteAction(readAction(startedAt)); err != nil {
		t.Fatalf("write action for %s: %v", id, err)
	}
	if exitReason == "" {
		return
	}
	if err := b.WriteProcessResult(processResultJSON(t, startedAt, exitReason)); err != nil {
		t.Fatalf("write result for %s: %v", id, err)
	}
	if err := b.Finalize(storage.Finalization{
		EndedAt:    startedAt.Add(1500 * time.Millisecond),
		ExitReason: exitReason,
	}); err != nil {
		t.Fatalf("finalize %s: %v", id, err)
	}
}

// readAction is one recorded file read whose payloads carry values that must
// never reach the rendered report.
func readAction(startedAt time.Time) action.Action {
	return action.Action{
		ID:         "a1",
		Type:       action.TypeFileRead,
		Provider:   "claude",
		Assurance:  action.AssuranceProviderReported,
		StartedAt:  startedAt.Add(time.Second),
		FinishedAt: startedAt.Add(2 * time.Second),
		Status:     "completed",
		Input:      json.RawMessage(`{"file_path":"README.md","unshown_input":"input-payload-marker"}`),
		Result:     json.RawMessage(`{"content":"result-payload-marker"}`),
	}
}

func processResultJSON(t *testing.T, startedAt time.Time, exitReason string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"startedAt":      startedAt,
		"endedAt":        startedAt.Add(1500 * time.Millisecond),
		"durationMillis": 1500,
		"exitCode":       0,
		"exitReason":     exitReason,
		"unshownField":   "result-json-marker",
	})
	if err != nil {
		t.Fatalf("encode process result: %v", err)
	}
	return raw
}

// run invokes the CLI and reports what the operator would have seen.
func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestListReportsRunsNewestFirstWithStableTieBreak(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", early, "completed")
	writeRun(t, root, "run-b", "codex", late, "failed")
	writeRun(t, root, "run-c", "claude", late, "")

	code, stdout, stderr := run(t, "list")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	want := strings.Join([]string{
		"RUN ID  PROVIDER  PROJECT  STARTED  EXIT  VERIFICATION",
		"run-c  claude  tmp  2026-07-27T10:00:00Z  unknown  NOT RUN",
		"run-b  codex  tmp  2026-07-27T10:00:00Z  failed  NOT RUN",
		"run-a  claude  tmp  2026-07-27T09:00:00Z  completed  NOT RUN",
		"",
	}, "\n")
	if stdout != want {
		t.Errorf("stdout =\n%q\nwant\n%q", stdout, want)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestListFiltersByExitReason(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", early, "completed")
	writeRun(t, root, "run-b", "codex", late, "failed")
	writeRun(t, root, "run-c", "claude", late, "failed")

	code, stdout, stderr := run(t, "list", "--exit-reason", "failed")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	want := strings.Join([]string{
		"RUN ID  PROVIDER  PROJECT  STARTED  EXIT  VERIFICATION",
		"run-c  claude  tmp  2026-07-27T10:00:00Z  failed  NOT RUN",
		"run-b  codex  tmp  2026-07-27T10:00:00Z  failed  NOT RUN",
		"",
	}, "\n")
	if stdout != want {
		t.Errorf("stdout =\n%q\nwant\n%q", stdout, want)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestListShowsAndFiltersByVerificationStatus(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", early, "completed")
	writeVerification(t, root, "run-a", passedVerification())
	writeRun(t, root, "run-b", "codex", late, "completed")
	writeVerification(t, root, "run-b", map[string]any{
		"status":      "failed",
		"attribution": evidence.VerificationAttribution,
		"checks":      []any{},
	})
	writeRun(t, root, "run-c", "claude", late, "completed")

	allCode, allStdout, allStderr := run(t, "list")
	if allCode != 0 {
		t.Fatalf("unfiltered exit code = %d, want 0 (stderr %q)", allCode, allStderr)
	}
	allWant := strings.Join([]string{
		"RUN ID  PROVIDER  PROJECT  STARTED  EXIT  VERIFICATION",
		"run-c  claude  tmp  2026-07-27T10:00:00Z  completed  NOT RUN",
		"run-b  codex  tmp  2026-07-27T10:00:00Z  completed  FAIL",
		"run-a  claude  tmp  2026-07-27T09:00:00Z  completed  PASS",
		"",
	}, "\n")
	if allStdout != allWant {
		t.Errorf("unfiltered stdout =\n%q\nwant\n%q", allStdout, allWant)
	}
	if allStderr != "" {
		t.Errorf("unfiltered stderr = %q, want empty", allStderr)
	}

	code, stdout, stderr := run(t, "list", "--verification-status", "FAIL")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	want := strings.Join([]string{
		"RUN ID  PROVIDER  PROJECT  STARTED  EXIT  VERIFICATION",
		"run-b  codex  tmp  2026-07-27T10:00:00Z  completed  FAIL",
		"",
	}, "\n")
	if stdout != want {
		t.Errorf("stdout =\n%q\nwant\n%q", stdout, want)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}

	code, stdout, stderr = run(t, "list", "--verification-status", "NOT RUN")
	if code != 0 {
		t.Fatalf("unavailable exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	want = strings.Join([]string{
		"RUN ID  PROVIDER  PROJECT  STARTED  EXIT  VERIFICATION",
		"run-c  claude  tmp  2026-07-27T10:00:00Z  completed  NOT RUN",
		"",
	}, "\n")
	if stdout != want {
		t.Errorf("unavailable stdout =\n%q\nwant\n%q", stdout, want)
	}
	if stderr != "" {
		t.Errorf("unavailable stderr = %q, want empty", stderr)
	}
}

func TestListFiltersByEscapedFutureVerificationStatus(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", early, "completed")
	writeVerification(t, root, "run-a", map[string]any{
		"status":      "future\nstatus",
		"attribution": evidence.VerificationAttribution,
		"checks":      []any{},
	})

	code, stdout, stderr := run(t, "list", "--verification-status", `FUTURE\nSTATUS`)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	want := strings.Join([]string{
		"RUN ID  PROVIDER  PROJECT  STARTED  EXIT  VERIFICATION",
		`run-a  claude  tmp  2026-07-27T09:00:00Z  completed  FUTURE\nSTATUS`,
		"",
	}, "\n")
	if stdout != want {
		t.Errorf("stdout =\n%q\nwant\n%q", stdout, want)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestListCombinesAllFiltersInEveryOrder(t *testing.T) {
	root := home(t)
	failed := map[string]any{
		"status":      "failed",
		"attribution": evidence.VerificationAttribution,
		"checks":      []any{},
	}
	writeRun(t, root, "match", "claude", late, "failed")
	writeVerification(t, root, "match", failed)
	writeRun(t, root, "only-verification", "codex", early, "failed")
	writeVerification(t, root, "only-verification", passedVerification())
	writeRun(t, root, "only-exit", "claude", early, "completed")
	writeVerification(t, root, "only-exit", failed)
	writeRun(t, root, "only-cwd", "codex", early, "failed")
	writeVerification(t, root, "only-cwd", failed)
	manifestPath := filepath.Join(root, "only-cwd", manifestFile)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read only-cwd manifest: %v", err)
	}
	var manifest storage.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode only-cwd manifest: %v", err)
	}
	manifest.CWD = "/var/tmp"
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode only-cwd manifest: %v", err)
	}
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatalf("rewrite only-cwd manifest: %v", err)
	}

	for _, args := range [][]string{
		{"list", "--cwd", "/tmp", "--exit-reason", "failed", "--verification-status", "FAIL"},
		{"list", "--cwd", "/tmp", "--verification-status", "FAIL", "--exit-reason", "failed"},
		{"list", "--exit-reason", "failed", "--cwd", "/tmp", "--verification-status", "FAIL"},
		{"list", "--exit-reason", "failed", "--verification-status", "FAIL", "--cwd", "/tmp"},
		{"list", "--verification-status", "FAIL", "--cwd", "/tmp", "--exit-reason", "failed"},
		{"list", "--verification-status", "FAIL", "--exit-reason", "failed", "--cwd", "/tmp"},
	} {
		code, stdout, stderr := run(t, args...)
		if code != 0 {
			t.Fatalf("run(%q) exit code = %d, want 0 (stderr %q)", args, code, stderr)
		}
		if !strings.Contains(stdout, "match  claude") || strings.Contains(stdout, "only-") {
			t.Errorf("run(%q) stdout = %q, want only the conjunction match", args, stdout)
		}
		if stderr != "" {
			t.Errorf("run(%q) stderr = %q, want empty", args, stderr)
		}
	}
}

func TestScanRunsUsesOneRunDirectoryIdentityForManifestAndVerification(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run", "claude", late, "failed")
	writeVerification(t, root, "run", passedVerification())
	writeRun(t, root, "zz-replacement", "codex", early, "completed")
	writeVerification(t, root, "zz-replacement", map[string]any{
		"status":      "failed",
		"attribution": evidence.VerificationAttribution,
		"checks":      []any{},
	})

	runs, unreadable, err := scanRuns(root, "", func(runRoot *os.Root, run *runSummary) (bool, error) {
		if run.ID != "run" {
			return true, nil
		}
		if err := os.Rename(filepath.Join(root, "run"), filepath.Join(root, "held-original")); err != nil {
			return false, err
		}
		if err := os.Rename(filepath.Join(root, "zz-replacement"), filepath.Join(root, "run")); err != nil {
			return false, err
		}
		verification, err := readVerificationFromRoot(runRoot)
		if err != nil {
			return false, err
		}
		run.Verification = verdict(verification.Status)
		return true, nil
	})
	if err != nil {
		t.Fatalf("scan runs: %v", err)
	}
	if unreadable != 0 {
		t.Fatalf("unreadable = %d, want 0", unreadable)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %#v, want one original run", runs)
	}
	if runs[0].Provider != "claude" || runs[0].Exit != "failed" || runs[0].Verification != "PASS" {
		t.Errorf("run = %#v, want manifest and verification from held original", runs[0])
	}
}

func TestListFiltersByTheEscapedExitReasonShownInTheTable(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", early, "failed\nreason")

	code, stdout, stderr := run(t, "list", "--exit-reason", `failed\nreason`)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	want := strings.Join([]string{
		"RUN ID  PROVIDER  PROJECT  STARTED  EXIT  VERIFICATION",
		`run-a  claude  tmp  2026-07-27T09:00:00Z  failed\nreason  NOT RUN`,
		"",
	}, "\n")
	if stdout != want {
		t.Errorf("stdout =\n%q\nwant\n%q", stdout, want)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestListFiltersByExitReasonsThatLookLikeOptions(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-cwd", "claude", early, "--cwd")
	writeRun(t, root, "run-exit", "codex", late, "--exit-reason")

	for _, tt := range []struct {
		reason string
		wantID string
	}{
		{"--cwd", "run-cwd"},
		{"--exit-reason", "run-exit"},
	} {
		code, stdout, stderr := run(t, "list", "--exit-reason", tt.reason)
		if code != 0 {
			t.Fatalf("reason %q exit code = %d, want 0 (stderr %q)", tt.reason, code, stderr)
		}
		if !strings.Contains(stdout, tt.wantID) {
			t.Errorf("reason %q stdout = %q, want run %q", tt.reason, stdout, tt.wantID)
		}
		if stderr != "" {
			t.Errorf("reason %q stderr = %q, want empty", tt.reason, stderr)
		}
	}
}

func TestListCombinesWorkingDirectoryAndExitReasonFiltersInEitherOrder(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", early, "completed")
	writeRun(t, root, "run-b", "codex", late, "failed")
	writeRunIn(t, root, "other-cwd", "/var/tmp")

	for _, args := range [][]string{
		{"list", "--cwd", "/tmp", "--exit-reason", "failed"},
		{"list", "--exit-reason", "failed", "--cwd", "/tmp"},
	} {
		code, stdout, stderr := run(t, args...)
		if code != 0 {
			t.Fatalf("run(%q) exit code = %d, want 0 (stderr %q)", args, code, stderr)
		}
		want := strings.Join([]string{
			"RUN ID  PROVIDER  PROJECT  STARTED  EXIT  VERIFICATION",
			"run-b  codex  tmp  2026-07-27T10:00:00Z  failed  NOT RUN",
			"",
		}, "\n")
		if stdout != want {
			t.Errorf("run(%q) stdout =\n%q\nwant\n%q", args, stdout, want)
		}
		if stderr != "" {
			t.Errorf("run(%q) stderr = %q, want empty", args, stderr)
		}
	}
}

func TestListFiltersByCleanedWorkingDirectory(t *testing.T) {
	root := home(t)
	work := t.TempDir()
	t.Chdir(work)
	target := filepath.Join(work, "project")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o700); err != nil {
		t.Fatalf("create target: %v", err)
	}

	writeRunIn(t, root, "match", target+string(filepath.Separator)+"child"+string(filepath.Separator)+"..")
	writeRunIn(t, root, "other", filepath.Join(work, "other"))
	writeRunIn(t, root, "relative", "project/child/..")
	writeRunIn(t, root, "missing", "")
	if err := os.Mkdir(filepath.Join(root, "malformed"), 0o700); err != nil {
		t.Fatalf("create malformed run: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "malformed", "manifest.json"), []byte(`{"cwd":42}`), 0o600); err != nil {
		t.Fatalf("write malformed manifest: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "unreadable"), 0o700); err != nil {
		t.Fatalf("create unreadable run: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "unreadable", "manifest.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write unreadable manifest: %v", err)
	}

	code, stdout, stderr := run(t, "list", "--cwd", "./project/child/..")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	want := strings.Join([]string{
		"RUN ID  PROVIDER  PROJECT  STARTED  EXIT  VERIFICATION",
		"match  claude  project  2026-07-27T09:00:00Z  unknown  NOT RUN",
		"",
	}, "\n")
	if stdout != want {
		t.Errorf("stdout =\n%q\nwant\n%q", stdout, want)
	}
	if stderr != "Warnings: 2 unreadable run(s).\n" {
		t.Errorf("stderr = %q, want malformed and unreadable manifests reported", stderr)
	}
}

// writeRunIn records a run whose manifest names cwd as the directory it ran in.
func writeRunIn(t *testing.T, root, id, cwd string) {
	t.Helper()
	if _, err := storage.Create(root, id, storage.Manifest{
		Provider:  "claude",
		Argv:      []string{"claude"},
		CWD:       cwd,
		StartedAt: early,
	}); err != nil {
		t.Fatalf("create run %s: %v", id, err)
	}
}

// A run is told apart from a run of another repository by the project it was
// recorded in, without having to open each one. A working directory the
// manifest does not record, or one that names no directory, is reported as
// unknown: a guessed project would be read as a fact about where the run ran.
func TestListNamesProjectFromRecordedWorkingDirectory(t *testing.T) {
	root := home(t)
	writeRunIn(t, root, "run-a", "/home/dev/agentrec")
	writeRunIn(t, root, "run-b", "")
	writeRunIn(t, root, "run-c", "relative/dir")

	code, stdout, stderr := run(t, "list")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	want := strings.Join([]string{
		"RUN ID  PROVIDER  PROJECT  STARTED  EXIT  VERIFICATION",
		"run-c  claude  unknown  2026-07-27T09:00:00Z  unknown  NOT RUN",
		"run-b  claude  unknown  2026-07-27T09:00:00Z  unknown  NOT RUN",
		"run-a  claude  agentrec  2026-07-27T09:00:00Z  unknown  NOT RUN",
		"",
	}, "\n")
	if stdout != want {
		t.Errorf("stdout =\n%q\nwant\n%q", stdout, want)
	}
}

func TestListWithoutRunsRootReportsNoRuns(t *testing.T) {
	home(t)

	code, stdout, stderr := run(t, "list")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if stdout != "No runs.\n" {
		t.Errorf("stdout = %q, want %q", stdout, "No runs.\n")
	}
}

// junk fills a runs root with everything that is not a readable run: two run
// directories whose manifests cannot be read, and three entries that are not
// run directories at all.
func junk(t *testing.T, root string) {
	t.Helper()
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o700); err != nil {
		t.Fatalf("create empty run: %v", err)
	}
	broken := filepath.Join(root, "broken")
	if err := os.Mkdir(broken, 0o700); err != nil {
		t.Fatalf("create broken run: %v", err)
	}
	if err := os.WriteFile(filepath.Join(broken, "manifest.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write broken manifest: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "run-a"), filepath.Join(root, "linked")); err != nil {
		t.Fatalf("create symlinked run: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("stray\n"), 0o600); err != nil {
		t.Fatalf("create stray file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "bad\x1bname"), 0o700); err != nil {
		t.Fatalf("create invalidly named entry: %v", err)
	}
}

func TestListFilterWithoutMatchesReportsNoRunsAndUnreadableWarnings(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", early, "completed")
	junk(t, root)

	code, stdout, stderr := run(t, "list", "--exit-reason", "nonzero")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if stdout != "No runs.\n" {
		t.Errorf("stdout = %q, want %q", stdout, "No runs.\n")
	}
	if stderr != "Warnings: 2 unreadable run(s).\n" {
		t.Errorf("stderr = %q, want unreadable runs preserved", stderr)
	}
}

func TestListExitFilterDoesNotReadExcludedVerification(t *testing.T) {
	root := home(t)
	writeRun(t, root, "match", "claude", late, "nonzero")
	writeRun(t, root, "excluded", "codex", early, "completed")
	writeVerification(t, root, "excluded", []byte(`{"status":`))

	code, stdout, stderr := run(t, "list", "--exit-reason", "nonzero")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "match  claude") || strings.Contains(stdout, "excluded") {
		t.Errorf("stdout = %q, want only the exit-reason match", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want excluded verification left unread", stderr)
	}
}

func TestListOmitsMalformedVerificationAndCountsTheRunUnreadable(t *testing.T) {
	root := home(t)
	writeRun(t, root, "good", "claude", late, "completed")
	writeVerification(t, root, "good", passedVerification())
	writeRun(t, root, "bad", "codex", early, "completed")
	writeVerification(t, root, "bad", []byte(`{"status":`))

	code, stdout, stderr := run(t, "list", "--verification-status", "PASS")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "good  claude") || strings.Contains(stdout, "bad  codex") {
		t.Errorf("stdout = %q, want only the readable verification", stdout)
	}
	if stderr != "Warnings: 1 unreadable run(s).\n" {
		t.Errorf("stderr = %q, want malformed verification counted", stderr)
	}
}

func TestListSkipsAndCountsUnreadableRuns(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", early, "completed")
	junk(t, root)

	code, stdout, stderr := run(t, "list")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "run-a") {
		t.Errorf("stdout = %q, want the readable run listed", stdout)
	}
	for _, skipped := range []string{"empty", "broken", "linked", "notes.txt", "name"} {
		if strings.Contains(stdout, skipped) {
			t.Errorf("stdout = %q, want %q skipped", stdout, skipped)
		}
	}
	// A run directory that cannot be read is a run missing from the table, so
	// it is counted; a stray file or an impossible name never was a run. The
	// count is a diagnostic, so it belongs on stderr and not in the table.
	if strings.Contains(stdout, "Warnings") {
		t.Errorf("stdout = %q, want the warning off the table", stdout)
	}
	if !strings.Contains(stderr, "Warnings: 2 unreadable run(s).") {
		t.Errorf("stderr = %q, want it to report 2 unreadable runs", stderr)
	}
}

func TestListEscapesControlCharactersInRecordedValues(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "cla\nude\x1b[31m", early, "completed")

	code, stdout, _ := run(t, "list")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if strings.Count(stdout, "\n") != 2 {
		t.Errorf("stdout = %q, want exactly a header and one row", stdout)
	}
	if !strings.Contains(stdout, `cla\nude\x1b[31m`) {
		t.Errorf("stdout = %q, want the control characters escaped", stdout)
	}
}

func TestListRejectsDuplicateFilters(t *testing.T) {
	home(t)
	for _, args := range [][]string{
		{"list", "--cwd", "/tmp", "--cwd", "/var/tmp"},
		{"list", "--exit-reason", "failed", "--exit-reason", "completed"},
		{"list", "--verification-status", "PASS", "--verification-status", "FAIL"},
	} {
		code, stdout, stderr := run(t, args...)
		if code != 2 {
			t.Fatalf("run(%q) exit code = %d, want 2", args, code)
		}
		if stdout != "" {
			t.Errorf("run(%q) stdout = %q, want empty", args, stdout)
		}
		if stderr != listUsage {
			t.Errorf("run(%q) stderr = %q, want the list usage", args, stderr)
		}
	}
}

func TestListRejectsExtraArguments(t *testing.T) {
	home(t)

	code, stdout, stderr := run(t, "list", "extra")

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "usage") {
		t.Errorf("stderr = %q, want it to state the usage", stderr)
	}
}

func TestListRejectsInvalidFilterArguments(t *testing.T) {
	home(t)
	for _, tt := range []struct {
		name string
		args []string
	}{
		{"missing cwd", []string{"list", "--cwd"}},
		{"missing exit reason", []string{"list", "--exit-reason"}},
		{"missing verification status", []string{"list", "--verification-status"}},
		{"extra argument", []string{"list", "--cwd", "path", "extra"}},
		{"attached cwd", []string{"list", "--cwd=path"}},
		{"attached exit reason", []string{"list", "--exit-reason=failed"}},
		{"attached verification status", []string{"list", "--verification-status=PASS"}},
		{"unknown option", []string{"list", "--provider", "claude"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := run(t, tt.args...)
			if code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if stderr != listUsage {
				t.Errorf("stderr = %q, want the list usage", stderr)
			}
		})
	}
}

// wantShow is the whole report for a run written by writeRun: the recorded
// action, then the supervisor's fields in their fixed order.
const wantShow = `ACTION TIMELINE

PROVIDER-REPORTED ACTIONS
10:00:01  READ  README.md
  Source       claude
  Assurance    provider_reported
  Result       success
  Duration     1s

SUPERVISOR-OBSERVED RESULT
  Provider     claude
  Exit Reason  completed
  Exit Code    0
  Duration     1.5s
  Warnings     0

REPOSITORY-OBSERVED CHANGES
  (none)

VERIFICATION-OBSERVED RESULT
  (none)
`

func TestShowRendersTheRecordedRun(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-b", "claude", late, "completed")

	code, stdout, stderr := run(t, "show", "run-b")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if stdout != wantShow {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout, wantShow)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestShowLatestSelectsTheNewestRun(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "codex", early, "completed")
	writeRun(t, root, "run-b", "claude", late, "completed")

	code, stdout, stderr := run(t, "show", "latest")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if stdout != wantShow {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout, wantShow)
	}
}

func TestShowLatestBreaksTiesByRunID(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "codex", late, "completed")
	writeRun(t, root, "run-b", "claude", late, "completed")

	code, stdout, _ := run(t, "show", "latest")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "Provider     claude") {
		t.Errorf("stdout = %q, want the highest run id (run-b, claude) chosen", stdout)
	}
}

func TestShowLatestWithoutRunsFails(t *testing.T) {
	home(t)

	code, stdout, stderr := run(t, "show", "latest")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "no runs") {
		t.Errorf("stderr = %q, want it to say there are no runs", stderr)
	}
}

// Which run is newest cannot be known while some run directory refuses to say
// when it started, so latest names the difficulty instead of quietly showing
// the newest run it happened to be able to read.
func TestShowLatestRefusesToGuessPastUnreadableRuns(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", early, "completed")
	if err := os.Mkdir(filepath.Join(root, "run-b"), 0o700); err != nil {
		t.Fatalf("create unreadable run: %v", err)
	}

	code, stdout, stderr := run(t, "show", "latest")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr %q)", code, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "1") || !strings.Contains(stderr, "unreadable") {
		t.Errorf("stderr = %q, want it to report the unreadable run", stderr)
	}
}

// Entries that never were runs say nothing about which run is newest.
func TestShowLatestIgnoresEntriesThatAreNotRuns(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", early, "completed")
	if err := os.Symlink(filepath.Join(root, "run-a"), filepath.Join(root, "linked")); err != nil {
		t.Fatalf("create symlinked run: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("stray\n"), 0o600); err != nil {
		t.Fatalf("create stray file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "bad\x1bname"), 0o700); err != nil {
		t.Fatalf("create invalidly named entry: %v", err)
	}

	code, stdout, stderr := run(t, "show", "latest")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "Provider     claude") {
		t.Errorf("stdout =\n%s\nwant the one readable run", stdout)
	}
}

// A run ID comes off the command line and is reported back on stderr, so one
// carrying terminal control characters must be refused and escaped, never
// echoed as it was typed.
func TestShowNeverEchoesAControlCharacterRunIDRaw(t *testing.T) {
	home(t)

	for _, id := range []string{"run\x1b[31m", "run\x00a", "run‮a", "run​a"} {
		code, stdout, stderr := run(t, "show", id)

		if code != 2 {
			t.Errorf("show %q exit code = %d, want 2", id, code)
		}
		if stdout != "" {
			t.Errorf("show %q stdout = %q, want empty", id, stdout)
		}
		if stderr == "" {
			t.Errorf("show %q stderr is empty, want an explanation", id)
		}
		for _, raw := range []string{"\x1b", "\x00", "‮", "​"} {
			if strings.Contains(stderr, raw) {
				t.Errorf("show %q stderr = %q, want the control character escaped", id, stderr)
			}
		}
	}
}

func TestShowNeverPrintsRawProviderPayloads(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")

	code, stdout, _ := run(t, "show", "run-a")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, leaked := range []string{
		"input-payload-marker",
		"result-payload-marker",
		"result-json-marker",
		"durationMillis",
		"{",
	} {
		if strings.Contains(stdout, leaked) {
			t.Errorf("stdout contains %q, want no raw payload", leaked)
		}
	}
}

func TestShowDerivesSupervisorFieldsFromAnActiveRun(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "")

	code, stdout, stderr := run(t, "show", "run-a")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	for _, want := range []string{
		"Provider     claude",
		"Exit Reason  unknown",
		"Duration     unknown",
		"Warnings     0",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout =\n%s\nwant it to contain %q", stdout, want)
		}
	}
	if strings.Contains(stdout, "Exit Code") {
		t.Errorf("stdout =\n%s\nwant no exit code for a run that has none", stdout)
	}
}

func TestExitReason(t *testing.T) {
	tests := []struct {
		name     string
		manifest storage.Manifest
		result   *processResult
		want     string
	}{
		{
			name:     "manifest wins",
			manifest: storage.Manifest{ExitReason: "interrupted"},
			result:   &processResult{ExitReason: "completed"},
			want:     "interrupted",
		},
		{
			name:   "process result fallback",
			result: &processResult{ExitReason: "completed"},
			want:   "completed",
		},
		{name: "both absent", want: unknownValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitReason(tt.manifest, tt.result); got != tt.want {
				t.Errorf("exitReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunDuration(t *testing.T) {
	ended := late.Add(time.Second)
	beforeStart := late.Add(-time.Second)
	tests := []struct {
		name     string
		manifest storage.Manifest
		result   *processResult
		want     string
	}{
		{
			name:     "process duration wins",
			manifest: storage.Manifest{StartedAt: late, EndedAt: &ended},
			result:   &processResult{DurationMillis: 1500},
			want:     "1.5s",
		},
		{
			name:     "manifest times fallback",
			manifest: storage.Manifest{StartedAt: late, EndedAt: &ended},
			want:     "1s",
		},
		{
			name:     "missing end",
			manifest: storage.Manifest{StartedAt: late},
			want:     unknownValue,
		},
		{
			name:     "reversed end",
			manifest: storage.Manifest{StartedAt: late, EndedAt: &beforeStart},
			want:     unknownValue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runDuration(tt.manifest, tt.result); got != tt.want {
				t.Errorf("runDuration() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShowReportsTheProviderVersionWhenRecorded(t *testing.T) {
	root := home(t)
	b, err := storage.Create(root, "run-a", storage.Manifest{
		Provider:        "claude",
		ProviderVersion: "1.4.2",
		Argv:            []string{"claude"},
		StartedAt:       late,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := b.Finalize(storage.Finalization{EndedAt: late.Add(time.Second), ExitReason: "completed"}); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	code, stdout, stderr := run(t, "show", "run-a")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "Version      1.4.2") {
		t.Errorf("stdout =\n%s\nwant the recorded provider version", stdout)
	}
	// No process result, so the duration comes from the manifest alone.
	if !strings.Contains(stdout, "Duration     1s") {
		t.Errorf("stdout =\n%s\nwant the manifest-derived duration", stdout)
	}
}

func TestShowReportsTheTerminatingSignal(t *testing.T) {
	root := home(t)
	b, err := storage.Create(root, "run-a", storage.Manifest{
		Provider:  "claude",
		Argv:      []string{"claude"},
		StartedAt: late,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"startedAt":      late,
		"endedAt":        late.Add(time.Second),
		"durationMillis": 1000,
		"exitCode":       nil,
		"signal":         "SIGKILL",
		"exitReason":     "timeout",
	})
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}
	if err := b.WriteProcessResult(raw); err != nil {
		t.Fatalf("write result: %v", err)
	}
	if err := b.Finalize(storage.Finalization{EndedAt: late.Add(time.Second), ExitReason: "timeout", WarningCount: 2}); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	code, stdout, stderr := run(t, "show", "run-a")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	for _, want := range []string{"Exit Reason  timeout", "Signal       SIGKILL", "Warnings     2"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout =\n%s\nwant it to contain %q", stdout, want)
		}
	}
	if strings.Contains(stdout, "Exit Code") {
		t.Errorf("stdout =\n%s\nwant no exit code for a signalled run", stdout)
	}
}

func TestShowRejectsRunIDsThatAreNotOneCleanComponent(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")

	for _, id := range []string{"", ".", "..", "../run-a", "sub/run-a", "run-a/"} {
		code, stdout, stderr := run(t, "show", id)

		if code != 2 {
			t.Errorf("show %q exit code = %d, want 2", id, code)
		}
		if stdout != "" {
			t.Errorf("show %q stdout = %q, want empty", id, stdout)
		}
		if stderr == "" {
			t.Errorf("show %q stderr is empty, want an explanation", id)
		}
	}
}

func TestShowRequiresExactlyOneRunID(t *testing.T) {
	home(t)

	for _, args := range [][]string{{"show"}, {"show", "run-a", "extra"}} {
		code, _, stderr := run(t, args...)

		if code != 2 {
			t.Errorf("Run(%q) exit code = %d, want 2", args, code)
		}
		if !strings.Contains(stderr, "usage") {
			t.Errorf("Run(%q) stderr = %q, want it to state the usage", args, stderr)
		}
	}
}

func TestShowRefusesASymlinkedRunDirectory(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")
	if err := os.Symlink(filepath.Join(root, "run-a"), filepath.Join(root, "linked")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	code, stdout, stderr := run(t, "show", "linked")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "linked") {
		t.Errorf("stderr = %q, want it to name the refused run", stderr)
	}
}

func TestShowRefusesASymlinkedBundleFile(t *testing.T) {
	for _, name := range []string{"manifest.json", "actions.jsonl"} {
		t.Run(name, func(t *testing.T) {
			root := home(t)
			writeRun(t, root, "run-a", "claude", late, "completed")
			writeRun(t, root, "run-b", "claude", late, "completed")

			path := filepath.Join(root, "run-a", name)
			if err := os.Remove(path); err != nil {
				t.Fatalf("remove %s: %v", name, err)
			}
			if err := os.Symlink(filepath.Join(root, "run-b", name), path); err != nil {
				t.Fatalf("symlink %s: %v", name, err)
			}

			code, stdout, stderr := run(t, "show", "run-a")

			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, name) {
				t.Errorf("stderr = %q, want it to name the refused file", stderr)
			}
		})
	}
}

// A symlink standing where the process directory should be is a directory
// component, not a file, and the file underneath it is outside the bundle: the
// run must be refused rather than reported from evidence it does not hold.
func TestShowRefusesASymlinkedProcessDirectory(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "")

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, resultFile), []byte(`{"exitReason":"outside-marker","durationMillis":1}`), 0o600); err != nil {
		t.Fatalf("write outside result: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "run-a", processDir)); err != nil {
		t.Fatalf("symlink process directory: %v", err)
	}

	code, stdout, stderr := run(t, "show", "run-a")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, processDir) {
		t.Errorf("stderr = %q, want it to name the refused directory", stderr)
	}
	if strings.Contains(stdout+stderr, "outside-marker") {
		t.Errorf("the CLI reported content from outside the bundle:\n%s%s", stdout, stderr)
	}
}

func TestShowRefusesASymlinkedProcessResult(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "")

	outside := filepath.Join(t.TempDir(), resultFile)
	if err := os.WriteFile(outside, []byte(`{"exitReason":"outside-marker","durationMillis":1}`), 0o600); err != nil {
		t.Fatalf("write outside result: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "run-a", processDir), 0o700); err != nil {
		t.Fatalf("create process directory: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "run-a", processDir, resultFile)); err != nil {
		t.Fatalf("symlink result: %v", err)
	}

	code, stdout, stderr := run(t, "show", "run-a")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, resultFile) {
		t.Errorf("stderr = %q, want it to name the refused file", stderr)
	}
	if strings.Contains(stdout+stderr, "outside-marker") {
		t.Errorf("the CLI reported content from outside the bundle:\n%s%s", stdout, stderr)
	}
}

// The handle a read is given must be the file that was checked, not whatever
// the same name resolves to afterwards.
func TestOpenRegularReturnsTheFileItChecked(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")
	dir := filepath.Join(root, "run-a")

	f, err := openRegular(dir, manifestFile)
	if err != nil {
		t.Fatalf("open %s: %v", manifestFile, err)
	}
	defer f.Close()

	opened, err := f.Stat()
	if err != nil {
		t.Fatalf("stat opened file: %v", err)
	}
	onDisk, err := os.Lstat(filepath.Join(dir, manifestFile))
	if err != nil {
		t.Fatalf("lstat %s: %v", manifestFile, err)
	}
	if !os.SameFile(opened, onDisk) {
		t.Errorf("opened a different file than the one named in the bundle")
	}
}

func TestShowReportsMalformedActions(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")
	path := filepath.Join(root, "run-a", "actions.jsonl")
	if err := os.WriteFile(path, []byte("{\"id\":\"a1\"}\nnot json\n"), 0o600); err != nil {
		t.Fatalf("corrupt actions: %v", err)
	}

	code, stdout, stderr := run(t, "show", "run-a")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "actions.jsonl") || !strings.Contains(stderr, "line 2") {
		t.Errorf("stderr = %q, want it to name the file and the line", stderr)
	}
}

func TestShowReportsAnOversizeActionLine(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")
	huge := append([]byte(`{"id":"a1","type":"file.read","assurance":"provider_reported","x":"`), bytes.Repeat([]byte("a"), 5<<20)...)
	huge = append(huge, []byte("\"}\n")...)
	if err := os.WriteFile(filepath.Join(root, "run-a", "actions.jsonl"), huge, 0o600); err != nil {
		t.Fatalf("write oversize actions: %v", err)
	}

	code, _, stderr := run(t, "show", "run-a")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "actions.jsonl") {
		t.Errorf("stderr = %q, want it to name the oversize stream", stderr)
	}
}

// A stream of individually acceptable lines is still a stream that must not be
// loaded without end, so the whole stream is bounded as well as each line.
func TestShowReportsAnOversizeActionStream(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")

	f, err := os.Create(filepath.Join(root, "run-a", actionsFile))
	if err != nil {
		t.Fatalf("write actions: %v", err)
	}
	line := append([]byte(`{"id":"a1","type":"file.read","assurance":"provider_reported","pad":"`), bytes.Repeat([]byte("a"), 64<<10)...)
	line = append(line, []byte(`"}`+"\n")...)
	w := bufio.NewWriter(f)
	for written := 0; written <= maxActionStreamBytes; written += len(line) {
		if _, err := w.Write(line); err != nil {
			t.Fatalf("write actions: %v", err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush actions: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close actions: %v", err)
	}

	code, stdout, stderr := run(t, "show", "run-a")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, actionsFile) {
		t.Errorf("stderr = %q, want it to name the oversize stream", stderr)
	}
}

func TestShowReportsAnOversizeManifest(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")
	path := filepath.Join(root, "run-a", "manifest.json")
	if err := os.WriteFile(path, bytes.Repeat([]byte("a"), (1<<20)+1), 0o600); err != nil {
		t.Fatalf("write oversize manifest: %v", err)
	}

	code, _, stderr := run(t, "show", "run-a")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "manifest.json") {
		t.Errorf("stderr = %q, want it to name the oversize manifest", stderr)
	}
}

func TestShowReportsAMalformedProcessResult(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")
	path := filepath.Join(root, "run-a", "process", "result.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt result: %v", err)
	}

	code, stdout, stderr := run(t, "show", "run-a")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "result.json") {
		t.Errorf("stderr = %q, want it to name the unreadable result", stderr)
	}
}

func TestShowReportsAMissingRun(t *testing.T) {
	home(t)

	code, _, stderr := run(t, "show", "absent")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "absent") {
		t.Errorf("stderr = %q, want it to name the missing run", stderr)
	}
}

// The bundle is evidence, so reading it must leave every byte as it was.
func TestReadingARunDoesNotMutateItsBundle(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")
	before := snapshot(t, filepath.Join(root, "run-a"))

	if code, _, stderr := run(t, "show", "run-a"); code != 0 {
		t.Fatalf("show exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if code, _, stderr := run(t, "list"); code != 0 {
		t.Fatalf("list exit code = %d, want 0 (stderr %q)", code, stderr)
	}

	after := snapshot(t, filepath.Join(root, "run-a"))
	if len(before) != len(after) {
		t.Fatalf("bundle holds %d files after reading, had %d", len(after), len(before))
	}
	for name, content := range before {
		if after[name] != content {
			t.Errorf("%s changed after reading", name)
		}
	}
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[path] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", dir, err)
	}
	return files
}

// --- repository and verification evidence -----------------------------------
//
// The two evidence sources a run collects for itself are read back off disk the
// same way the rest of the bundle is: bounded, confined, and believed only as
// far as what they actually recorded.

// Digests as the evidence records them: whole, so a reader can compare one
// against the repository rather than against a prefix of it.
const (
	evidenceBaseline  = "1f0c2f2a2b6b4f2d9c8e7a6b5c4d3e2f1a0b9c8d"
	evidenceConfigSum = "3b1d2c4e5f60718293a4b5c6d7e8f90112233445566778899aabbccddeeff001"
)

// writeEvidence files one evidence document under a recorded run, the way the
// repository capture and the verification file theirs. A []byte body is written
// as it is, so a test can plant a document that is not the shape it claims.
func writeEvidence(t *testing.T, root, id, dir, name string, doc any) {
	t.Helper()
	at := filepath.Join(root, id, dir)
	if err := os.MkdirAll(at, 0o700); err != nil {
		t.Fatalf("create %s: %v", at, err)
	}
	raw, ok := doc.([]byte)
	if !ok {
		encoded, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("encode %s: %v", name, err)
		}
		raw = encoded
	}
	if err := os.WriteFile(filepath.Join(at, name), raw, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func writeGit(t *testing.T, root, id string, doc any) {
	t.Helper()
	writeEvidence(t, root, id, "git", "result.json", doc)
}

func writeVerification(t *testing.T, root, id string, doc any) {
	t.Helper()
	writeEvidence(t, root, id, "verification", "results.json", doc)
}

// availableGit is a repository capture that ran to completion: the counts a
// finished measurement holds, under the claim it is recorded with.
func availableGit() map[string]any {
	return map[string]any{
		"status":          "available",
		"attribution":     evidence.Attribution,
		"baseline":        evidenceBaseline,
		"trackedFiles":    2,
		"added":           32,
		"deleted":         8,
		"binaryTracked":   0,
		"untrackedFiles":  1,
		"storedTextFiles": 1,
	}
}

// passedVerification is a verification whose one pinned check ran and passed.
func passedVerification() map[string]any {
	return map[string]any{
		"status":       "passed",
		"attribution":  evidence.VerificationAttribution,
		"config":       verifyConfigFile,
		"configSha256": evidenceConfigSum,
		"checks": []map[string]any{{
			"name":       "test",
			"command":    []string{"./gradlew", "test"},
			"timeout":    "30s",
			"status":     "passed",
			"exitCode":   0,
			"durationMs": 8210,
		}},
	}
}

// wantEvidenceSections is what the two collected sources say about a run that
// changed the repository and was verified: what was measured, what it is
// claimed to mean, and how each pinned check ended.
const wantEvidenceSections = `REPOSITORY-OBSERVED CHANGES
  Status       AVAILABLE
  Files        3 (2 tracked, 1 untracked)
  Diff         +32/-8, 0 binary
  Stored Text  1
  Baseline     ` + evidenceBaseline + `
  Attribution  observed during run, not causal proof

VERIFICATION-OBSERVED RESULT
  Status       PASS
  Config       .agentrec.yaml
  Config SHA-256 ` + evidenceConfigSum + `
  Check        PASS test  "./gradlew" "test"  8.21s  exit 0
  Attribution  verification_observed
`

func TestShowRendersRepositoryAndVerificationEvidence(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-b", "claude", late, "completed")
	writeGit(t, root, "run-b", availableGit())
	writeVerification(t, root, "run-b", passedVerification())

	code, stdout, stderr := run(t, "show", "run-b")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if !strings.HasSuffix(stdout, wantEvidenceSections) {
		t.Errorf("stdout =\n%s\nwant it to end with\n%s", stdout, wantEvidenceSections)
	}
	// The provider's own timeline is untouched by the sections after it.
	if !strings.Contains(stdout, "10:00:01  READ  README.md") {
		t.Errorf("stdout =\n%s\nwant the recorded action kept", stdout)
	}
}

// A status is shown as it was recorded, not as a reader would like to read it:
// evidence that was never collected, could not be collected, or ended in a word
// this command does not know must never render as a run that succeeded.
func TestShowNeverReadsUnfinishedEvidenceAsSuccess(t *testing.T) {
	tests := []struct {
		name         string
		git          map[string]any
		verification map[string]any
		wantSections string
	}{
		{
			name: "pending",
			git: map[string]any{
				"status":      "pending",
				"attribution": evidence.Attribution,
			},
			verification: map[string]any{
				"status":       "pending",
				"attribution":  evidence.VerificationAttribution,
				"config":       verifyConfigFile,
				"configSha256": evidenceConfigSum,
				"checks": []map[string]any{{
					"name":    "test",
					"command": []string{"./gradlew", "test"},
					"timeout": "30s",
				}},
			},
			wantSections: `REPOSITORY-OBSERVED CHANGES
  Status       PENDING
  Attribution  observed during run, not causal proof

VERIFICATION-OBSERVED RESULT
  Status       PENDING
  Config       .agentrec.yaml
  Config SHA-256 ` + evidenceConfigSum + `
  Check        PENDING test  "./gradlew" "test"
  Attribution  verification_observed
`,
		},
		{
			name: "unavailable and failed",
			git: map[string]any{
				"status":      "unavailable",
				"reason":      "baseline_unreachable",
				"attribution": evidence.Attribution,
				// Counts a collection that failed cannot stand behind.
				"trackedFiles": 0,
				"added":        0,
			},
			verification: map[string]any{
				"status":       "failed",
				"attribution":  evidence.VerificationAttribution,
				"config":       verifyConfigFile,
				"configSha256": evidenceConfigSum,
				"checks": []map[string]any{{
					"name":       "test",
					"command":    []string{"./gradlew", "test"},
					"timeout":    "30s",
					"status":     "failed",
					"exitCode":   3,
					"durationMs": 1500,
				}},
			},
			wantSections: `REPOSITORY-OBSERVED CHANGES
  Status       NOT RECORDED
  Reason       baseline_unreachable
  Attribution  observed during run, not causal proof

VERIFICATION-OBSERVED RESULT
  Status       FAIL
  Config       .agentrec.yaml
  Config SHA-256 ` + evidenceConfigSum + `
  Check        FAIL test  "./gradlew" "test"  1.5s  exit 3
  Attribution  verification_observed
`,
		},
		{
			name: "tainted",
			git:  availableGit(),
			verification: map[string]any{
				"status":       "tainted",
				"reason":       "config_changed",
				"attribution":  evidence.VerificationAttribution,
				"config":       verifyConfigFile,
				"configSha256": evidenceConfigSum,
				"checks": []map[string]any{{
					"name":    "test",
					"command": []string{"./gradlew", "test"},
					"timeout": "30s",
				}},
			},
			wantSections: `VERIFICATION-OBSERVED RESULT
  Status       TAINTED
  Reason       config_changed
  Config       .agentrec.yaml
  Config SHA-256 ` + evidenceConfigSum + `
  Check        PENDING test  "./gradlew" "test"
  Attribution  verification_observed
`,
		},
		{
			name: "words this command does not know",
			git: map[string]any{
				"status":      "partially_collected",
				"attribution": evidence.Attribution,
			},
			verification: map[string]any{
				"status":       "inconclusive",
				"attribution":  evidence.VerificationAttribution,
				"config":       verifyConfigFile,
				"configSha256": evidenceConfigSum,
				"checks": []map[string]any{{
					"name":       "test",
					"command":    []string{"./gradlew", "test"},
					"timeout":    "30s",
					"status":     "timeout",
					"signal":     "SIGKILL",
					"durationMs": 30000,
				}},
			},
			wantSections: `REPOSITORY-OBSERVED CHANGES
  Status       PARTIALLY_COLLECTED
  Attribution  observed during run, not causal proof

VERIFICATION-OBSERVED RESULT
  Status       INCONCLUSIVE
  Config       .agentrec.yaml
  Config SHA-256 ` + evidenceConfigSum + `
  Check        TIMEOUT test  "./gradlew" "test"  30s  signal SIGKILL  timeout 30s
  Attribution  verification_observed
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := home(t)
			writeRun(t, root, "run-b", "claude", late, "completed")
			writeGit(t, root, "run-b", tt.git)
			writeVerification(t, root, "run-b", tt.verification)

			code, stdout, stderr := run(t, "show", "run-b")

			if code != 0 {
				t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
			}
			if !strings.Contains(stdout, tt.wantSections) {
				t.Errorf("stdout =\n%s\nwant it to contain\n%s", stdout, tt.wantSections)
			}
			if strings.Contains(stdout, "Status       PASS") {
				t.Errorf("stdout =\n%s\nwant no passing verdict for checks that did not pass", stdout)
			}
		})
	}
}

// A warning says something about the conditions the checks reported under, and
// is never folded into a check's own verdict: a verification whose checks all
// passed still says what the checks did to the repository.
func TestShowReportsVerificationWarningsApartFromCheckResults(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-b", "claude", late, "completed")
	doc := passedVerification()
	doc["warnings"] = []map[string]any{{
		"code": "verification_mutated_repository",
		// Recorded in an order a reader must not have to trust.
		"paths": []string{"src/b.txt", "build/out.bin", "src/a.txt"},
	}}
	writeVerification(t, root, "run-b", doc)

	code, stdout, stderr := run(t, "show", "run-b")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	want := `  Status       PASS
  Config       .agentrec.yaml
  Config SHA-256 ` + evidenceConfigSum + `
  Check        PASS test  "./gradlew" "test"  8.21s  exit 0
  Warning      verification_mutated_repository  build/out.bin, src/a.txt, src/b.txt
  Attribution  verification_observed
`
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout =\n%s\nwant it to contain\n%s", stdout, want)
	}
}

// Evidence is read back off a filesystem where anything may have replaced it,
// so a document that is not the one this run wrote is refused rather than
// summarized: a report is only worth reading if what it quotes is real.
func TestShowRefusesEvidenceItCannotTrust(t *testing.T) {
	tests := []struct {
		name  string
		plant func(t *testing.T, root, id string)
	}{
		{"malformed git", func(t *testing.T, root, id string) {
			writeGit(t, root, id, []byte(`{"status":`))
		}},
		{"malformed verification", func(t *testing.T, root, id string) {
			writeVerification(t, root, id, []byte(`not json`))
		}},
		{"oversize git", func(t *testing.T, root, id string) {
			doc := availableGit()
			doc["reason"] = strings.Repeat("x", maxDocumentBytes+1)
			writeGit(t, root, id, doc)
		}},
		{"oversize verification", func(t *testing.T, root, id string) {
			doc := passedVerification()
			doc["reason"] = strings.Repeat("x", maxDocumentBytes+1)
			writeVerification(t, root, id, doc)
		}},
		{"git result is a symlink", func(t *testing.T, root, id string) {
			outside := filepath.Join(t.TempDir(), "result.json")
			writeEvidence(t, root, id, "git", "placeholder", availableGit())
			if err := os.WriteFile(outside, mustJSON(t, availableGit()), 0o600); err != nil {
				t.Fatalf("write %s: %v", outside, err)
			}
			if err := os.Symlink(outside, filepath.Join(root, id, "git", "result.json")); err != nil {
				t.Fatalf("plant symlink: %v", err)
			}
		}},
		{"git directory is a symlink", func(t *testing.T, root, id string) {
			outside := t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "result.json"), mustJSON(t, availableGit()), 0o600); err != nil {
				t.Fatalf("write result: %v", err)
			}
			if err := os.Symlink(outside, filepath.Join(root, id, "git")); err != nil {
				t.Fatalf("plant symlink: %v", err)
			}
		}},
		{"verification directory is a symlink", func(t *testing.T, root, id string) {
			outside := t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "results.json"), mustJSON(t, passedVerification()), 0o600); err != nil {
				t.Fatalf("write results: %v", err)
			}
			if err := os.Symlink(outside, filepath.Join(root, id, "verification")); err != nil {
				t.Fatalf("plant symlink: %v", err)
			}
		}},
		{"git claims another meaning", func(t *testing.T, root, id string) {
			doc := availableGit()
			doc["attribution"] = "proof the agent made these changes"
			writeGit(t, root, id, doc)
		}},
		{"verification claims another meaning", func(t *testing.T, root, id string) {
			doc := passedVerification()
			doc["attribution"] = "the agent verified its own work"
			writeVerification(t, root, id, doc)
		}},
		{"git has no status", func(t *testing.T, root, id string) {
			doc := availableGit()
			delete(doc, "status")
			writeGit(t, root, id, doc)
		}},
		{"verification has no status", func(t *testing.T, root, id string) {
			doc := passedVerification()
			delete(doc, "status")
			writeVerification(t, root, id, doc)
		}},
		{"negative counts", func(t *testing.T, root, id string) {
			doc := availableGit()
			doc["added"] = -1
			writeGit(t, root, id, doc)
		}},
		{"negative duration", func(t *testing.T, root, id string) {
			doc := passedVerification()
			doc["checks"].([]map[string]any)[0]["durationMs"] = -1
			writeVerification(t, root, id, doc)
		}},
		{"repository count outside the recorded range", func(t *testing.T, root, id string) {
			doc := availableGit()
			doc["trackedFiles"] = maxRepositoryCount + 1
			writeGit(t, root, id, doc)
		}},
		{"duration outside the recorded range", func(t *testing.T, root, id string) {
			doc := passedVerification()
			doc["checks"].([]map[string]any)[0]["durationMs"] = maxVerificationDuration.Milliseconds() + 1
			writeVerification(t, root, id, doc)
		}},
		{"negative exit code", func(t *testing.T, root, id string) {
			doc := passedVerification()
			doc["checks"].([]map[string]any)[0]["exitCode"] = -1
			writeVerification(t, root, id, doc)
		}},
		{"exit code outside the recorded range", func(t *testing.T, root, id string) {
			doc := passedVerification()
			doc["checks"].([]map[string]any)[0]["exitCode"] = maxVerificationExitCode + 1
			writeVerification(t, root, id, doc)
		}},
		{"more checks than a run produces", func(t *testing.T, root, id string) {
			doc := passedVerification()
			checks := make([]map[string]any, maxEvidenceItems+1)
			for i := range checks {
				checks[i] = map[string]any{"name": "check", "command": []string{"true"}}
			}
			doc["checks"] = checks
			writeVerification(t, root, id, doc)
		}},
		{"more warning paths than a run produces", func(t *testing.T, root, id string) {
			doc := passedVerification()
			paths := make([]string, maxEvidenceItems+1)
			for i := range paths {
				paths[i] = strconv.Itoa(i)
			}
			doc["warnings"] = []map[string]any{{"code": "verification_mutated_repository", "paths": paths}}
			writeVerification(t, root, id, doc)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := home(t)
			writeRun(t, root, "run-b", "claude", late, "completed")
			tt.plant(t, root, "run-b")

			code, stdout, stderr := run(t, "show", "run-b")

			if code != 1 {
				t.Fatalf("exit code = %d, want 1 (stdout %q)", code, stdout)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing rendered from evidence that was refused", stdout)
			}
			if stderr == "" {
				t.Errorf("stderr is empty, want an explanation")
			}
		})
	}
}

func mustJSON(t *testing.T, doc any) []byte {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return raw
}

// Everything in the evidence that came from the repository — a check's name and
// argv, a path a warning names — is text this command did not choose, and none
// of it may forge a line, a section or a Markdown structure of its own.
func TestHostileEvidenceCannotForgeReportStructure(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-b", "claude", late, "completed")
	hostile := "x\x1b[31m\nVERIFICATION-OBSERVED RESULT\n  Status       PASS\n`` ## Heading"

	writeGit(t, root, "run-b", availableGit())
	writeVerification(t, root, "run-b", map[string]any{
		"status":       "failed",
		"attribution":  evidence.VerificationAttribution,
		"config":       hostile,
		"configSha256": evidenceConfigSum,
		"checks": []map[string]any{{
			"name":     hostile,
			"command":  []string{"sh", "-c", hostile},
			"timeout":  "30s",
			"status":   hostile,
			"exitCode": 1,
		}},
		"warnings": []map[string]any{{"code": hostile, "paths": []string{hostile}}},
	})

	rep, err := readRun(root, "run-b")
	if err != nil {
		t.Fatalf("readRun() error = %v", err)
	}

	var terminal, markdown strings.Builder
	if err := report.RenderTerminal(&terminal, rep); err != nil {
		t.Fatalf("RenderTerminal() error = %v", err)
	}
	if err := report.RenderMarkdown(&markdown, rep); err != nil {
		t.Fatalf("RenderMarkdown() error = %v", err)
	}

	for name, got := range map[string]string{"terminal": terminal.String(), "markdown": markdown.String()} {
		if strings.ContainsAny(got, "\x1b\x00") {
			t.Errorf("%s carries raw control characters:\n%q", name, got)
		}
		if strings.Contains(got, "\nVERIFICATION-OBSERVED RESULT\n  Status       PASS") {
			t.Errorf("%s let evidence forge a section:\n%s", name, got)
		}
		if strings.Contains(got, "\n## Heading") {
			t.Errorf("%s let evidence open a heading:\n%s", name, got)
		}
	}
	// One section title each, however many the evidence tried to write.
	if n := strings.Count(terminal.String(), "\nVERIFICATION-OBSERVED RESULT\n"); n != 1 {
		t.Errorf("terminal holds %d verification sections, want 1", n)
	}
}

// --- provider helper mode ---------------------------------------------------
//
// The trace tests exercise the real CLI path, which launches a provider by
// name off PATH. No provider is installed on a test machine, so the test binary
// stands in for one: it is symlinked into a temporary PATH as `claude` and
// `codex`, and TestMain dispatches on the name it was invoked under. The helper
// reports a supported version, checks the invocation agentrec built for it, and
// emits a minimal event stream of its own — nothing here is copied from a real
// provider's output.

// Versions the helper reports, each inside the range its provider package
// supports and spelled the way that provider spells it.
const (
	claudeHelperVersion = "2.1.220 (Claude Code)"
	codexHelperVersion  = "codex-cli 0.144.6"
)

// helperContractExit is what a helper exits with when agentrec launched it
// wrongly, so a broken invocation fails the test that expected a clean run
// rather than passing on a stream the provider would never have produced.
const helperContractExit = 3

// startedEnv names a file the helper creates when it is launched to record a
// run, which is how a test can tell that a run agentrec should have refused was
// never started. Reporting a version is preparation and not a run, so it leaves
// no mark.
const startedEnv = "AGENTREC_TEST_PROVIDER_STARTED"

// providerVersionEnv lets a CLI fixture report an exact version without ever
// consulting an installed provider binary.
const providerVersionEnv = "AGENTREC_TEST_PROVIDER_VERSION"

// agentrecName is the name the test binary is symlinked under when it is the
// recorder itself rather than a provider, for the one test that has to signal
// agentrec as the operating system would rather than call it in process.
const agentrecName = "agentrec"

// lingerEnv names a file the helper writes its pid into before waiting to be
// signalled, which is how a test drives a run that is still going when the
// recorder is asked to stop. The wait is a backstop rather than a delay any
// passing test sits through: the provider is signalled long before it elapses.
const lingerEnv = "AGENTREC_TEST_PROVIDER_LINGER"
const lingerDurationEnv = "AGENTREC_TEST_PROVIDER_LINGER_DURATION"

const lingerLimit = 2 * time.Minute

// failPrompt asks the helper to end nonzero after a complete stream, which is
// what a provider does when the work it recorded did not succeed.
const failPrompt = "fail"

// Env options the helper acts on once it has been launched to record a run, so
// that a test can drive a provider which changes the repository the way a real
// agent would: a tracked file it overwrites, an untracked file it creates, and a
// demand that the baseline was pinned before it was allowed to change anything.
const (
	trackedEnv    = "AGENTREC_TEST_PROVIDER_TRACKED"
	untrackedEnv  = "AGENTREC_TEST_PROVIDER_UNTRACKED"
	requireRefEnv = "AGENTREC_TEST_PROVIDER_REQUIRE_REF"
)

// refNamespace is where agentrec pins the baseline it measures a run against.
const refNamespace = "refs/agentrec/"

// helperToken is a synthetic secret the helper writes into everything it
// changes, so a test can require the recorded evidence to name it with a marker
// rather than carry it.
const helperToken = "ghp_syntheticMMMMNNNNOOOOPPPP"

// helperContent is what the helper writes into the files it was asked to change.
// It is two lines where the fixture's tracked file held one, so the recorded
// statistics have exactly one right answer.
const helperContent = "changed by the helper\ntoken = " + helperToken + "\n"

// changeRepository carries out the changes the helper was asked to make. The
// baseline ref is checked first when the test demands it: the commit agentrec
// measures the run against has to be pinned before the provider can change
// anything, and a helper that ran without it recorded nothing worth comparing.
func changeRepository() error {
	if os.Getenv(requireRefEnv) != "" {
		out, err := exec.Command("git", "for-each-ref", "--format=%(refname)", refNamespace).Output()
		if err != nil {
			return fmt.Errorf("list %s: %v", refNamespace, err)
		}
		if len(bytes.TrimSpace(out)) == 0 {
			return fmt.Errorf("no %s ref pinned the baseline", refNamespace)
		}
	}
	for _, env := range []string{trackedEnv, untrackedEnv} {
		name := os.Getenv(env)
		if name == "" {
			continue
		}
		if err := os.WriteFile(name, []byte(helperContent), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// forbiddenFlags are the permission, sandbox and approval overrides agentrec
// must never inject: what the operator may do is the operator's decision, and a
// recorder that widens it silently is recording a different run than the one
// asked for.
var forbiddenFlags = []string{
	"--dangerously-skip-permissions",
	"--dangerously-bypass-approvals-and-sandbox",
	"--permission-mode",
	"--allowedTools",
	"--allowed-tools",
	"--sandbox",
	"--ask-for-approval",
	"--full-auto",
	"--yolo",
}

// claudeStream is a minimal stream-json Read lifecycle: the tool use, then the
// result that closes it.
const claudeStream = `{"type":"assistant","timestamp":"2026-07-27T10:00:01Z","message":{"content":[{"type":"tool_use","id":"tool-1","name":"Read","input":{"file_path":"README.md"}}]}}
{"type":"user","timestamp":"2026-07-27T10:00:02Z","message":{"content":[{"type":"tool_result","tool_use_id":"tool-1","content":"file contents"}]}}
`

// codexStream is a minimal JSONL command lifecycle: the item started, then the
// item completed with its exit code.
const codexStream = `{"type":"item.started","timestamp":"2026-07-27T10:00:01Z","item":{"id":"item-1","type":"command_execution","command":"echo hi","status":"in_progress"}}
{"type":"item.completed","timestamp":"2026-07-27T10:00:02Z","item":{"id":"item-1","type":"command_execution","command":"echo hi","status":"completed","aggregated_output":"hi","exit_code":0}}
`

func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case agentrecName:
		installReleasePause()
		installSignalForwardMarker()
		os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr))
	case "claude":
		os.Exit(claudeHelper(os.Args[1:]))
	case "codex":
		os.Exit(codexHelper(os.Args[1:]))
	case verifyHelperName:
		os.Exit(verifyHelper(os.Args[1:]))
	case gitHelperName:
		os.Exit(gitHelper(os.Args[1:]))
	}
	os.Exit(m.Run())
}

const (
	releasePauseEnv  = "AGENTREC_TEST_RELEASE_PAUSE"
	releaseResumeEnv = "AGENTREC_TEST_RELEASE_RESUME"
	signalForwardEnv = "AGENTREC_TEST_SIGNAL_FORWARDED"
	versionPauseEnv  = "AGENTREC_TEST_VERSION_PAUSE"
	versionResumeEnv = "AGENTREC_TEST_VERSION_RESUME"
)

func pauseVersionProbe() error {
	ready := os.Getenv(versionPauseEnv)
	if ready == "" {
		return nil
	}
	if err := os.WriteFile(ready, []byte("probing\n"), 0o600); err != nil {
		return err
	}
	if !waitForFile(os.Getenv(versionResumeEnv), lingerLimit) {
		return errors.New("test version probe was never resumed")
	}
	return nil
}

func installSignalForwardMarker() {
	path := os.Getenv(signalForwardEnv)
	if path == "" {
		return
	}
	commandSignalForwarded = func() {
		if err := os.WriteFile(path, []byte("forwarded\n"), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "signal marker:", err)
		}
	}
}

func installReleasePause() {
	ready := os.Getenv(releasePauseEnv)
	if ready == "" {
		return
	}
	release := releaseRepository
	releaseRepository = func(repo *lock.Repository) error {
		if err := os.WriteFile(ready, []byte("releasing\n"), 0o600); err != nil {
			return err
		}
		if !waitForFile(os.Getenv(releaseResumeEnv), lingerLimit) {
			return errors.New("test release pause was never resumed")
		}
		return release(repo)
	}
}

// --- git stand-in mode -------------------------------------------------------
//
// agentrec asks the repository its questions by launching Git directly, so the
// test binary can stand in for Git the same way it stands in for a provider.
// The stand-in only exists to hold one command open: it announces the question
// it was asked, waits to be released, and then hands the question to the real
// Git and reports back exactly what Git said. That window is where a test puts
// a signal it needs to arrive at a particular moment of the recording.

// gitHelperName is the name the test binary is symlinked under when it stands
// in for Git.
const gitHelperName = "git"

const (
	// gitRealEnv names the Git the stand-in delegates to.
	gitRealEnv = "AGENTREC_TEST_GIT_REAL"
	// gitPauseEnv names the file the stand-in creates when it has been asked
	// the question a test is waiting for.
	gitPauseEnv = "AGENTREC_TEST_GIT_PAUSE"
	// gitResumeEnv names the file a test creates to release the stand-in.
	gitResumeEnv = "AGENTREC_TEST_GIT_RESUME"
)

// gitPauseQuestion is the argument that identifies the repository measurement
// agentrec makes once the provider has ended: the diff from the pinned baseline
// is asked for with --binary and nothing else agentrec runs uses it.
const gitPauseQuestion = "--binary"

func gitHelper(args []string) int {
	real := os.Getenv(gitRealEnv)
	if real == "" {
		fmt.Fprintln(os.Stderr, "git helper: no git to delegate to")
		return helperContractExit
	}
	if pause := os.Getenv(gitPauseEnv); pause != "" && slices.Contains(args, gitPauseQuestion) {
		if err := os.WriteFile(pause, []byte("measuring\n"), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "git helper:", err)
			return helperContractExit
		}
		if !waitForFile(os.Getenv(gitResumeEnv), lingerLimit) {
			fmt.Fprintln(os.Stderr, "git helper: never released")
			return helperContractExit
		}
	}
	cmd := exec.Command(real, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	fmt.Fprintln(os.Stderr, "git helper:", err)
	return helperContractExit
}

// waitForFile reports whether path appeared before the deadline.
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

// --- verification helper mode ------------------------------------------------
//
// A pinned check is an argv agentrec launches directly, with no shell anywhere,
// so the test binary stands in for one the same way it stands in for a provider.
// Everything the check does is decided by the argv a test wrote into the
// configuration, which is the only thing that reaches it.

// verifyHelperName is the name the test binary is symlinked under when it is a
// verification check rather than a provider.
const verifyHelperName = "verify-helper"

// verifyHelperMark is what a check writes where a test told it to, so a test
// can tell a check that ran from one that was never allowed to.
const verifyHelperMark = "ran\n"

// verifyHelperContent is what a check writes into the repository when a test
// drives one that changes the work it is judging.
const verifyHelperContent = "written by the check\n"

func verifyHelper(args []string) int {
	if len(args) != 1 && len(args) != 2 {
		fmt.Fprintf(os.Stderr, "verify helper: %q is not a mode and its argument\n", args)
		return helperContractExit
	}
	switch args[0] {
	case "pass":
		return 0
	case "fail":
		code, err := strconv.Atoi(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "verify helper:", err)
			return helperContractExit
		}
		return code
	case "marker":
		return verifyHelperWrite(args[1], verifyHelperMark)
	case "write":
		return verifyHelperWrite(args[1], verifyHelperContent)
	}
	fmt.Fprintf(os.Stderr, "verify helper: %q is not a mode\n", args[0])
	return helperContractExit
}

func verifyHelperWrite(path, content string) int {
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "verify helper:", err)
		return helperContractExit
	}
	return 0
}

// commitVerifyConfig gives the repository the configuration a verified run is
// pinned to. It is committed rather than left in the worktree: agentrec records
// only a clean repository, so a configuration that is not already part of the
// baseline is one no run could be started with.
func commitVerifyConfig(t *testing.T, repo string, argv ...string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("version: 1\nverify:\n  - name: \"check\"\n    timeout: \"30s\"\n    command:\n")
	for _, arg := range argv {
		fmt.Fprintf(&b, "      - %s\n", strconv.Quote(arg))
	}
	writeVerifyConfig(t, repo, b.String())
	gitIn(t, repo, "add", verifyConfigFile)
	gitIn(t, repo, "commit", "-m", "pin verification")
}

func writeVerifyConfig(t *testing.T, repo, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, verifyConfigFile), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", verifyConfigFile, err)
	}
}

// verifyResultDoc is the verification status document as an operator's tooling
// would read it back off disk.
type verifyResultDoc struct {
	Status       string `json:"status"`
	Reason       string `json:"reason"`
	Attribution  string `json:"attribution"`
	Config       string `json:"config"`
	ConfigSHA256 string `json:"configSha256"`
	Checks       []struct {
		Name     string `json:"name"`
		Status   string `json:"status"`
		ExitCode *int   `json:"exitCode"`
	} `json:"checks"`
	Warnings []struct {
		Code  string   `json:"code"`
		Paths []string `json:"paths"`
	} `json:"warnings"`
}

func readVerifyResult(t *testing.T, dir string) verifyResultDoc {
	t.Helper()
	var doc verifyResultDoc
	readJSONFile(t, filepath.Join(dir, "verification", "results.json"), &doc)
	return doc
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// exists reports whether a path is there at all, which is how a test asks
// whether a check that should have been refused nevertheless ran.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// A verified run executes the checks the repository already held, after the
// provider has stopped, and files their verdict under the run.
func TestTraceVerifiesTheRunAgainstThePinnedConfiguration(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	started := providerStarted(t)
	stubProviders(t, "claude", verifyHelperName)
	marker := filepath.Join(t.TempDir(), "checked")
	commitVerifyConfig(t, repo, verifyHelperName, "marker", marker)

	code, stdout, stderr := run(t, "trace", "claude", "--verify", "--", "-p", "read the README")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if !started() {
		t.Fatalf("the provider was never launched")
	}
	if !exists(marker) {
		t.Errorf("the pinned check never ran")
	}
	dir := filepath.Join(root, traceRunID(t, stdout))

	// The flag is agentrec's own: what the operator asked the provider to do is
	// everything after the delimiter and nothing else.
	manifest, err := readManifest(dir)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if slices.Contains(manifest.Argv, verifyFlag) {
		t.Errorf("argv = %q, want %q kept off the provider's invocation", manifest.Argv, verifyFlag)
	}

	res := readVerifyResult(t, dir)
	if res.Status != "passed" || res.Reason != "" || res.Attribution == "" {
		t.Errorf("results.json = %+v, want a passed verification that says what it means", res)
	}
	if res.Config != verifyConfigFile {
		t.Errorf("results.json config = %q, want %q", res.Config, verifyConfigFile)
	}
	if want := fileSHA256(t, filepath.Join(repo, verifyConfigFile)); res.ConfigSHA256 != want {
		t.Errorf("results.json configSha256 = %q, want %q", res.ConfigSHA256, want)
	}
	if len(res.Checks) != 1 {
		t.Fatalf("results.json checks = %+v, want the one pinned check", res.Checks)
	}
	check := res.Checks[0]
	if check.Name != "check" || check.Status != "passed" || check.ExitCode == nil || *check.ExitCode != 0 {
		t.Errorf("results.json check = %+v, want the pinned check reported as passed", check)
	}
}

// A verification the run did not survive is the recorder's own finding, and it
// ends the command as a failure whatever the provider itself reported.
func TestTraceReportsAFailedVerification(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	stubProviders(t, "claude", verifyHelperName)
	commitVerifyConfig(t, repo, verifyHelperName, "fail", "3")

	code, stdout, stderr := run(t, "trace", "claude", "--verify", "--", "-p", "read the README")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr %q)", code, stderr)
	}
	res := readVerifyResult(t, filepath.Join(root, traceRunID(t, stdout)))
	if res.Status != "failed" {
		t.Errorf("results.json status = %q, want %q", res.Status, "failed")
	}
	if len(res.Checks) != 1 {
		t.Fatalf("results.json checks = %+v, want the one pinned check", res.Checks)
	}
	// The check's own ending is preserved rather than reduced to the command's.
	check := res.Checks[0]
	if check.Status != "failed" || check.ExitCode == nil || *check.ExitCode != 3 {
		t.Errorf("results.json check = %+v, want the check's own exit code recorded", check)
	}
}

// A configuration the run rewrote is not the one an operator reviewed, so
// nothing from it is executed and the run says why.
func TestTraceRefusesAVerificationTheRunRewrote(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	stubProviders(t, "claude", verifyHelperName)
	marker := filepath.Join(t.TempDir(), "checked")
	commitVerifyConfig(t, repo, verifyHelperName, "marker", marker)
	t.Setenv(trackedEnv, verifyConfigFile)

	code, stdout, stderr := run(t, "trace", "claude", "--verify", "--", "-p", "rewrite the verification")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr %q)", code, stderr)
	}
	if exists(marker) {
		t.Errorf("a check ran against a configuration the run rewrote")
	}
	res := readVerifyResult(t, filepath.Join(root, traceRunID(t, stdout)))
	if res.Status != "tainted" || res.Reason != "config_changed" {
		t.Errorf("results.json status = %q, reason = %q, want tainted(config_changed)", res.Status, res.Reason)
	}
}

// A check that changes the repository is reported for what it did and never
// undone — and what the run itself changed was measured before the checks ran,
// so the check's own writing cannot be read as the agent's work.
func TestTraceReportsAVerificationThatChangedTheRepository(t *testing.T) {
	const written = "verified.txt"
	root := home(t)
	repo := cleanRepo(t)
	stubProviders(t, "claude", verifyHelperName)
	commitVerifyConfig(t, repo, verifyHelperName, "write", written)

	code, stdout, stderr := run(t, "trace", "claude", "--verify", "--", "-p", "read the README")

	// The checks passed, and what they did to the repository is a warning about
	// the conditions rather than the provider's ending rewritten.
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	dir := filepath.Join(root, traceRunID(t, stdout))
	res := readVerifyResult(t, dir)
	if res.Status != "passed" {
		t.Errorf("results.json status = %q, want %q", res.Status, "passed")
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Code != "verification_mutated_repository" || !slices.Contains(res.Warnings[0].Paths, written) {
		t.Errorf("results.json warnings = %+v, want the changed path reported", res.Warnings)
	}
	if got := readFileString(t, filepath.Join(repo, written)); got != verifyHelperContent {
		t.Errorf("%s = %q, want the check's own writing left alone", written, got)
	}

	// The repository evidence was finalized before the checks ran, so it holds
	// what the provider left and nothing the verification added.
	var untracked struct {
		Count int `json:"count"`
	}
	readJSONFile(t, filepath.Join(dir, "git", "untracked.json"), &untracked)
	if untracked.Count != 0 {
		t.Errorf("untracked.json count = %d, want the run's own changes only", untracked.Count)
	}
	if patch := readFileString(t, filepath.Join(dir, "git", "tracked.patch")); strings.Contains(patch, written) {
		t.Errorf("tracked.patch =\n%s\nwant nothing the verification wrote", patch)
	}
}

// Without the flag there is no verification at all: an absent document is a run
// whose checks were never asked for, and it must not be confused with one whose
// checks were asked for and never reached.
func TestTraceWithoutVerifyRecordsNoVerification(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	stubProviders(t, "claude", verifyHelperName)
	commitVerifyConfig(t, repo, verifyHelperName, "pass")

	code, stdout, stderr := run(t, "trace", "claude", "--", "-p", "read the README")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if dir := filepath.Join(root, traceRunID(t, stdout), "verification"); exists(dir) {
		t.Errorf("%s exists, want no verification for a run that asked for none", dir)
	}
}

// A verification that cannot be pinned is a run that cannot say afterwards what
// it was checked against, so the provider is never launched.
func TestTraceRefusesToVerifyWithoutAPinnableConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(t *testing.T, repo string)
	}{
		{"missing", func(*testing.T, string) {}},
		{"empty checks", func(t *testing.T, repo string) {
			writeVerifyConfig(t, repo, "version: 1\nverify: []\n")
			gitIn(t, repo, "add", ".")
			gitIn(t, repo, "commit", "-m", "add empty verification")
		}},
		{"omitted checks", func(t *testing.T, repo string) {
			writeVerifyConfig(t, repo, "version: 1\n")
			gitIn(t, repo, "add", ".")
			gitIn(t, repo, "commit", "-m", "omit verification checks")
		}},
		{"null checks", func(t *testing.T, repo string) {
			writeVerifyConfig(t, repo, "version: 1\nverify: null\n")
			gitIn(t, repo, "add", ".")
			gitIn(t, repo, "commit", "-m", "null verification checks")
		}},
		{"unknown version", func(t *testing.T, repo string) {
			commitVerifyConfig(t, repo, verifyHelperName, "pass")
			writeVerifyConfig(t, repo, "version: 2\nverify: []\n")
			gitIn(t, repo, "commit", "-am", "change the schema")
		}},
		{"unreadable", func(t *testing.T, repo string) {
			commitVerifyConfig(t, repo, verifyHelperName, "pass")
			writeVerifyConfig(t, repo, "version: 1\nverify:\n  - name: check\n")
			gitIn(t, repo, "commit", "-am", "drop the command")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := home(t)
			repo := cleanRepo(t)
			started := providerStarted(t)
			stubProviders(t, "claude", verifyHelperName)
			tc.write(t, repo)

			code, stdout, stderr := run(t, "trace", "claude", "--verify", "--", "-p", "read the README")

			if code != 1 {
				t.Fatalf("exit code = %d, want 1 (stderr %q)", code, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if stderr == "" {
				t.Errorf("stderr is empty, want an explanation")
			}
			if started() {
				t.Errorf("the provider was launched for a run agentrec could not verify")
			}

			// The bundle is left describing a run that stopped rather than one
			// still going, holds no verification it never ran, and the baseline
			// ref the capture pinned is back out of the repository.
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 {
				t.Fatalf("runs root holds %v (%v), want the one refused run", entries, err)
			}
			dir := filepath.Join(root, entries[0].Name())
			manifest, err := readManifest(dir)
			if err != nil {
				t.Fatalf("read manifest: %v", err)
			}
			if manifest.ExitReason != runner.ReasonStorageError || manifest.EndedAt == nil {
				t.Errorf("manifest exit reason = %q, ended = %v, want a run recorded as stopped", manifest.ExitReason, manifest.EndedAt)
			}
			if verification := filepath.Join(dir, "verification"); exists(verification) {
				t.Errorf("%s exists, want no verification for checks that never ran", verification)
			}
			var git struct {
				Status string `json:"status"`
			}
			readJSONFile(t, filepath.Join(dir, "git", "result.json"), &git)
			if git.Status != "pending" {
				t.Errorf("git/result.json status = %q, want the collection left pending", git.Status)
			}
			if refs := gitIn(t, repo, "for-each-ref", "--format=%(refname)", refNamespace); refs != "" {
				t.Errorf("%s holds %q, want the temporary ref removed", refNamespace, refs)
			}
		})
	}
}

func helperProviderVersion(fallback string) string {
	if version := os.Getenv(providerVersionEnv); version != "" {
		return version
	}
	return fallback
}

func claudeHelper(args []string) int {
	if slices.Equal(args, []string{"--version"}) {
		if err := pauseVersionProbe(); err != nil {
			fmt.Fprintln(os.Stderr, "claude helper:", err)
			return helperContractExit
		}
		fmt.Println(helperProviderVersion(claudeHelperVersion))
		return 0
	}
	markStarted()
	if err := probeWorkspace("claude"); err != nil {
		fmt.Fprintln(os.Stderr, "claude helper:", err)
		return helperContractExit
	}
	if err := changeRepository(); err != nil {
		fmt.Fprintln(os.Stderr, "claude helper:", err)
		return helperContractExit
	}
	if err := mutateSourceCheckout("claude"); err != nil {
		fmt.Fprintln(os.Stderr, "claude helper:", err)
		return helperContractExit
	}
	if err := checkInvocation(args, [][]string{
		{"--output-format", "stream-json"},
		{"--verbose"},
		{"--include-hook-events"},
		{"-p"},
	}); err != nil {
		fmt.Fprintln(os.Stderr, "claude helper:", err)
		return helperContractExit
	}
	fmt.Print(claudeStream)
	linger()
	return providerExit(args)
}

// linger records the pid of the group agentrec put this provider in and then
// waits to be signalled, so a test can stop the recorder while the run it is
// recording is still going. The pid is renamed into place, so a test that finds
// the file finds a whole pid in it.
func linger() {
	path := os.Getenv(lingerEnv)
	if path == "" {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "helper:", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		fmt.Fprintln(os.Stderr, "helper:", err)
		return
	}
	duration := lingerLimit
	if raw := os.Getenv(lingerDurationEnv); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			fmt.Fprintln(os.Stderr, "helper: invalid linger duration")
			return
		}
		duration = parsed
	}
	time.Sleep(duration)
}

func codexHelper(args []string) int {
	if slices.Equal(args, []string{"--version"}) {
		if err := pauseVersionProbe(); err != nil {
			fmt.Fprintln(os.Stderr, "codex helper:", err)
			return helperContractExit
		}
		fmt.Println(codexHelperVersion)
		return 0
	}
	markStarted()
	if err := probeWorkspace("codex"); err != nil {
		fmt.Fprintln(os.Stderr, "codex helper:", err)
		return helperContractExit
	}
	if err := changeRepository(); err != nil {
		fmt.Fprintln(os.Stderr, "codex helper:", err)
		return helperContractExit
	}
	if err := mutateSourceCheckout("codex"); err != nil {
		fmt.Fprintln(os.Stderr, "codex helper:", err)
		return helperContractExit
	}
	if len(args) == 0 || args[0] != "exec" {
		fmt.Fprintln(os.Stderr, "codex helper: exec must be the first argument")
		return helperContractExit
	}
	if err := checkInvocation(args, [][]string{{"--json"}}); err != nil {
		fmt.Fprintln(os.Stderr, "codex helper:", err)
		return helperContractExit
	}
	fmt.Print(codexStream)
	linger()
	return providerExit(args)
}

// workspaceProbe is what a provider stand-in found where it was launched, taken
// before it changes anything. A shadow run deletes the checkout its providers
// ran in, so what that checkout held is a question only the provider itself can
// still answer afterwards.
type workspaceProbe struct {
	CWD    string `json:"cwd"`
	Head   string `json:"head"`
	Status string `json:"status"`
	Config bool   `json:"config"`
	Mode   uint32 `json:"mode"`
}

// probeEnv names the directory the stand-ins describe their workspace into, one
// document per provider name.
const probeEnv = "AGENTREC_TEST_PROVIDER_PROBE"

const mutateSourceEnv = "AGENTREC_TEST_PROVIDER_MUTATE_SOURCE"

func mutateSourceCheckout(provider string) error {
	raw := os.Getenv(mutateSourceEnv)
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) != 3 || parts[0] != provider {
		return nil
	}
	switch parts[1] {
	case "file":
		return os.WriteFile(filepath.Join(parts[2], "README.md"), []byte("mutated outside the shadow worktree\n"), 0o600)
	case "assume-unchanged", "skip-worktree":
		cmd := exec.Command("git", "-C", parts[2], "update-index", "--"+parts[1], "README.md")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("mutate source index flag: %s: %w", strings.TrimSpace(string(out)), err)
		}
		return os.WriteFile(filepath.Join(parts[2], "README.md"), []byte("mutated outside the shadow worktree\n"), 0o600)
	case "ref":
		cmd := exec.Command("git", "-C", parts[2], "branch", "provider-mutated-source")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("mutate source ref: %s: %w", strings.TrimSpace(string(out)), err)
		}
		return nil
	case "head":
		cmd := exec.Command("git", "-C", parts[2], "checkout", "provider-alternate-source")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("mutate source HEAD: %s: %w", strings.TrimSpace(string(out)), err)
		}
		return nil
	case "config":
		cmd := exec.Command("git", "-C", parts[2], "config", "--local", "agentrec.provider-mutated", "true")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("mutate source config: %s: %w", strings.TrimSpace(string(out)), err)
		}
		return nil
	default:
		return fmt.Errorf("unknown source mutation %q", parts[1])
	}
}

func probeWorkspace(name string) error {
	dir := os.Getenv(probeEnv)
	if dir == "" {
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	head, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("read HEAD: %v", err)
	}
	status, err := exec.Command("git", "status", "--porcelain=v1", "--untracked-files=all").Output()
	if err != nil {
		return fmt.Errorf("read status: %v", err)
	}
	_, cerr := os.Stat(verifyConfigFile)
	here, err := os.Stat(".")
	if err != nil {
		return err
	}
	raw, err := json.Marshal(workspaceProbe{
		CWD:    cwd,
		Head:   strings.TrimSpace(string(head)),
		Status: strings.TrimSpace(string(status)),
		Config: cerr == nil,
		Mode:   uint32(here.Mode().Perm()),
	})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name+".json"), raw, 0o600)
}

// markStarted records that the helper was launched to record a run, for the
// tests that require agentrec to have refused before ever reaching a provider.
func markStarted() {
	path := os.Getenv(startedEnv)
	if path == "" {
		return
	}
	if err := os.WriteFile(path, []byte("started\n"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "helper:", err)
	}
}

// providerExit lets one test drive a provider that ends nonzero after having
// emitted a complete stream.
func providerExit(args []string) int {
	if slices.Contains(args, failPrompt) {
		return 7
	}
	return 0
}

// checkInvocation reports what is wrong with the argument list agentrec built:
// a required flag sequence that is missing, or an override agentrec must never
// have added.
func checkInvocation(args []string, required [][]string) error {
	for _, want := range required {
		if !containsSequence(args, want) {
			return fmt.Errorf("missing %s in %q", strings.Join(want, " "), args)
		}
	}
	for _, arg := range args {
		name, _, _ := strings.Cut(arg, "=")
		if slices.Contains(forbiddenFlags, name) {
			return fmt.Errorf("agentrec injected %q", arg)
		}
	}
	return nil
}

func containsSequence(args, want []string) bool {
	for i := range args {
		if slices.Equal(args[i:min(i+len(want), len(args))], want) {
			return true
		}
	}
	return false
}

// stubProviders puts the test binary on an otherwise empty PATH under each
// given provider name and returns the directory it built. Git is put there too:
// agentrec asks the repository where it is and whether it is clean before it
// records anything, so a PATH holding only providers would be a PATH no run
// could start from.
func stubProviders(t *testing.T, names ...string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	dir := t.TempDir()
	for _, name := range names {
		if err := os.Symlink(exe, filepath.Join(dir, name)); err != nil {
			t.Fatalf("stub %s: %v", name, err)
		}
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("locate git: %v", err)
	}
	if err := os.Symlink(git, filepath.Join(dir, "git")); err != nil {
		t.Fatalf("stub git: %v", err)
	}
	t.Setenv("PATH", dir)
	return dir
}

// cleanRepo makes a fresh repository holding one commit the working directory
// for the rest of the test, and returns it. agentrec records runs against a
// clean repository, which is not something these tests may require of the
// repository they are themselves being run in. The working directory is
// process-wide, so it is restored afterwards and no test in this package runs
// in parallel.
func cleanRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("write README: %v", err)
	}
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "agentrec test"},
		{"add", "README.md"},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"LC_ALL=C",
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("locate working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("enter %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatalf("return to %s: %v", previous, err)
		}
	})

	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	return real
}

// providerStarted names a file the stubbed provider creates when it is launched
// to record a run, and reports whether it exists.
func providerStarted(t *testing.T) func() bool {
	t.Helper()
	path := filepath.Join(t.TempDir(), "started")
	t.Setenv(startedEnv, path)
	return func() bool {
		_, err := os.Stat(path)
		return err == nil
	}
}

// traceRunID reads the run ID off the header trace prints before the report,
// which is the only place an operator learns what the run was called.
func traceRunID(t *testing.T, stdout string) string {
	t.Helper()
	header, rest, ok := strings.Cut(stdout, "\n")
	id, hasPrefix := strings.CutPrefix(header, "Run ID: ")
	if !ok || !hasPrefix || id == "" || !strings.HasPrefix(rest, "\n") {
		t.Fatalf("stdout =\n%s\nwant it to start with %q", stdout, "Run ID: <id>\n\n")
	}
	return id
}

// wantNoRawJSON fails when a rendered timeline carries provider event text
// rather than the summary the report is supposed to reduce it to.
func wantNoRawJSON(t *testing.T, stdout string) {
	t.Helper()
	for _, leaked := range []string{"{", "tool_use", "item.completed", "aggregated_output", "file_contents"} {
		if strings.Contains(stdout, leaked) {
			t.Errorf("stdout contains %q, want no raw event text:\n%s", leaked, stdout)
		}
	}
}

func TestTraceRecordsAClaudeRunAndRendersItsTimeline(t *testing.T) {
	root := home(t)
	cleanRepo(t)
	started := providerStarted(t)
	stubProviders(t, "claude")

	code, stdout, stderr := run(t, "trace", "claude", "--", "-p", "read the README")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if !started() {
		t.Fatalf("the provider was never launched")
	}
	runID := traceRunID(t, stdout)
	for _, want := range []string{"READ", "README.md", "Provider     claude", "Version      2.1.220", "Exit Reason  completed"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout =\n%s\nwant it to contain %q", stdout, want)
		}
	}
	wantNoRawJSON(t, stdout)

	code, shown, stderr := run(t, "show", "latest")
	if code != 0 {
		t.Fatalf("show latest exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if !strings.Contains(shown, "READ") {
		t.Errorf("show latest =\n%s\nwant the recorded read", shown)
	}

	code, listed, stderr := run(t, "list")
	if code != 0 {
		t.Fatalf("list exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if !strings.Contains(listed, runID) || !strings.Contains(listed, "claude") {
		t.Errorf("list =\n%s\nwant it to name run %s and its provider", listed, runID)
	}
	if _, err := os.Stat(filepath.Join(root, runID, "manifest.json")); err != nil {
		t.Errorf("recorded bundle: %v", err)
	}
	wantGitArtifacts(t, filepath.Join(root, runID))
}

// wantGitArtifacts fails when a recorded run does not say what it left in the
// repository. A run that changed nothing still has to say so: an absent
// document is indistinguishable from evidence that was never collected.
func wantGitArtifacts(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"baseline.json", "tracked.patch", "tracked-stat.json", "untracked.json", "result.json"} {
		if _, err := os.Stat(filepath.Join(dir, "git", name)); err != nil {
			t.Errorf("repository evidence: %v", err)
		}
	}
	// The status document is what tells a later reader that the collection ran
	// to completion, rather than being interrupted partway with the artifacts it
	// had already written left standing.
	var result struct {
		Status      string `json:"status"`
		Reason      string `json:"reason"`
		Attribution string `json:"attribution"`
	}
	readJSONFile(t, filepath.Join(dir, "git", "result.json"), &result)
	if result.Status != "available" || result.Reason != "" || result.Attribution == "" {
		t.Errorf("git/result.json = %+v, want a completed collection that says what it means", result)
	}
}

func TestTraceRecordsACodexRunAndRendersItsTimeline(t *testing.T) {
	root := home(t)
	cleanRepo(t)
	stubProviders(t, "codex")

	code, stdout, stderr := run(t, "trace", "codex", "--", "exec", "run echo hi")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	runID := traceRunID(t, stdout)
	for _, want := range []string{"SHELL", "echo hi", "Provider     codex", "Version      0.144.6", "Exit Reason  completed"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout =\n%s\nwant it to contain %q", stdout, want)
		}
	}
	wantNoRawJSON(t, stdout)

	code, listed, stderr := run(t, "list")
	if code != 0 {
		t.Fatalf("list exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if !strings.Contains(listed, runID) || !strings.Contains(listed, "codex") {
		t.Errorf("list =\n%s\nwant it to name run %s and its provider", listed, runID)
	}
	wantGitArtifacts(t, filepath.Join(root, runID))
}

// What a run left in the repository is evidence agentrec collects itself rather
// than takes the provider's word for: the baseline is pinned before the provider
// starts, the difference is measured after it has ended, and the ref that pinned
// it does not outlive the run.
func TestTraceRecordsWhatTheRunChangedInTheRepository(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	stubProviders(t, "claude")
	t.Setenv(trackedEnv, "README.md")
	t.Setenv(untrackedEnv, "notes.txt")
	t.Setenv(requireRefEnv, "1")

	code, stdout, stderr := run(t, "trace", "claude", "--", "-p", "change the repository")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	dir := filepath.Join(root, traceRunID(t, stdout))
	git := filepath.Join(dir, "git")

	var baseline struct {
		Status string `json:"status"`
		Commit string `json:"commit"`
		Ref    string `json:"ref"`
	}
	readJSONFile(t, filepath.Join(git, "baseline.json"), &baseline)
	if baseline.Status != "available" || baseline.Commit == "" || !strings.HasPrefix(baseline.Ref, refNamespace) {
		t.Errorf("baseline.json = %+v, want the commit the run started at, pinned", baseline)
	}

	patch := readFileString(t, filepath.Join(git, "tracked.patch"))
	for _, want := range []string{"README.md", "changed by the helper"} {
		if !strings.Contains(patch, want) {
			t.Errorf("tracked.patch =\n%s\nwant it to contain %q", patch, want)
		}
	}

	var stat struct {
		Status      string `json:"status"`
		Attribution string `json:"attribution"`
		Files       []struct {
			Path      string `json:"path"`
			Additions *int   `json:"additions"`
			Deletions *int   `json:"deletions"`
		} `json:"files"`
		Totals struct {
			Files     int `json:"files"`
			Additions int `json:"additions"`
			Deletions int `json:"deletions"`
		} `json:"totals"`
	}
	readJSONFile(t, filepath.Join(git, "tracked-stat.json"), &stat)
	if stat.Status != "available" || stat.Attribution == "" {
		t.Errorf("tracked-stat.json status = %q, attribution = %q, want available evidence that says what it means", stat.Status, stat.Attribution)
	}
	// The fixture's tracked file held one line and the helper wrote two.
	if stat.Totals.Files != 1 || stat.Totals.Additions != 2 || stat.Totals.Deletions != 1 {
		t.Errorf("tracked-stat.json totals = %+v, want 1 file, 2 additions, 1 deletion", stat.Totals)
	}
	if len(stat.Files) != 1 || stat.Files[0].Path != "README.md" {
		t.Fatalf("tracked-stat.json files = %+v, want only README.md", stat.Files)
	}

	var untracked struct {
		Attribution string `json:"attribution"`
		Count       int    `json:"count"`
		Stored      int    `json:"stored"`
		Files       []struct {
			Path     string `json:"path"`
			Kind     string `json:"kind"`
			SHA256   string `json:"sha256"`
			Stored   bool   `json:"stored"`
			StoredAs string `json:"storedAs"`
		} `json:"files"`
	}
	readJSONFile(t, filepath.Join(git, "untracked.json"), &untracked)
	if untracked.Count != 1 || untracked.Stored != 1 || len(untracked.Files) != 1 {
		t.Fatalf("untracked.json = %+v, want the one file the run created, stored", untracked)
	}
	entry := untracked.Files[0]
	if entry.Path != "notes.txt" || entry.Kind != "file" || entry.SHA256 == "" || !entry.Stored || entry.StoredAs == "" {
		t.Fatalf("untracked entry = %+v, want notes.txt described and its body stored", entry)
	}
	body := readFileString(t, filepath.Join(git, entry.StoredAs))
	if !strings.Contains(body, "changed by the helper") {
		t.Errorf("stored body = %q, want the content the run wrote", body)
	}

	// The ref existed while the provider ran — the helper refused to change
	// anything otherwise — and the repository is left as agentrec found it.
	if refs := gitIn(t, repo, "for-each-ref", "--format=%(refname)", refNamespace); refs != "" {
		t.Errorf("%s holds %q after the run, want the temporary ref removed", refNamespace, refs)
	}

	// The token the run wrote into the repository reaches the evidence, and the
	// evidence names it with a marker instead of carrying it. The repository
	// itself still holds it: only the bundle is scanned.
	for name, content := range snapshot(t, dir) {
		if strings.Contains(content, helperToken) {
			t.Errorf("%s holds the token verbatim", name)
		}
	}
	for name, content := range map[string]string{"tracked.patch": patch, entry.StoredAs: body} {
		if !strings.Contains(content, "[REDACTED:") {
			t.Errorf("%s = %q, want the token replaced by a marker", name, content)
		}
	}
}

// A recorded run is worth having after the repository it was recorded against
// is gone, so the whole report is written into the bundle while the evidence is
// still there to render — and it says what the run changed and how the checks
// that judged it ended.
func TestTraceWritesTheReportIntoTheBundle(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	stubProviders(t, "claude", verifyHelperName)
	commitVerifyConfig(t, repo, verifyHelperName, "pass")
	t.Setenv(trackedEnv, "README.md")
	t.Setenv(untrackedEnv, "notes.txt")

	code, stdout, stderr := run(t, "trace", "claude", "--verify", "--", "-p", "change the repository")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	runID := traceRunID(t, stdout)
	dir := filepath.Join(root, runID)

	// The helper rewrote the fixture's one tracked line as two and added one
	// untracked file, so the counts have exactly one right answer.
	wantLines := []string{
		"REPOSITORY-OBSERVED CHANGES",
		"  Status       AVAILABLE",
		"  Files        2 (1 tracked, 1 untracked)",
		"  Diff         +2/-1, 0 binary",
		"  Stored Text  1",
		"  Attribution  " + evidence.Attribution,
		"VERIFICATION-OBSERVED RESULT",
		"  Status       PASS",
		"  Attribution  " + evidence.VerificationAttribution,
	}
	for _, want := range wantLines {
		if !strings.Contains(stdout, "\n"+want+"\n") {
			t.Errorf("stdout =\n%s\nwant line %q", stdout, want)
		}
	}
	if !strings.Contains(stdout, `  Check        PASS check  "`+verifyHelperName+`" "pass"`) {
		t.Errorf("stdout =\n%s\nwant the pinned check reported as passed", stdout)
	}

	// The rendered report is installed beside the evidence, readable only by
	// the operator who recorded it.
	path := filepath.Join(dir, "report.md")
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("report.md: %v", err)
	}
	if info.Mode() != 0o600 {
		t.Errorf("report.md mode = %v, want -rw-------", info.Mode())
	}
	rendered := readFileString(t, path)
	for _, want := range []string{
		"## Repository-Observed Changes",
		"- `Status`: `AVAILABLE`",
		"- `Files`: `2 (1 tracked, 1 untracked)`",
		"- `Attribution`: `" + evidence.Attribution + "`",
		"## Verification-Observed Result",
		"- `Status`: `PASS`",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("report.md =\n%s\nwant %q", rendered, want)
		}
	}
	// It is a report, not a copy of the bundle: no raw provider event, no
	// patch, and no untracked file body reaches it.
	for _, leaked := range []string{"tool_use", "aggregated_output", "diff --git", "@@", helperContent, helperToken} {
		if strings.Contains(rendered, leaked) {
			t.Errorf("report.md carries %q:\n%s", leaked, rendered)
		}
	}

	// The repository is gone; the run still reads the same.
	if err := os.RemoveAll(repo); err != nil {
		t.Fatalf("remove %s: %v", repo, err)
	}
	if readFileString(t, path) != rendered {
		t.Errorf("report.md changed once the repository was removed")
	}
	code, shown, stderr := run(t, "show", runID)
	if code != 0 {
		t.Fatalf("show exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	for _, want := range wantLines {
		if !strings.Contains(shown, "\n"+want+"\n") {
			t.Errorf("show =\n%s\nwant line %q", shown, want)
		}
	}
}

// A run recorded without checks still says what it left in the repository, and
// says plainly that nothing verified it.
func TestTraceWithoutVerifyStillReportsTheRepository(t *testing.T) {
	root := home(t)
	cleanRepo(t)
	stubProviders(t, "claude")

	code, stdout, stderr := run(t, "trace", "claude", "--", "-p", "read the README")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	want := `REPOSITORY-OBSERVED CHANGES
  Status       AVAILABLE
  Files        0 (0 tracked, 0 untracked)
  Diff         +0/-0, 0 binary
  Stored Text  0
`
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout =\n%s\nwant it to contain\n%s", stdout, want)
	}
	if !strings.HasSuffix(stdout, "VERIFICATION-OBSERVED RESULT\n  (none)\n") {
		t.Errorf("stdout =\n%s\nwant the verification section to state it has nothing", stdout)
	}
	if rendered := readFileString(t, filepath.Join(root, traceRunID(t, stdout), reportFile)); !strings.Contains(rendered, "## Verification-Observed Result\n\n(none)\n") {
		t.Errorf("report.md =\n%s\nwant the verification section to state it has nothing", rendered)
	}
}

// A report already standing where this run's would go is not this run's to
// replace: whatever it is, it is left exactly as it was found, and nothing is
// written through it.
func TestInstallReportRefusesToReplaceWhatIsAlreadyThere(t *testing.T) {
	const planted = "not this run's report\n"

	tests := []struct {
		name  string
		plant func(t *testing.T, dir, outside string)
	}{
		{"a file", func(t *testing.T, dir, _ string) {
			if err := os.WriteFile(filepath.Join(dir, reportFile), []byte(planted), 0o600); err != nil {
				t.Fatalf("plant %s: %v", reportFile, err)
			}
		}},
		{"a symlink out of the run", func(t *testing.T, dir, outside string) {
			if err := os.Symlink(outside, filepath.Join(dir, reportFile)); err != nil {
				t.Fatalf("plant symlink: %v", err)
			}
		}},
		{"a leftover temporary", func(t *testing.T, dir, _ string) {
			if err := os.WriteFile(filepath.Join(dir, reportFile+".tmp"), []byte(planted), 0o600); err != nil {
				t.Fatalf("plant temporary: %v", err)
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			outside := filepath.Join(t.TempDir(), "outside.md")
			if err := os.WriteFile(outside, []byte(planted), 0o600); err != nil {
				t.Fatalf("write %s: %v", outside, err)
			}
			tt.plant(t, dir, outside)

			if err := installReport(dir, report.Report{}); err == nil {
				t.Fatalf("installReport() = nil, want a refusal")
			}
			if got := readFileString(t, outside); got != planted {
				t.Errorf("%s = %q, want it left alone", outside, got)
			}
			if got, err := os.ReadFile(filepath.Join(dir, reportFile)); err == nil && string(got) != planted {
				t.Errorf("%s = %q, want it left alone", reportFile, got)
			}
		})
	}
}

func TestLimitWriterRefusesTheWriteThatWouldCrossItsBound(t *testing.T) {
	var dst bytes.Buffer
	w := &limitWriter{w: &dst, limit: 3}

	if n, err := w.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatalf("first Write() = (%d, %v), want (3, nil)", n, err)
	}
	if n, err := w.Write([]byte("d")); err == nil || n != 0 {
		t.Fatalf("over-limit Write() = (%d, %v), want (0, error)", n, err)
	}
	if got := dst.String(); got != "abc" {
		t.Fatalf("destination = %q, want no partial over-limit write", got)
	}
}

// Reading a run is reading: `show` renders what is there and writes nothing,
// so a bundle recorded before reports were written stays as it was recorded.
func TestShowNeverWritesAReport(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-b", "claude", late, "completed")
	writeGit(t, root, "run-b", availableGit())

	if code, _, stderr := run(t, "show", "run-b"); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if exists(filepath.Join(root, "run-b", reportFile)) {
		t.Errorf("show wrote %s, want reading to leave the bundle alone", reportFile)
	}
}

func readJSONFile(t *testing.T, path string, into any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// gitIn asks the repository a question and returns its answer, trimmed.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// The recorded invocation is what agentrec decided to launch, so it is checked
// directly as well as by the helper that received it.
func TestTraceLaunchesTheProviderWithStructuredOutputAndNoInjectedPermissions(t *testing.T) {
	root := home(t)
	cleanRepo(t)
	stubProviders(t, "claude")

	code, stdout, stderr := run(t, "trace", "claude", "--", "-p", "read the README")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}

	manifest, err := readManifest(filepath.Join(root, traceRunID(t, stdout)))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if len(manifest.Argv) == 0 || manifest.Argv[0] != "claude" {
		t.Fatalf("argv = %q, want it to start with the executable", manifest.Argv)
	}
	for _, want := range [][]string{{"--output-format", "stream-json"}, {"--verbose"}, {"--include-hook-events"}, {"-p", "read the README"}} {
		if !containsSequence(manifest.Argv, want) {
			t.Errorf("argv = %q, want it to contain %q", manifest.Argv, want)
		}
	}
	for _, arg := range manifest.Argv {
		name, _, _ := strings.Cut(arg, "=")
		if slices.Contains(forbiddenFlags, name) {
			t.Errorf("argv = %q, want no injected %q", manifest.Argv, arg)
		}
	}
	if manifest.CWD == "" {
		t.Errorf("manifest records no working directory")
	}
}

func TestTraceTimesOutTheProviderAndPreservesTheBundle(t *testing.T) {
	root := home(t)
	cleanRepo(t)
	stubProviders(t, "claude")
	pidFile := filepath.Join(t.TempDir(), "provider.pid")
	t.Setenv(lingerEnv, pidFile)
	t.Setenv(lingerDurationEnv, "300ms")

	code, stdout, stderr := run(t, "trace", "claude", timeoutFlag, "50ms", "--", "-p", "read the README")

	if code != exitFailure {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, exitFailure, stderr)
	}
	if !strings.Contains(stderr, "timed out") {
		t.Errorf("stderr = %q, want timeout diagnostic", stderr)
	}
	id := traceRunID(t, stdout)
	manifest, err := readManifest(filepath.Join(root, id))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if manifest.ExitReason != runner.ReasonTimeout || manifest.EndedAt == nil {
		t.Errorf("manifest = reason %q, ended %v; want finalized timeout", manifest.ExitReason, manifest.EndedAt)
	}
	if !strings.Contains(stdout, "Exit Reason  timeout") {
		t.Errorf("stdout =\n%s\nwant timeout exit reason", stdout)
	}
}

// A token the operator passed on the command line reaches the prompt, the
// manifest's argv and, through them, every artifact derived from either. None
// of it may hold the secret itself.
func TestTraceNeverRecordsOrPrintsASecretFromTheInvocation(t *testing.T) {
	const secret = "sk-agentrecTESTSECRET0123456789"
	root := home(t)
	cleanRepo(t)
	stubProviders(t, "claude")

	code, stdout, stderr := run(t, "trace", "claude", "--", "-p", "export API_TOKEN="+secret+" then read the README")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	dir := filepath.Join(root, traceRunID(t, stdout))
	for name, content := range snapshot(t, dir) {
		if strings.Contains(content, secret) {
			t.Errorf("%s holds the secret verbatim", name)
		}
	}
	// The prompt was recorded, and the secret in it was replaced rather than
	// the whole prompt being dropped: a bundle that never held the prompt would
	// pass the scan above without proving anything.
	recorded, err := os.ReadFile(filepath.Join(dir, "prompt.txt"))
	if err != nil {
		t.Fatalf("read recorded prompt: %v", err)
	}
	if !strings.Contains(string(recorded), "[REDACTED:") || !strings.Contains(string(recorded), "then read the README") {
		t.Errorf("prompt.txt = %q, want the prompt with its secret replaced by a marker", recorded)
	}
	if strings.Contains(stdout, secret) || strings.Contains(stderr, secret) {
		t.Errorf("the CLI printed the secret")
	}
}

func TestTraceReportsTheProviderExitCode(t *testing.T) {
	home(t)
	cleanRepo(t)
	stubProviders(t, "claude")

	code, stdout, stderr := run(t, "trace", "claude", "--", "-p", failPrompt)

	if code != 7 {
		t.Fatalf("exit code = %d, want the provider's 7 (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "Exit Reason  nonzero") {
		t.Errorf("stdout =\n%s\nwant the run reported as nonzero", stdout)
	}
}

func TestTraceRejectsInvocationsItCannotRecord(t *testing.T) {
	for _, args := range [][]string{
		{"trace"},
		{"trace", "claude"},
		{"trace", "claude", "-p", "hello"},
		{"trace", "claude", "--"},
		{"trace", "gemini", "--", "-p", "hello"},
		{"trace", "claude", "--verify"},
		{"trace", "claude", "--verify", "--"},
		{"trace", "claude", "--verify", "--verify", "--", "-p", "hello"},
		{"trace", "claude", "--unknown", "--", "-p", "hello"},
		{"trace", "claude", "--verify=1", "--", "-p", "hello"},
		{"trace", "--verify", "claude", "--", "-p", "hello"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := home(t)
			stubProviders(t, "claude", "codex")

			code, stdout, stderr := run(t, args...)

			if code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, "usage") {
				t.Errorf("stderr = %q, want it to state the usage", stderr)
			}
			if entries, err := os.ReadDir(root); err == nil && len(entries) > 0 {
				t.Errorf("runs root holds %d entries, want no bundle", len(entries))
			}
		})
	}
}

// A repository that already differs from its last commit cannot be told apart
// afterwards from one the recorded agent changed, so the run is refused before
// anything is recorded and before the provider is launched.
func TestTraceRefusesToRecordADirtyRepository(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	started := providerStarted(t)
	stubProviders(t, "claude")
	if err := os.WriteFile(filepath.Join(repo, "scratch.txt"), []byte("scratch\n"), 0o600); err != nil {
		t.Fatalf("dirty the repository: %v", err)
	}

	code, stdout, stderr := run(t, "trace", "claude", "--", "-p", "read the README")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr %q)", code, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "commit") && !strings.Contains(stderr, "stash") {
		t.Errorf("stderr = %q, want it to say what to do about it", stderr)
	}
	wantNothingRecorded(t, root, started)
}

// Two runs recording the same repository at once would each observe the other's
// changes, so the second is refused rather than queued behind the first.
func TestTraceRefusesToRecordWhileAnotherRunHoldsTheRepository(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	started := providerStarted(t)
	stubProviders(t, "claude")

	held, err := lock.Acquire(context.Background(), filepath.Join(filepath.Dir(root), "locks"), repo)
	if err != nil {
		t.Fatalf("hold the repository lock: %v", err)
	}
	defer held.Release()

	code, stdout, stderr := run(t, "trace", "claude", "--", "-p", "read the README")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr %q)", code, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "already") {
		t.Errorf("stderr = %q, want it to say the repository is already being recorded", stderr)
	}
	wantNothingRecorded(t, root, started)
}

// wantNothingRecorded fails when a refused run left anything behind: a bundle
// on disk, or a provider that was launched at all.
func wantNothingRecorded(t *testing.T, root string, started func() bool) {
	t.Helper()
	if entries, err := os.ReadDir(root); err == nil && len(entries) > 0 {
		t.Errorf("runs root holds %d entries, want no bundle", len(entries))
	}
	if started() {
		t.Errorf("the provider was launched for a run agentrec refused")
	}
}

// --- trace bookkeeping ------------------------------------------------------

// An interrupted run is still the operator's ending, but a supervisor failure
// on the way out is why the evidence may be short and must be said out loud.
func TestTraceExitReportsWhyAnInterruptedRunFailed(t *testing.T) {
	var stderr bytes.Buffer

	code := traceExit(runner.Result{ExitReason: runner.ReasonInterrupted}, errors.New("synthetic supervisor failure"), &stderr, "run-a")

	if code != exitInterrupted {
		t.Errorf("exit code = %d, want %d", code, exitInterrupted)
	}
	if !strings.Contains(stderr.String(), "synthetic supervisor failure") {
		t.Errorf("stderr = %q, want it to report why the run failed", stderr.String())
	}
}

// A run whose own request could not be recorded leaves a finalized bundle: one
// that describes a run that stopped, and that refuses every later write.
func TestUnrecordableFinalizesTheBundle(t *testing.T) {
	root := home(t)
	bundle, err := storage.Create(root, "run-a", storage.Manifest{
		Provider:  "claude",
		Argv:      []string{"claude"},
		StartedAt: late,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	var stderr bytes.Buffer

	out := unrecordable(bundle, &stderr, "run-a", errors.New("synthetic storage failure"))

	if out.Recorded {
		t.Errorf("outcome = %+v, want a run reported as never having reached its provider", out)
	}
	if !strings.Contains(stderr.String(), "synthetic storage failure") {
		t.Errorf("stderr = %q, want it to report the cause", stderr.String())
	}
	manifest, err := readManifest(filepath.Join(root, "run-a"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if manifest.ExitReason != runner.ReasonStorageError {
		t.Errorf("exit reason = %q, want %q", manifest.ExitReason, runner.ReasonStorageError)
	}
	if manifest.EndedAt == nil {
		t.Errorf("manifest records no ending")
	}
	if err := bundle.WriteAction(readAction(late)); !errors.Is(err, storage.ErrFinalized) {
		t.Errorf("write after finalize = %v, want %v", err, storage.ErrFinalized)
	}
}

// A prompt agentrec cannot identify is left unrecorded: a wrong prompt is worse
// evidence than none, and an option's value is not a prompt.
func TestCodexPromptRecordsOnlyAnUnambiguousPrompt(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"prompt last", []string{"exec", "read the README"}, "read the README"},
		{"option value last", []string{"exec", "read the README", "--model", "o3"}, ""},
		{"short option value last", []string{"exec", "-m", "o3"}, ""},
		{"prompt after an option", []string{"exec", "--model", "o3", "read the README"}, "read the README"},
		{"prompt after an inline option value", []string{"exec", "--model=o3", "read the README"}, "read the README"},
		{"prompt after the inner delimiter", []string{"exec", "--", "-leading-dash prompt"}, "-leading-dash prompt"},
		{"flag last", []string{"exec", "--json"}, ""},
		{"no prompt", []string{"exec"}, ""},
		{"nothing", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexPrompt(tc.args); got != tc.want {
				t.Errorf("codexPrompt(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

func TestClaudePromptRecordsOnlyAnUnambiguousPrompt(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"prompt after -p", []string{"-p", "read the README"}, "read the README"},
		{"prompt after --print", []string{"--print", "read the README"}, "read the README"},
		{"prompt among other options", []string{"--model", "opus", "-p", "read the README", "--verbose"}, "read the README"},
		{"flag after -p", []string{"-p", "--verbose"}, ""},
		{"nothing after -p", []string{"--verbose", "-p"}, ""},
		{"no print flag", []string{"read the README"}, ""},
		{"nothing", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudePrompt(tc.args); got != tc.want {
				t.Errorf("claudePrompt(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

// Options agentrec takes for itself are spelled exactly and given at most once.
// An operator who asked for something must never be told a run was recorded the
// way they asked for, so an unknown, repeated, or malformed option is a usage
// refusal — before any provider is probed, any bundle exists or any lock is taken.
func TestTraceRejectsInvalidOwnOptions(t *testing.T) {
	home(t)
	for _, tt := range []struct {
		name string
		args []string
	}{
		{"unknown option", []string{"trace", "claude", "--allow-unsupported", "--", "-p", "x"}},
		{"repeated verify", []string{"trace", "claude", "--verify", "--verify", "--", "-p", "x"}},
		{"repeated override", []string{"trace", "claude", allowUnsupportedVersionFlag, allowUnsupportedVersionFlag, "--", "-p", "x"}},
		{"missing timeout", []string{"trace", "claude", timeoutFlag, "--", "-p", "x"}},
		{"zero timeout", []string{"trace", "claude", timeoutFlag, "0s", "--", "-p", "x"}},
		{"negative timeout", []string{"trace", "claude", timeoutFlag, "-1s", "--", "-p", "x"}},
		{"invalid timeout", []string{"trace", "claude", timeoutFlag, "soon", "--", "-p", "x"}},
		{"repeated timeout", []string{"trace", "claude", timeoutFlag, "1s", timeoutFlag, "2s", "--", "-p", "x"}},
		{"provider option before the delimiter", []string{"trace", "claude", "-p", "--", "x"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := run(t, tt.args...)
			if code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if stderr != traceUsage {
				t.Errorf("stderr = %q, want the trace usage", stderr)
			}
		})
	}
}

// What agentrec accepts for itself is decided by parsing alone, so it is
// established by parsing alone. Driving the whole command to prove a flag was
// accepted would launch a real agent against the repository the test runs in —
// and would then pass or fail on which provider CLIs happen to be installed
// rather than on the parsing it claims to be about.
func TestParseTraceOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want traceOptions
		ok   bool
	}{
		{"none", nil, traceOptions{}, true},
		{"timeout alone", []string{timeoutFlag, "2m"}, traceOptions{timeout: 2 * time.Minute}, true},
		{"timeout then verify", []string{timeoutFlag, "2m", verifyFlag}, traceOptions{verify: true, timeout: 2 * time.Minute}, true},
		{"verify then timeout", []string{verifyFlag, timeoutFlag, "2m"}, traceOptions{verify: true, timeout: 2 * time.Minute}, true},
		{"verify alone", []string{verifyFlag}, traceOptions{verify: true}, true},
		{"override alone", []string{allowUnsupportedVersionFlag}, traceOptions{allowUnsupported: true}, true},
		// Neither option is the other's prerequisite, so an operator who spelled
		// both correctly is never refused over how they arranged them.
		{"both", []string{verifyFlag, allowUnsupportedVersionFlag}, traceOptions{verify: true, allowUnsupported: true}, true},
		{"both reversed", []string{allowUnsupportedVersionFlag, verifyFlag}, traceOptions{verify: true, allowUnsupported: true}, true},
		{"unknown", []string{"--allow-unsupported"}, traceOptions{}, false},
		{"repeated verify", []string{verifyFlag, verifyFlag}, traceOptions{}, false},
		{"repeated override", []string{allowUnsupportedVersionFlag, allowUnsupportedVersionFlag}, traceOptions{}, false},
		{"missing timeout", []string{timeoutFlag}, traceOptions{}, false},
		{"zero timeout", []string{timeoutFlag, "0s"}, traceOptions{}, false},
		{"negative timeout", []string{timeoutFlag, "-1s"}, traceOptions{}, false},
		{"invalid timeout", []string{timeoutFlag, "soon"}, traceOptions{}, false},
		{"repeated timeout", []string{timeoutFlag, "1s", timeoutFlag, "2s"}, traceOptions{}, false},
		{"timeout value attached", []string{"--timeout=1s"}, traceOptions{}, false},
		{"provider option", []string{"-p"}, traceOptions{}, false},
		{"prefix of a known option", []string{"--verif"}, traceOptions{}, false},
		{"known option with a value attached", []string{"--verify=true"}, traceOptions{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseTraceOptions(tc.args)
			if ok != tc.ok || got != tc.want {
				t.Errorf("parseTraceOptions(%q) = (%+v, %v), want (%+v, %v)", tc.args, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestTraceRecordsAnExplicitUnsupportedVersionOverride(t *testing.T) {
	root := home(t)
	cleanRepo(t)
	stubProviders(t, "claude")
	started := providerStarted(t)
	t.Setenv(providerVersionEnv, "3.0.0 (Claude Code)")

	code, stdout, stderr := run(t, "trace", "claude", "--", "-p", "hello")
	if code != exitFailure {
		t.Fatalf("strict trace exit code = %d, want %d (stdout %q, stderr %q)", code, exitFailure, stdout, stderr)
	}
	if !strings.Contains(stderr, "outside the supported range") {
		t.Errorf("strict trace stderr = %q, want unsupported version refusal", stderr)
	}
	wantNothingRecorded(t, root, started)

	code, stdout, stderr = run(t, "trace", "claude", allowUnsupportedVersionFlag, "--", "-p", "hello")
	if code != 0 {
		t.Fatalf("override trace exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if !started() {
		t.Fatal("override trace did not launch the stub provider")
	}
	if !strings.Contains(stderr, "timeline may be incomplete") {
		t.Errorf("override trace stderr = %q, want uncertainty warning", stderr)
	}
	if !strings.Contains(stdout, "unsupported; timeline may be incomplete") {
		t.Errorf("override trace stdout =\n%s\nwant unsupported report annotation", stdout)
	}

	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("runs root = %v (%v), want one recorded override run", entries, err)
	}
	id := entries[0].Name()
	manifest, err := readManifest(filepath.Join(root, id))
	if err != nil {
		t.Fatalf("read override manifest: %v", err)
	}
	if manifest.ProviderVersion != "3.0.0" || !manifest.VersionUnverified {
		t.Errorf("manifest version = %q, unverified = %v; want 3.0.0 and true", manifest.ProviderVersion, manifest.VersionUnverified)
	}

	code, shown, showErr := run(t, "show", id)
	if code != 0 {
		t.Fatalf("show exit code = %d, want 0 (stderr %q)", code, showErr)
	}
	if !strings.Contains(shown, "Version      3.0.0  (unsupported; timeline may be incomplete)") {
		t.Errorf("show output =\n%s\nwant unsupported version annotation", shown)
	}
	if rendered := readFileString(t, filepath.Join(root, id, reportFile)); !strings.Contains(rendered, "unsupported; timeline may be incomplete") {
		t.Errorf("report.md =\n%s\nwant unsupported version annotation", rendered)
	}
}

// A run recorded against a version this parser was not written for, and a run
// whose provider printed lines that were not events, both leave a bundle that
// says so. Neither may render as an ordinary run: what the timeline shows in
// each case is less than it appears to be, and the report is where a reader
// finds that out.
func TestShowStatesAnUnverifiedVersionAndUnparsedStdoutLines(t *testing.T) {
	root := home(t)
	b, err := storage.Create(root, "run-x", storage.Manifest{
		Provider:          "claude",
		ProviderVersion:   "4.0.0",
		VersionUnverified: true,
		Argv:              []string{"claude", "-p", "hello"},
		CWD:               "/tmp",
		StartedAt:         late,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := b.WriteAction(readAction(late)); err != nil {
		t.Fatalf("write action: %v", err)
	}
	for _, line := range []string{"update available", "deprecated option"} {
		if err := b.WriteUnparsedLine([]byte(line)); err != nil {
			t.Fatalf("write unparsed line: %v", err)
		}
	}
	if err := b.Finalize(storage.Finalization{
		EndedAt:       late.Add(time.Second),
		ExitReason:    "completed",
		WarningCount:  2,
		UnparsedLines: 2,
	}); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	code, stdout, stderr := run(t, "show", "run-x")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "Version      4.0.0  (unsupported; timeline may be incomplete)") {
		t.Errorf("stdout =\n%s\nwant the version shown as unsupported", stdout)
	}
	if !strings.Contains(stdout, "Unparsed     2 stdout line(s) kept in provider-stdout.unparsed.log") {
		t.Errorf("stdout =\n%s\nwant the unparsed lines named with the file holding them", stdout)
	}
}

func TestShowRefusesAnUnparsedStreamThatDoesNotMatchTheManifest(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []string
	}{
		{name: "missing file"},
		{name: "too few lines", lines: []string{"one line"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := home(t)
			b, err := storage.Create(root, "run-x", storage.Manifest{
				Provider:  "claude",
				Argv:      []string{"claude", "-p", "hello"},
				CWD:       "/tmp",
				StartedAt: late,
			})
			if err != nil {
				t.Fatalf("create run: %v", err)
			}
			for _, line := range tc.lines {
				if err := b.WriteUnparsedLine([]byte(line)); err != nil {
					t.Fatalf("write unparsed line: %v", err)
				}
			}
			if err := b.Finalize(storage.Finalization{
				EndedAt:       late.Add(time.Second),
				ExitReason:    "completed",
				UnparsedLines: 2,
			}); err != nil {
				t.Fatalf("finalize: %v", err)
			}

			code, _, stderr := run(t, "show", "run-x")
			if code != exitFailure {
				t.Fatalf("exit code = %d, want %d (stderr %q)", code, exitFailure, stderr)
			}
			if !strings.Contains(stderr, "provider-stdout.unparsed.log") {
				t.Errorf("stderr = %q, want the inconsistent artifact named", stderr)
			}
		})
	}
}

// A run with nothing unusual to report says nothing unusual: an ordinary run
// gets no Unparsed line and no qualifier on its version, because a zero a reader
// has to scan past is noise in evidence that is meant to be read.
func TestShowOmitsTheUnparsedLineWhenThereWereNone(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-y", "claude", late, "completed")

	code, stdout, stderr := run(t, "show", "run-y")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if strings.Contains(stdout, "Unparsed") {
		t.Errorf("stdout =\n%s\nwant no unparsed line for a run that had none", stdout)
	}
	if strings.Contains(stdout, "unsupported") {
		t.Errorf("stdout =\n%s\nwant no version qualifier on an ordinary run", stdout)
	}
}
