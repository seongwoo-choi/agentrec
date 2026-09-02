package cli

import (
	"encoding/json"
	"fmt"
	"github.com/seongwoo-choi/agentrec/internal/runner"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// `agentrec hook claude` is the command Claude Code runs on each configured
// event, with the event payload on stdin. It does one thing — hand the payload
// to the recorder serving this session — and it does it quietly: it exits 0
// whatever happens, because a hook that fails can block the operator's session,
// and it writes nothing to stdout, because for some events stdout is injected
// into the model's context.

const hookUsage = "usage: agentrec hook <claude|codex> [--verify]   (run by the provider's hooks; the event payload arrives on stdin)\n"

const (
	// hookAck is what the recorder answers once a delivery is in its hands.
	hookAck = "ok\n"

	// hookBudget is all the time one hook invocation may take, dialling,
	// delivering, starting a recorder and retrying included. It sits under the
	// timeout the settings fragment gives the hook (hookCommandTimeout), so the
	// hook always ends on its own terms and never by being killed mid-write.
	hookBudget = 4 * time.Second

	hookDialTimeout = time.Second
	// hookAckTimeout bounds waiting for the recorder to take a delivery. It is
	// not tight: the recorder answers as soon as the bytes are read, but a
	// large payload takes a moment to arrive. SessionEnd is bounded tighter,
	// because Claude Code gives those hooks less time by default.
	hookAckTimeout           = 3 * time.Second
	hookSessionEndAckTimeout = time.Second
	// hookStartPoll is how often SessionStart retries a recorder it just
	// started, until the budget runs out.
	hookStartPoll = 50 * time.Millisecond
)

// hookStdin and sessionExecutable are replaced in tests, which feed a payload
// without a terminal and point the recorder at the test binary.
var (
	hookStdin         io.Reader = os.Stdin
	sessionExecutable           = os.Executable
)

func runHook(args []string, _ io.Writer, stderr io.Writer) int {
	verify := false
	switch {
	case len(args) == 1 && sessionProviders[args[0]]:
	case len(args) == 2 && sessionProviders[args[0]] && args[1] == verifyFlag:
		verify = true
	default:
		fmt.Fprint(stderr, hookUsage)
		// Not exitUsage: a hook's exit status is read by the session that ran
		// it, and 2 blocks the event on some of them.
		return exitFailure
	}
	// A provider agentrec itself launched — a traced run, a shadow leg — is
	// already being recorded by the process that launched it. Its hooks say
	// nothing, so the run is not filed twice.
	if os.Getenv(runner.HooksOffEnv) != "" {
		return 0
	}
	provider := args[0]
	deadline := time.Now().Add(hookBudget)

	raw, err := io.ReadAll(io.LimitReader(hookStdin, sessionReadLimit+1))
	if err != nil {
		fmt.Fprintf(stderr, "agentrec hook: read payload: %v\n", err)
		return 0
	}
	var env hookEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || env.SessionID == "" {
		fmt.Fprintln(stderr, "agentrec hook: payload is not a hook event with a session_id; nothing recorded")
		return 0
	}
	socket, err := sessionSocketPath(env.SessionID)
	if err != nil {
		fmt.Fprintf(stderr, "agentrec hook: %v\n", err)
		return 0
	}
	ackTimeout := hookAckTimeout
	if env.HookEventName == hookSessionEnd {
		ackTimeout = hookSessionEndAckTimeout
	}
	attempt := func() error {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("no answer within %s", hookBudget)
		}
		return deliverHook(socket, raw, min(ackTimeout, remaining))
	}

	if err := attempt(); err == nil {
		return 0
	}
	if env.HookEventName != hookSessionStart {
		// No recorder took this: none is serving the session — it was opened
		// before the hooks were installed, or its recorder has ended — or the
		// one serving it did not answer in time. Either way the delivery is
		// lost, and said so here rather than queued for a recorder that may
		// never come.
		fmt.Fprintf(stderr, "agentrec hook: no recorder took %s for session %s; not recorded\n", env.HookEventName, env.SessionID)
		return 0
	}
	if err := startSessionRecorder(env, socket, provider, verify); err != nil {
		fmt.Fprintf(stderr, "agentrec hook: start recorder: %v\n", err)
		return 0
	}
	for {
		err := attempt()
		if err == nil {
			return 0
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(stderr, "agentrec hook: the recorder did not answer within %s: %v\n", hookBudget, err)
			return 0
		}
		time.Sleep(hookStartPoll)
	}
}

// deliverHook hands one payload to the recorder and waits until it is taken,
// all within timeout.
func deliverHook(socket string, raw []byte, timeout time.Duration) error {
	conn, err := net.DialTimeout("unix", socket, min(hookDialTimeout, timeout))
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	if _, err := conn.Write(raw); err != nil {
		return err
	}
	// The recorder reads to the end of the payload, so the writing side is
	// closed to mark it while the reading side stays open for the answer.
	if unixConn, ok := conn.(*net.UnixConn); ok {
		if err := unixConn.CloseWrite(); err != nil {
			return err
		}
	}
	ack := make([]byte, len(hookAck))
	if _, err := io.ReadFull(conn, ack); err != nil {
		return err
	}
	if string(ack) != hookAck {
		return fmt.Errorf("unexpected answer %q from the recorder", ack)
	}
	return nil
}

// startSessionRecorder launches the recorder for a session in a session group
// of its own, so it outlives the hook that started it and the terminal that
// hook ran in. Its stderr is its log, beside the socket; its stdout is nothing.
func startSessionRecorder(env hookEnvelope, socket, provider string, verify bool) error {
	exe, err := sessionExecutable()
	if err != nil {
		return err
	}
	cwd := env.CWD
	if cwd == "" {
		// Hooks run in the session's working directory, so the recorder's own
		// is the next best name for it.
		if cwd, err = os.Getwd(); err != nil {
			return err
		}
	}
	logFile, err := os.OpenFile(sessionLogPath(socket), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()

	args := []string{"session", "serve", "--session-id", env.SessionID, "--cwd", cwd, "--provider", provider, "--socket", socket}
	if verify {
		args = append(args, verifyFlag)
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = cwd
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// sessionLogPath is the recorder's log, beside its socket.
func sessionLogPath(socket string) string {
	return strings.TrimSuffix(socket, ".sock") + ".log"
}
