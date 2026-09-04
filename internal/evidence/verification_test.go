package evidence

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestVerifyHelper is not a test. It is the program the verification tests
// pin and run: a real executable with a real process group, so that what the
// tests observe is what a check command actually does rather than a stand-in
// for it. The ordinary test run reaches it with no arguments and skips.
func TestVerifyHelper(t *testing.T) {
	args := flag.Args()
	if len(args) == 0 || args[0] != "helper" {
		t.Skip("not running as the helper process")
	}
	runVerifyHelper(args[1:])
}

// runVerifyHelper performs the operations named on the command line in order
// and never returns. The operations are positional rather than flags, so the
// testing package's own flag parsing stops before them.
func runVerifyHelper(args []string) {
	for len(args) > 0 {
		op := args[0]
		args = args[1:]
		switch op {
		case "out":
			os.Stdout.WriteString(args[0])
			args = args[1:]
		case "err":
			os.Stderr.WriteString(args[0])
			args = args[1:]
		case "outbytes":
			n, _ := strconv.Atoi(args[0])
			args = args[1:]
			os.Stdout.Write(bytes.Repeat([]byte("x"), n))
		case "write":
			os.WriteFile(args[0], []byte("marker"), 0o600)
			args = args[1:]
		case "sleep":
			d, err := time.ParseDuration(args[0])
			if err != nil {
				os.Exit(98)
			}
			args = args[1:]
			time.Sleep(d)
		case "spawn":
			// A descendant in the same process group, which only a signal to
			// the whole group takes down with its parent.
			path, delay := args[0], args[1]
			args = args[2:]
			child := exec.Command(os.Args[0], "-test.run=^TestVerifyHelper$", "helper", "sleep", delay, "write", path)
			if err := child.Start(); err != nil {
				os.Exit(97)
			}
		case "spawn-wait":
			path, resume := args[0], args[1]
			args = args[2:]
			child := exec.Command(os.Args[0], "-test.run=^TestVerifyHelper$", "helper", "wait", resume, "write", path)
			child.Stdout = os.Stdout
			child.Stderr = os.Stderr
			if err := child.Start(); err != nil {
				os.Exit(97)
			}
		case "wait":
			path := args[0]
			args = args[1:]
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(path); err == nil {
					break
				}
				if time.Now().After(deadline) {
					os.Exit(96)
				}
				time.Sleep(10 * time.Millisecond)
			}
		case "exit":
			n, _ := strconv.Atoi(args[0])
			os.Exit(n)
		default:
			os.Stderr.WriteString("unknown helper operation " + op)
			os.Exit(99)
		}
	}
	os.Exit(0)
}

// helperBin is the test binary itself, named absolutely so that a pinned argv
// does not depend on a working directory.
func helperBin(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}
	return exe
}

func helperArgv(t *testing.T, ops ...string) []string {
	t.Helper()
	return append([]string{helperBin(t), "-test.run=^TestVerifyHelper$", "helper"}, ops...)
}

type checkSpec struct {
	name    string
	timeout string
	argv    []string
}

// configYAML writes the configuration a run pins. Every string is quoted, so
// a path or an argument holding YAML punctuation is carried literally.
func configYAML(checks ...checkSpec) string {
	var b strings.Builder
	b.WriteString("version: 1\nverify:\n")
	for _, c := range checks {
		fmt.Fprintf(&b, "  - name: %s\n", strconv.Quote(c.name))
		fmt.Fprintf(&b, "    timeout: %s\n", strconv.Quote(c.timeout))
		b.WriteString("    command:\n")
		for _, a := range c.argv {
			fmt.Fprintf(&b, "      - %s\n", strconv.Quote(a))
		}
	}
	return b.String()
}

const configName = ".agentrec.yaml"

func configPathIn(repo string) string { return filepath.Join(repo, configName) }

func writeConfig(t *testing.T, repo, body string) string {
	t.Helper()
	write(t, repo, configName, body)
	return configPathIn(repo)
}

func pin(t *testing.T, repo, run string) *PinnedVerification {
	t.Helper()
	return pinWith(t, repo, run, VerificationOptions{})
}

