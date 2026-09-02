package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/seongwoo-choi/agentrec/internal/action"
	"github.com/seongwoo-choi/agentrec/internal/evidence"
	"github.com/seongwoo-choi/agentrec/internal/provider/claude"
	"github.com/seongwoo-choi/agentrec/internal/provider/codex"
	"github.com/seongwoo-choi/agentrec/internal/runner"
	"github.com/seongwoo-choi/agentrec/internal/storage"
)

// Session mode records an interactive provider session that agentrec did not
// start. The provider's own hooks deliver each event to a recorder that holds
// the bundle for the length of the session, so the evidence is filed the way a
// traced run files it — with the differences the report states: there is no
// supervised process to observe, the baseline is pinned when the SessionStart
// hook arrives rather than before the process started, the checkout was open
// to the operator throughout, and the session's end is reported by a hook
// rather than observed.

const sessionServeUsage = "usage: agentrec session serve --session-id <id> --cwd <path> [--provider <claude|codex>] [--verify] [--socket <path>] [--idle-timeout <duration>]\n"

// sessionProviders are the providers whose hooks are understood: both deliver
// the same events with the same fields, and differ in what they call their
// tools. Claude Code is the default because it came first.
var sessionProviders = map[string]bool{"claude": true, "codex": true}

const (
	defaultSessionProvider = "claude"

	// defaultSessionIdleTimeout ends a session whose SessionEnd never arrives —
	// a terminal closed, a machine asleep past the hook — so the recorder does
	// not hold the bundle open forever. The evidence is measured then, and the
	// exit reason says the session did not close the window itself.
	defaultSessionIdleTimeout = 8 * time.Hour

	// sessionReadLimit bounds one delivery as read off the socket. It is well
	// above what is recorded whole, so the envelope of an oversized payload can
	// still be read and the drop filed under the right tool_use_id.
	sessionReadLimit = 32 << 20

	// sessionPayloadLimit is the most of one delivery that is recorded whole. It
	// sits under the stream line limit with room for redaction to grow the line:
	// a payload that fit before redaction and not after would otherwise poison
	// the bundle for the rest of the session.
	sessionPayloadLimit = storage.MaxStreamLineBytes - 64<<10

	// sessionDeliveryTimeout bounds reading one delivery and acknowledging it.
	sessionDeliveryTimeout = 5 * time.Second

	// sessionAcceptBackoff is the pause after an accept failure that is not the
	// listener closing — file descriptors running out, say — before trying
	// again. One such failure must not end the recording.
	sessionAcceptBackoff = 100 * time.Millisecond

	// sessionInboxDepth is how many deliveries may wait for the recorder. The
	// provider runs its hooks in parallel, so several arrive at once; past this
	// many the senders wait, which is what they would do anyway.
	sessionInboxDepth = 64
)

// Exit reasons of a session-mode run. No process ending is observed, so what
// ends the recording is either the session saying it ended or the recorder
// giving up waiting for it to.
const (
	reasonSessionEnded = "session_ended"
	reasonSessionLost  = "session_lost"
)

// Hook events this recorder acts on. Every delivery is kept as a provider event
// whatever its name; these are the ones that also become something else.
const (
	hookSessionStart       = "SessionStart"
	hookUserPromptSubmit   = "UserPromptSubmit"
	hookPostToolUse        = "PostToolUse"
	hookPostToolUseFailure = "PostToolUseFailure"
	hookStop               = "Stop"
	hookSessionEnd         = "SessionEnd"
)

// Action lifecycle statuses, in the vocabulary the stream parsers use.
const (
	hookStatusCompleted = "completed"
	hookStatusFailed    = "failed"
)

