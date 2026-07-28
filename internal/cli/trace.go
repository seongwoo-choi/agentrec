package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/evidence"
	"github.com/seongwoo-choi/agentrec/internal/lock"
	"github.com/seongwoo-choi/agentrec/internal/provider"
	"github.com/seongwoo-choi/agentrec/internal/provider/claude"
	"github.com/seongwoo-choi/agentrec/internal/provider/codex"
	"github.com/seongwoo-choi/agentrec/internal/report"
	"github.com/seongwoo-choi/agentrec/internal/runner"
	"github.com/seongwoo-choi/agentrec/internal/storage"
)

// traceDelimiter separates agentrec's own arguments from the provider's. It is
// required rather than inferred: everything after it belongs to the provider,
// including flags agentrec itself understands.
const traceDelimiter = "--"

const traceUsage = "usage: agentrec trace <claude|codex> [--verify] -- <provider args...>\n"

// verifyFlag asks for the repository's own checks to be run against the work
// once the provider has stopped. It is the only option agentrec takes for
// itself, and it is spelled exactly: anything else before the delimiter is an
// invocation agentrec does not understand rather than one it guesses at.
const verifyFlag = "--verify"

// verifyConfigFile is where the checks are read from, at the root of the
// repository being recorded. It is not configurable: a verification an operator
// has to be told the location of is one they cannot check was the one that ran.
const verifyConfigFile = ".agentrec.yaml"

// verifyPinTimeout bounds reading and fixing that configuration, which is one
// file and a handful of Git questions. The checks themselves are bounded by the
// configuration, each by its own timeout.
const verifyPinTimeout = 10 * time.Second

// versionTimeout bounds preparation alone — running the provider's `--version`
// — and nothing else. The recorded run itself is bounded by the operator, not
// by a clock here.
const versionTimeout = 10 * time.Second

// gitTimeout bounds the questions asked of the repository before the run — where
// it is, and whether it is clean. They answer in milliseconds; a repository that
// does not answer is one this process must not wait on forever.
const gitTimeout = 30 * time.Second

// locksDirName holds the per-repository locks beside the recorded runs.
const locksDirName = "locks"

// Bounds on the repository evidence, which is collected before and after the
// run but never during it. Pinning the baseline is a handful of Git commands and
// answers as quickly as the checks above; measuring what the run changed reads
// the worktree and hashes what it finds, so it is given the longer deadline a
// large repository needs and still cannot hang this process forever.
const (
	evidenceStartTimeout    = 10 * time.Second
	evidenceFinalizeTimeout = 2 * time.Minute
	evidenceCloseTimeout    = 10 * time.Second
)

// Exit codes. A provider's own exit code is passed through when it is one a
// shell would read back as the provider's; 126 and above are the shell's own
// vocabulary for how a command failed to run, and 130 is reserved here for a
// run the operator interrupted.
const (
	exitFailure         = 1
	exitUsage           = 2
	exitInterrupted     = 130
	maxProviderExitCode = 125
)

// runIDTimeLayout stamps a run with the UTC instant it started, to the
// nanosecond, so run IDs sort in the order their runs happened.
const runIDTimeLayout = "20060102T150405.000000000Z"

// The rendered report, filed under the run beside the evidence it was rendered
// from. It may quote a private repository, so it is readable only by the user
// who recorded it, and it is bounded: a report past this size is a bundle that
// grew pathologically rather than a run worth reading.
const (
	reportFile                 = "report.md"
	reportMode     os.FileMode = 0o600
	maxReportBytes             = 2 * maxActionStreamBytes
)