func pinWith(t *testing.T, repo, run string, opts VerificationOptions) *PinnedVerification {
	t.Helper()
	p, err := PinVerification(context.Background(), repo, run, configPathIn(repo), opts)
	if err != nil {
		t.Fatalf("PinVerification: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func verifyDirOf(run string) string { return filepath.Join(run, "verification") }

func verifyResultPath(run string) string {
	return filepath.Join(verifyDirOf(run), "results.json")
}

func readVerification(t *testing.T, run string) VerificationResult {
	t.Helper()
	return readJSON[VerificationResult](t, verifyResultPath(run))
}

// outside is a directory no repository and no run holds, where a marker file
// proves whether a command ran without disturbing what the run measures.
func outside(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	return real
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func checkByName(t *testing.T, res VerificationResult, name string) VerificationCheck {
	t.Helper()
	for _, c := range res.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in %+v", name, res.Checks)
	return VerificationCheck{}
}

// 1. What the run will execute is fixed before the provider is allowed to
// touch anything, and it is on disk saying so while the provider runs.
func TestPinRecordsTheConfigAndArgvBeforeAnythingRuns(t *testing.T) {
	repo, run, out := gitRepo(t), runDir(t), outside(t)
	marker := filepath.Join(out, "ran")
	body := configYAML(checkSpec{name: "unit", timeout: "5s", argv: helperArgv(t, "write", marker)})
	writeConfig(t, repo, body)

	pin(t, repo, run)

	if exists(marker) {
		t.Error("pinning ran the command")
	}
	doc := readVerification(t, run)
	if doc.Status != "pending" {
		t.Errorf("status = %q, want pending", doc.Status)
	}
	if doc.Attribution != VerificationAttribution {
		t.Errorf("attribution = %q, want %q", doc.Attribution, VerificationAttribution)
	}
	if doc.Config != configName {
		t.Errorf("config = %q, want the repository-relative %q", doc.Config, configName)
	}
	if want := sum(body); doc.ConfigSHA256 != want {
		t.Errorf("configSha256 = %q, want %q", doc.ConfigSHA256, want)
	}
	if len(doc.Checks) != 1 {
		t.Fatalf("checks = %+v, want one", doc.Checks)
	}
	pinned := doc.Checks[0]
	if pinned.Name != "unit" || pinned.Timeout != "5s" {
		t.Errorf("pinned check = %+v, want unit/5s", pinned)
	}
	want := helperArgv(t, "write", marker)
	if strings.Join(pinned.Command, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("pinned command = %q, want %q", pinned.Command, want)
	}
	if pinned.Status != "" {
		t.Errorf("pinned check already has status %q", pinned.Status)
	}
	// The raw configuration is evidence of nothing and is never copied out.
	assertAbsent(t, run, "version: 1")
}

// 2. A configuration that changed under the run is not the one that was
// reviewed, so nothing from it is executed.
func TestConfigChangedUnderTheRunTaintsWithoutExecuting(t *testing.T) {
	out := outside(t)
	swapped := configYAML(checkSpec{
		name:    "unit",
		timeout: "5s",
		argv:    helperArgv(t, "write", filepath.Join(out, "swapped")),
	})

	cases := map[string]func(t *testing.T, repo string){
		"rewritten": func(t *testing.T, repo string) {
			write(t, repo, configName, swapped)
		},
		"removed": func(t *testing.T, repo string) {
			if err := os.Remove(configPathIn(repo)); err != nil {
				t.Fatalf("remove config: %v", err)
			}
		},
		"replaced by a symlink to the same bytes": func(t *testing.T, repo string) {
			elsewhere := filepath.Join(outside(t), "copy.yaml")
			body, err := os.ReadFile(configPathIn(repo))
			if err != nil {
				t.Fatalf("read config: %v", err)
			}
			if err := os.WriteFile(elsewhere, body, 0o600); err != nil {
				t.Fatalf("write the copy: %v", err)
			}
			if err := os.Remove(configPathIn(repo)); err != nil {
				t.Fatalf("remove config: %v", err)
			}
			if err := os.Symlink(elsewhere, configPathIn(repo)); err != nil {
				t.Fatalf("plant the symlink: %v", err)
			}
		},
	}

	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			repo, run := gitRepo(t), runDir(t)
			marker := filepath.Join(outside(t), "ran")
			writeConfig(t, repo, configYAML(checkSpec{
				name: "unit", timeout: "5s", argv: helperArgv(t, "write", marker),
			}))
			p := pin(t, repo, run)
			tamper(t, repo)

			res, err := p.Run(context.Background())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.Status != "tainted" || res.Reason != "config_changed" {
				t.Errorf("result = %q/%q, want tainted/config_changed", res.Status, res.Reason)
			}
			if exists(marker) {
				t.Error("the pinned command ran against a changed configuration")
			}
			if doc := readVerification(t, run); doc.Status != "tainted" {
				t.Errorf("on disk status = %q, want tainted", doc.Status)
			}
		})
	}
}

// 3. Bytes that are the same bytes are the same configuration, however they
// got back: the run is pinned to content, not to an inode or a timestamp.
func TestConfigRestoredByteForByteStillRuns(t *testing.T) {
	repo, run, out := gitRepo(t), runDir(t), outside(t)
	marker := filepath.Join(out, "ran")
	body := configYAML(checkSpec{name: "unit", timeout: "10s", argv: helperArgv(t, "write", marker)})
	writeConfig(t, repo, body)

	p := pin(t, repo, run)
	write(t, repo, configName, "version: 1\nverify: []\n")
	write(t, repo, configName, body)

	res, err := p.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "passed" {
		t.Fatalf("status = %q/%q, want passed", res.Status, res.Reason)
	}
	if !exists(marker) {
		t.Error("the pinned command did not run")
	}
}

// 4. There is no shell between this package and a check, so punctuation in an
// argument is an argument.
func TestArgumentsAreNeverInterpretedByAShell(t *testing.T) {
	repo, run, out := gitRepo(t), runDir(t), outside(t)
	injected := filepath.Join(out, "pwned")
	arg := "; touch " + injected + " #"
	writeConfig(t, repo, configYAML(checkSpec{
		name: "unit", timeout: "10s", argv: helperArgv(t, "out", arg),
	}))

	res, err := pin(t, repo, run).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exists(injected) {
		t.Error("an argument was interpreted as a command")
	}
	if got := checkByName(t, res, "unit").Stdout; got != arg {
		t.Errorf("stdout = %q, want the argument verbatim %q", got, arg)
	}
}

// 5. Every ending a check can have is recorded as what it was.
func TestChecksRecordHowTheyEnded(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	missing := filepath.Join(outside(t), "no-such-program")
	writeConfig(t, repo, configYAML(
		checkSpec{name: "ok", timeout: "10s", argv: helperArgv(t, "out", "fine")},
		checkSpec{name: "nonzero", timeout: "10s", argv: helperArgv(t, "err", "bad", "exit", "3")},
		checkSpec{name: "slow", timeout: "200ms", argv: helperArgv(t, "sleep", "30s")},
		checkSpec{name: "absent", timeout: "10s", argv: []string{missing}},
	))

	res, err := pin(t, repo, run).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Errorf("status = %q, want failed", res.Status)
	}
	if len(res.Checks) != 4 {
		t.Fatalf("checks = %d, want 4 — a failing check does not stop the rest", len(res.Checks))
	}

	ok := checkByName(t, res, "ok")
	if ok.Status != "passed" || ok.ExitCode == nil || *ok.ExitCode != 0 || ok.Stdout != "fine" {
		t.Errorf("ok = %+v, want passed with exit 0 and its output", ok)
	}
	if ok.StartedAt.IsZero() || ok.EndedAt.Before(ok.StartedAt) || ok.DurationMS < 0 {
		t.Errorf("ok timing = %+v", ok)
	}

	nonzero := checkByName(t, res, "nonzero")
	if nonzero.Status != "failed" || nonzero.ExitCode == nil || *nonzero.ExitCode != 3 {
		t.Errorf("nonzero = %+v, want failed with exit 3", nonzero)
	}
	if nonzero.Stderr != "bad" {
		t.Errorf("nonzero stderr = %q, want %q", nonzero.Stderr, "bad")
	}

	slow := checkByName(t, res, "slow")
	if slow.Status != "timeout" {
		t.Errorf("slow = %+v, want timeout", slow)
	}
	if slow.Signal == "" {
		t.Errorf("slow = %+v, want the signal it was killed with", slow)
	}
	if slow.Timeout != "200ms" {
		t.Errorf("slow timeout = %q, want the pinned 200ms", slow.Timeout)
	}

	absent := checkByName(t, res, "absent")
	if absent.Status != "error" || absent.ExitCode != nil {
		t.Errorf("absent = %+v, want error with no exit code", absent)
	}
	// A start error itself is not persisted. The executable still appears once
	// in the deliberately pinned command, but never as unsanitized process output.
	if absent.Stdout != "" || absent.Stderr != "" {
		t.Errorf("absent output = stdout %q, stderr %q; want no start-error text", absent.Stdout, absent.Stderr)
	}
}

