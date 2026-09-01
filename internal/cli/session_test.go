package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/action"
	"github.com/seongwoo-choi/agentrec/internal/storage"
)

// sessionSocketHome points session sockets at a directory short enough for a
// Unix socket path on every supported platform: t.TempDir is too long on
// macOS, where sun_path holds 104 bytes.
func sessionSocketHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ar")
	if err != nil {
		t.Fatalf("make socket directory: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv(sessionSocketDirEnv, dir)
	return dir
}

// sessionEvent is one hook payload as Claude Code sends it: the common fields
// every hook carries, plus the event's own.
func sessionEvent(t *testing.T, sessionID, cwd, event string, extra map[string]any) []byte {
	t.Helper()
	fields := map[string]any{
		"session_id":      sessionID,
		"transcript_path": filepath.Join(cwd, "transcript.jsonl"),
		"cwd":             cwd,
		"hook_event_name": event,
	}
	for k, v := range extra {
		fields[k] = v
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("encode %s payload: %v", event, err)
	}
	return raw
}

// serveInProcess runs the recorder for one session in this process and returns
// its socket, the channel its exit code arrives on, and its stderr, which may
// be read once the exit code has been received. A test that fails before it
// ends the session still ends it, so the recorder does not outlive the test.
func serveInProcess(t *testing.T, sessionID, cwd string, extra ...string) (string, <-chan int, *bytes.Buffer) {
	t.Helper()
	socket, err := sessionSocketPath(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	done := make(chan int, 1)
	exited := make(chan struct{})
	args := []string{"session", "serve", "--session-id", sessionID, "--cwd", cwd, "--socket", socket}
	hasIdle := false
	for _, arg := range extra {
		if arg == "--idle-timeout" {
			hasIdle = true
		}
	}
	if !hasIdle {
		extra = append(extra, "--idle-timeout", "30s")
	}
	go func() {
		done <- Run(append(args, extra...), io.Discard, &stderr)
		close(exited)
	}()
	t.Cleanup(func() {
		select {
		case <-exited:
			return
		default:
		}
		_ = deliverHook(socket, sessionEvent(t, sessionID, cwd, hookSessionEnd, nil), time.Second)
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
		}
	})
	if !waitForSocket(socket, 10*time.Second) {
		t.Fatalf("recorder never listened on %s", socket)
	}
	return socket, done, &stderr
}

func waitForSocket(path string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("unix", path, 100*time.Millisecond); err == nil {
			conn.Close()
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func deliver(t *testing.T, socket string, payload []byte) {
	t.Helper()
	if err := deliverHook(socket, payload, 10*time.Second); err != nil {
		t.Fatalf("deliver %.80s: %v", payload, err)
	}
}

// sendRaw writes bytes that are not a hook payload and reports whether the
// recorder acknowledged them.
func sendRaw(t *testing.T, socket string, raw []byte) bool {
	t.Helper()
	// Short, because a delivery the recorder declines to acknowledge is only
	// known by the wait running out.
	return deliverHook(socket, raw, 2*time.Second) == nil
}

func waitExit(t *testing.T, done <-chan int) int {
	t.Helper()
	select {
	case code := <-done:
		return code
	case <-time.After(60 * time.Second):
		t.Fatal("recorder did not exit")
		return -1
	}
}

func runDirs(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read runs root: %v", err)
	}
	var dirs []string
	for _, e := range entries {
		dirs = append(dirs, filepath.Join(root, e.Name()))
	}
	return dirs
}

// onlyRunDir is the one run recorded under root.
func onlyRunDir(t *testing.T, root string) string {
	t.Helper()
	dirs := runDirs(t, root)
	if len(dirs) != 1 {
		t.Fatalf("runs under %s = %d, want 1", root, len(dirs))
	}
	return dirs[0]
}

func readManifestFile(t *testing.T, dir string) storage.Manifest {
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

func readActionsFile(t *testing.T, dir string) []action.Action {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "actions.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("open actions: %v", err)
	}
	defer f.Close()
	var actions []action.Action
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), storage.MaxStreamLineBytes)
	for sc.Scan() {
		var a action.Action
		if err := json.Unmarshal(sc.Bytes(), &a); err != nil {
			t.Fatalf("decode action %q: %v", sc.Bytes(), err)
		}
		actions = append(actions, a)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan actions: %v", err)
	}
	return actions
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return bytes.Count(raw, []byte("\n"))
}

