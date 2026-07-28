package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/action"
	"github.com/seongwoo-choi/agentrec/internal/provider"
	"github.com/seongwoo-choi/agentrec/internal/storage"
)

// The provider process under supervision is this test binary re-executed in a
// helper mode, so the supervisor is exercised against a real process group
// without depending on any agent CLI being installed.
const helperPrefix = "helper:"

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && strings.HasPrefix(os.Args[1], helperPrefix) {
		os.Exit(helperMain(strings.TrimPrefix(os.Args[1], helperPrefix), os.Args[2:]))
	}
	os.Exit(m.Run())
}

// helperMain is the entire provider side of these tests. Each mode prints
// provider events on stdout and cooperates, or refuses to, with the signals the
// supervisor sends.
func helperMain(mode string, args []string) int {
	switch mode {
	case "stream":
		// Emits one event, then waits to be told the recorder has already parsed
		// it. A supervisor that buffered the whole run would never release it.
		emit("evt-1")
		if !waitForFile(args[0], 10*time.Second) {
			fmt.Fprintln(os.Stderr, "helper: first event was never acknowledged")
			return 1
		}
		emit("evt-2")
		return 0
	case "stderr":
		fmt.Fprintln(os.Stderr, "starting up")
		fmt.Fprintln(os.Stderr, helperSecret)
		fmt.Fprintln(os.Stderr, "shutting down")
		emit("evt-1")
		return 0
	case "exit":
		emit("evt-1")
		code, _ := strconv.Atoi(args[0])
		return code
	case "overlong":
		// One line past what the recorder can store whole, then far more than a
		// pipe buffer's worth of further output. A supervisor that stops reading
		// at the unstorable line leaves this process blocked on stdout forever.
		os.Stdout.Write(bytes.Repeat([]byte("x"), maxLineBytes+1))
		os.Stdout.Write(bytes.Repeat([]byte("y\n"), 1<<19))
		emit("evt-1")
		return 0
	case "spawn":
		// Dies on SIGINT by default, as does the descendant it leaves behind.
		spawn("helper:sleep", args[0])
		time.Sleep(10 * time.Minute)
		return 0
	case "stubborn":
		// Refuses SIGTERM, so only SIGKILL ends it or its descendant.
		term := make(chan os.Signal, 1)
		signal.Notify(term, syscall.SIGTERM)
		spawn("helper:stubborn-child", args[1])
		go func() {
			<-term
			os.WriteFile(args[0], []byte("term"), 0o600)
		}()
		time.Sleep(10 * time.Minute)
		return 0
	case "stubborn-child":
		signal.Notify(make(chan os.Signal, 1), syscall.SIGTERM)
		time.Sleep(10 * time.Minute)
		return 0
	case "defiant":
		// Ignores SIGINT outright, as does the descendant it leaves behind: an
		// interrupt alone never ends this tree, only a group SIGKILL does. One
		// event is emitted first, so the run has evidence to finalize with.
		signal.Ignore(syscall.SIGINT)
		emit("evt-1")
		spawnDefiant(args[0])
		time.Sleep(10 * time.Minute)
		return 0
	case "defiant-child":
		signal.Ignore(syscall.SIGINT)
		os.WriteFile(args[0], []byte(strconv.Itoa(os.Getpid())), 0o600)
		time.Sleep(10 * time.Minute)
		return 0
	case "sleep":
		time.Sleep(10 * time.Minute)
		return 0
	case "mark":
		// Records that a process really was launched, which is the mark a run
		// refused before its provider must never leave behind.
		os.WriteFile(args[0], []byte("launched"), 0o600)
		emit("evt-1")
		return 0
	}
	fmt.Fprintf(os.Stderr, "helper: unknown mode %q\n", mode)
	return 2
}

// helperSecret is a synthetic private key written across several lines, which
// only a whole-capture redaction can recognise.
const helperSecret = `-----BEGIN RSA PRIVATE KEY-----
MIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX6Ppy1tPf9Cnzj4p4WGeKLs1Pt8Qu
KUpRKfFLfRYC9AIKjbJTWit+CqvjWYzvQwECAwEAAQJAIJLixBy2qpFoS4DSmoEm
-----END RSA PRIVATE KEY-----`

// emit prints one provider event line.
func emit(id string) {
	fmt.Printf("{\"id\":%q,\"type\":\"tool_use\"}\n", id)
}