// 6. A check's children go when the check goes: a timeout that left a
// descendant running would leave the repository being written to after the
// run said it had stopped.
func TestTimeoutKillsTheWholeProcessGroup(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	survivor := filepath.Join(outside(t), "survivor")
	writeConfig(t, repo, configYAML(checkSpec{
		name:    "spawner",
		timeout: "300ms",
		argv:    helperArgv(t, "spawn", survivor, "3s", "sleep", "30s"),
	}))

	res, err := pin(t, repo, run).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := checkByName(t, res, "spawner").Status; got != "timeout" {
		t.Fatalf("status = %q, want timeout", got)
	}
	// Long enough that a surviving descendant would have written by now.
	time.Sleep(4 * time.Second)
	if exists(survivor) {
		t.Error("a descendant outlived the check it was started by")
	}
}

func TestSuccessfulCheckStopsProcessGroupBeforeReapingLeader(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	writeConfig(t, repo, configYAML(checkSpec{
		name:    "success",
		timeout: "10s",
		argv:    helperArgv(t, "exit", "0"),
	}))

	originalStop := stopVerificationProcess
	called := false
	calledAfterReap := false
	stopVerificationProcess = func(cmd *exec.Cmd) error {
		called = true
		calledAfterReap = cmd.ProcessState != nil
		return nil
	}
	t.Cleanup(func() { stopVerificationProcess = originalStop })

	res, err := pin(t, repo, run).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := checkByName(t, res, "success").Status; got != "passed" {
		t.Fatalf("status = %q, want passed", got)
	}
	if !called {
		t.Fatal("the process group was not stopped")
	}
	if calledAfterReap {
		t.Error("the process group was signalled after its leader was reaped")
	}
}