// A session recorded from its hooks leaves the same bundle a traced run leaves,
// minus the process result nobody observed, and the report says which mode
// recorded it, over what window, and on whose word it ended.
func TestSessionServeRecordsAnInteractiveSessionFromHooks(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	sessionSocketHome(t)
	const sessionID = "session-test-0001"

	socket, done, stderr := serveInProcess(t, sessionID, repo)

	deliver(t, socket, sessionEvent(t, sessionID, repo, hookSessionStart, map[string]any{"source": "startup"}))
	deliver(t, socket, sessionEvent(t, sessionID, repo, hookUserPromptSubmit, map[string]any{"prompt": "add a note"}))
	deliver(t, socket, sessionEvent(t, sessionID, repo, hookPostToolUse, map[string]any{
		"tool_name":     "Bash",
		"tool_input":    map[string]any{"command": "echo hi"},
		"tool_use_id":   "toolu_01",
		"tool_response": "hi\n",
		"duration_ms":   12,
	}))
	// The session writes a file, then its hook reports the write.
	writeFile(t, filepath.Join(repo, "notes.txt"), "a note\n")
	deliver(t, socket, sessionEvent(t, sessionID, repo, hookPostToolUse, map[string]any{
		"tool_name":     "Write",
		"tool_input":    map[string]any{"file_path": filepath.Join(repo, "notes.txt"), "content": "a note\n"},
		"tool_use_id":   "toolu_02",
		"tool_response": map[string]any{"type": "create", "filePath": filepath.Join(repo, "notes.txt")},
	}))
	// A subagent's tool call arrives under the same session with agent fields.
	deliver(t, socket, sessionEvent(t, sessionID, repo, hookPostToolUseFailure, map[string]any{
		"tool_name":   "Bash",
		"tool_input":  map[string]any{"command": "false"},
		"tool_use_id": "toolu_03",
		"error":       "Exit code 1",
		"duration_ms": 3,
		"agent_id":    "agent-7",
		"agent_type":  "Explore",
	}))
	deliver(t, socket, sessionEvent(t, sessionID, repo, hookSessionEnd, map[string]any{"reason": "other"}))

	if code := waitExit(t, done); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr.String())
	}

	dir := onlyRunDir(t, root)
	m := readManifestFile(t, dir)
	if m.Mode != storage.ModeSession || m.SessionID != sessionID {
		t.Errorf("manifest mode/session = %q/%q, want %q/%q", m.Mode, m.SessionID, storage.ModeSession, sessionID)
	}
	if m.ExitReason != reasonSessionEnded || m.EndedAt == nil {
		t.Errorf("manifest exitReason/endedAt = %q/%v, want %q and an end", m.ExitReason, m.EndedAt, reasonSessionEnded)
	}
	if m.WarningCount != 0 {
		t.Errorf("manifest warningCount = %d, want 0 (stderr %q)", m.WarningCount, stderr.String())
	}
	if got, err := os.ReadFile(filepath.Join(dir, "prompt.txt")); err != nil || string(got) != "add a note\n" {
		t.Errorf("prompt.txt = %q, %v; want the first prompt", got, err)
	}

	actions := readActionsFile(t, dir)
	if len(actions) != 3 {
		t.Fatalf("actions = %d, want 3", len(actions))
	}
	want := []struct{ id, typ, status string }{
		{"toolu_01", action.TypeShellExec, hookStatusCompleted},
		{"toolu_02", action.TypeFileWrite, hookStatusCompleted},
		{"toolu_03", action.TypeShellExec, hookStatusFailed},
	}
	for i, w := range want {
		a := actions[i]
		if a.ID != w.id || a.Type != w.typ || a.Status != w.status || a.Provider != "claude" || a.Assurance != action.AssuranceProviderReported {
			t.Errorf("action %d = %s %s %s %s %s, want %s %s %s claude provider_reported", i, a.ID, a.Type, a.Status, a.Provider, a.Assurance, w.id, w.typ, w.status)
		}
	}
	if got := actions[0].FinishedAt.Sub(actions[0].StartedAt); got != 12*time.Millisecond {
		t.Errorf("action 0 duration = %s, want the provider's 12ms", got)
	}
	if !strings.Contains(string(actions[0].Result), `"source":"hook.PostToolUse"`) || !strings.Contains(string(actions[0].Result), `"toolResponse":"hi\n"`) {
		t.Errorf("action 0 result = %s, want the hook's response under its source", actions[0].Result)
	}
	if got := actions[1].RepositoryPaths; len(got) != 1 || got[0] != "notes.txt" || !actions[1].RepositoryPathsRecorded {
		t.Errorf("action 1 repository paths = %v (recorded %v), want [notes.txt]", got, actions[1].RepositoryPathsRecorded)
	}
	if r := string(actions[2].Result); !strings.Contains(r, `"error":"Exit code 1"`) || !strings.Contains(r, `"agentId":"agent-7"`) || !strings.Contains(r, `"agentType":"Explore"`) {
		t.Errorf("action 2 result = %s, want the hook's error and the subagent named", r)
	}

	if got := countLines(t, filepath.Join(dir, "provider-events.sanitized.jsonl")); got != 6 {
		t.Errorf("provider events = %d, want every delivery (6)", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "process", "result.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("process/result.json: %v, want it absent — no process was supervised", err)
	}
	for _, name := range []string{filepath.Join("git", "result.json"), "report.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: %v, want it filed", name, err)
		}
	}

	code, stdout, stderrText := run(t, "show", "latest")
	if code != 0 {
		t.Fatalf("show exit code = %d, want 0 (stderr %q)", code, stderrText)
	}
	for _, want := range []string{
		"Status       " + sessionSupervisorStatus,
		"Session      " + sessionID,
		"Exit Reason  " + reasonSessionEnded,
		"Ended By     " + sessionEndedBy(reasonSessionEnded),
		"1 untracked",
		"Window       " + sessionRepositoryWindow,
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("show output lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "Exit Code") {
		t.Errorf("show output claims an exit code for a session nobody supervised:\n%s", stdout)
	}
	if code, stdout, _ := run(t, "list"); code != 0 || !strings.Contains(stdout, reasonSessionEnded) {
		t.Errorf("list exit %d, output %q; want the session run listed as %s", code, stdout, reasonSessionEnded)
	}
	// Hook payloads name themselves under hook_event_name, and the event
	// summary reads that rather than calling every one untyped.
	if code, stdout, _ := run(t, "events", "latest"); code != 0 || !strings.Contains(stdout, hookPostToolUse) || strings.Contains(stdout, "(untyped)") {
		t.Errorf("events exit %d, output:\n%s\nwant hook event names, not (untyped)", code, stdout)
	}
}

