// Package runner supervises one provider process: it starts the prepared
// command, records what the process emits as it emits it, ends the whole
// process group when the run is interrupted or overruns, and leaves a finalized
// bundle behind however the run turned out. A run that stopped badly is exactly
// the run whose record matters, so every ending is recorded rather than
// reported as a failure to record.
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
	"time"

	"github.com/seongwoo-choi/agentrec/internal/action"
	"github.com/seongwoo-choi/agentrec/internal/provider"
	"github.com/seongwoo-choi/agentrec/internal/storage"
	"github.com/seongwoo-choi/agentrec/internal/usage"
)

// Exit reasons record why a run ended. They are the vocabulary of both
// process/result.json and the manifest.
const (
	ReasonCompleted    = "completed"
	ReasonNonzero      = "nonzero"
	ReasonInterrupted  = "interrupted"
	ReasonTimeout      = "timeout"
	ReasonParseError   = "parse_error"
	ReasonStorageError = "storage_error"
	ReasonStartError   = "start_error"
)

// DefaultKillGrace is how long a provider asked to stop is given before it is
// killed. It is long enough for an agent CLI to flush its transcript and short
// enough that a wedged run does not hold the recorder open.
const DefaultKillGrace = 5 * time.Second

// Stream limits. A provider event may legitimately carry a whole file, so the
// line limit is generous; stderr is unstructured and is kept only as context,
// so it is capped and the rest read and dropped.
const (
	initialLineBuffer = 64 << 10
	maxLineBytes      = 4 << 20
	maxStderrBytes    = 4 << 20
)

// stderrTruncationMarker says, in the capture itself, that stderr went past the
// cap. It is derived from the cap so the two can never disagree.
var stderrTruncationMarker = fmt.Sprintf("\n[agentrec: stderr truncated after %d bytes]\n", maxStderrBytes)

// ErrInterrupted and ErrTimedOut report how a run was ended by the supervisor
// rather than by the provider.
var (
	ErrInterrupted = errors.New("runner: run interrupted")
	ErrTimedOut    = errors.New("runner: run timed out")
)

// ParseResult is what a provider parser recovered from an event stream.
type ParseResult struct {
	Actions      []action.Action
	Usage        *usage.Report
	WarningCount int
}

// Parser turns a provider's event stream into normalized actions. It is handed
// the stream as it arrives, not the finished run.
type Parser func(io.Reader) (ParseResult, error)

// StartGate serializes the final process launch with an external lifecycle.
// Returning ErrInterrupted means the process was deliberately not started.
type StartGate func(start func() error) error

// Request is one supervised run.
type Request struct {
	Command provider.Command
	CWD     string
	Bundle  *storage.Bundle
	Parser  Parser
	// Timeout bounds the whole run. Zero means no bound.
	Timeout time.Duration
	// KillGrace is how long the provider has to end itself after being asked to.
	// Zero means DefaultKillGrace.
	KillGrace time.Duration
	// Interrupt carries the operator's interrupt, usually from signal.Notify. A
	// nil channel simply never fires.
	Interrupt <-chan os.Signal
	StartGate StartGate
}

// Result is how the run went. ExitCode is nil when there was none: a process
// killed by a signal, or one that never started.
type Result struct {
	StartedAt    time.Time
	EndedAt      time.Time
	Duration     time.Duration
	ExitCode     *int
	Signal       string
	ExitReason   string
	WarningCount int
	// UnparsedLines counts the stdout lines that were not provider events and
	// were kept apart from them. They are included in WarningCount, and counted
	// separately as well, so a reader can tell a run whose agent reported
	// problems from one whose CLI merely printed a banner.
	UnparsedLines int
}

// processResult is process/result.json: how the run was executed, kept apart
// from what the run recorded.
type processResult struct {
	StartedAt      time.Time `json:"startedAt"`
	EndedAt        time.Time `json:"endedAt"`
	DurationMillis int64     `json:"durationMillis"`
	ExitCode       *int      `json:"exitCode"`
	Signal         string    `json:"signal,omitempty"`
	ExitReason     string    `json:"exitReason"`
}