// spawn starts a descendant in the helper's own process group and records its
// pid where the test can find it.
func spawn(mode, pidFile string) {
	cmd := exec.Command(os.Args[0], mode)
	// Nil streams are /dev/null: a descendant holding the run's stdout pipe
	// would hide whether the supervisor ended it.
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "helper: spawn: %v\n", err)
		return
	}
	os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
}

// spawnDefiant starts a descendant that records its own pid only once it is
// already ignoring SIGINT, so a test cannot signal the group in the window
// before the whole tree is defiant.
func spawnDefiant(pidFile string) {
	cmd := exec.Command(os.Args[0], helperPrefix+"defiant-child", pidFile)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "helper: spawn: %v\n", err)
	}
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

// helperCommand builds the structured command that runs this test binary in the
// named helper mode.
func helperCommand(mode string, args ...string) provider.Command {
	return provider.Command{
		Executable: os.Args[0],
		Args:       append([]string{helperPrefix + mode}, args...),
		Version:    "0.0.0-test",
	}
}

func newBundle(t *testing.T) *storage.Bundle {
	t.Helper()
	b, err := storage.Create(t.TempDir(), "run-1", storage.Manifest{
		Provider:  "test",
		Argv:      []string{"agentrec"},
		CWD:       t.TempDir(),
		StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("storage.Create: %v", err)
	}
	return b
}

// jsonlParser stands in for a provider parser: one action per event line, plus
// however many warnings the test wants the manifest to carry. When gate is set
// it is created as soon as the first event has been parsed, which is what makes
// streaming observable from the provider side.
func jsonlParser(gate string, warnings int) Parser {
	return func(r io.Reader) (ParseResult, error) {
		res := ParseResult{WarningCount: warnings}
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			var ev struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(line, &ev); err != nil {
				return res, err
			}
			res.Actions = append(res.Actions, action.Action{
				ID:        ev.ID,
				Type:      action.TypeToolCall,
				Assurance: action.AssuranceProviderReported,
			})
			if gate != "" && len(res.Actions) == 1 {
				if err := os.WriteFile(gate, []byte("parsed"), 0o600); err != nil {
					return res, err
				}
			}
		}
		return res, sc.Err()
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

func readManifest(t *testing.T, dir string) storage.Manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m storage.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return m
}

// processResultOnDisk is the recorded shape of process/result.json.
type processResultOnDisk struct {
	StartedAt      time.Time `json:"startedAt"`
	EndedAt        time.Time `json:"endedAt"`
	DurationMillis int64     `json:"durationMillis"`
	ExitCode       *int      `json:"exitCode"`
	Signal         string    `json:"signal"`
	ExitReason     string    `json:"exitReason"`
}

func readProcessResult(t *testing.T, dir string) processResultOnDisk {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "process", "result.json"))
	if err != nil {
		t.Fatalf("read process result: %v", err)
	}
	var pr processResultOnDisk
	if err := json.Unmarshal(raw, &pr); err != nil {
		t.Fatalf("decode process result: %v", err)
	}
	return pr
}

// requireFinalizedStartError asserts the bundle records a run that never
// reached a process: a start_error, finalized, whose timing is present and
// coherent in the result the caller got and everywhere it was recorded. Zero
// times are called out on their own, because an endedAt that was never set
// still reads as coherent against a startedAt it merely precedes.
func requireFinalizedStartError(t *testing.T, b *storage.Bundle, res Result) {
	t.Helper()
	if res.ExitReason != ReasonStartError {
		t.Errorf("ExitReason = %q, want %q", res.ExitReason, ReasonStartError)
	}
	if res.StartedAt.IsZero() || res.EndedAt.IsZero() {
		t.Errorf("Result timing is not recorded: startedAt = %v, endedAt = %v", res.StartedAt, res.EndedAt)
	}
	if res.EndedAt.Before(res.StartedAt) || res.Duration < 0 {
		t.Errorf("Result timing is not coherent: %+v", res)
	}

	pr := readProcessResult(t, b.Dir())
	if pr.ExitReason != ReasonStartError {
		t.Errorf("result.json exitReason = %q, want %q", pr.ExitReason, ReasonStartError)
	}
	if pr.ExitCode != nil {
		t.Errorf("result.json exitCode = %v, want null for a run that never started", *pr.ExitCode)
	}
	if pr.StartedAt.IsZero() || pr.EndedAt.IsZero() {
		t.Errorf("result.json timing is not recorded: %+v", pr)
	}
	if pr.EndedAt.Before(pr.StartedAt) || pr.DurationMillis < 0 {
		t.Errorf("result.json timing is not coherent: %+v", pr)
	}

	m := readManifest(t, b.Dir())
	if m.ExitReason != ReasonStartError {
		t.Errorf("manifest exitReason = %q, want %q", m.ExitReason, ReasonStartError)
	}
	if m.EndedAt == nil || m.EndedAt.IsZero() {
		t.Fatalf("manifest endedAt = %v, want the time the run ended", m.EndedAt)
	}
	if m.EndedAt.Before(m.StartedAt) {
		t.Errorf("manifest endedAt %v is before startedAt %v", m.EndedAt, m.StartedAt)
	}
}