// Checks are a command the repository chose, and the hook fragment applies to
// every repository the operator opens, so a session runs them only when the
// operator opted in with --verify — and then only the committed configuration,
// identical to HEAD, because the checkout is one the agent can edit.
func TestSessionServeVerifiesOnlyWhenAskedAndOnlyCommittedChecks(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	sessionSocketHome(t)
	stubProviders(t, verifyHelperName)
	commitVerifyConfig(t, repo, verifyHelperName, "pass")

	endSession := func(id string, extra ...string) *bytes.Buffer {
		t.Helper()
		socket, done, stderr := serveInProcess(t, id, repo, extra...)
		deliver(t, socket, sessionEvent(t, id, repo, hookSessionStart, nil))
		deliver(t, socket, sessionEvent(t, id, repo, hookSessionEnd, nil))
		if code := waitExit(t, done); code != 0 {
			t.Fatalf("%s: exit code = %d, want 0 (stderr %q)", id, code, stderr.String())
		}
		return stderr
	}

	// Committed and identical to HEAD, but nobody asked: not run.
	endSession("session-unasked")
	if _, stdout, _ := run(t, "show", "latest"); !strings.Contains(stdout, "VERIFICATION-OBSERVED RESULT\n  (none)") {
		t.Errorf("checks ran without --verify:\n%s", stdout)
	}

	// Asked for, committed and identical to HEAD: pinned and run.
	endSession("session-verified", verifyFlag)
	if _, stdout, _ := run(t, "show", "latest"); !strings.Contains(stdout, "VERIFICATION-OBSERVED RESULT\n  Status       PASS") || !strings.Contains(stdout, "Pinned       "+sessionVerificationPin) {
		t.Errorf("committed checks were not run and reported:\n%s", stdout)
	}

	// Asked for, but edited and not committed: the file on disk is not the
	// repository's, so nothing is run.
	writeVerifyConfig(t, repo, "version: 1\nverify:\n  - name: \"check\"\n    timeout: \"30s\"\n    command:\n      - \""+verifyHelperName+"\"\n      - \"fail\"\n      - \"1\"\n")
	stderr := endSession("session-unverified", verifyFlag)
	if _, stdout, _ := run(t, "show", "latest"); !strings.Contains(stdout, "VERIFICATION-OBSERVED RESULT\n  (none)") {
		t.Errorf("an uncommitted configuration was used for verification:\n%s", stdout)
	}
	if !strings.Contains(stderr.String(), "differs from HEAD") {
		t.Errorf("stderr = %q, want it to say why the session is not verified", stderr.String())
	}
	if got := len(runDirs(t, root)); got != 3 {
		t.Errorf("runs = %d, want 3", got)
	}
}