func TestSuccessfulCheckWithInheritedPipesPassesAfterDescendantCleanup(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	survivor := filepath.Join(outside(t), "pipe-survivor")
	resume := filepath.Join(outside(t), "resume-pipe-survivor")
	writeConfig(t, repo, configYAML(checkSpec{
		name:    "pipe-spawner",
		timeout: "10s",
		argv:    helperArgv(t, "spawn-wait", survivor, resume),
	}))

	res, err := pin(t, repo, run).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := checkByName(t, res, "pipe-spawner").Status; got != "passed" {
		t.Fatalf("status = %q, want passed after descendant cleanup", got)
	}
	if err := os.WriteFile(resume, []byte("resume\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if exists(survivor) {
		t.Error("a pipe-holding descendant outlived a successful check")
	}
}

// 7. Output is bounded before it is held and sanitized before it is written.
func TestOutputIsBoundedAndSanitized(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	const secret = "SECRET-TOKEN-abcdef"
	writeConfig(t, repo, configYAML(
		checkSpec{name: "loud", timeout: "10s", argv: helperArgv(t, "outbytes", "500")},
		checkSpec{name: "leaky", timeout: "10s", argv: helperArgv(t, "err", "before "+secret+" after")},
	))

	res, err := pinWith(t, repo, run, VerificationOptions{
		MaxOutputBytes: 64,
		Sanitize: func(s string) (string, error) {
			return strings.ReplaceAll(s, secret, "[redacted]"), nil
		},
	}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	loud := checkByName(t, res, "loud")
	if int64(len(loud.Stdout)) > 64 || !loud.StdoutTruncated {
		t.Errorf("loud stdout = %d bytes, truncated=%v, want at most 64 and marked", len(loud.Stdout), loud.StdoutTruncated)
	}
	leaky := checkByName(t, res, "leaky")
	if !strings.Contains(leaky.Stderr, "[redacted]") {
		t.Errorf("leaky stderr = %q, want the redacted marker", leaky.Stderr)
	}
	assertAbsent(t, run, secret)
}

// 8. A check that changed the repository is reported as having done so, and
// what it reported about itself is left exactly as it was.
func TestRepositoryMutationIsWarnedAboutWithoutRewritingTheCheck(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	writeConfig(t, repo, configYAML(checkSpec{
		name:    "writer",
		timeout: "10s",
		argv:    helperArgv(t, "write", filepath.Join(repo, "b.txt"), "exit", "3"),
	}))

	res, err := pin(t, repo, run).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	writer := checkByName(t, res, "writer")
	if writer.Status != "failed" || writer.ExitCode == nil || *writer.ExitCode != 3 {
		t.Errorf("writer = %+v, want its real failure preserved", writer)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Code != "verification_mutated_repository" {
		t.Fatalf("warnings = %+v, want the mutation warning", res.Warnings)
	}
	if got := res.Warnings[0].Paths; len(got) != 1 || got[0] != "b.txt" {
		t.Errorf("changed paths = %v, want [b.txt]", got)
	}
	// The mutation is reported, never undone.
	body, err := os.ReadFile(filepath.Join(repo, "b.txt"))
	if err != nil {
		t.Fatalf("read b.txt: %v", err)
	}
	if string(body) != "marker" {
		t.Errorf("b.txt = %q, want the check's own write left alone", body)
	}
}

func TestIndexFlagMutationIsWarnedAbout(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	writeConfig(t, repo, configYAML(checkSpec{
		name:    "index flag writer",
		timeout: "10s",
		argv:    []string{"git", "update-index", "--assume-unchanged", "b.txt"},
	}))

	res, err := pin(t, repo, run).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "passed" {
		t.Fatalf("status = %q/%q, want passed with a separate warning", res.Status, res.Reason)
	}
	check := checkByName(t, res, "index flag writer")
	if check.Status != "passed" || check.ExitCode == nil || *check.ExitCode != 0 {
		t.Fatalf("check = %+v, want passed with exit code 0", check)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Code != "verification_mutated_repository" {
		t.Fatalf("warnings = %+v, want index mutation warning", res.Warnings)
	}
	if len(res.Warnings[0].Paths) != 0 {
		t.Fatalf("warning paths = %v, want none for index-only mutation", res.Warnings[0].Paths)
	}
}

// 9. A repository that was already dirty hides a mutation from Git's summary:
// a file modified before the run and modified again during it is reported the
// same way both times. The content is what is compared, so it is still seen.
func TestMutationIsSeenInARepositoryThatWasAlreadyDirty(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	write(t, repo, "b.txt", "dirty before the run\n")
	writeConfig(t, repo, configYAML(checkSpec{
		name:    "writer",
		timeout: "10s",
		argv:    helperArgv(t, "write", filepath.Join(repo, "b.txt")),
	}))
	before, err := gitOut(t, repo, "status", "--porcelain")
	if err != nil {
		t.Fatalf("git status: %v", err)
	}

	res, err := pin(t, repo, run).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	after, err := gitOut(t, repo, "status", "--porcelain")
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if before != after {
		t.Fatalf("this test needs a status that did not change: %q then %q", before, after)
	}
	if len(res.Warnings) != 1 || len(res.Warnings[0].Paths) != 1 || res.Warnings[0].Paths[0] != "b.txt" {
		t.Errorf("warnings = %+v, want b.txt reported as changed", res.Warnings)
	}
}

// 10. Build output the operator already excluded is not the run's work and is
// not a mutation of the repository.
func TestIgnoredFilesAreNotRepositoryMutations(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	write(t, repo, ".gitignore", "build/\n")
	runGit(t, repo, "add", ".gitignore")
	runGit(t, repo, "commit", "-m", "ignore build output")
	if err := os.Mkdir(filepath.Join(repo, "build"), 0o700); err != nil {
		t.Fatalf("create build: %v", err)
	}
	writeConfig(t, repo, configYAML(checkSpec{
		name:    "builder",
		timeout: "10s",
		argv:    helperArgv(t, "write", filepath.Join(repo, "build", "out.bin")),
	}))

	res, err := pin(t, repo, run).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "passed" {
		t.Errorf("status = %q/%q, want passed", res.Status, res.Reason)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings = %+v, want none for ignored output", res.Warnings)
	}
}

// 11. Passed means every check passed, and nothing else does.
func TestPassedMeansEveryCheckPassed(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	writeConfig(t, repo, configYAML(
		checkSpec{name: "a", timeout: "10s", argv: helperArgv(t, "exit", "0")},
		checkSpec{name: "b", timeout: "10s", argv: helperArgv(t, "exit", "0")},
	))
	res, err := pin(t, repo, run).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "passed" {
		t.Errorf("status = %q, want passed", res.Status)
	}
	if doc := readVerification(t, run); doc.Status != "passed" || len(doc.Checks) != 2 {
		t.Errorf("on disk = %+v, want the same two passing checks", doc)
	}
}

// 12. The evidence is the operator's alone, before the run and after it.
func TestVerificationEvidenceIsPrivate(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	writeConfig(t, repo, configYAML(checkSpec{name: "a", timeout: "10s", argv: helperArgv(t, "exit", "0")}))
	p := pin(t, repo, run)

	assertMode := func(when string) {
		t.Helper()
		dir, err := os.Stat(verifyDirOf(run))
		if err != nil {
			t.Fatalf("stat the verification directory %s: %v", when, err)
		}
		if got := dir.Mode().Perm(); got != 0o700 {
			t.Errorf("%s: directory mode = %o, want 700", when, got)
		}
		doc, err := os.Stat(verifyResultPath(run))
		if err != nil {
			t.Fatalf("stat the results %s: %v", when, err)
		}
		if got := doc.Mode().Perm(); got != 0o600 {
			t.Errorf("%s: results mode = %o, want 600", when, got)
		}
	}
	assertMode("while pending")
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertMode("once final")
}

// 13. Nothing planted where the evidence goes decides where it lands.
func TestPlantedNamesAreRefused(t *testing.T) {
	t.Run("a verification directory that is already there", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		writeConfig(t, repo, configYAML(checkSpec{name: "a", timeout: "10s", argv: helperArgv(t, "exit", "0")}))
		if err := os.Mkdir(verifyDirOf(run), 0o700); err != nil {
			t.Fatalf("plant the directory: %v", err)
		}
		if _, err := PinVerification(context.Background(), repo, run, configPathIn(repo), VerificationOptions{}); err == nil {
			t.Fatal("PinVerification accepted a directory it did not create")
		}
	})

	t.Run("a verification directory that is a symlink", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		elsewhere := outside(t)
		writeConfig(t, repo, configYAML(checkSpec{name: "a", timeout: "10s", argv: helperArgv(t, "exit", "0")}))
		if err := os.Symlink(elsewhere, verifyDirOf(run)); err != nil {
			t.Fatalf("plant the symlink: %v", err)
		}
		if _, err := PinVerification(context.Background(), repo, run, configPathIn(repo), VerificationOptions{}); err == nil {
			t.Fatal("PinVerification followed a planted symlink")
		}
		if entries, err := os.ReadDir(elsewhere); err != nil || len(entries) != 0 {
			t.Errorf("wrote through the symlink: %v %v", entries, err)
		}
	})

	t.Run("results replaced by a regular file during the run", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		writeConfig(t, repo, configYAML(checkSpec{name: "a", timeout: "10s", argv: helperArgv(t, "exit", "0")}))
		p := pin(t, repo, run)

		if err := os.Remove(verifyResultPath(run)); err != nil {
			t.Fatalf("remove the pending results: %v", err)
		}
		const intruder = "not this run's result\n"
		if err := os.WriteFile(verifyResultPath(run), []byte(intruder), 0o600); err != nil {
			t.Fatalf("write the replacement: %v", err)
		}
		replacement, err := os.Lstat(verifyResultPath(run))
		if err != nil {
			t.Fatalf("inspect the replacement: %v", err)
		}
		if os.SameFile(replacement, p.resultInfo) {
			t.Fatal("replacement reused the identity of the held pending result")
		}
		if _, err := p.Run(context.Background()); err == nil {
			t.Fatal("Run replaced a result it did not write")
		}
		body, err := os.ReadFile(verifyResultPath(run))
		if err != nil {
			t.Fatalf("read the replacement: %v", err)
		}
		if string(body) != intruder {
			t.Errorf("replacement = %q, want it untouched", body)
		}
	})

	t.Run("results replaced by a symlink during the run", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		target := filepath.Join(outside(t), "target")
		if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
			t.Fatalf("write the target: %v", err)
		}
		writeConfig(t, repo, configYAML(checkSpec{name: "a", timeout: "10s", argv: helperArgv(t, "exit", "0")}))
		p := pin(t, repo, run)

		if err := os.Remove(verifyResultPath(run)); err != nil {
			t.Fatalf("remove the pending results: %v", err)
		}
		if err := os.Symlink(target, verifyResultPath(run)); err != nil {
			t.Fatalf("plant the symlink: %v", err)
		}
		if _, err := p.Run(context.Background()); err == nil {
			t.Fatal("Run wrote through a planted symlink")
		}
		body, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("read the target: %v", err)
		}
		if string(body) != "untouched" {
			t.Errorf("target = %q, want it untouched", body)
		}
	})
}

