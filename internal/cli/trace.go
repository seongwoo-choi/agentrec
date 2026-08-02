package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/evidence"
	"github.com/seongwoo-choi/agentrec/internal/lock"
	"github.com/seongwoo-choi/agentrec/internal/provider"
	"github.com/seongwoo-choi/agentrec/internal/provider/claude"
	"github.com/seongwoo-choi/agentrec/internal/provider/codex"
	"github.com/seongwoo-choi/agentrec/internal/report"
	"github.com/seongwoo-choi/agentrec/internal/runner"
)

// traceDelimiter separates agentrec's own arguments from the provider's. It is
// required rather than inferred: everything after it belongs to the provider,
// including flags agentrec itself understands.
const traceDelimiter = "--"

const traceUsage = "usage: agentrec trace <claude|codex> [--verify] [--allow-unsupported-version] [--timeout <duration>] -- <provider args...>\n"

// verifyFlag asks for the repository's own checks to be run against the work
// once the provider has stopped. Options agentrec takes for itself are spelled
// exactly: anything else before the delimiter is an invocation agentrec does
// not understand rather than one it guesses at.
const verifyFlag = "--verify"

// timeoutFlag bounds the provider process itself. Omitting it preserves the
// default operator-controlled run with no deadline.
const timeoutFlag = "--timeout"

// allowUnsupportedVersionFlag records a run against a provider version outside
// the range agentrec's parser was written for, instead of refusing it. The
// refusal is still the default, because a stream this parser does not
// understand produces a timeline that quietly says less than it appears to.
// This is the operator's way of saying they know that and want the other three
// evidence layers anyway — the process result, the repository difference and
// the pinned checks do not depend on the parser at all — and the bundle is
// stamped so every later reader is told which run they are looking at.
const allowUnsupportedVersionFlag = "--allow-unsupported-version"

// verifyConfigFile is where the checks are read from, at the root of the
// repository being recorded. It is not configurable: a verification an operator
// has to be told the location of is one they cannot check was the one that ran.
const verifyConfigFile = ".agentrec.yaml"

// verifyPinTimeout bounds reading and fixing that configuration, which is one
// file and a handful of Git questions. The checks themselves are bounded by the
// configuration, each by its own timeout.
const verifyPinTimeout = 10 * time.Second

// versionTimeout bounds preparation alone — running the provider's `--version`
// — and nothing else. The provider run remains unbounded unless the operator
// supplies --timeout.
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

// traceOptions are the options agentrec takes for itself, as opposed to the
// ones it hands to the provider untouched.
type traceOptions struct {
	verify           bool
	allowUnsupported bool
	timeout          time.Duration
}

// parseTraceOptions reads the arguments between the provider and the delimiter,
// which are agentrec's own and are two things at most. An option it does not
// know, or one given twice, is refused rather than ignored: an operator who
// asked for something must not be told a run was recorded the way they asked
// for. Order carries no meaning — neither option is the other's prerequisite.
//
// It is a function of its arguments and nothing else, so what agentrec accepts
// can be established without launching an agent: a test that drove the whole
// command would depend on which provider CLIs happen to be installed and on the
// state of the repository it ran in, and would prove nothing about parsing on a
// machine where either differed.
func parseTraceOptions(args []string) (traceOptions, bool) {
	var opts traceOptions
	for i := 0; i < len(args); i++ {
		opt := args[i]
		switch {
		case opt == verifyFlag && !opts.verify:
			opts.verify = true
		case opt == allowUnsupportedVersionFlag && !opts.allowUnsupported:
			opts.allowUnsupported = true
		case opt == timeoutFlag && opts.timeout == 0 && i+1 < len(args):
			timeout, err := time.ParseDuration(args[i+1])
			if err != nil || timeout <= 0 {
				return traceOptions{}, false
			}
			opts.timeout = timeout
			i++
		default:
			return traceOptions{}, false
		}
	}
	return opts, true
}

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
	own, ok := parseTraceOptions(args[1:delimiter])
	if !ok {
		fmt.Fprint(stderr, traceUsage)
		return exitUsage
	}
	verify := own.verify
	opts := provider.Options{AllowUnsupportedVersion: own.allowUnsupported}

	var (
		cmd    provider.Command
		parse  runner.Parser
		prompt string
		err    error
	)
	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	switch name {
	case "claude":
		cmd, err = claude.PrepareCommand(ctx, providerArgs, nil, opts)
		parse, prompt = claudeParser, claudePrompt(providerArgs)
	case "codex":
		cmd, err = codex.PrepareCommand(ctx, providerArgs, nil, opts)
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
	// Said once, where the operator is standing, and recorded in the manifest for
	// everyone who reads the bundle later: a run whose event stream was read by a
	// parser that does not claim to understand it must never look like an
	// ordinary one.
	if cmd.VersionUnverified {
		fmt.Fprintf(stderr, "cli: %s %s is outside the range agentrec's parser was written for; recording anyway, and the provider-reported timeline may be incomplete\n", name, cmd.Version)
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
	// with the provider's process group still running. Without --timeout the run
	// remains operator-bounded by that same Ctrl-C.
	signals := holdCommandSignals()
	interrupt := signals.Interrupt()

	out := record(recordRequest{
		Provider:  name,
		Command:   cmd,
		Parser:    parse,
		Prompt:    prompt,
		CWD:       cwd,
		RepoRoot:  repo.Root(),
		RunsRoot:  root,
		RunID:     runID,
		Verify:    verify,
		Timeout:   own.timeout,
		Interrupt: interrupt,
		StartGate: signals.Start,
		Timeline:  stdout,
	}, stderr)
	out.Interrupted = signals.Stop() || out.Interrupted

	// A run that never reached the supervisor has left a finalized bundle saying
	// so, and the recorder has already said why.
	if !out.Recorded {
		return exitFailure
	}
	// An interrupt that arrived while the recorder was measuring, verifying or
	// filing the report is the operator's own ending, whatever the provider had
	// reported before it.
	if out.Interrupted {
		return exitInterrupted
	}
	// A run whose repository evidence is missing is not a run to report as the
	// provider's own ending: what the operator has been shown is incomplete, and
	// the exit code says so. A verification that did not pass is the recorder's
	// own finding about the work, and it fails the command whatever the provider
	// reported — while a verification that passed leaves that reporting alone.
	if out.Incomplete {
		return exitFailure
	}
	if out.Verified && out.Verification.Status != evidence.VerificationPassed {
		return exitFailure
	}
	return traceExit(out.Result, out.RunErr, stderr, runID)
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
	return runner.ParseResult{Actions: out.Actions, WarningCount: out.WarningCount, Usage: out.Usage}, err
}

func codexParser(r io.Reader) (runner.ParseResult, error) {
	out, err := codex.Parse(r)
	return runner.ParseResult{Actions: out.Actions, WarningCount: out.WarningCount, Usage: out.Usage}, err
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