// One delivery must never cost the rest of the session: whatever its size or
// shape, it is filed as far as the bundle allows, and recording goes on.
func TestSessionServeKeepsRecordingAfterHostileDeliveries(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	sessionSocketHome(t)
	const sessionID = "session-hostile"

	socket, done, stderr := serveInProcess(t, sessionID, repo)
	deliver(t, socket, sessionEvent(t, sessionID, repo, hookSessionStart, nil))

	deep := strings.Repeat("[", 70) + strings.Repeat("]", 70)
	for _, tc := range []struct {
		name    string
		payload []byte
		acked   bool
	}{
		{"oversized tool response", sessionEvent(t, sessionID, repo, hookPostToolUse, map[string]any{
			"tool_name": "Read", "tool_input": map[string]any{"file_path": "big.txt"}, "tool_use_id": "toolu_big",
			"tool_response": strings.Repeat("x", sessionPayloadLimit+1024),
		}), true},
		{"oversized non-JSON", bytes.Repeat([]byte("y"), sessionPayloadLimit+1024), true},
		{"small non-JSON", []byte("hello, not json"), true},
		{"another session", sessionEvent(t, "someone-else", repo, hookPostToolUse, map[string]any{"tool_name": "Bash", "tool_use_id": "toolu_other"}), true},
		{"nested past the event depth", []byte(`{"session_id":"` + sessionID + `","hook_event_name":"PostToolUse","tool_name":"Bash","tool_use_id":"toolu_deep","tool_input":` + deep + `}`), true},
		{"fractional duration", sessionEvent(t, sessionID, repo, hookPostToolUse, map[string]any{
			"tool_name": "Bash", "tool_input": map[string]any{"command": "true"}, "tool_use_id": "toolu_frac", "duration_ms": 12.5,
		}), true},
		{"empty", nil, false},
	} {
		if got := sendRaw(t, socket, tc.payload); got != tc.acked {
			t.Errorf("%s: acknowledged = %v, want %v", tc.name, got, tc.acked)
		}
	}
	deliver(t, socket, sessionEvent(t, sessionID, repo, hookPostToolUse, map[string]any{
		"tool_name": "Bash", "tool_input": map[string]any{"command": "true"}, "tool_use_id": "toolu_after", "tool_response": "",
	}))
	deliver(t, socket, sessionEvent(t, sessionID, repo, hookSessionEnd, nil))
	if code := waitExit(t, done); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr.String())
	}

	dir := onlyRunDir(t, root)
	m := readManifestFile(t, dir)
	if m.ExitReason != reasonSessionEnded {
		t.Errorf("manifest exitReason = %q, want %s — one hostile delivery must not poison the bundle (stderr %q)", m.ExitReason, reasonSessionEnded, stderr.String())
	}
	// Every hostile delivery is a warning: oversized ×2, non-JSON, another
	// session, too deep, fractional duration.
	if m.WarningCount != 6 {
		t.Errorf("manifest warningCount = %d, want 6 (stderr %q)", m.WarningCount, stderr.String())
	}
	actions := readActionsFile(t, dir)
	ids := make([]string, 0, len(actions))
	for _, a := range actions {
		ids = append(ids, a.ID)
	}
	if strings.Join(ids, ",") != "toolu_big,toolu_deep,toolu_frac,toolu_after" {
		t.Fatalf("actions = %v, want the dropped calls, the fractional one and the one after them", ids)
	}
	if r := string(actions[0].Result); !strings.Contains(r, `"dropped":"payload of`) || strings.Contains(r, "xxxx") {
		t.Errorf("oversized action result = %s, want the drop noted and the bulk gone", r)
	}
	if r := string(actions[1].Result); !strings.Contains(r, `"dropped":"payload is not one the event stream can hold`) {
		t.Errorf("deep action result = %s, want the drop noted", r)
	}
	if !actions[2].StartedAt.IsZero() {
		t.Errorf("fractional duration action StartedAt = %v, want unset — the field did not decode", actions[2].StartedAt)
	}
	events, err := os.ReadFile(filepath.Join(dir, "provider-events.sanitized.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(events, []byte("xxxx")) || bytes.Contains(events, []byte("yyyy")) {
		t.Errorf("event stream holds bulk that should have been dropped")
	}
	unparsed, err := os.ReadFile(filepath.Join(dir, "provider-stdout.unparsed.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(unparsed, []byte("hello, not json")) || !bytes.Contains(unparsed, []byte("unparsable delivery of")) || bytes.Contains(unparsed, []byte("yyyy")) {
		t.Errorf("unparsed log = %q, want the small line kept and the large one noted", unparsed)
	}
	for _, line := range bytes.Split(unparsed, []byte("\n")) {
		if len(line) >= storage.MaxStreamLineBytes {
			t.Errorf("unparsed log holds a line of %d bytes", len(line))
		}
	}
}

// A session whose end never arrives is closed as lost once nothing has been
// heard for the idle timeout, so the evidence is measured and the bundle does
// not read as a session still going.
func TestSessionServeClosesALostSessionAfterTheIdleTimeout(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	sessionSocketHome(t)

	socket, done, stderr := serveInProcess(t, "session-idle", repo, "--idle-timeout", "300ms")
	deliver(t, socket, sessionEvent(t, "session-idle", repo, hookSessionStart, nil))
	if code := waitExit(t, done); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	m := readManifestFile(t, onlyRunDir(t, root))
	if m.ExitReason != reasonSessionLost {
		t.Errorf("manifest exitReason = %q, want %s", m.ExitReason, reasonSessionLost)
	}
	if !strings.Contains(stderr.String(), "closing the run as lost") {
		t.Errorf("stderr = %q, want it to say the session was given up on", stderr.String())
	}
	if _, stdout, _ := run(t, "show", "latest"); !strings.Contains(stdout, "Ended By     "+sessionEndedBy(reasonSessionLost)) {
		t.Errorf("show output does not say the recorder ended the run:\n%s", stdout)
	}
}

// A signal ends the session as lost and the run is still measured and filed:
// the signal is held over the close-out the way a traced run holds it.
func TestSessionServeFilesTheRunOnASignal(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	sessionSocketHome(t)

	socket, done, stderr := serveInProcess(t, "session-signal", repo)
	deliver(t, socket, sessionEvent(t, "session-signal", repo, hookSessionStart, nil))
	deliver(t, socket, sessionEvent(t, "session-signal", repo, hookPostToolUse, map[string]any{"tool_name": "Bash", "tool_use_id": "toolu_sig"}))
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signal self: %v", err)
	}
	if code := waitExit(t, done); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	dir := onlyRunDir(t, root)
	if m := readManifestFile(t, dir); m.ExitReason != reasonSessionLost {
		t.Errorf("manifest exitReason = %q, want %s", m.ExitReason, reasonSessionLost)
	}
	if actions := readActionsFile(t, dir); len(actions) != 1 {
		t.Errorf("actions = %d, want the one delivered before the signal", len(actions))
	}
	for _, name := range []string{filepath.Join("git", "result.json"), "report.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: %v, want it filed despite the signal", name, err)
		}
	}
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket after exit: %v, want it gone", err)
	}
}