// 14. A configuration this package cannot vouch for is refused outright,
// before a run is pinned to it.
func TestConfigurationsThatCannotBePinned(t *testing.T) {
	good := configYAML(checkSpec{name: "a", timeout: "1s", argv: []string{"/bin/true"}})

	bodies := map[string]string{
		"not YAML at all":          "version: 1\nverify: [",
		"an unknown field":         good + "extra: 1\n",
		"another version":          strings.Replace(good, "version: 1", "version: 2", 1),
		"no version":               strings.Replace(good, "version: 1\n", "", 1),
		"two documents":            good + "---\nversion: 1\nverify: []\n",
		"an empty document":        "",
		"no checks":                "version: 1\nverify: []\n",
		"a duplicate name":         good + strings.TrimPrefix(configYAML(checkSpec{name: "a", timeout: "1s", argv: []string{"/bin/true"}}), "version: 1\nverify:\n"),
		"an empty name":            configYAML(checkSpec{name: "", timeout: "1s", argv: []string{"/bin/true"}}),
		"no command":               "version: 1\nverify:\n  - name: \"a\"\n    timeout: \"1s\"\n    command: []\n",
		"an empty executable":      configYAML(checkSpec{name: "a", timeout: "1s", argv: []string{""}}),
		"a timeout of zero":        configYAML(checkSpec{name: "a", timeout: "0s", argv: []string{"/bin/true"}}),
		"a negative timeout":       configYAML(checkSpec{name: "a", timeout: "-1s", argv: []string{"/bin/true"}}),
		"a timeout beyond an hour": configYAML(checkSpec{name: "a", timeout: "2h", argv: []string{"/bin/true"}}),
		"no timeout":               "version: 1\nverify:\n  - name: \"a\"\n    command: [\"/bin/true\"]\n",
		"an alias":                 "version: 1\nverify: &v\n  - name: \"a\"\n    timeout: \"1s\"\n    command: [\"/bin/true\"]\nother: *v\n",
		"a custom tag":             "version: 1\nverify:\n  - name: !secret \"a\"\n    timeout: \"1s\"\n    command: [\"/bin/true\"]\n",
		"a binary tag":             "version: 1\nverify:\n  - name: !!binary \"YQ==\"\n    timeout: \"1s\"\n    command: [\"/bin/true\"]\n",
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			repo, run := gitRepo(t), runDir(t)
			writeConfig(t, repo, body)
			if _, err := PinVerification(context.Background(), repo, run, configPathIn(repo), VerificationOptions{}); err == nil {
				t.Fatal("PinVerification accepted it")
			}
			if exists(verifyDirOf(run)) {
				t.Error("a refused configuration still left a verification directory behind")
			}
		})
	}

	t.Run("larger than the bound", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		writeConfig(t, repo, "version: 1\nverify: []\n# "+strings.Repeat("p", 1<<20))
		if _, err := PinVerification(context.Background(), repo, run, configPathIn(repo), VerificationOptions{}); err == nil {
			t.Fatal("PinVerification accepted a configuration over the bound")
		}
	})

	t.Run("not valid UTF-8", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		writeBytes(t, repo, configName, append([]byte("version: 1\nverify: [] # "), 0xff, 0xfe))
		if _, err := PinVerification(context.Background(), repo, run, configPathIn(repo), VerificationOptions{}); err == nil {
			t.Fatal("PinVerification accepted bytes that are not UTF-8")
		}
	})
}

