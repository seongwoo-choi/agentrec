package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/evidence"
	"github.com/seongwoo-choi/agentrec/internal/provider"
	"github.com/seongwoo-choi/agentrec/internal/report"
	"github.com/seongwoo-choi/agentrec/internal/runner"
	"github.com/seongwoo-choi/agentrec/internal/storage"
)

// handledSignals are the two ways a recorded run is asked to stop: an operator
// types Ctrl-C, and a parent runner, a scheduler or a container asks with
// SIGTERM. Both mean the same thing to this recorder, and both are held rather
// than obeyed where they land while a run is being written down.
var handledSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// recordRequest is one run to record: a prepared provider invocation, the
// checkout it is launched in, and where the evidence for it goes. Everything
// here is decided before the run starts, because a run whose terms were settled
// while it was going is one nothing can be said about afterwards.
type recordRequest struct {
	Provider string
	Command  provider.Command
	Parser   runner.Parser
	Prompt   string

	// CWD is where the provider is launched, and RepoRoot is the top level of
	// the repository the evidence is measured in. They are usually the same
	// directory and need not be: a run recorded from a subdirectory is still
	// measured against the whole repository.
	CWD      string
	RepoRoot string

	RunsRoot string
	RunID    string
	Verify   bool

	// Interrupt carries the operator's interrupt, for the length of the run and
	// of the recorder's own work after it. A nil channel never fires.
	Interrupt <-chan os.Signal

	// Timeline receives the run ID and the rendered timeline once the report has
	// been filed under the run. A nil writer files the report and prints
	// nothing, which is what a caller recording several runs wants: the reports
	// are on disk either way, and the terminal is not the place to interleave
	// them.
	Timeline io.Writer
}

// recordOutcome is how one recorded run ended, in the terms a caller decides an
// exit code from. It carries no exit code of its own: what a provider's ending
// means to the shell is the command's decision and not the recorder's.
type recordOutcome struct {
	// Recorded reports whether the run reached the supervisor at all. A run that
	// did not has left a finalized bundle saying so, and nothing else here is
	// meaningful.
	Recorded bool

	Result runner.Result
	RunErr error

	// Verified reports whether checks were pinned and run, which is what tells
	// a verification that found nothing apart from one that was never asked for.
	Verified     bool
	Verification evidence.VerificationResult

	// Interrupted reports a signal that reached the recorder after the provider
	// had already ended — while the repository was being measured, the checks
	// were running, or the report was being filed.
	Interrupted bool

	// Incomplete reports that some part of the record could not be produced:
	// the repository evidence, the verification, or the report. What the
	// operator has been shown is short, and the caller says so.
	Incomplete bool
}