// Two hooks racing to start a recorder for the same session must leave one
// recorder and one bundle, and the loser must leave quietly.
func TestSessionServeLeavesASessionAlreadyServed(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	sessionSocketHome(t)

	socket, done, stderr := serveInProcess(t, "session-dup", repo)
	var second bytes.Buffer
	if code := Run([]string{"session", "serve", "--session-id", "session-dup", "--cwd", repo, "--socket", socket}, io.Discard, &second); code != 0 || second.Len() != 0 {
		t.Errorf("second recorder exit code = %d, stderr %q; want 0 and silence", code, second.String())
	}
	// A socket file removed under a live recorder does not let a second one in
	// either: the lock, not the file, says who holds the session.
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	if code := Run([]string{"session", "serve", "--session-id", "session-dup", "--cwd", repo, "--socket", socket}, io.Discard, &second); code != 0 || second.Len() != 0 {
		t.Errorf("recorder after socket removal exit code = %d, stderr %q; want 0 and silence", code, second.String())
	}
	// The first recorder can no longer be reached, so it is ended by its own
	// listener closing rather than by a delivery.
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket was recreated by a losing recorder: %v", err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := waitExit(t, done); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	onlyRunDir(t, root)
}

// Once the session has ended, the recorder gives the session up before it
// measures and files the run, so a session resumed under the same ID a moment
// later gets a recorder of its own instead of a socket nobody answers.
func TestSessionServeReleasesTheSessionBeforeClosingOut(t *testing.T) {
	if _, err := os.Stat("/bin/sleep"); err != nil {
		t.Skip("needs /bin/sleep")
	}
	root := home(t)
	repo := cleanRepo(t)
	sessionSocketHome(t)
	commitVerifyConfig(t, repo, "/bin/sleep", "2")
	const sessionID = "session-resumed"

	socket, done, stderr := serveInProcess(t, sessionID, repo, verifyFlag)
	deliver(t, socket, sessionEvent(t, sessionID, repo, hookSessionStart, map[string]any{"source": "startup"}))
	deliver(t, socket, sessionEvent(t, sessionID, repo, hookSessionEnd, map[string]any{"reason": "resume"}))

	// The first recorder is now running a 2 s check. The session is free.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); errors.Is(err, os.ErrNotExist) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket still present after SessionEnd: %v", err)
	}
	socket2, done2, stderr2 := serveInProcess(t, sessionID, repo)
	if socket2 != socket {
		t.Fatalf("resumed session socket = %s, want %s", socket2, socket)
	}
	deliver(t, socket2, sessionEvent(t, sessionID, repo, hookSessionStart, map[string]any{"source": "resume"}))
	deliver(t, socket2, sessionEvent(t, sessionID, repo, hookPostToolUse, map[string]any{"tool_name": "Bash", "tool_use_id": "toolu_resumed"}))
	deliver(t, socket2, sessionEvent(t, sessionID, repo, hookSessionEnd, nil))
	if code := waitExit(t, done2); code != 0 {
		t.Fatalf("resumed recorder exit code = %d, want 0 (stderr %q)", code, stderr2.String())
	}
	if code := waitExit(t, done); code != 0 {
		t.Fatalf("first recorder exit code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	dirs := runDirs(t, root)
	if len(dirs) != 2 {
		t.Fatalf("runs = %d, want one per session window", len(dirs))
	}
	for _, dir := range dirs {
		if m := readManifestFile(t, dir); m.ExitReason != reasonSessionEnded || m.SessionID != sessionID {
			t.Errorf("%s: manifest = %+v, want %s for %s", dir, m, reasonSessionEnded, sessionID)
		}
	}
}

func TestParseSessionOptions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		ok     bool
		verify bool
		idle   time.Duration
	}{
		{"required flags", []string{"--session-id", "s", "--cwd", "/tmp"}, true, false, defaultSessionIdleTimeout},
		{"every flag", []string{"--session-id", "s", "--cwd", "/tmp", "--socket", "/s.sock", "--idle-timeout", "2h", verifyFlag}, true, true, 2 * time.Hour},
		{"verify first", []string{verifyFlag, "--session-id", "s", "--cwd", "/tmp"}, true, true, defaultSessionIdleTimeout},
		{"missing cwd", []string{"--session-id", "s"}, false, false, 0},
		{"missing value", []string{"--session-id", "s", "--cwd"}, false, false, 0},
		{"unknown flag", []string{"--session-id", "s", "--cwd", "/tmp", "--transcript", "/t"}, false, false, 0},
		{"bad idle", []string{"--session-id", "s", "--cwd", "/tmp", "--idle-timeout", "0"}, false, false, 0},
	} {
		opts, ok := parseSessionOptions(tc.args)
		if ok != tc.ok || (ok && (opts.idle != tc.idle || opts.verify != tc.verify)) {
			t.Errorf("%s: parseSessionOptions(%q) = %+v, %v; want ok=%v verify=%v idle=%s", tc.name, tc.args, opts, ok, tc.ok, tc.verify, tc.idle)
		}
	}
}