// waitForPID reads a pid a helper recorded, waiting for the file to be complete.
func waitForPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no pid appeared at %s", path)
	return 0
}

// requireGone fails unless the process is no longer there. Signal 0 delivers
// nothing and only reports whether the pid still exists.
func requireGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant %d is still running", pid)
}

func TestRunStreamsStdoutWhileTheProviderIsStillRunning(t *testing.T) {
	b := newBundle(t)
	gate := filepath.Join(t.TempDir(), "acknowledged")

	res, err := Run(context.Background(), Request{
		Command: helperCommand("stream", gate),
		Bundle:  b,
		Parser:  jsonlParser(gate, 2),
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitReason != ReasonCompleted {
		t.Errorf("ExitReason = %q, want %q", res.ExitReason, ReasonCompleted)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Errorf("ExitCode = %v, want 0", res.ExitCode)
	}

	events := readLines(t, filepath.Join(b.Dir(), "provider-events.sanitized.jsonl"))
	if len(events) != 2 {
		t.Fatalf("provider events = %d, want 2: %q", len(events), events)
	}
	for i, want := range []string{"evt-1", "evt-2"} {
		var ev struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(events[i]), &ev); err != nil {
			t.Fatalf("event %d is not JSON: %v", i, err)
		}
		if ev.ID != want {
			t.Errorf("event %d id = %q, want %q", i, ev.ID, want)
		}
	}

	actions := readLines(t, filepath.Join(b.Dir(), "actions.jsonl"))
	if len(actions) != 2 {
		t.Fatalf("actions = %d, want 2: %q", len(actions), actions)
	}
	var a action.Action
	if err := json.Unmarshal([]byte(actions[0]), &a); err != nil {
		t.Fatalf("action 0 is not JSON: %v", err)
	}
	if a.ID != "evt-1" || a.Assurance != action.AssuranceProviderReported {
		t.Errorf("action 0 = %+v, want evt-1 provider_reported", a)
	}

	m := readManifest(t, b.Dir())
	if m.ExitReason != ReasonCompleted {
		t.Errorf("manifest exitReason = %q, want %q", m.ExitReason, ReasonCompleted)
	}
	if m.WarningCount != 2 {
		t.Errorf("manifest warningCount = %d, want 2 (from the parser)", m.WarningCount)
	}
}

func TestRunKeepsStderrSeparateAndRedactsItWhole(t *testing.T) {
	b := newBundle(t)

	if _, err := Run(context.Background(), Request{
		Command: helperCommand("stderr"),
		Bundle:  b,
		Parser:  jsonlParser("", 0),
		Timeout: 30 * time.Second,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(b.Dir(), "process", "stderr.sanitized.log"))
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	stderr := string(raw)
	if !strings.Contains(stderr, "starting up") || !strings.Contains(stderr, "shutting down") {
		t.Errorf("stderr lost ordinary output: %q", stderr)
	}
	if strings.Contains(stderr, "MIIBOgIBAAJBAKj") {
		t.Errorf("stderr still carries the key body: %q", stderr)
	}
	if !strings.Contains(stderr, "[REDACTED:") {
		t.Errorf("stderr has no redaction marker: %q", stderr)
	}

	events := readLines(t, filepath.Join(b.Dir(), "provider-events.sanitized.jsonl"))
	if len(events) != 1 {
		t.Fatalf("provider events = %d, want 1: %q", len(events), events)
	}
	if strings.Contains(events[0], "starting up") || strings.Contains(events[0], "PRIVATE KEY") {
		t.Errorf("stderr leaked into the provider event stream: %q", events[0])
	}
}

func TestRunRecordsNonzeroExitAsEvidenceNotFailure(t *testing.T) {
	b := newBundle(t)

	res, err := Run(context.Background(), Request{
		Command: helperCommand("exit", "7"),
		Bundle:  b,
		Parser:  jsonlParser("", 0),
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run error = %v, want nil: a nonzero provider exit is evidence", err)
	}
	if res.ExitCode == nil || *res.ExitCode != 7 {
		t.Fatalf("ExitCode = %v, want 7", res.ExitCode)
	}
	if res.ExitReason != ReasonNonzero {
		t.Errorf("ExitReason = %q, want %q", res.ExitReason, ReasonNonzero)
	}

	pr := readProcessResult(t, b.Dir())
	if pr.ExitCode == nil || *pr.ExitCode != 7 {
		t.Errorf("result.json exitCode = %v, want 7", pr.ExitCode)
	}
	if pr.ExitReason != ReasonNonzero {
		t.Errorf("result.json exitReason = %q, want %q", pr.ExitReason, ReasonNonzero)
	}
	if pr.DurationMillis < 0 || pr.EndedAt.Before(pr.StartedAt) {
		t.Errorf("result.json timing is not coherent: %+v", pr)
	}
	if m := readManifest(t, b.Dir()); m.ExitReason != ReasonNonzero {
		t.Errorf("manifest exitReason = %q, want %q", m.ExitReason, ReasonNonzero)
	}
}

func TestRunForwardsAnInterruptToTheWholeProcessGroup(t *testing.T) {
	b := newBundle(t)
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	interrupt := make(chan os.Signal, 1)

	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := Run(context.Background(), Request{
			Command:   helperCommand("spawn", pidFile),
			Bundle:    b,
			Parser:    jsonlParser("", 0),
			Timeout:   60 * time.Second,
			Interrupt: interrupt,
		})
		done <- outcome{res, err}
	}()

	descendant := waitForPID(t, pidFile)
	interrupt <- os.Interrupt

	var got outcome
	select {
	case got = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after the interrupt")
	}

	if !errors.Is(got.err, ErrInterrupted) {
		t.Errorf("Run error = %v, want ErrInterrupted", got.err)
	}
	if got.res.ExitReason != ReasonInterrupted {
		t.Errorf("ExitReason = %q, want %q", got.res.ExitReason, ReasonInterrupted)
	}
	if got.res.Signal != "interrupt" {
		t.Errorf("Signal = %q, want %q", got.res.Signal, "interrupt")
	}
	requireGone(t, descendant)

	if m := readManifest(t, b.Dir()); m.ExitReason != ReasonInterrupted {
		t.Errorf("manifest exitReason = %q, want %q", m.ExitReason, ReasonInterrupted)
	}
}

// An interrupt is an ask, and a provider that ignores it must not be able to
// hold the recorder open: the ask is bounded by the same grace an overrun gets,
// and what the run already said is still finalized.
func TestRunKillsAProviderThatIgnoresTheInterrupt(t *testing.T) {
	b := newBundle(t)
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	interrupt := make(chan os.Signal, 1)

	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	started := time.Now()
	go func() {
		res, err := Run(context.Background(), Request{
			Command: helperCommand("defiant", pidFile),
			Bundle:  b,
			Parser:  jsonlParser("", 0),
			// No timeout: nothing but the interrupt itself can end this run, so
			// the interrupt alone has to be enough.
			Timeout:   0,
			KillGrace: 300 * time.Millisecond,
			Interrupt: interrupt,
		})
		done <- outcome{res, err}
	}()

	descendant := waitForPID(t, pidFile)
	interrupt <- os.Interrupt

	var got outcome
	select {
	case got = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Run never returned: the provider ignored SIGINT and was never killed")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("run took %v, want the caller's short grace to bound the interrupt", elapsed)
	}

	if !errors.Is(got.err, ErrInterrupted) {
		t.Errorf("Run error = %v, want ErrInterrupted", got.err)
	}
	// The escalation is how the run was ended, not why: a killed provider that
	// was asked to stop is still an interrupted run, never a timeout.
	if got.res.ExitReason != ReasonInterrupted {
		t.Errorf("ExitReason = %q, want %q", got.res.ExitReason, ReasonInterrupted)
	}
	if got.res.Signal != "killed" {
		t.Errorf("Signal = %q, want %q", got.res.Signal, "killed")
	}
	if got.res.ExitCode != nil {
		t.Errorf("ExitCode = %v, want nil for a signalled process", *got.res.ExitCode)
	}
	requireGone(t, descendant)

	if m := readManifest(t, b.Dir()); m.ExitReason != ReasonInterrupted {
		t.Errorf("manifest exitReason = %q, want %q", m.ExitReason, ReasonInterrupted)
	}
	pr := readProcessResult(t, b.Dir())
	if pr.ExitReason != ReasonInterrupted || pr.ExitCode != nil || pr.Signal != "killed" {
		t.Errorf("result.json = %+v, want interrupted with a null exitCode and signal killed", pr)
	}
	// What the run said before it was ended is evidence, and a run ended this
	// way is exactly the one whose partial record matters.
	if events := readLines(t, filepath.Join(b.Dir(), "provider-events.sanitized.jsonl")); len(events) != 1 {
		t.Errorf("provider events = %d, want the one emitted before the interrupt", len(events))
	}
	if actions := readLines(t, filepath.Join(b.Dir(), "actions.jsonl")); len(actions) != 1 {
		t.Errorf("actions = %d, want the one parsed before the interrupt", len(actions))
	}
}

// A second interrupt is the operator saying they are done waiting: the grace
// still owed to the provider is given up rather than served out.
func TestRunKillsImmediatelyOnASecondInterrupt(t *testing.T) {
	b := newBundle(t)
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	// Both interrupts are queued before the watcher reads either, so the test
	// depends on the second one being acted upon rather than on any timing.
	interrupt := make(chan os.Signal, 2)

	done := make(chan error, 1)
	started := time.Now()
	go func() {
		res, err := Run(context.Background(), Request{
			Command: helperCommand("defiant", pidFile),
			Bundle:  b,
			Parser:  jsonlParser("", 0),
			// A grace far longer than the test is willing to wait: only the
			// second interrupt can end this run in time.
			KillGrace: 5 * time.Minute,
			Interrupt: interrupt,
		})
		if err == nil || res.ExitReason != ReasonInterrupted {
			err = fmt.Errorf("res = %+v, err = %v, want an interrupted run", res, err)
		}
		done <- err
	}()

	descendant := waitForPID(t, pidFile)
	interrupt <- os.Interrupt
	interrupt <- os.Interrupt

	select {
	case err := <-done:
		if !errors.Is(err, ErrInterrupted) {
			t.Fatalf("Run error = %v, want ErrInterrupted", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run waited out the full grace: the second interrupt did not force a kill")
	}
	if elapsed := time.Since(started); elapsed > 30*time.Second {
		t.Errorf("run took %v, want the second interrupt to cut the grace short", elapsed)
	}
	requireGone(t, descendant)
}

func TestRunEscalatesToKillAfterTheCallersGrace(t *testing.T) {
	b := newBundle(t)
	tmp := t.TempDir()
	termFile := filepath.Join(tmp, "term")
	pidFile := filepath.Join(tmp, "descendant.pid")

	started := time.Now()
	res, err := Run(context.Background(), Request{
		Command:   helperCommand("stubborn", termFile, pidFile),
		Bundle:    b,
		Parser:    jsonlParser("", 0),
		Timeout:   200 * time.Millisecond,
		KillGrace: 300 * time.Millisecond,
	})
	elapsed := time.Since(started)

	if !errors.Is(err, ErrTimedOut) {
		t.Fatalf("Run error = %v, want ErrTimedOut", err)
	}
	// The caller's grace was honoured, not the five-second default.
	if elapsed > 3*time.Second {
		t.Errorf("run took %v, want the caller's short grace", elapsed)
	}
	if _, statErr := os.Stat(termFile); statErr != nil {
		t.Errorf("the provider never saw SIGTERM: %v", statErr)
	}
	if res.Signal != "killed" {
		t.Errorf("Signal = %q, want %q", res.Signal, "killed")
	}
	if res.ExitCode != nil {
		t.Errorf("ExitCode = %v, want nil for a signalled process", *res.ExitCode)
	}
	if res.ExitReason != ReasonTimeout {
		t.Errorf("ExitReason = %q, want %q", res.ExitReason, ReasonTimeout)
	}
	requireGone(t, waitForPID(t, pidFile))

	if m := readManifest(t, b.Dir()); m.ExitReason != ReasonTimeout {
		t.Errorf("manifest exitReason = %q, want %q", m.ExitReason, ReasonTimeout)
	}
	pr := readProcessResult(t, b.Dir())
	if pr.ExitCode != nil || pr.Signal != "killed" {
		t.Errorf("result.json = %+v, want a null exitCode and signal killed", pr)
	}
}

func TestDefaultKillGraceIsFiveSeconds(t *testing.T) {
	if DefaultKillGrace != 5*time.Second {
		t.Errorf("DefaultKillGrace = %v, want 5s", DefaultKillGrace)
	}
}

func TestRunFinalizesWhenTheParserFails(t *testing.T) {
	b := newBundle(t)
	parseErr := errors.New("unreadable transcript")

	res, err := Run(context.Background(), Request{
		Command: helperCommand("exit", "0"),
		Bundle:  b,
		Parser: func(io.Reader) (ParseResult, error) {
			return ParseResult{Actions: []action.Action{{
				ID:        "half-parsed",
				Type:      action.TypeToolCall,
				Assurance: action.AssuranceProviderReported,
			}}}, parseErr
		},
		Timeout: 30 * time.Second,
	})
	if !errors.Is(err, parseErr) {
		t.Fatalf("Run error = %v, want %v", err, parseErr)
	}
	if res.ExitReason != ReasonParseError {
		t.Errorf("ExitReason = %q, want %q", res.ExitReason, ReasonParseError)
	}
	if actions := readLines(t, filepath.Join(b.Dir(), "actions.jsonl")); len(actions) != 0 {
		t.Errorf("actions = %q, want none from a failed parse", actions)
	}
	// The raw evidence is still there: only the normalized reading of it failed.
	if events := readLines(t, filepath.Join(b.Dir(), "provider-events.sanitized.jsonl")); len(events) != 1 {
		t.Errorf("provider events = %q, want 1", events)
	}
	if m := readManifest(t, b.Dir()); m.ExitReason != ReasonParseError {
		t.Errorf("manifest exitReason = %q, want %q", m.ExitReason, ReasonParseError)
	}
}

func TestRunFinalizesWhenStorageFails(t *testing.T) {
	b := newBundle(t)
	// A plain file where the process directory belongs: every process artifact
	// this run tries to write will be refused.
	if err := os.WriteFile(filepath.Join(b.Dir(), "process"), []byte("blocked"), 0o600); err != nil {
		t.Fatalf("block process directory: %v", err)
	}

	res, err := Run(context.Background(), Request{
		Command: helperCommand("exit", "0"),
		Bundle:  b,
		Parser:  jsonlParser("", 0),
		Timeout: 30 * time.Second,
	})
	if err == nil {
		t.Fatal("Run error = nil, want the storage failure surfaced")
	}
	if res.ExitReason != ReasonStorageError {
		t.Errorf("ExitReason = %q, want %q", res.ExitReason, ReasonStorageError)
	}
	if m := readManifest(t, b.Dir()); m.ExitReason != ReasonStorageError {
		t.Errorf("manifest exitReason = %q, want %q", m.ExitReason, ReasonStorageError)
	}
}

// An unbounded run is the one with nothing to rescue it: no timeout fires to
// mask a stream the recorder gave up on, so a provider that emits a line too
// large to store must still be read to the end and reported as such.
func TestRunDoesNotHangOnAnOverlongStdoutLineWithoutATimeout(t *testing.T) {
	b := newBundle(t)
	// Cancellation is the test's own escape hatch: if Run does hang, ending the
	// context ends the process group rather than leaving it behind.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := Run(ctx, Request{
			Command: helperCommand("overlong"),
			Bundle:  b,
			Parser:  jsonlParser("", 0),
			Timeout: 0,
		})
		done <- outcome{res, err}
	}()

	var got outcome
	select {
	case got = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Run never returned: the unstorable line left the provider blocked on stdout")
	}

	if got.err == nil {
		t.Fatal("Run error = nil, want the unstorable line surfaced")
	}
	if got.res.ExitReason != ReasonStorageError {
		t.Errorf("ExitReason = %q, want %q", got.res.ExitReason, ReasonStorageError)
	}
	if m := readManifest(t, b.Dir()); m.ExitReason != ReasonStorageError {
		t.Errorf("manifest exitReason = %q, want %q", m.ExitReason, ReasonStorageError)
	}
	if pr := readProcessResult(t, b.Dir()); pr.ExitReason != ReasonStorageError {
		t.Errorf("result.json exitReason = %q, want %q", pr.ExitReason, ReasonStorageError)
	}
	// Half an event is not evidence: the line that could not be stored whole was
	// not stored at all.
	if events := readLines(t, filepath.Join(b.Dir(), "provider-events.sanitized.jsonl")); len(events) != 0 {
		t.Errorf("provider events = %d, want none", len(events))
	}
}

func TestRunRejectsAnUnusableRequest(t *testing.T) {
	tests := []struct {
		name string
		req  Request
	}{
		{"nil bundle", Request{Command: helperCommand("exit", "0"), Parser: jsonlParser("", 0)}},
		{"nil parser", Request{Command: helperCommand("exit", "0"), Bundle: newBundle(t)}},
		{"empty executable", Request{Bundle: newBundle(t), Parser: jsonlParser("", 0)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Run(context.Background(), tt.req); err == nil {
				t.Fatal("Run error = nil, want a validation failure")
			}
		})
	}
}

// A request the recorder cannot run is a run someone will still ask about, so
// once there is a bundle to record into the rejection is recorded and the
// bundle closed, exactly as any other ending is. The request never becomes a
// process: the recorder decided against it before there was one.
func TestRunFinalizesAnUnusableRequestThatHasABundle(t *testing.T) {
	tests := []struct {
		name    string
		request func(*storage.Bundle) Request
	}{
		// A runnable command with no parser: nothing may be started for it.
		{"nil parser", func(b *storage.Bundle) Request {
			return Request{Command: helperCommand("exit", "0"), Bundle: b}
		}},
		{"empty executable", func(b *storage.Bundle) Request {
			return Request{Bundle: b, Parser: jsonlParser("", 0)}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newBundle(t)

			res, err := Run(context.Background(), tt.request(b))
			if err == nil {
				t.Fatal("Run error = nil, want the validation failure surfaced")
			}
			requireFinalizedStartError(t, b, res)

			// The helper emits an event as its first act, so an empty event stream
			// is how a process that was never started shows up here.
			if events := readLines(t, filepath.Join(b.Dir(), "provider-events.sanitized.jsonl")); len(events) != 0 {
				t.Errorf("provider events = %q, want none: the request was refused before any process", events)
			}
			if werr := b.WriteProviderEvent([]byte(`{"id":"late"}`)); !errors.Is(werr, storage.ErrFinalized) {
				t.Errorf("write after Run = %v, want storage.ErrFinalized", werr)
			}
		})
	}
}

// An interrupt that arrived while the run was still being prepared is the
// operator's answer to this run too. The supervisor is handed a signal it
// already holds, so there is no window here for the answer to be lost in: the
// provider is never launched, and the bundle is still finalized as the
// interrupted run it is rather than left open.
func TestRunNeverLaunchesAProviderItAlreadyHoldsAnInterruptFor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command func(marker string) provider.Command
	}{
		// A provider that records having been launched. What it leaves behind is
		// the mark of a process that ran at all.
		{"a provider that marks its own launch", func(marker string) provider.Command {
			return helperCommand("mark", marker)
		}},
		// An executable that is not there: launching it can only fail, and that
		// failure is recorded as a start error. So the reason recorded here says
		// whether the launch was ever attempted, without depending on how quickly
		// a launched process gets to run before the supervisor ends it.
		{"an executable no launch could have succeeded with", func(string) provider.Command {
			return provider.Command{Executable: filepath.Join(t.TempDir(), "not-installed")}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBundle(t)
			marker := filepath.Join(t.TempDir(), "launched")
			interrupt := make(chan os.Signal, 1)
			interrupt <- os.Interrupt

			res, err := Run(context.Background(), Request{
				Command:   tc.command(marker),
				Bundle:    b,
				Parser:    jsonlParser("", 0),
				Timeout:   30 * time.Second,
				Interrupt: interrupt,
			})

			if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("provider launch marker = %v, want the provider never launched", statErr)
			}
			if !errors.Is(err, ErrInterrupted) {
				t.Errorf("Run error = %v, want ErrInterrupted", err)
			}
			if res.ExitReason != ReasonInterrupted {
				t.Errorf("ExitReason = %q, want %q: nothing may be launched after the interrupt", res.ExitReason, ReasonInterrupted)
			}
			if res.ExitCode != nil {
				t.Errorf("ExitCode = %v, want nil for a run that never started", *res.ExitCode)
			}
			if res.StartedAt.IsZero() || res.EndedAt.IsZero() || res.EndedAt.Before(res.StartedAt) {
				t.Errorf("Result timing is not recorded coherently: %+v", res)
			}

			// The bundle describes a run that stopped, not one still going.
			if m := readManifest(t, b.Dir()); m.ExitReason != ReasonInterrupted || m.EndedAt == nil {
				t.Errorf("manifest exitReason = %q, endedAt = %v, want a finalized interrupted run", m.ExitReason, m.EndedAt)
			}
			if pr := readProcessResult(t, b.Dir()); pr.ExitReason != ReasonInterrupted || pr.ExitCode != nil {
				t.Errorf("result.json = %+v, want interrupted with a null exit code", pr)
			}
			if events := readLines(t, filepath.Join(b.Dir(), "provider-events.sanitized.jsonl")); len(events) != 0 {
				t.Errorf("provider events = %q, want none: no provider ran", events)
			}
			if werr := b.WriteProviderEvent([]byte(`{"id":"late"}`)); !errors.Is(werr, storage.ErrFinalized) {
				t.Errorf("write after Run = %v, want storage.ErrFinalized", werr)
			}
		})
	}
}