// 15. Where the configuration is read from is this package's decision.
func TestConfigPathsThatCannotBeTrusted(t *testing.T) {
	body := configYAML(checkSpec{name: "a", timeout: "1s", argv: []string{"/bin/true"}})

	paths := map[string]func(t *testing.T, repo string) string{
		"a relative path": func(t *testing.T, repo string) string {
			writeConfig(t, repo, body)
			return configName
		},
		"below the repository root": func(t *testing.T, repo string) string {
			write(t, repo, filepath.Join("sub", configName), body)
			return filepath.Join(repo, "sub", configName)
		},
		"outside the repository": func(t *testing.T, repo string) string {
			dir := outside(t)
			write(t, dir, configName, body)
			return filepath.Join(dir, configName)
		},
		"reached by traversal": func(t *testing.T, repo string) string {
			writeConfig(t, repo, body)
			return repo + string(os.PathSeparator) + "sub" + string(os.PathSeparator) + ".." + string(os.PathSeparator) + configName
		},
		"a symlink": func(t *testing.T, repo string) string {
			dir := outside(t)
			write(t, dir, "real.yaml", body)
			if err := os.Symlink(filepath.Join(dir, "real.yaml"), configPathIn(repo)); err != nil {
				t.Fatalf("plant the symlink: %v", err)
			}
			return configPathIn(repo)
		},
		"a directory": func(t *testing.T, repo string) string {
			if err := os.Mkdir(configPathIn(repo), 0o700); err != nil {
				t.Fatalf("create the directory: %v", err)
			}
			return configPathIn(repo)
		},
		"nothing at all": func(t *testing.T, repo string) string {
			return configPathIn(repo)
		},
	}

	for name, setup := range paths {
		t.Run(name, func(t *testing.T) {
			repo, run := gitRepo(t), runDir(t)
			path := setup(t, repo)
			if _, err := PinVerification(context.Background(), repo, run, path, VerificationOptions{}); err == nil {
				t.Fatalf("PinVerification accepted %s", path)
			}
		})
	}
}