// The settings fragment names this binary, once per event the recorder acts
// on, quotes the path only when the shell would otherwise split it, and asks
// for verification only when the operator did.
func TestHooksPrintEmitsTheClaudeSettingsFragment(t *testing.T) {
	home(t)
	restore := sessionExecutable
	t.Cleanup(func() { sessionExecutable = restore })

	sessionExecutable = func() (string, error) { return "/opt/agent rec/agentrec", nil }
	code, stdout, stderr := run(t, "hooks", "print", "--claude")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	var settings hookSettings
	if err := json.Unmarshal([]byte(stdout), &settings); err != nil {
		t.Fatalf("stdout is not a settings fragment: %v\n%s", err, stdout)
	}
	for _, event := range []string{hookSessionStart, hookUserPromptSubmit, hookPostToolUse, hookPostToolUseFailure, hookSessionEnd} {
		groups := settings.Hooks[event]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s: groups = %+v, want one command", event, groups)
		}
		h := groups[0].Hooks[0]
		if h.Type != "command" || h.Command != "'/opt/agent rec/agentrec' hook claude" || h.Timeout != hookCommandTimeout {
			t.Errorf("%s: hook = %+v", event, h)
		}
	}
	if len(settings.Hooks) != 5 {
		t.Errorf("events = %d, want 5", len(settings.Hooks))
	}
	if !strings.Contains(stderr, "next session") {
		t.Errorf("stderr = %q, want the note that open sessions are not recorded", stderr)
	}

	sessionExecutable = func() (string, error) { return "/usr/local/bin/agentrec", nil }
	_, stdout, _ = run(t, "hooks", "print", "--claude")
	if !strings.Contains(stdout, `"command": "/usr/local/bin/agentrec hook claude"`) {
		t.Errorf("a plain path was quoted:\n%s", stdout)
	}
	_, stdout, _ = run(t, "hooks", "print", "--claude", verifyFlag)
	if !strings.Contains(stdout, `"command": "/usr/local/bin/agentrec hook claude --verify"`) {
		t.Errorf("--verify was not passed on to the hook:\n%s", stdout)
	}

	if code, _, _ := run(t, "hooks", "print"); code != exitUsage {
		t.Errorf("hooks print without a provider exit code = %d, want %d", code, exitUsage)
	}
}

// The hook hands the payload over byte for byte, waits for the recorder to
// take it, and keeps quiet: stdout because Claude Code injects it into the
// model for some events, stderr because a delivered event has nothing to say.
func TestHookDeliversThePayloadToTheSessionRecorder(t *testing.T) {
	sessionSocketHome(t)
	socket, err := sessionSocketPath("session-hook")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	const ackDelay = 200 * time.Millisecond
	received := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			received <- nil
			return
		}
		defer conn.Close()
		raw, _ := io.ReadAll(conn)
		time.Sleep(ackDelay)
		conn.Write([]byte(hookAck))
		received <- raw
	}()

	payload := sessionEvent(t, "session-hook", t.TempDir(), hookPostToolUse, map[string]any{"tool_name": "Bash"})
	restore := hookStdin
	t.Cleanup(func() { hookStdin = restore })
	hookStdin = bytes.NewReader(payload)

	started := time.Now()
	code, stdout, stderr := run(t, "hook", "claude")
	if code != 0 || stdout != "" || stderr != "" {
		t.Errorf("hook exit %d, stdout %q, stderr %q; want 0 and silence", code, stdout, stderr)
	}
	if elapsed := time.Since(started); elapsed < ackDelay {
		t.Errorf("hook returned after %s, before the recorder took the delivery", elapsed)
	}
	if got := <-received; !bytes.Equal(got, payload) {
		t.Errorf("recorder received %s, want %s", got, payload)
	}
}

// Without a recorder the hook still exits 0 — a hook that fails can block the
// session — and says what was not recorded. Misuse is reported as 1, never 2,
// because 2 is the exit code that blocks an event.
func TestHookExitsZeroWhenNothingCanBeRecorded(t *testing.T) {
	sessionSocketHome(t)
	restore := hookStdin
	t.Cleanup(func() { hookStdin = restore })

	hookStdin = bytes.NewReader(sessionEvent(t, "session-none", t.TempDir(), hookPostToolUse, nil))
	code, stdout, stderr := run(t, "hook", "claude")
	if code != 0 || stdout != "" || !strings.Contains(stderr, "no recorder took PostToolUse for session session-none") {
		t.Errorf("hook exit %d, stdout %q, stderr %q; want 0, nothing, and the lost delivery named", code, stdout, stderr)
	}

	hookStdin = strings.NewReader("not a hook payload")
	code, stdout, stderr = run(t, "hook", "claude")
	if code != 0 || stdout != "" || !strings.Contains(stderr, "nothing recorded") {
		t.Errorf("hook exit %d, stdout %q, stderr %q for a non-payload; want 0, nothing, and a note", code, stdout, stderr)
	}

	for _, args := range [][]string{{"hook", "gemini"}, {"hook", "claude", "--transcript"}} {
		if code, _, _ := run(t, args...); code != exitFailure {
			t.Errorf("%v exit code = %d, want %d", args, code, exitFailure)
		}
	}
}