// record supervises one provider run and leaves a finished bundle behind: the
// baseline pinned before it starts, the checks fixed before it starts, the
// process recorded as it runs, and the repository measured, verified and
// rendered once it has ended. Every diagnostic is written to stderr as it
// happens; what the ending means is the caller's to decide.
func record(req recordRequest, stderr io.Writer) recordOutcome {
	runID := req.RunID

	// The manifest records the invocation exactly as it will be launched,
	// executable included, so the recorded argv is the command that ran and not
	// the one the operator typed. Storage sets the redaction rule version.
	bundle, err := storage.Create(req.RunsRoot, runID, storage.Manifest{
		Provider:        req.Provider,
		ProviderVersion: req.Command.Version,
		Argv:            append([]string{req.Command.Executable}, req.Command.Args...),
		CWD:             req.CWD,
		StartedAt:       time.Now(),
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return recordOutcome{}
	}

	// The baseline is pinned before the prompt is written and before the
	// provider is launched: what the run changed can only be measured against
	// where the repository stood before any of the run happened. A baseline that
	// could not be pinned is a run whose changes could never be attributed, so
	// it is not started. Sanitizing goes through the bundle's own redactor, so a
	// secret the evidence carries reads as the secret the rest of the run named.
	startCtx, cancelStart := context.WithTimeout(context.Background(), evidenceStartTimeout)
	capture, err := evidence.Start(startCtx, req.RepoRoot, runID, bundle.Dir(), evidence.Options{
		Sanitize: bundle.SanitizeText,
	})
	cancelStart()
	if err != nil {
		return unrecordable(bundle, stderr, runID, err)
	}
	// Deferred before anything else can fail, so a prompt that could not be
	// written or a provider that never started still takes the ref back out of
	// the repository. Finalize closes the capture itself, and this close reports
	// the same outcome, so it stands down once the run has been measured.
	evidenceMeasured := false
	defer func() {
		if evidenceMeasured {
			return
		}
		closeCtx, cancelClose := context.WithTimeout(context.Background(), evidenceCloseTimeout)
		defer cancelClose()
		if err := capture.Close(closeCtx); err != nil {
			fmt.Fprintf(stderr, "cli: run %s: %v\n", runID, err)
		}
	}()

	// The checks are fixed before the provider starts, for the same reason the
	// baseline is: what a run is verified against has to be what an operator
	// reviewed, and a configuration read afterwards is one the run could have
	// written. A verification that could not be pinned is a run whose result
	// could never be trusted, so it is not started.
	var verifier *evidence.PinnedVerification
	verifierClosed := false
	if req.Verify {
		pinCtx, cancelPin := context.WithTimeout(context.Background(), verifyPinTimeout)
		verifier, err = evidence.PinVerification(pinCtx, req.RepoRoot, bundle.Dir(), filepath.Join(req.RepoRoot, verifyConfigFile), evidence.VerificationOptions{
			Sanitize: bundle.SanitizeText,
		})
		cancelPin()
		if err != nil {
			return unrecordable(bundle, stderr, runID, err)
		}
		// Deferred for the paths that return before the checks are run. Close
		// reports the same outcome every time, so the one below stands down once
		// the run has closed the verification itself.
		defer func() {
			if verifierClosed {
				return
			}
			if err := verifier.Close(); err != nil {
				fmt.Fprintf(stderr, "cli: run %s: %v\n", runID, err)
			}
		}()
	}

	// The prompt is written before the provider starts: a run whose own request
	// could not be recorded is not one to launch, because whatever it then did
	// would be evidence with nothing to explain it.
	if req.Prompt != "" {
		if err := bundle.WritePrompt(req.Prompt); err != nil {
			return unrecordable(bundle, stderr, runID, err)
		}
	}

	out := recordOutcome{Recorded: true}
	out.Result, out.RunErr = runner.Run(context.Background(), runner.Request{
		Command:   req.Command,
		CWD:       req.CWD,
		Bundle:    bundle,
		Parser:    req.Parser,
		Interrupt: req.Interrupt,
	})

	// Taken over now that the supervisor has stopped listening. Everything below
	// is the recorder's own work — measuring the repository, running the checks,
	// filing the report — and a signal arriving during it is the operator asking
	// for the run to stop, not permission to abandon a half-written record. It
	// is held here and reported at the end.
	held := holdSignals(req.Interrupt)

	// Measured now that the provider's process group has ended and its streams
	// and manifest are on disk, so what the repository shows is what the run
	// left. Attempted for every ending, including a bad one: an interrupted or
	// failed run changed the repository too, and the lock is still held, so
	// nothing else has touched it in between. Its deadline is its own: a
	// measurement abandoned halfway is a run with nothing to show.
	finalizeCtx, cancelFinalize := context.WithTimeout(context.Background(), evidenceFinalizeTimeout)
	_, evidenceErr := capture.Finalize(finalizeCtx)
	cancelFinalize()
	evidenceMeasured = true

	// The checks run against the repository the run left, and after it has been
	// measured, so nothing a check writes can be read as the agent's work. They
	// run whatever the provider did: a failed or interrupted run still left work
	// behind, and whether it holds up is the question the checks answer. An
	// operator's interrupt does stop them: a verification is minutes of the
	// recorder's own work, and one they asked to end is one to end.
	var verifyErr, verifyCloseErr error
	if verifier != nil {
		verifyCtx, stopVerify := context.WithCancel(context.Background())
		go func() {
			select {
			case <-held.fired:
				stopVerify()
			case <-verifyCtx.Done():
			}
		}()
		out.Verification, verifyErr = verifier.Run(verifyCtx)
		stopVerify()
		verifyCloseErr = verifier.Close()
		verifierClosed = true
		out.Verified = true
	}

	// The report is read back from the finalized bundle rather than rendered
	// from what this process still holds, so what the operator sees is what was
	// persisted. It is attempted for every ending, including a bad one: partial
	// evidence is what an interrupted or failed run has to show.
	rep, renderErr := installRunReport(req.RunsRoot, runID)
	if renderErr == nil && req.Timeline != nil {
		renderErr = printRun(req.Timeline, runID, rep)
	}

	// Handed back to the operating system now that the run has been recorded to
	// the end. A signal from here on is one this process no longer has anything
	// to hold it for.
	out.Interrupted = held.stop()

	if evidenceErr != nil {
		fmt.Fprintf(stderr, "cli: run %s: capture repository evidence: %v\n", runID, evidenceErr)
	}
	if renderErr != nil {
		fmt.Fprintf(stderr, "cli: run %s: %v\n", runID, renderErr)
	}
	for _, err := range []error{verifyErr, verifyCloseErr} {
		if err != nil {
			fmt.Fprintf(stderr, "cli: run %s: %v\n", runID, err)
		}
	}
	out.Incomplete = evidenceErr != nil || renderErr != nil || verifyErr != nil || verifyCloseErr != nil
	return out
}

// unrecordable ends a run that could not be recorded before its provider was
// ever launched. The bundle is finalized rather than left open, so the run
// directory describes a run that stopped instead of one still going.
func unrecordable(bundle *storage.Bundle, stderr io.Writer, runID string, cause error) recordOutcome {
	fmt.Fprintf(stderr, "cli: run %s: %v\n", runID, cause)
	if err := bundle.Finalize(storage.Finalization{
		EndedAt:    time.Now(),
		ExitReason: runner.ReasonStorageError,
	}); err != nil {
		fmt.Fprintf(stderr, "cli: run %s: %v\n", runID, err)
	}
	return recordOutcome{}
}

// heldSignals watches for the operator's interrupt over the recorder's own work
// after the provider has ended. The signal is held rather than obeyed where it
// lands: a recorder that dies between the provider stopping and the evidence
// being written leaves a run that never says how it ended.
//
// The first signal is the last one this holds. Once it has been taken, the
// disposition goes back to the operating system, so an operator who asks a
// second time gets the ending the second ask means rather than another wait.
type heldSignals struct {
	fired  chan struct{}
	done   chan struct{}
	closed chan struct{}
}

func holdSignals(ch <-chan os.Signal) *heldSignals {
	h := &heldSignals{
		fired:  make(chan struct{}),
		done:   make(chan struct{}),
		closed: make(chan struct{}),
	}
	go func() {
		defer close(h.closed)
		select {
		case <-ch:
			// Given straight back to the operating system, before anything is
			// reported: from here on this process is finishing the run it was
			// asked to stop, and an operator who asks a second time has decided
			// that wait is over. Reset rather than Stop, because the run has to
			// end on the second ask whichever channel is listening.
			signal.Reset(handledSignals...)
			close(h.fired)
		case <-h.done:
		}
	}()
	return h
}

// stop ends the watch and reports whether a signal reached it. It returns only
// once the watching goroutine has finished, so what it reports is settled.
func (h *heldSignals) stop() bool {
	close(h.done)
	<-h.closed
	select {
	case <-h.fired:
		return true
	default:
		return false
	}
}

// installRunReport reads the recorded run back and files the rendered report
// under it, returning the reading both were made from.
//
// A run whose report could not be written is reported as a failure rather than
// shown: the terminal keeps what it was given only until the next command,
// while the bundle is what is left of a run once the repository it was recorded
// against has moved on.
func installRunReport(root, runID string) (report.Report, error) {
	rep, err := readRun(root, runID)
	if err != nil {
		return report.Report{}, err
	}
	dir, err := runDir(root, runID)
	if err != nil {
		return report.Report{}, err
	}
	if err := installReport(dir, rep); err != nil {
		return report.Report{}, err
	}
	return rep, nil
}

// printRun announces the run ID so the operator can ask for it again later, and
// then prints the same reading of the same evidence the filed report was
// rendered from.
func printRun(stdout io.Writer, runID string, rep report.Report) error {
	if _, err := fmt.Fprintf(stdout, "Run ID: %s\n\n", runID); err != nil {
		return err
	}
	return report.RenderTerminal(stdout, rep)
}