// hookEnvelope is the part of a hook payload the recorder reads. Everything
// else is kept verbatim in the event stream; nothing here is trusted beyond
// routing and naming.
type hookEnvelope struct {
	SessionID     string `json:"session_id"`
	HookEventName string `json:"hook_event_name"`
	CWD           string `json:"cwd"`
	Prompt        string `json:"prompt"`
	// PromptID (Claude Code) and TurnID (Codex) name the turn a prompt opened,
	// and the Stop hook that closes it carries the same id beside the
	// assistant's final message, so the two can be read as one exchange.
	PromptID             string          `json:"prompt_id"`
	TurnID               string          `json:"turn_id"`
	LastAssistantMessage string          `json:"last_assistant_message"`
	StopHookActive       bool            `json:"stop_hook_active"`
	ToolName             string          `json:"tool_name"`
	ToolInput            json.RawMessage `json:"tool_input"`
	ToolResponse         json.RawMessage `json:"tool_response"`
	ToolUseID            string          `json:"tool_use_id"`
	DurationMs           *int64          `json:"duration_ms"`
	Error                string          `json:"error"`
	// AgentID and AgentType are set on hooks a subagent's tool calls fire. They
	// arrive under the parent's session_id, so without them a subagent's write
	// would read as the operator-facing agent's.
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`
}

// hookActionResult is what a hook-recorded action files as its result. Source
// names the hook it came from, so a reader can tell a result the session's
// hook reported from one a stream parser read.
type hookActionResult struct {
	Source         string          `json:"source"`
	Turn           string          `json:"turn,omitempty"`
	StopHookActive bool            `json:"stopHookActive,omitempty"`
	ToolResponse   json.RawMessage `json:"toolResponse,omitempty"`
	Error          string          `json:"error,omitempty"`
	DurationMs     *int64          `json:"durationMs,omitempty"`
	AgentID        string          `json:"agentId,omitempty"`
	AgentType      string          `json:"agentType,omitempty"`
	Dropped        string          `json:"dropped,omitempty"`
}

type sessionOptions struct {
	sessionID string
	cwd       string
	provider  string
	socket    string
	verify    bool
	idle      time.Duration
}

// parseSessionOptions reads the recorder's own flags. An unknown flag or
// provider is refused, and the session and its directory are required: a
// recorder that guessed either would file a session under the wrong name.
func parseSessionOptions(args []string) (sessionOptions, bool) {
	opts := sessionOptions{provider: defaultSessionProvider, idle: defaultSessionIdleTimeout}
	for i := 0; i < len(args); i++ {
		if args[i] == verifyFlag {
			opts.verify = true
			continue
		}
		if i+1 >= len(args) {
			return sessionOptions{}, false
		}
		value := args[i+1]
		switch args[i] {
		case "--session-id":
			opts.sessionID = value
		case "--cwd":
			opts.cwd = value
		case "--provider":
			if !sessionProviders[value] {
				return sessionOptions{}, false
			}
			opts.provider = value
		case "--socket":
			opts.socket = value
		case "--idle-timeout":
			d, err := time.ParseDuration(value)
			if err != nil || d <= 0 {
				return sessionOptions{}, false
			}
			opts.idle = d
		default:
			return sessionOptions{}, false
		}
		i++
	}
	if opts.sessionID == "" || opts.cwd == "" {
		return sessionOptions{}, false
	}
	return opts, true
}

func runSession(args []string, _ io.Writer, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "serve" {
		fmt.Fprint(stderr, sessionServeUsage)
		return exitUsage
	}
	opts, ok := parseSessionOptions(args[1:])
	if !ok {
		fmt.Fprint(stderr, sessionServeUsage)
		return exitUsage
	}
	if opts.socket == "" {
		path, err := sessionSocketPath(opts.sessionID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitFailure
		}
		opts.socket = path
	}
	return serveSession(opts, stderr)
}

// serveSession records one session from the hooks that reach its socket, then
// closes the run out the way a traced run is closed out. It returns once the
// session has ended or been given up on.
func serveSession(opts sessionOptions, stderr io.Writer) int {
	// Subscribed before the socket is bound: from the moment a hook can see
	// this recorder, a signal is held until the recorder can end the run
	// properly rather than ending the process with its default disposition
	// and leaving a bundle that never says how it ended.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, handledSignals...)
	// Stop, not Reset: a recorder that loses the socket to another one in the
	// same process — as the tests arrange — must not take that recorder's
	// subscription with it on the way out.
	defer signal.Stop(stop)

	// The socket is taken before anything is written: a second recorder for
	// the same session — two hooks racing to start one — must find the first
	// and leave, not record the session twice. Deliveries are read from the
	// moment it is bound, so a hook that fires while the bundle is still being
	// prepared is not left blocking on a socket nobody reads.
	listener, lock, err := listenSession(opts.socket)
	if errors.Is(err, errSessionServed) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	inbox := newSessionInbox(listener)
	// release ends this recorder's claim on the session: the socket is closed
	// and the lock let go, so a hook arriving afterwards finds no recorder and
	// starts a fresh one — which is what a resumed session needs while this
	// recorder is still measuring and filing the one that ended.
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		inbox.close()
		lock.Close()
	}
	defer release()

	root, err := runsRoot()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	runID, err := newRunID()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	gitCtx, cancelGit := context.WithTimeout(context.Background(), gitTimeout)
	repoRoot, err := gitToplevel(gitCtx, opts.cwd)
	cancelGit()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	manifestCWD, manifestRepoRoot, err := canonicalManifestPaths(opts.cwd, repoRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}

	// There is no invocation to record: the operator started the provider, not
	// agentrec, and what they typed is not known here.
	bundle, err := storage.Create(root, runID, storage.Manifest{
		Provider:     opts.provider,
		Mode:         storage.ModeSession,
		SessionID:    opts.sessionID,
		Argv:         []string{},
		CWD:          opts.cwd,
		CanonicalCWD: manifestCWD,
		RepoRoot:     manifestRepoRoot,
		StartedAt:    time.Now(),
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}

	// The baseline is pinned now, which is as early as a session allows: after
	// the provider started and after the operator accepted the workspace, but
	// before the session has done anything the hooks will report. The report
	// says so. A baseline that could not be pinned is a session whose changes
	// could never be attributed, so it is not recorded.
	startCtx, cancelStart := context.WithTimeout(context.Background(), evidenceStartTimeout)
	capture, err := evidence.Start(startCtx, manifestRepoRoot, runID, bundle.Dir(), evidence.Options{
		Sanitize: bundle.SanitizeText,
	})
	cancelStart()
	if err != nil {
		unrecordable(bundle, stderr, runID, err)
		return exitFailure
	}
	var verifier *evidence.PinnedVerification
	if opts.verify {
		verifier = pinCommittedVerification(manifestRepoRoot, bundle, stderr)
	}

	rec := &sessionRecorder{
		bundle:       bundle,
		runID:        runID,
		sessionID:    opts.sessionID,
		provider:     opts.provider,
		cwd:          opts.cwd,
		canonicalCWD: manifestCWD,
		repoRoot:     manifestRepoRoot,
		stderr:       stderr,
	}
	reason := rec.serve(inbox, opts.idle, stop)
	release()

	// Everything below is the recorder's own work, and a signal during it is
	// held the way a traced run holds it: the first cancels a verification
	// still running, the second ends the process.
	held := holdSignals(stop)
	if rec.storageErr != nil {
		reason = runner.ReasonStorageError
	}
	if err := bundle.Finalize(storage.Finalization{
		EndedAt:      time.Now(),
		ExitReason:   reason,
		WarningCount: rec.warnings,
	}); err != nil {
		fmt.Fprintf(stderr, "cli: run %s: %v\n", runID, err)
	}
	closed := closeOut(closeOutRequest{
		RunsRoot: root,
		RunID:    runID,
		Capture:  capture,
		Verifier: verifier,
		Cancel:   held.fired,
	}, stderr)
	held.stop()
	if closed.Incomplete || rec.storageErr != nil {
		return exitFailure
	}
	return 0
}

// pinCommittedVerification pins the repository's checks for a session, but only
// the committed ones. A traced run refuses a dirty checkout, and that is what
// makes the configuration it reads from disk the one the repository holds. A
// session runs in a checkout the operator is free to edit — and so is the agent
// — so here the file is pinned only when it is tracked and identical to HEAD.
// Anything else leaves the session unverified, which the report shows as
// (none), rather than verified against a configuration nobody committed.
func pinCommittedVerification(repoRoot string, bundle *storage.Bundle, stderr io.Writer) *evidence.PinnedVerification {
	path := filepath.Join(repoRoot, verifyConfigFile)
	if _, err := os.Lstat(path); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), verifyPinTimeout)
	defer cancel()
	if _, err := gitOutput(ctx, repoRoot, "ls-files", "--error-unmatch", "--", verifyConfigFile); err != nil {
		fmt.Fprintf(stderr, "cli: %s is not tracked; the session is not verified\n", verifyConfigFile)
		return nil
	}
	if _, err := gitOutput(ctx, repoRoot, "diff", "--quiet", "HEAD", "--", verifyConfigFile); err != nil {
		fmt.Fprintf(stderr, "cli: %s differs from HEAD; the session is not verified\n", verifyConfigFile)
		return nil
	}
	verifier, err := evidence.PinVerification(ctx, repoRoot, bundle.Dir(), path, evidence.VerificationOptions{
		Sanitize: bundle.SanitizeText,
	})
	if err != nil {
		fmt.Fprintf(stderr, "cli: %v; the session is not verified\n", err)
		return nil
	}
	return verifier
}

// delivery is one hook payload as it came off the socket. An empty raw with no
// error is a connection that said nothing; truncated marks one cut at the read
// limit, whose bytes are not a payload any more.
type delivery struct {
	raw       []byte
	truncated bool
	err       error
	// at is when the delivery was read off the socket. Doubled deliveries
	// are told apart by when they arrived, not by when the recorder — which
	// may be busy redacting a large one — gets to them.
	at time.Time
}

// sessionInbox takes deliveries off the socket as they arrive and queues them
// for the recorder. Reading is separate from recording so that a hook is never
// left blocking on a full kernel buffer while the recorder is busy: the
// provider runs its hooks in parallel, and a delivery is acknowledged as soon
// as it is in this process's memory.
type sessionInbox struct {
	listener net.Listener
	queue    chan delivery
	stopped  chan struct{}
	done     chan struct{}
}

func newSessionInbox(listener net.Listener) *sessionInbox {
	in := &sessionInbox{
		listener: listener,
		queue:    make(chan delivery, sessionInboxDepth),
		stopped:  make(chan struct{}),
		done:     make(chan struct{}),
	}
	go in.run()
	return in
}

func (in *sessionInbox) run() {
	defer close(in.done)
	for {
		conn, err := in.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// Not the listener closing: descriptors running out, most likely.
			// One failed accept is not the session ending.
			select {
			case <-in.stopped:
				return
			case <-time.After(sessionAcceptBackoff):
			}
			continue
		}
		d := readDelivery(conn)
		if len(d.raw) == 0 && d.err == nil {
			conn.Close()
			continue
		}
		// Acknowledged only once queued, so a hook that was told its delivery
		// was taken is never wrong: whatever is queued is filed before the
		// recorder stops.
		select {
		case in.queue <- d:
			conn.Write([]byte(hookAck))
		case <-in.stopped:
		}
		conn.Close()
	}
}

// close stops taking deliveries. A hook arriving afterwards finds no socket.
func (in *sessionInbox) close() {
	close(in.stopped)
	in.listener.Close()
	<-in.done
}

// readDelivery reads one payload to its end. The connection is left open for
// the acknowledgement, which the caller sends once the delivery is queued. A
// connection that says nothing — a liveness probe, a hook that died before
// writing — yields an empty delivery: there is nothing to file.
func readDelivery(conn net.Conn) delivery {
	conn.SetDeadline(time.Now().Add(sessionDeliveryTimeout))
	raw, err := io.ReadAll(io.LimitReader(conn, sessionReadLimit+1))
	if err != nil {
		return delivery{err: err}
	}
	if len(raw) == 0 {
		return delivery{}
	}
	truncated := len(raw) > sessionReadLimit
	if truncated {
		raw = raw[:sessionReadLimit]
	}
	return delivery{raw: raw, truncated: truncated, at: time.Now()}
}

// sessionRecorder turns deliveries into the bundle's streams. It is used from
// one goroutine, which is the Bundle's contract.
type sessionRecorder struct {
	bundle       *storage.Bundle
	runID        string
	sessionID    string
	provider     string
	cwd          string
	canonicalCWD string
	repoRoot     string
	stderr       io.Writer

	prompted bool
	actions  int
	// seen remembers what was already filed and when, so a hook registered
	// twice — once in the user's settings and once in the project's — files
	// each tool call, prompt and reply once: a tool call by event and
	// tool_use_id, for good; a prompt or reply by turn, text digest and
	// stop_hook_active, within duplicateDeliveryWindow; and every action id
	// handed out.
	seen       map[string]time.Time
	warnings   int
	storageErr error
}

// serve takes deliveries until the session ends, the idle timeout passes, or
// the recorder is asked to stop, and reports which.
func (s *sessionRecorder) serve(inbox *sessionInbox, idle time.Duration, stop <-chan os.Signal) string {
	timer := time.NewTimer(idle)
	defer timer.Stop()
	for {
		select {
		case d := <-inbox.queue:
			if s.take(d) {
				return reasonSessionEnded
			}
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(idle)
		case <-timer.C:
			s.warn("no hook delivery for %s; closing the run as lost", idle)
			return s.drain(inbox)
		case sig := <-stop:
			s.warn("%v; closing the run as lost", sig)
			return s.drain(inbox)
		}
	}
}

// drain files whatever was already taken off the socket when the recorder was
// told to stop: those deliveries were acknowledged, so their hooks believe them
// recorded. It reports the session ended if one of them says so, and lost
// otherwise.
func (s *sessionRecorder) drain(inbox *sessionInbox) string {
	for {
		select {
		case d := <-inbox.queue:
			if s.take(d) {
				return reasonSessionEnded
			}
		default:
			return reasonSessionLost
		}
	}
}

// take files one delivery: always as a provider event, and as the prompt or an
// action when the event is one of those. It reports whether the delivery ended
// the session.
//
// Nothing reaching the bundle from here may be larger or deeper than a stream
// line allows: the bundle refuses such a line and every line after it, and one
// delivery must never cost the rest of the session.
func (s *sessionRecorder) take(d delivery) (ended bool) {
	if d.err != nil {
		s.warnings++
		s.warn("read delivery: %v", d.err)
		return false
	}
	if d.truncated {
		// Cut at the read limit, so not a payload any more, and its envelope
		// cannot be trusted either: what is kept is that it happened.
		s.warnings++
		s.store(s.bundle.WriteUnparsedLine([]byte(fmt.Sprintf("[agentrec: a delivery of more than %d bytes was cut short and not recorded]", sessionReadLimit))))
		return false
	}

	raw := d.raw
	var env hookEnvelope
	err := json.Unmarshal(raw, &env)
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &typeErr):
		// One field had a shape this recorder did not expect; the rest decoded,
		// and an event whose duration came as a fraction is still the event.
		// The pointer field is cleared by hand: the decoder allocates it before
		// finding the value does not fit, and would otherwise leave a zero
		// duration that the provider never reported.
		s.warnings++
		s.warn("delivery field %s: %v", typeErr.Field, err)
		if typeErr.Field == "duration_ms" {
			env.DurationMs = nil
		}
	case err != nil:
		// Not a payload this recorder can read as a hook event; still something
		// the hook said, so it is kept where unparsed provider output goes.
		s.warnings++
		s.store(s.bundle.WriteUnparsedLine(boundedLine(raw, "unparsable delivery")))
		return false
	}
	if env.SessionID != s.sessionID {
		s.warnings++
		s.warn("delivery for session %s ignored", strconv.Quote(env.SessionID))
		return false
	}

	dropped := ""
	if len(raw) > sessionPayloadLimit {
		dropped = fmt.Sprintf("payload of %d bytes exceeds the %d-byte limit; tool input and response were not recorded", len(raw), sessionPayloadLimit)
	} else if _, err := storage.ValidateProviderEvent(raw, storage.MaxProviderEventTokens); err != nil {
		dropped = fmt.Sprintf("payload is not one the event stream can hold (%v); tool input and response were not recorded", err)
	}
	if dropped != "" {
		// The envelope is kept and the bulk is not: a payload of this shape
		// would end the recording of everything after it.
		s.warnings++
		env.ToolInput, env.ToolResponse = nil, nil
		raw = droppedPayload(env, dropped)
	}
	if err := s.bundle.WriteProviderEvent(raw); errors.Is(err, storage.ErrNotProviderEvent) {
		s.store(s.bundle.WriteUnparsedLine(boundedLine(raw, "delivery that is not one JSON object")))
	} else if errors.Is(err, storage.ErrLineTooLarge) {
		// Under the limit as delivered, over it once redacted: kept the way
		// an oversized delivery is, envelope without bulk.
		s.warnings++
		if dropped == "" {
			dropped = fmt.Sprintf("payload of %d bytes grew past the %d-byte stream limit under redaction; tool input and response were not recorded", len(raw), storage.MaxStreamLineBytes)
		}
		env.ToolInput, env.ToolResponse = nil, nil
		s.store(s.bundle.WriteProviderEvent(droppedPayload(env, dropped)))
	} else {
		s.store(err)
	}

	switch env.HookEventName {
	case hookUserPromptSubmit:
		// The prompt file holds one prompt, so it takes the first of the
		// session; the rest are in the event stream with everything else.
		if !s.prompted && env.Prompt != "" {
			s.prompted = true
			s.store(s.bundle.WritePrompt(boundedPrompt(env.Prompt)))
		}
		s.recordText(env, dropped, d.at)
	case hookStop:
		s.recordText(env, dropped, d.at)
	case hookPostToolUse, hookPostToolUseFailure:
		s.recordAction(env, dropped, d.at)
	case hookSessionEnd:
		return true
	}
	return false
}

// recordAction files a tool call the session's hook reported on. The hook
// carries no timestamps: the times here are when the recorder took delivery,
// and the duration is the provider's own.
func (s *sessionRecorder) recordAction(env hookEnvelope, dropped string, at time.Time) {
	if env.ToolUseID != "" && !s.first(env.HookEventName+":"+env.ToolUseID, 0, at) {
		return
	}
	s.actions++
	id := env.ToolUseID
	if id == "" {
		// Claude Code before 2.0.43 sends no tool_use_id. The action is still
		// one the session reported; it just cannot be correlated by the
		// provider's own name for it.
		id = fmt.Sprintf("hook-%d", s.actions)
	}
	now := time.Now()
	act := action.Action{
		ID:         id,
		Type:       hookActionType(s.provider, env.ToolName),
		Provider:   s.provider,
		Assurance:  action.AssuranceProviderReported,
		FinishedAt: now,
		Status:     hookStatusCompleted,
	}
	if env.HookEventName == hookPostToolUseFailure {
		act.Status = hookStatusFailed
	}
	if env.DurationMs != nil && *env.DurationMs >= 0 {
		act.StartedAt = now.Add(-time.Duration(*env.DurationMs) * time.Millisecond)
	}
	if len(env.ToolInput) > 0 {
		act.Input = env.ToolInput
	}
	if recordsRepositoryPaths(act) {
		act.RepositoryPaths = observeActionRepositoryPaths(act, s.cwd, s.canonicalCWD, s.repoRoot)
		act.RepositoryPathsRecorded = true
	}
	s.fileAction(act, hookActionResult{
		Source:       "hook." + env.HookEventName,
		ToolResponse: env.ToolResponse,
		Error:        env.Error,
		DurationMs:   env.DurationMs,
		AgentID:      env.AgentID,
		AgentType:    env.AgentType,
		Dropped:      dropped,
	})
}

// fileAction writes an action with its result, and when the redacted line
// would not fit the stream, writes it again without its bulk: the envelope,
// the source and a note saying what was dropped. One delivery never ends the
// recording of the ones after it.
func (s *sessionRecorder) fileAction(act action.Action, res hookActionResult) {
	var err error
	if act.Result, err = json.Marshal(res); err == nil {
		err = s.bundle.WriteAction(act)
	}
	if !errors.Is(err, storage.ErrLineTooLarge) {
		s.store(err)
		return
	}
	s.warnings++
	act.Input = nil
	res.ToolResponse = nil
	res.Error = bounded(res.Error, droppedFieldLimit)
	res.Dropped = fmt.Sprintf("the action grew past the %d-byte stream limit under redaction; input and response were not recorded", storage.MaxStreamLineBytes)
	if act.Result, err = json.Marshal(res); err == nil {
		err = s.bundle.WriteAction(act)
	}
	s.store(err)
}

// droppedFieldLimit bounds each string a dropped delivery's stub keeps from
// its envelope, so the stub itself always fits the stream.
const droppedFieldLimit = 4096

// bounded cuts s to limit bytes at a character boundary, marking the cut.
func bounded(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// recordText files what was said: the operator's prompt as submitted, and the
// assistant's final message for the turn as the Stop hook reports it. Both
// are the provider's word, filed under the same types the stream parser uses
// for a traced run, and both pass through redaction like any other action.
// The turn id pairs a reply with its prompt (prompt-<id> and reply-<id>).
func (s *sessionRecorder) recordText(env hookEnvelope, dropped string, at time.Time) {
	var typ, prefix, key, text string
	switch env.HookEventName {
	case hookUserPromptSubmit:
		typ, prefix, key, text = action.TypeUserPrompt, "prompt-", "prompt", env.Prompt
	case hookStop:
		typ, prefix, key, text = action.TypeAgentMessage, "reply-", "text", env.LastAssistantMessage
	default:
		return
	}
	if dropped != "" {
		// The text went with the payload; the action says so in its result.
		text = ""
	}
	if text == "" && dropped == "" {
		return
	}
	turn := env.PromptID
	if turn == "" {
		turn = env.TurnID
	}
	// A doubled delivery has the same turn, the same text and the same
	// stop_hook_active, and arrives within moments of the first. The same
	// words said again later — a repeated prompt, a continued turn that
	// ends the same way — are filed again. Without a turn id (Claude Code
	// before 2.1.196) the moment is all there is to go on.
	digest := sha256.Sum256([]byte(text))
	if !s.first(env.HookEventName+":"+turn+":"+hex.EncodeToString(digest[:])+":"+strconv.FormatBool(env.StopHookActive), duplicateDeliveryWindow, at) {
		return
	}
	s.actions++
	id := fmt.Sprintf("hook-%d", s.actions)
	if turn != "" {
		id = prefix + turn
		if !s.first("id:"+id, 0, at) {
			// A turn a stop hook continued reports Stop again: filed beside
			// the first, not over it.
			id = fmt.Sprintf("%s-%d", id, s.actions)
		}
	}
	now := time.Now()
	input, _ := json.Marshal(map[string]string{key: text})
	act := action.Action{
		ID:         id,
		Type:       typ,
		Provider:   s.provider,
		Assurance:  action.AssuranceProviderReported,
		StartedAt:  now,
		FinishedAt: now,
		Status:     hookStatusCompleted,
		Input:      input,
	}
	s.fileAction(act, hookActionResult{
		Source:         "hook." + env.HookEventName,
		Turn:           turn,
		StopHookActive: env.StopHookActive,
		AgentID:        env.AgentID,
		AgentType:      env.AgentType,
		Dropped:        dropped,
	})
}

// duplicateDeliveryWindow is how close two identical text deliveries must
// arrive to count as one hook registered twice rather than the same thing
// said again. Doubled deliveries arrive milliseconds apart; a person does
// not. Arrival is what counts: the recorder may take longer than the window
// to redact and file a large delivery before it reads the next.
const duplicateDeliveryWindow = 2 * time.Second

// first reports whether key was not seen within window of at (ever, when
// window is zero), and remembers at.
func (s *sessionRecorder) first(key string, window time.Duration, at time.Time) bool {
	if s.seen == nil {
		s.seen = map[string]time.Time{}
	}
	if at.IsZero() {
		at = time.Now()
	}
	if prev, ok := s.seen[key]; ok && (window == 0 || at.Sub(prev) < window) {
		return false
	}
	s.seen[key] = at
	return true
}

// promptFileLimit bounds prompt.txt to what every reader of the bundle will
// open: the viewer refuses a document over maxDocumentBytes. A longer first
// prompt is cut at a character boundary and says so at its end; the text the
// payload limit allowed is in the user.prompt action.
const promptFileLimit = maxDocumentBytes - 256

func boundedPrompt(p string) string {
	if len(p) <= promptFileLimit {
		return p
	}
	cut := promptFileLimit
	for cut > 0 && !utf8.RuneStart(p[cut]) {
		cut--
	}
	return p[:cut] + fmt.Sprintf("\n[agentrec: prompt of %d bytes cut to %d here; see the user.prompt action]", len(p), cut)
}

// hookActionType maps a tool name onto an action type the way the provider's
// own stream parser would, so a session's actions file under the same types a
// traced run's do.
func hookActionType(provider, toolName string) string {
	if provider == "codex" {
		return codex.ActionType(toolName)
	}
	return claude.ActionType(toolName)
}

// store keeps the first storage failure. The bundle refuses every later write
// itself, so nothing after it is lost silently; the recorder keeps serving to
// learn when the session ends, and the run is closed out with the failure as
// its exit reason.
func (s *sessionRecorder) store(err error) {
	if err != nil && s.storageErr == nil {
		s.storageErr = err
		s.warn("%v", err)
	}
}

func (s *sessionRecorder) warn(format string, args ...any) {
	fmt.Fprintf(s.stderr, "cli: session %s: %s\n", strconv.Quote(s.sessionID), fmt.Sprintf(format, args...))
}

// boundedLine is raw when the unparsed stream can hold it, and a note saying
// what was dropped when it cannot.
func boundedLine(raw []byte, what string) []byte {
	if len(raw) < sessionPayloadLimit {
		return raw
	}
	return []byte(fmt.Sprintf("[agentrec: %s of %d bytes dropped]", what, len(raw)))
}

// droppedPayload is what stands in the event stream for a delivery too large or
// too deep to record: the envelope, and the reason the rest is missing.
func droppedPayload(env hookEnvelope, note string) []byte {
	out, err := json.Marshal(struct {
		SessionID      string `json:"session_id"`
		HookEventName  string `json:"hook_event_name"`
		CWD            string `json:"cwd,omitempty"`
		ToolName       string `json:"tool_name,omitempty"`
		ToolUseID      string `json:"tool_use_id,omitempty"`
		DurationMs     *int64 `json:"duration_ms,omitempty"`
		Error          string `json:"error,omitempty"`
		AgentID        string `json:"agent_id,omitempty"`
		AgentType      string `json:"agent_type,omitempty"`
		PromptID       string `json:"prompt_id,omitempty"`
		TurnID         string `json:"turn_id,omitempty"`
		StopHookActive bool   `json:"stop_hook_active,omitempty"`
		Dropped        string `json:"agentrec_dropped"`
	}{env.SessionID, env.HookEventName, bounded(env.CWD, droppedFieldLimit), bounded(env.ToolName, droppedFieldLimit), bounded(env.ToolUseID, droppedFieldLimit), env.DurationMs, bounded(env.Error, droppedFieldLimit), bounded(env.AgentID, droppedFieldLimit), bounded(env.AgentType, droppedFieldLimit), bounded(env.PromptID, droppedFieldLimit), bounded(env.TurnID, droppedFieldLimit), env.StopHookActive, note})
	if err != nil {
		return []byte(`{"agentrec_dropped":"oversized delivery"}`)
	}
	return out
}

// The socket a session's hooks deliver to. It lives under a private directory
// in the temporary directory rather than under AGENTREC_HOME, because a Unix
// socket path is limited to about a hundred bytes and a data directory can be
// anywhere.
const (
	// sessionSocketDirEnv lets an operator — or a test — choose where session
	// sockets live.
	sessionSocketDirEnv = "AGENTREC_SESSION_SOCKET_DIR"
	// maxUnixSocketPath is the shortest sun_path among the supported platforms
	// (104 bytes on macOS, terminator included); a longer path fails to bind.
	maxUnixSocketPath             = 103
	socketMode        os.FileMode = 0o600
)

var errSessionServed = errors.New("cli: another recorder already serves this session")

func sessionSocketDir() (string, error) {
	dir := os.Getenv(sessionSocketDirEnv)
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "agentrec-"+strconv.Itoa(os.Getuid()))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("cli: create session socket directory %s: %w", strconv.Quote(dir), err)
	}
	// A symlink there is refused rather than followed: where the sockets live is
	// this process's decision and not something a link in the temporary
	// directory may change.
	info, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("cli: stat session socket directory %s: %w", strconv.Quote(dir), err)
	}
	if !info.Mode().IsDir() {
		return "", fmt.Errorf("cli: session socket directory %s is %s, want a directory", strconv.Quote(dir), info.Mode().Type())
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("cli: restrict session socket directory %s: %w", strconv.Quote(dir), err)
	}
	return dir, nil
}

// sessionSocketPath names the socket for one session. The session ID is hashed
// so the name is one short path component whatever the provider puts in it.
func sessionSocketPath(sessionID string) (string, error) {
	dir, err := sessionSocketDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(sessionID))
	path := filepath.Join(dir, hex.EncodeToString(sum[:8])+".sock")
	if len(path) > maxUnixSocketPath {
		return "", fmt.Errorf("cli: session socket path %s is longer than %d bytes: set %s to a shorter directory", strconv.Quote(path), maxUnixSocketPath, sessionSocketDirEnv)
	}
	return path, nil
}

// sessionLockPath is the lock beside a session's socket. The lock, not the
// socket, says whether a recorder holds the session: a socket file can be
// stale or removed under a live recorder, while a lock is released by the
// kernel the moment its holder is gone.
func sessionLockPath(socket string) string {
	return strings.TrimSuffix(socket, ".sock") + ".lock"
}

// listenSession claims the session and binds its socket. The lock file is
// never removed — a file removed under a lock is a lock a later recorder
// cannot see — and whoever holds it may treat any socket file at the path as
// left behind.
func listenSession(path string) (net.Listener, *os.File, error) {
	lockPath := sessionLockPath(path)
	lock, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("cli: open session lock %s: %w", strconv.Quote(lockPath), err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, nil, errSessionServed
		}
		return nil, nil, fmt.Errorf("cli: lock session %s: %w", strconv.Quote(lockPath), err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		lock.Close()
		return nil, nil, fmt.Errorf("cli: remove stale session socket %s: %w", strconv.Quote(path), err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		lock.Close()
		return nil, nil, fmt.Errorf("cli: listen on %s: %w", strconv.Quote(path), err)
	}
	// Kept to this user whatever the umask allowed.
	if err := os.Chmod(path, socketMode); err != nil {
		listener.Close()
		lock.Close()
		return nil, nil, fmt.Errorf("cli: restrict session socket %s: %w", strconv.Quote(path), err)
	}
	return listener, lock, nil
}