// The first hook of a session starts the recorder in a session group of its
// own, and the recorder outlives the hook to take the rest of the session.
func TestHookStartsARecorderOnSessionStart(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	sessionSocketHome(t)
	bin := stubProviders(t, agentrecName)
	restoreExe := sessionExecutable
	restoreStdin := hookStdin
	t.Cleanup(func() {
		sessionExecutable = restoreExe
		hookStdin = restoreStdin
	})
	sessionExecutable = func() (string, error) { return filepath.Join(bin, agentrecName), nil }
	const sessionID = "session-spawn"
	socket, err := sessionSocketPath(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	// Whatever the test finds, the recorder it started must not outlive it.
	t.Cleanup(func() { _ = deliverHook(socket, sessionEvent(t, sessionID, repo, hookSessionEnd, nil), time.Second) })

	for _, payload := range [][]byte{
		sessionEvent(t, sessionID, repo, hookSessionStart, map[string]any{"source": "startup"}),
		sessionEvent(t, sessionID, repo, hookPostToolUse, map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "true"}, "tool_use_id": "toolu_spawn", "tool_response": ""}),
		sessionEvent(t, sessionID, repo, hookSessionEnd, map[string]any{"reason": "other"}),
	} {
		hookStdin = bytes.NewReader(payload)
		if code, stdout, stderr := run(t, "hook", "claude"); code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("hook exit %d, stdout %q, stderr %q; want 0 and silence", code, stdout, stderr)
		}
	}

	var dir string
	deadline := time.Now().Add(30 * time.Second)
	for dir == "" && time.Now().Before(deadline) {
		if dirs := runDirs(t, root); len(dirs) == 1 {
			dir = dirs[0]
		}
		time.Sleep(50 * time.Millisecond)
	}
	if dir == "" || !waitForFile(filepath.Join(dir, "report.md"), 30*time.Second) {
		t.Fatalf("the recorder started by the hook never filed a report under %s", root)
	}
	m := readManifestFile(t, dir)
	if m.Mode != storage.ModeSession || m.SessionID != sessionID || m.ExitReason != reasonSessionEnded {
		t.Errorf("manifest = %+v, want a session run that ended", m)
	}
	if actions := readActionsFile(t, dir); len(actions) != 1 || actions[0].ID != "toolu_spawn" {
		t.Errorf("actions = %+v, want the one tool call", actions)
	}
	if _, err := os.Stat(sessionLogPath(socket)); err != nil {
		t.Errorf("recorder log beside the socket: %v", err)
	}
}

// Codex delivers the same hooks under the same fields, minus
// PostToolUseFailure, names its tools differently, and allows a SessionEnd hook
// three seconds at most: the fragment and the recorder follow it.
func TestHooksPrintEmitsTheCodexHooksFragment(t *testing.T) {
	home(t)
	restore := sessionExecutable
	t.Cleanup(func() { sessionExecutable = restore })
	sessionExecutable = func() (string, error) { return "/usr/local/bin/agentrec", nil }

	code, stdout, stderr := run(t, "hooks", "print", "--codex", verifyFlag)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	var settings hookSettings
	if err := json.Unmarshal([]byte(stdout), &settings); err != nil {
		t.Fatalf("stdout is not a hooks fragment: %v\n%s", err, stdout)
	}
	want := map[string]int{hookSessionStart: 5, hookUserPromptSubmit: 5, hookPostToolUse: 5, hookSessionEnd: 3}
	if len(settings.Hooks) != len(want) {
		t.Errorf("events = %v, want %v", settings.Hooks, want)
	}
	for event, timeout := range want {
		groups := settings.Hooks[event]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s: groups = %+v, want one command", event, groups)
		}
		if h := groups[0].Hooks[0]; h.Command != "/usr/local/bin/agentrec hook codex --verify" || h.Timeout != timeout {
			t.Errorf("%s: hook = %+v, want hook codex --verify with timeout %d", event, h, timeout)
		}
	}
	if _, registered := settings.Hooks[hookPostToolUseFailure]; registered {
		t.Errorf("the Codex fragment registers PostToolUseFailure, which Codex never sends")
	}
	if !strings.Contains(stderr, "/hooks") || !strings.Contains(stderr, ".codex/hooks.json") {
		t.Errorf("stderr = %q, want the hooks file named and the trust step", stderr)
	}
}