func TestRunHonorsAnInterruptedStartGate(t *testing.T) {
	b := newBundle(t)
	marker := filepath.Join(t.TempDir(), "launched")
	gateCalled := false

	res, err := Run(context.Background(), Request{
		Command: helperCommand("mark", marker),
		Bundle:  b,
		Parser:  jsonlParser("", 0),
		Timeout: 30 * time.Second,
		StartGate: func(start func() error) error {
			gateCalled = true
			return ErrInterrupted
		},
	})

	if !gateCalled {
		t.Fatal("StartGate was not called")
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("provider launch marker = %v, want the provider never launched", statErr)
	}
	if !errors.Is(err, ErrInterrupted) || res.ExitReason != ReasonInterrupted {
		t.Errorf("Run = (%+v, %v), want interrupted before launch", res, err)
	}
}

// A nil bundle is the one ending that cannot be recorded, because there is
// nowhere to record it: it stays a plain validation failure.
func TestRunRejectsANilBundleWithoutRecordingAnything(t *testing.T) {
	res, err := Run(context.Background(), Request{
		Command: helperCommand("exit", "0"),
		Parser:  jsonlParser("", 0),
	})
	if err == nil {
		t.Fatal("Run error = nil, want a validation failure")
	}
	if res != (Result{}) {
		t.Errorf("Result = %+v, want the zero Result: there was no run to describe", res)
	}
}