// Run executes the prepared command and records it. It returns nil only for a
// run that was executed and recorded end to end; a provider that merely exited
// nonzero is one of those, because a failed agent run is a recorded fact and
// not a failure of the recorder.
func Run(ctx context.Context, req Request) (Result, error) {
	// A nil bundle is the one rejection with nowhere to be recorded, so it alone
	// stays a plain validation failure. Every other unusable request has a bundle
	// someone will ask about, and is recorded as the run that never started.
	if req.Bundle == nil {
		return Result{}, errors.New("runner: no bundle to record into")
	}
	res := Result{StartedAt: time.Now()}
	switch {
	case req.Parser == nil:
		return unstarted(req.Bundle, res, errors.New("runner: no parser for the provider stream"))
	case req.Command.Executable == "":
		return unstarted(req.Bundle, res, errors.New("runner: no executable to run"))
	}
	grace := req.KillGrace
	if grace <= 0 {
		grace = DefaultKillGrace
	}

	cmd := exec.Command(req.Command.Executable, req.Command.Args...)
	cmd.Dir = req.CWD
	// A nil stdin is /dev/null. This MVP records non-interactive runs only, so a
	// provider that asks for input reads EOF instead of the operator's terminal.
	cmd.Stdin = nil
	// Not exec.CommandContext: its cancellation kills the provider alone and
	// leaves every process it spawned behind. Cancellation is handled below, on
	// the whole group.
	setProcessGroup(cmd)

	stdout, stderr, err := pipes(cmd)
	if err != nil {
		return unstarted(req.Bundle, res, fmt.Errorf("runner: start %s: %w", req.Command.Executable, err))
	}
	// Asked at the final userspace boundary before launch. The watch below can
	// only end a process that already exists, so an interrupt that arrived while
	// this run was preparing its command or pipes must prevent that process from
	// starting at all. The run is still finalized as interrupted evidence.
	if held(req.Interrupt) {
		res.ExitReason = ReasonInterrupted
		res.EndedAt = time.Now()
		res.Duration = res.EndedAt.Sub(res.StartedAt)
		return finish(req.Bundle, res, ErrInterrupted)
	}
	start := cmd.Start
	if req.StartGate != nil {
		start = func() error { return req.StartGate(cmd.Start) }
	}
	if err := start(); errors.Is(err, ErrInterrupted) {
		res.ExitReason = ReasonInterrupted
		res.EndedAt = time.Now()
		res.Duration = res.EndedAt.Sub(res.StartedAt)
		return finish(req.Bundle, res, ErrInterrupted)
	} else if err != nil {
		return unstarted(req.Bundle, res, fmt.Errorf("runner: start %s: %w", req.Command.Executable, err))
	}

	// Stderr is drained by its own goroutine: a provider that talks on both
	// streams would otherwise fill the stderr pipe and block itself while the
	// recorder waits on stdout.
	stderrDone := make(chan capture, 1)
	go func() { stderrDone <- drain(stderr) }()

	// Each complete stdout line is recorded as evidence and handed to the parser
	// as it arrives, through a pipe rather than a buffer: nothing here waits for
	// the run to end before writing what the run already said.
	pr, pw := io.Pipe()
	parsed := make(chan parseOutcome, 1)
	go func() {
		out, err := req.Parser(pr)
		// However the parser finished, the tee must not block writing to a
		// reader that has stopped reading.
		pr.CloseWithError(err)
		parsed <- parseOutcome{out, err}
	}()
	streamed := make(chan teeOutcome, 1)
	go func() { streamed <- tee(stdout, pw, req.Bundle) }()

	signaller := &groupSignaller{pid: cmd.Process.Pid}
	watcher := watch(ctx, req, grace, signaller)

	// Wait only once every reader is finished: the pipes are closed by Wait, and
	// what has not been read by then is evidence that never arrives.
	stream := <-streamed
	parse := <-parsed
	stderrOut := <-stderrDone

	reason := watcher.stop()
	waitErr := cmd.Wait()
	signaller.stop()

	res.EndedAt = time.Now()
	res.Duration = res.EndedAt.Sub(res.StartedAt)
	// Counted, not added: a line neither the parser nor the event writer could
	// read is one line, and both of them saw it. The parsers already raise a
	// warning for a line they cannot read as an event, so this is the same
	// occurrence named more precisely — adding the two would report one banner
	// as two things having gone wrong.
	res.UnparsedLines = stream.unparsed
	res.WarningCount = parse.out.WarningCount
	if cmd.ProcessState != nil {
		res.ExitCode, res.Signal = exitStatus(cmd.ProcessState)
	}

	storageErr := writeEvidence(req.Bundle, stderrOut.text, parse)
	if storageErr == nil {
		storageErr = firstOf(stream.err, stderrOut.err)
	}

	// The supervisor's own reason comes first: a run it ended is described by
	// how it ended it, whatever the truncated stream then looked like.
	switch {
	case reason != "":
		res.ExitReason = reason
	case storageErr != nil:
		res.ExitReason = ReasonStorageError
	case parse.err != nil:
		res.ExitReason = ReasonParseError
	case res.ExitCode == nil || *res.ExitCode != 0:
		res.ExitReason = ReasonNonzero
	default:
		res.ExitReason = ReasonCompleted
	}

	var exited *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exited) {
		waitErr = fmt.Errorf("runner: wait for %s: %w", req.Command.Executable, waitErr)
	} else {
		waitErr = nil
	}

	runErr := firstOf(
		terminationError(ctx, reason),
		storageErr,
		parseError(parse.err),
		waitErr,
		watcher.err,
	)
	return finish(req.Bundle, res, runErr)
}