// 16. A run that was cancelled before it began executes nothing and leaves
// the pending document behind rather than a verdict it never reached.
func TestCancelledRunExecutesNothing(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	marker := filepath.Join(outside(t), "ran")
	writeConfig(t, repo, configYAML(checkSpec{
		name: "unit", timeout: "10s", argv: helperArgv(t, "write", marker),
	}))
	p := pin(t, repo, run)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Run(ctx); err == nil {
		t.Fatal("Run reported success on a cancelled context")
	}
	if exists(marker) {
		t.Error("a cancelled run executed a check")
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// 17. A run that never reached its verdict is unfinished on disk, not passing.
func TestPendingSurvivesARunThatNeverHappened(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	writeConfig(t, repo, configYAML(checkSpec{name: "a", timeout: "10s", argv: helperArgv(t, "exit", "0")}))
	p := pin(t, repo, run)
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if doc := readVerification(t, run); doc.Status != "pending" {
		t.Errorf("status = %q, want it still pending", doc.Status)
	}
	if _, err := p.Run(context.Background()); err == nil {
		t.Error("Run wrote through a closed verification")
	}
}

// 18. One verdict, once: a second Run is a recorder that has lost track of its
// own run rather than a second opinion.
func TestRunHappensOnlyOnce(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	writeConfig(t, repo, configYAML(checkSpec{name: "a", timeout: "10s", argv: helperArgv(t, "exit", "0")}))
	p := pin(t, repo, run)
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := p.Run(context.Background()); err == nil {
		t.Error("Run ran twice")
	}
}

// The example configuration shipped with agentrec has to be one this package
// would accept: an example that does not parse is documentation teaching an
// operator a schema that does not exist.
func TestTheExampleConfigurationParses(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".agentrec.example.yaml"))
	if err != nil {
		t.Fatalf("read the example configuration: %v", err)
	}
	entries, err := parseVerifyConfig(raw)
	if err != nil {
		t.Fatalf("parse the example configuration: %v", err)
	}
	if len(entries) == 0 {
		t.Errorf("the example configuration describes no checks")
	}
}