func TestRunReportsAProcessThatNeverStarted(t *testing.T) {
	b := newBundle(t)

	res, err := Run(context.Background(), Request{
		Command: provider.Command{Executable: filepath.Join(t.TempDir(), "not-installed")},
		Bundle:  b,
		Parser:  jsonlParser("", 0),
	})
	if err == nil {
		t.Fatal("Run error = nil, want the start failure surfaced")
	}
	requireFinalizedStartError(t, b, res)
}

func TestRunDoesNotMutateTheCallersRequest(t *testing.T) {
	b := newBundle(t)
	cmd := helperCommand("exit", "0")
	args := append([]string(nil), cmd.Args...)
	req := Request{
		Command: cmd,
		CWD:     t.TempDir(),
		Bundle:  b,
		Parser:  jsonlParser("", 0),
		Timeout: 30 * time.Second,
	}
	cwd := req.CWD

	if _, err := Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !reflect.DeepEqual(req.Command.Args, args) {
		t.Errorf("Args = %q, want %q", req.Command.Args, args)
	}
	if req.Command.Executable != cmd.Executable || req.Command.Version != cmd.Version {
		t.Errorf("Command = %+v, want %+v", req.Command, cmd)
	}
	if req.CWD != cwd || req.Timeout != 30*time.Second {
		t.Errorf("Request fields changed: %+v", req)
	}
}