// held reports whether the operator has already asked for the run to stop,
// without waiting for an ask that has not arrived. A nil channel never has one.
func held(interrupt <-chan os.Signal) bool {
	select {
	case <-interrupt:
		return true
	default:
		return false
	}
}

// unstarted records a run that never reached a running process, whether the
// request was unusable or the command would not start, and reports the cause
// that stopped it. The ending is timed here rather than left unset: a record
// whose run has no end reads as one still in progress, and this one is over.
func unstarted(b *storage.Bundle, res Result, cause error) (Result, error) {
	res.ExitReason = ReasonStartError
	res.EndedAt = time.Now()
	res.Duration = res.EndedAt.Sub(res.StartedAt)
	return finish(b, res, cause)
}

// finish records how the run was executed and closes the bundle. It runs for
// every started run and for one that never started, because a run that failed
// early is still a run someone will ask about. A failure here becomes the
// manifest's exit reason unless the supervisor already had a stronger one to
// report, so the manifest never claims a clean end it could not write.
func finish(b *storage.Bundle, res Result, runErr error) (Result, error) {
	resultErr := writeProcessResult(b, res)
	if resultErr != nil && res.ExitReason != ReasonInterrupted && res.ExitReason != ReasonTimeout && res.ExitReason != ReasonStartError {
		res.ExitReason = ReasonStorageError
	}
	finalizeErr := b.Finalize(storage.Finalization{
		EndedAt:       res.EndedAt,
		ExitReason:    res.ExitReason,
		WarningCount:  res.WarningCount,
		UnparsedLines: res.UnparsedLines,
	})
	return res, firstOf(runErr, resultErr, finalizeErr)
}

// pipes opens the provider's output streams before it starts.
func pipes(cmd *exec.Cmd) (stdout, stderr io.Reader, err error) {
	if stdout, err = cmd.StdoutPipe(); err != nil {
		return nil, nil, fmt.Errorf("open stdout: %w", err)
	}
	if stderr, err = cmd.StderrPipe(); err != nil {
		return nil, nil, fmt.Errorf("open stderr: %w", err)
	}
	return stdout, stderr, nil
}

// teeOutcome is what recording the stdout stream came to: how many lines were
// not provider events, and the first failure that stopped one from being
// recorded at all.
type teeOutcome struct {
	unparsed int
	err      error
}

// tee records every complete stdout line as a provider event and passes it on
// to the parser reading the other end of pw. The stream is read to the end even
// once something has gone wrong, so the provider never blocks writing into a
// pipe nobody is emptying.
//
// A line that is not a provider event at all — an update banner, a deprecation
// warning, anything an agent CLI prints beside its event stream — is kept in
// the bundle's unparsed stream and counted, not treated as a failure to record.
// A provider that printed one line of prose has still run, and a recorder that
// threw the whole run away over it would be destroying the evidence it exists
// to keep. What does fail is the recorder being unable to store that line
// either: at that point something the provider said is being lost.
func tee(stdout io.Reader, pw *io.PipeWriter, b *storage.Bundle) teeOutcome {
	defer pw.Close()

	var out teeOutcome
	keep := func(err error) {
		if err != nil && out.err == nil {
			out.err = err
		}
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, initialLineBuffer), maxLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) > 0 {
			err := b.WriteProviderEvent(line)
			if errors.Is(err, storage.ErrNotProviderEvent) {
				err = b.WriteUnparsedLine(line)
				if err == nil {
					out.unparsed++
				}
			}
			keep(err)
		}
		// A write that fails here means the parser stopped reading, which is
		// already reported as the parser's own error; the loop carries on so the
		// provider keeps draining.
		if _, err := pw.Write(line); err == nil {
			pw.Write(newline)
		}
	}
	if err := sc.Err(); err != nil {
		// A line past the limit is one the recorder cannot store whole, and a
		// half-stored event is not evidence: it is surfaced rather than dropped.
		// The scanner stops there, so the rest of the stream is read and dropped
		// here; a provider still writing into a pipe nobody empties never exits,
		// and an unbounded run has no timeout to rescue it.
		keep(fmt.Errorf("runner: read provider output: %w", err))
		if _, derr := io.Copy(io.Discard, stdout); derr != nil {
			keep(fmt.Errorf("runner: read provider output: %w", derr))
		}
	}
	return out
}

var newline = []byte("\n")

// capture is a drained stream and whatever stopped the draining.
type capture struct {
	text string
	err  error
}