// runTrace records one provider run: it prepares the invocation, opens a
// bundle for it, supervises the process, and then renders the run back from the
// bundle it just wrote.
func runTrace(args []string, stdout, stderr io.Writer) int {
	// The provider and the delimiter are both required, and so is at least one
	// argument for the provider: agentrec can only record a non-interactive run,
	// which is never an empty argument list.
	delimiter := slices.Index(args, traceDelimiter)
	if delimiter < 1 || delimiter == len(args)-1 {
		fmt.Fprint(stderr, traceUsage)
		return exitUsage
	}
	name, providerArgs := args[0], args[delimiter+1:]
	// Everything between the provider and the delimiter is agentrec's own, and
	// there is one thing it may be. An option it does not know, or one given
	// twice, is refused rather than ignored: an operator who asked for something
	// must not be told a run was recorded the way they asked for.
	verify := false
	for _, opt := range args[1:delimiter] {
		if opt != verifyFlag || verify {
			fmt.Fprint(stderr, traceUsage)
			return exitUsage
		}
		verify = true
	}

	var (
		cmd    provider.Command
		parse  runner.Parser
		prompt string
		err    error
	)
	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	switch name {
	case "claude":
		cmd, err = claude.PrepareCommand(ctx, providerArgs, nil)
		parse, prompt = claudeParser, claudePrompt(providerArgs)
	case "codex":
		cmd, err = codex.PrepareCommand(ctx, providerArgs, nil)
		parse, prompt = codexParser, codexPrompt(providerArgs)
	default:
		cancel()
		fmt.Fprint(stderr, traceUsage)
		return exitUsage
	}
	cancel()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "cli: locate working directory: %v\n", err)
		return exitFailure
	}
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

	// The repository is taken before it is judged, and both happen before any
	// evidence is written: a run recorded against a repository another run is
	// already changing, or against one that was not clean to begin with, cannot
	// say afterwards which changes were the agent's. The lock is held until this
	// function returns, which is after the run has been finalized and read back.
	gitCtx, cancelGit := context.WithTimeout(context.Background(), gitTimeout)
	defer cancelGit()
	repo, err := lock.Acquire(gitCtx, filepath.Join(filepath.Dir(root), locksDirName), cwd)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	defer func() {
		if err := repo.Release(); err != nil {
			fmt.Fprintln(stderr, err)
		}
	}()
	if err := lock.CheckClean(gitCtx, repo.Root()); err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}

	// Installed before the bundle exists, and buffered, so an interrupt arriving
	// from here on is held rather than ending this process where it stands: a
	// Ctrl-C between creating the bundle and running the provider would otherwise
	// leave a run directory that never says how it ended. SIGTERM is held for the
	// same reason and treated the same way: an operator types Ctrl-C, but a parent
	// runner, a scheduler or a container asks a process to stop with SIGTERM, and
	// a recorder that dies on the spot there leaves the same unfinished bundle
	// with the provider's process group still running. The run itself has no
	// timeout: how long an agent may work is the operator's decision, taken with
	// that same Ctrl-C.
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupt)

	// The manifest records the invocation exactly as it will be launched,
	// executable included, so the recorded argv is the command that ran and not
	// the one the operator typed. Storage sets the redaction rule version.
	bundle, err := storage.Create(root, runID, storage.Manifest{
		Provider:        name,
		ProviderVersion: cmd.Version,
		Argv:            append([]string{cmd.Executable}, cmd.Args...),
		CWD:             cwd,
		StartedAt:       time.Now(),
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}

	// The baseline is pinned before the prompt is written and before the
	// provider is launched: what the run changed can only be measured against
	// where the repository stood before any of the run happened. A baseline that
	// could not be pinned is a run whose changes could never be attributed, so
	// it is not started. Sanitizing goes through the bundle's own redactor, so a
	// secret the evidence carries reads as the secret the rest of the run named.
	startCtx, cancelStart := context.WithTimeout(context.Background(), evidenceStartTimeout)
	capture, err := evidence.Start(startCtx, repo.Root(), runID, bundle.Dir(), evidence.Options{
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
	if verify {
		pinCtx, cancelPin := context.WithTimeout(context.Background(), verifyPinTimeout)
		verifier, err = evidence.PinVerification(pinCtx, repo.Root(), bundle.Dir(), filepath.Join(repo.Root(), verifyConfigFile), evidence.VerificationOptions{
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
	if prompt != "" {
		if err := bundle.WritePrompt(prompt); err != nil {
			return unrecordable(bundle, stderr, runID, err)
		}
	}

	res, runErr := runner.Run(context.Background(), runner.Request{
		Command:   cmd,
		CWD:       cwd,
		Bundle:    bundle,
		Parser:    parse,
		Interrupt: interrupt,
	})
	// Handed straight back to the operating system now that the run this
	// process was holding interrupts for is over. Everything below is agentrec's
	// own work on the operator's terminal, and a Ctrl-C during it is an operator
	// who wants out: buffering it here would swallow the one signal they have.
	// The deferred Stop stays for the paths that return before the run started.
	signal.Stop(interrupt)

	// Measured now that the provider's process group has ended and its streams
	// and manifest are on disk, so what the repository shows is what the run
	// left. Attempted for every ending, including a bad one: an interrupted or
	// failed run changed the repository too, and the lock is still held, so
	// nothing else has touched it in between.
	finalizeCtx, cancelFinalize := context.WithTimeout(context.Background(), evidenceFinalizeTimeout)
	_, evidenceErr := capture.Finalize(finalizeCtx)
	cancelFinalize()
	evidenceMeasured = true

	// The checks run against the repository the run left, and after it has been
	// measured, so nothing a check writes can be read as the agent's work. They
	// run whatever the provider did: a failed or interrupted run still left work
	// behind, and whether it holds up is the question the checks answer.
	var (
		verification      evidence.VerificationResult
		verifyErr         error
		verifyCloseErr    error
		verifyInterrupted bool
	)
	if verifier != nil {
		// Its own handler, installed for the checks alone: an operator's Ctrl-C
		// during a verification is a verification they want stopped, and the
		// cancellation is what takes the check's process group down with it. A
		// SIGTERM says the same thing, from a parent rather than from a terminal.
		verifyCtx, stopVerify := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		verification, verifyErr = verifier.Run(verifyCtx)
		verifyInterrupted = verifyCtx.Err() != nil
		stopVerify()
		verifyCloseErr = verifier.Close()
		verifierClosed = true
	}

	// The report is read back from the finalized bundle rather than rendered
	// from what this process still holds, so what the operator sees is what was
	// persisted. It is attempted for every ending, including a bad one: partial
	// evidence is what an interrupted or failed run has to show.
	renderErr := renderRun(stdout, root, runID)
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
	if verifyInterrupted {
		return exitInterrupted
	}
	// A run whose repository evidence is missing is not a run to report as the
	// provider's own ending: what the operator has been shown is incomplete, and
	// the exit code says so. A verification that did not pass is the recorder's
	// own finding about the work, and it fails the command whatever the provider
	// reported — while a verification that passed leaves that reporting alone.
	if evidenceErr != nil || renderErr != nil || verifyErr != nil || verifyCloseErr != nil {
		return exitFailure
	}
	if verifier != nil && verification.Status != evidence.VerificationPassed {
		return exitFailure
	}
	return traceExit(res, runErr, stderr, runID)
}

// unrecordable ends a run that could not be recorded before its provider was
// ever launched. The bundle is finalized rather than left open, so the run
// directory describes a run that stopped instead of one still going.
func unrecordable(bundle *storage.Bundle, stderr io.Writer, runID string, cause error) int {
	fmt.Fprintf(stderr, "cli: run %s: %v\n", runID, cause)
	if err := bundle.Finalize(storage.Finalization{
		EndedAt:    time.Now(),
		ExitReason: runner.ReasonStorageError,
	}); err != nil {
		fmt.Fprintf(stderr, "cli: run %s: %v\n", runID, err)
	}
	return exitFailure
}

// traceExit reports the run to the shell. How the run was ended comes first: an
// interrupted run is the operator's own ending, whatever else the supervisor
// reported on the way out — but what else went wrong is still said, because it
// is why the evidence the operator is looking at may be short.
func traceExit(res runner.Result, runErr error, stderr io.Writer, runID string) int {
	switch {
	case res.ExitReason == runner.ReasonInterrupted:
		if runErr != nil {
			fmt.Fprintf(stderr, "cli: run %s: %v\n", runID, runErr)
		}
		return exitInterrupted
	case runErr != nil:
		fmt.Fprintf(stderr, "cli: run %s: %v\n", runID, runErr)
		return exitFailure
	case res.ExitReason == runner.ReasonCompleted:
		return 0
	case res.ExitReason == runner.ReasonNonzero:
		if res.ExitCode != nil && *res.ExitCode >= 1 && *res.ExitCode <= maxProviderExitCode {
			return *res.ExitCode
		}
		return exitFailure
	}
	fmt.Fprintf(stderr, "cli: run %s ended: %s\n", runID, res.ExitReason)
	return exitFailure
}

// renderRun writes the recorded run into the bundle and then prints it,
// announcing the run ID first so the operator can ask for it again later. The
// bundle is read once and rendered twice from what it said, so the Markdown
// filed under the run and the timeline on the terminal describe the same
// reading of the same evidence.
//
// The report is written first, and a run whose report could not be written is
// reported as a failure rather than shown: the terminal keeps what it was given
// only until the next command, while the bundle is what is left of a run once
// the repository it was recorded against has moved on.
func renderRun(stdout io.Writer, root, runID string) error {
	rep, err := readRun(root, runID)
	if err != nil {
		return err
	}
	dir, err := runDir(root, runID)
	if err != nil {
		return err
	}
	if err := installReport(dir, rep); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "Run ID: %s\n\n", runID); err != nil {
		return err
	}
	return report.RenderTerminal(stdout, rep)
}

// installReport writes the rendered report into the run directory. The run is
// opened as a root that cannot be escaped, the content is written to a
// temporary file of this command's own and synced, and the report is installed
// by linking that file into place: a link never replaces what is already there,
// so a report — or a symlink pointing anywhere at all — standing at that name
// is refused rather than overwritten or written through.
func installReport(dir string, rep report.Report) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("cli: write %s: %w", reportFile, err)
	}
	defer root.Close()

	tmp := reportFile + ".tmp"
	// O_EXCL, so a symlink or a leftover file at this name is refused rather
	// than written through.
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, reportMode)
	if err != nil {
		return fmt.Errorf("cli: create %s: %w", tmp, err)
	}
	defer root.Remove(tmp)
	// Set again after opening, because the umask masks the mode passed to open.
	if err := f.Chmod(reportMode); err != nil {
		f.Close()
		return fmt.Errorf("cli: restrict %s: %w", reportFile, err)
	}
	limited := &limitWriter{w: f, limit: maxReportBytes}
	if err := report.RenderMarkdown(limited, rep); err != nil {
		f.Close()
		return fmt.Errorf("cli: render %s: %w", reportFile, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("cli: sync %s: %w", reportFile, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("cli: close %s: %w", reportFile, err)
	}
	if err := root.Link(tmp, reportFile); err != nil {
		return fmt.Errorf("cli: install %s: %w", reportFile, err)
	}
	if err := root.Remove(tmp); err != nil {
		return fmt.Errorf("cli: remove the temporary %s: %w", reportFile, err)
	}
	// The directory entry is persisted too, so that a report that was synced is
	// also found again.
	d, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("cli: open the run directory: %w", err)
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("cli: sync the run directory: %w", err)
	}
	return nil
}

// limitWriter bounds the persisted report while streaming it directly to the
// synced temporary file. It never buffers report content in memory.
type limitWriter struct {
	w     io.Writer
	limit int64
	wrote int64
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.limit-w.wrote {
		return 0, fmt.Errorf("cli: the rendered report is larger than %d bytes", w.limit)
	}
	n, err := w.w.Write(p)
	w.wrote += int64(n)
	return n, err
}

// newRunID names a run: the instant it started, so runs sort in the order they
// happened, and four random bytes, so two runs started in the same nanosecond
// cannot claim the same directory. The result is one path component.
func newRunID() (string, error) {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("cli: generate run id: %w", err)
	}
	return time.Now().UTC().Format(runIDTimeLayout) + "-" + hex.EncodeToString(suffix[:]), nil
}

// claudeParser and codexParser adapt each provider's parser to the one shape
// the runner streams into.
func claudeParser(r io.Reader) (runner.ParseResult, error) {
	out, err := claude.Parse(r)
	return runner.ParseResult{Actions: out.Actions, WarningCount: out.WarningCount}, err
}

func codexParser(r io.Reader) (runner.ParseResult, error) {
	out, err := codex.Parse(r)
	return runner.ParseResult{Actions: out.Actions, WarningCount: out.WarningCount}, err
}

// claudePrompt is what the operator asked Claude Code to do: the positional
// argument immediately after -p/--print. Anything more elaborate — a prompt on
// stdin, a prompt spelled across several arguments — is left unrecorded rather
// than guessed at, because a wrong prompt is worse evidence than none.
func claudePrompt(args []string) string {
	for i, arg := range args {
		if arg != "-p" && arg != "--print" {
			continue
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			return args[i+1]
		}
		return ""
	}
	return ""
}

// codexValueOptions are the Codex options whose value is the argument after
// them. An argument in that position is that option's value, not a prompt, and
// telling them apart is the whole reason this list exists.
var codexValueOptions = []string{
	"-m", "--model",
	"-s", "--sandbox",
	"-c", "--config",
	"--profile",
	"--color",
	"--cd",
	"--add-dir",
	"--output-schema",
	"-o", "--output-last-message",
}

// codexPrompt is what the operator asked Codex to do: the final argument of
// `codex exec`, which is where Codex reads its prompt from — but only when that
// argument is a prompt and nothing else. An option's value, or a flag, is left
// unrecorded rather than guessed at, because a wrong prompt is worse evidence
// than none.
func codexPrompt(args []string) string {
	if len(args) < 2 {
		return ""
	}
	last, before := args[len(args)-1], args[len(args)-2]
	if strings.HasPrefix(last, "-") && before != traceDelimiter {
		return ""
	}
	if slices.Contains(codexValueOptions, before) {
		return ""
	}
	return last
}