// A Codex session files under its own provider and tool names: Bash is a shell
// command, apply_patch a file edit whose paths come from the patch headers,
// mcp__ prefixes an MCP call.
func TestSessionServeRecordsACodexSession(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	sessionSocketHome(t)
	const sessionID = "codex-thread-0001"

	socket, done, stderr := serveInProcess(t, sessionID, repo, "--provider", "codex")
	deliver(t, socket, sessionEvent(t, sessionID, repo, hookSessionStart, map[string]any{"source": "startup", "model": "gpt-test", "permission_mode": "default"}))
	deliver(t, socket, sessionEvent(t, sessionID, repo, hookUserPromptSubmit, map[string]any{"prompt": "write a note", "turn_id": "turn-1"}))
	deliver(t, socket, sessionEvent(t, sessionID, repo, hookPostToolUse, map[string]any{
		"tool_name": "Bash", "tool_input": map[string]any{"command": "echo hi"}, "tool_use_id": "exec-1", "tool_response": "hi\n", "turn_id": "turn-1",
	}))
	writeFile(t, filepath.Join(repo, "notes.txt"), "probe\n")
	deliver(t, socket, sessionEvent(t, sessionID, repo, hookPostToolUse, map[string]any{
		"tool_name":     "apply_patch",
		"tool_input":    map[string]any{"command": "*** Begin Patch\n*** Add File: notes.txt\n+probe\n*** End Patch\n"},
		"tool_use_id":   "exec-2",
		"tool_response": "Exit code: 0\nOutput:\nSuccess. Updated the following files:\nA notes.txt\n",
		"turn_id":       "turn-1",
	}))
	deliver(t, socket, sessionEvent(t, sessionID, repo, hookPostToolUse, map[string]any{
		"tool_name": "mcp__fs__read", "tool_input": map[string]any{"path": "x"}, "tool_use_id": "exec-3", "tool_response": map[string]any{"ok": true},
	}))
	deliver(t, socket, sessionEvent(t, sessionID, repo, hookSessionEnd, map[string]any{"reason": "other"}))
	if code := waitExit(t, done); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr.String())
	}

	dir := onlyRunDir(t, root)
	if m := readManifestFile(t, dir); m.Provider != "codex" || m.Mode != storage.ModeSession || m.ExitReason != reasonSessionEnded {
		t.Errorf("manifest = %+v, want a codex session that ended", m)
	}
	actions := readActionsFile(t, dir)
	if len(actions) != 3 {
		t.Fatalf("actions = %d, want 3", len(actions))
	}
	want := []struct{ id, typ string }{{"exec-1", action.TypeShellExec}, {"exec-2", action.TypeFileEdit}, {"exec-3", action.TypeMCPCall}}
	for i, w := range want {
		if a := actions[i]; a.ID != w.id || a.Type != w.typ || a.Provider != "codex" {
			t.Errorf("action %d = %s %s %s, want %s %s codex", i, a.ID, a.Type, a.Provider, w.id, w.typ)
		}
	}
	if got := actions[1].RepositoryPaths; len(got) != 1 || got[0] != "notes.txt" {
		t.Errorf("apply_patch repository paths = %v, want [notes.txt] from the patch header", got)
	}
	if _, stdout, _ := run(t, "show", "latest"); !strings.Contains(stdout, "Provider     codex") {
		t.Errorf("show output does not name the provider:\n%s", stdout)
	}
}

func TestParseSessionOptionsProvider(t *testing.T) {
	if opts, ok := parseSessionOptions([]string{"--session-id", "s", "--cwd", "/tmp"}); !ok || opts.provider != defaultSessionProvider {
		t.Errorf("default provider = %q, %v; want %s", opts.provider, ok, defaultSessionProvider)
	}
	if opts, ok := parseSessionOptions([]string{"--session-id", "s", "--cwd", "/tmp", "--provider", "codex"}); !ok || opts.provider != "codex" {
		t.Errorf("provider codex = %q, %v; want codex", opts.provider, ok)
	}
	if _, ok := parseSessionOptions([]string{"--session-id", "s", "--cwd", "/tmp", "--provider", "gemini"}); ok {
		t.Errorf("an unknown provider was accepted")
	}
}

func TestPatchFilePaths(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: a/new.go\n+package a\n*** Update File: b/old.go\n@@\n-x\n+y\n*** Delete File: c/gone.go\n*** Update File: d/from.go\n*** Move to: d/to.go\n*** End Patch\n"
	got, ok := patchFilePaths(patch)
	if want := []string{"a/new.go", "b/old.go", "c/gone.go", "d/from.go", "d/to.go"}; !ok || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("patchFilePaths = %v, %v; want %v", got, ok, want)
	}
	if got, ok := patchFilePaths("echo hi"); got != nil || !ok {
		t.Errorf("a shell command was read as a patch: %v, %v", got, ok)
	}
}

// A session bundle with no ending reads as running while a recorder holds its
// lock and as unknown once nothing does: the same word in the table, the
// report and the viewer, and never "unknown" for a session that is simply
// still open.
func TestOpenSessionReadsAsRunningWhileItsRecorderHoldsTheLock(t *testing.T) {
	root := home(t)
	sessionSocketHome(t)
	const sessionID = "session-open"
	startedAt := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	b, err := storage.Create(root, "run-open", storage.Manifest{
		Provider: "claude", CWD: "/tmp", StartedAt: startedAt,
		Mode: storage.ModeSession, SessionID: sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteAction(readAction(startedAt)); err != nil {
		t.Fatal(err)
	}

	socket, err := sessionSocketPath(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	listener, lock, err := listenSession(socket)
	if err != nil {
		t.Fatal(err)
	}
	held := true
	release := func() {
		if held {
			listener.Close()
			lock.Close()
			held = false
		}
	}
	t.Cleanup(release)

	if _, stdout, _ := run(t, "list"); !strings.Contains(stdout, "  running  ") {
		t.Errorf("list while the recorder holds the lock:\n%s", stdout)
	}
	if _, stdout, _ := run(t, "show", "latest"); !strings.Contains(stdout, "Exit Reason  running") || !strings.Contains(stdout, "Ended By     "+sessionEndedBy(reasonRunning)) {
		t.Errorf("show while the recorder holds the lock:\n%s", stdout)
	}
	release()
	if _, stdout, _ := run(t, "list"); !strings.Contains(stdout, "  unknown  ") {
		t.Errorf("list after the recorder is gone:\n%s", stdout)
	}
	if _, stdout, _ := run(t, "show", "latest"); !strings.Contains(stdout, "Exit Reason  unknown") || !strings.Contains(stdout, "Ended By     "+sessionEndedBy("")) {
		t.Errorf("show after the recorder is gone:\n%s", stdout)
	}
}