// drain reads a stream to its end, keeping at most maxStderrBytes of it. The
// cap bounds what is kept, not what is read: a provider must never block
// writing because the recorder stopped listening.
func drain(r io.Reader) capture {
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, maxStderrBytes))
	if err != nil {
		return capture{buf.String(), fmt.Errorf("runner: read provider stderr: %w", err)}
	}
	if n < maxStderrBytes {
		return capture{buf.String(), nil}
	}
	extra, err := io.Copy(io.Discard, r)
	if extra > 0 {
		buf.WriteString(stderrTruncationMarker)
	}
	if err != nil {
		return capture{buf.String(), fmt.Errorf("runner: read provider stderr: %w", err)}
	}
	return capture{buf.String(), nil}
}

// parseOutcome is what the parser made of the stream.
type parseOutcome struct {
	out ParseResult
	err error
}

// termination watches for every reason to end the run early.
type termination struct {
	done   chan struct{}
	closed chan struct{}
	reason string
	err    error
}

// stop ends the watch and reports the reason it fired, if it fired. It is
// called after the provider streams close but before the leader is reaped. That
// keeps the process-group id safe to signal while the watcher removes any
// descendant that outlived the leader.
func (t *termination) stop() string {
	close(t.done)
	<-t.closed
	return t.reason
}

// watch ends the run when the caller cancels it, when the operator interrupts
// it, or when it overruns. It asks first and insists afterwards: the group is
// asked to stop with the signal that fits the reason, and an agent CLI asked to
// stop is the one that knows how to stop. But the asking is bounded, because a
// provider that ignores it would otherwise hold the recorder open for as long
// as it liked, with everything it spawned still running. A second interrupt is
// the operator declining to wait out the rest of that grace.
//
// Only how the run was ended escalates. Why it ended does not: a killed
// provider that was asked to stop is still an interrupted run.
func watch(ctx context.Context, req Request, grace time.Duration, signaller *groupSignaller) *termination {
	t := &termination{done: make(chan struct{}), closed: make(chan struct{})}
	go func() {
		defer close(t.closed)

		var overran <-chan time.Time
		if req.Timeout > 0 {
			timer := time.NewTimer(req.Timeout)
			defer timer.Stop()
			overran = timer.C
		}

		ask := sigInterrupt
		select {
		case <-t.done:
			return
		case <-ctx.Done():
			t.reason = ReasonInterrupted
		case <-req.Interrupt:
			t.reason = ReasonInterrupted
		case <-overran:
			// An overrun has already stopped answering its deadline, so it is
			// asked with SIGTERM rather than with an interrupt.
			t.reason, ask = ReasonTimeout, sigTerminate
		}
		t.err = signaller.send(ask)

		// The provider now has the grace to end itself. If Run observes closed
		// streams first, done insists immediately while the leader is still
		// unreaped: a descendant can close inherited streams, ignore the first
		// signal, and otherwise survive with the same process-group id.
		insist := time.NewTimer(grace)
		defer insist.Stop()
		select {
		case <-t.done:
		case <-req.Interrupt:
		case <-insist.C:
		}
		if err := signaller.send(sigKill); t.err == nil {
			t.err = err
		}
	}()
	return t
}

// writeEvidence records what the provider left behind: its stderr as one
// capture, so a secret written across several lines is judged whole, and then
// the normalized actions. A parser that failed describes a stream it could not
// read through, so the actions it recovered up to that point are not written:
// half a reading of a run is not a record of it.
func writeEvidence(b *storage.Bundle, stderrText string, parse parseOutcome) error {
	err := b.WriteProcessStderr(stderrText)
	if parse.err != nil {
		return err
	}
	for _, a := range parse.out.Actions {
		if werr := b.WriteAction(a); werr != nil && err == nil {
			err = werr
		}
	}
	if parse.out.Usage != nil {
		if werr := b.WriteUsage(*parse.out.Usage); werr != nil && err == nil {
			err = werr
		}
	}
	return err
}

// writeProcessResult records how the process itself ended.
func writeProcessResult(b *storage.Bundle, res Result) error {
	raw, err := json.Marshal(processResult{
		StartedAt:      res.StartedAt,
		EndedAt:        res.EndedAt,
		DurationMillis: res.Duration.Milliseconds(),
		ExitCode:       res.ExitCode,
		Signal:         res.Signal,
		ExitReason:     res.ExitReason,
	})
	if err != nil {
		return fmt.Errorf("runner: encode process result: %w", err)
	}
	return b.WriteProcessResult(raw)
}

// terminationError names the supervisor's ending as an error for the caller,
// keeping a cancelled context recognisable as one.
func terminationError(ctx context.Context, reason string) error {
	switch reason {
	case ReasonTimeout:
		return ErrTimedOut
	case ReasonInterrupted:
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %w", ErrInterrupted, err)
		}
		return ErrInterrupted
	}
	return nil
}

func parseError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("runner: parse provider output: %w", err)
}

// firstOf returns the first failure, which is the one that explains the rest.
func firstOf(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
