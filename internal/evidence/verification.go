package evidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Artifacts of one verification, under the run's own directory.
const (
	verifyDirName    = "verification"
	verifyResultFile = "results.json"
)

// VerificationAttribution is what a verification result does and does not
// mean: these checks were run by this recorder after the provider stopped, and
// what they report is the repository as the run left it — not the agent's own
// claim about its work.
const VerificationAttribution = "verification_observed"

// Statuses a verification and its checks end in. They are recorded rather than
// inferred, so that a run whose checks never executed cannot be read as one
// whose checks passed.
const (
	statusPassed  = "passed"
	statusFailed  = "failed"
	statusTimeout = "timeout"
	statusError   = "error"
	// statusTainted is the configuration having changed under the run: what
	// would have been executed is no longer what was reviewed and pinned, so
	// nothing was executed at all.
	statusTainted = "tainted"
)

// VerificationPassed is the one status a caller acts on differently: every
// pinned check ran and every one of them passed.
const VerificationPassed = statusPassed

const (
	reasonConfigChanged  = "config_changed"
	reasonSnapshotFailed = "snapshot_failed"
	reasonCancelled      = "cancelled"
	// reasonNotUTF8 stands in for output that cannot be carried as text. The
	// bytes are dropped rather than mangled into U+FFFD, which would read as
	// the check's output while differing from it.
	reasonNotUTF8 = "not_utf8"
)

// warnMutatedRepository reports that the repository was not the same after the
// checks as before them. The checks are meant to observe the run's work, and
// one that changes the work is reported for what it did — never undone.
const warnMutatedRepository = "verification_mutated_repository"

// Bounds on what a verification may read and hold. A configuration or a stream
// of output beyond these is already past anything a reviewable run produced.
const (
	maxConfigBytes        int64 = 1 << 20
	defaultMaxOutputBytes int64 = 64 << 10
	maxCheckTimeout             = time.Hour
)

// verifyWaitDelay bounds how long a killed check's pipes are waited on, so a
// process that ignored its ending cannot hold the run open indefinitely.
const verifyWaitDelay = 5 * time.Second

// verifyConfigVersion is the only schema this package reads. A configuration
// written for another one is refused rather than interpreted as this one.
const verifyConfigVersion = 1

// yamlTags are the tags a value in this configuration may carry. Anything else
// asks the parser to construct something the schema does not describe.
var yamlTags = []string{"", "!!map", "!!seq", "!!str", "!!int", "!!bool", "!!null", "!!float"}

// VerificationOptions bound what a verification holds and how it sanitizes
// what it writes. A zero value is the default policy, and a nil Sanitize keeps
// text as it was produced.
type VerificationOptions struct {
	Sanitize       func(string) (string, error)
	MaxOutputBytes int64
	// DirName is the directory under the run that holds the result; the
	// run-end verification uses the default, a later one its own.
	DirName string
	// Attribution names the evidence layer the result is filed under.
	Attribution string
}

func (o VerificationOptions) withDefaults() VerificationOptions {
	if o.MaxOutputBytes <= 0 {
		o.MaxOutputBytes = defaultMaxOutputBytes
	}
	if o.DirName == "" {
		o.DirName = verifyDirName
	}
	if o.Attribution == "" {
		o.Attribution = VerificationAttribution
	}
	if o.Sanitize == nil {
		o.Sanitize = func(s string) (string, error) { return s, nil }
	}
	return o
}

// VerificationCheck is one check, as it was pinned and — once it has run — as
// it ended. The same document carries both, so that what a reader finds while
// a run is in flight is the command that is going to be executed, under the
// name the verdict will be filed under.
type VerificationCheck struct {
	Name    string   `json:"name"`
	Command []string `json:"command"`
	Timeout string   `json:"timeout"`

	Status     string    `json:"status,omitempty"`
	ExitCode   *int      `json:"exitCode,omitempty"`
	Signal     string    `json:"signal,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	EndedAt    time.Time `json:"endedAt,omitzero"`
	DurationMS int64     `json:"durationMs,omitempty"`

	Stdout          string `json:"stdout,omitempty"`
	StdoutTruncated bool   `json:"stdoutTruncated,omitempty"`
	StdoutReason    string `json:"stdoutReason,omitempty"`
	Stderr          string `json:"stderr,omitempty"`
	StderrTruncated bool   `json:"stderrTruncated,omitempty"`
	StderrReason    string `json:"stderrReason,omitempty"`
}

// VerificationWarning is something observed about the run that is not a check
// result: the checks say whether the work holds up, a warning says something
// about the conditions they said it under.
type VerificationWarning struct {
	Code  string   `json:"code"`
	Paths []string `json:"paths,omitempty"`
}

// VerificationResult is the status document, in memory and on disk alike. It
// is written pending before the provider starts and replaced once, so what a
// reader finds distinguishes a verification that never ran from one that ran
// and failed.
type VerificationResult struct {
	Status       string                `json:"status"`
	Reason       string                `json:"reason,omitempty"`
	Attribution  string                `json:"attribution"`
	Config       string                `json:"config"`
	ConfigSHA256 string                `json:"configSha256"`
	Checks       []VerificationCheck   `json:"checks"`
	Warnings     []VerificationWarning `json:"warnings,omitempty"`
}

// pinnedCheck is one check as this process will run it: the argv and timeout
// held in memory, copied out of the configuration and never read again, beside
// the sanitized form that is the only one written down.
type pinnedCheck struct {
	argv    []string
	timeout time.Duration
	safe    VerificationCheck
}

// PinnedVerification is one run's hold on a verification configuration. It is
// created before the provider starts, and what it will execute is fixed at
// that moment: a configuration the agent rewrites during the run is a
// different one, and this package refuses it rather than running it.
type PinnedVerification struct {
	repoRoot   string
	dir        string
	configPath string
	configRel  string
	configSum  string
	checks     []pinnedCheck
	opts       VerificationOptions

	// root is the verification directory held open for the length of the run,
	// so that a directory replaced or moved while the provider works cannot
	// decide where the evidence lands.
	root *os.Root

	// resultInfo identifies the status document as this verification wrote it,
	// so the one file this package replaces is only ever replaced while it is
	// still the file it installed. The open handle keeps its inode allocated, so
	// a replacement cannot reuse the same identity after deleting it.
	resultInfo os.FileInfo
	resultHold *os.File

	ran      bool
	closed   bool
	closeErr error
}

// Dir is the directory holding this verification's artifacts.
func (p *PinnedVerification) Dir() string { return p.dir }

// PinVerification reads and fixes the verification configuration before the
// provider is allowed to run. Everything it will execute — the argv and the
// timeout of every check — is copied into memory here, and the bytes it was
// read from are hashed, so that the run can tell afterwards whether it is
// still about to execute what an operator reviewed.
func PinVerification(ctx context.Context, repoRoot, runDir, configPath string, opts VerificationOptions) (*PinnedVerification, error) {
	if err := existingDir("repository root", repoRoot); err != nil {
		return nil, err
	}
	if err := existingDir("run directory", runDir); err != nil {
		return nil, err
	}
	// The snapshots below are taken with Git and are scoped to the directory
	// they are taken in: a subdirectory would measure part of the work while
	// still calling it the run's.
	if err := checkToplevel(ctx, repoRoot); err != nil {
		return nil, err
	}

	rel, err := verifyConfigName(repoRoot, configPath)
	if err != nil {
		return nil, err
	}
	raw, err := readVerifyConfig(configPath)
	if err != nil {
		return nil, err
	}
	entries, err := parseVerifyConfig(raw)
	if err != nil {
		return nil, err
	}

	opts = opts.withDefaults()
	if opts.DirName != filepath.Base(opts.DirName) || opts.DirName == "." || opts.DirName == ".." {
		return nil, fmt.Errorf("evidence: %s is not a directory name", strconv.Quote(opts.DirName))
	}
	p := &PinnedVerification{
		repoRoot:   repoRoot,
		dir:        filepath.Join(runDir, opts.DirName),
		configPath: configPath,
		configSum:  hashOf(raw),
		opts:       opts,
	}
	if p.configRel, err = p.opts.Sanitize(rel); err != nil {
		return nil, fmt.Errorf("evidence: sanitize the verification config path: %w", err)
	}
	if p.checks, err = p.pinChecks(entries); err != nil {
		return nil, err
	}

	// Mkdir, not MkdirAll: a directory already there belongs to something else,
	// and a symlink there would put this run's evidence wherever it points.
	if err := os.Mkdir(p.dir, dirMode); err != nil {
		return nil, fmt.Errorf("evidence: create %s: %w", strconv.Quote(p.dir), err)
	}
	// Set again after creation, because the umask masks the mode passed to
	// Mkdir and is not this package's to trust.
	if err := os.Chmod(p.dir, dirMode); err != nil {
		return nil, p.abort(fmt.Errorf("evidence: restrict %s: %w", strconv.Quote(p.dir), err))
	}
	root, err := os.OpenRoot(p.dir)
	if err != nil {
		return nil, p.abort(fmt.Errorf("evidence: hold %s open: %w", strconv.Quote(p.dir), err))
	}
	p.root = root

	if err := p.pend(); err != nil {
		return nil, p.abort(err)
	}
	return p, nil
}

// pinChecks takes the run's own copy of what it will execute. The argv is
// cloned rather than referenced, so that nothing the parser still holds can be
// what the process is finally started with.
func (p *PinnedVerification) pinChecks(entries []verifyEntry) ([]pinnedCheck, error) {
	checks := make([]pinnedCheck, 0, len(entries))
	for _, e := range entries {
		safeName, err := p.opts.Sanitize(e.Name)
		if err != nil {
			return nil, fmt.Errorf("evidence: sanitize a verification check name: %w", err)
		}
		safeArgv := make([]string, 0, len(e.Command))
		for _, arg := range e.Command {
			safe, err := p.opts.Sanitize(arg)
			if err != nil {
				return nil, fmt.Errorf("evidence: sanitize a verification command: %w", err)
			}
			safeArgv = append(safeArgv, safe)
		}
		timeout, err := time.ParseDuration(e.Timeout)
		if err != nil {
			return nil, fmt.Errorf("evidence: verification check %s has no readable timeout: %w", quote(safeName), err)
		}
		checks = append(checks, pinnedCheck{
			argv:    slices.Clone(e.Command),
			timeout: timeout,
			safe: VerificationCheck{
				Name:    safeName,
				Command: safeArgv,
				Timeout: timeout.String(),
			},
		})
	}
	return checks, nil
}

// pend installs the status document that says this verification has begun and
// has not yet reached a verdict, and remembers the file it installed so that
// the one document this package replaces is only ever replaced when it is
// still that file.
func (p *PinnedVerification) pend() error {
	doc := p.document(statusPending, "", p.pinnedChecks(), nil)
	if err := p.write(doc); err != nil {
		return err
	}
	info, err := p.root.Lstat(verifyResultFile)
	if err != nil {
		return fmt.Errorf("evidence: inspect %s: %w", verifyResultFile, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("evidence: %s is %s, want the regular file this verification just wrote", verifyResultFile, info.Mode().Type())
	}
	hold, err := p.root.Open(verifyResultFile)
	if err != nil {
		return fmt.Errorf("evidence: hold %s: %w", verifyResultFile, err)
	}
	heldInfo, err := hold.Stat()
	if err != nil {
		hold.Close()
		return fmt.Errorf("evidence: inspect held %s: %w", verifyResultFile, err)
	}
	if !heldInfo.Mode().IsRegular() || !os.SameFile(info, heldInfo) {
		hold.Close()
		return fmt.Errorf("evidence: %s changed before it could be held", verifyResultFile)
	}
	p.resultInfo = heldInfo
	p.resultHold = hold
	return nil
}

// abort undoes what a pin that cannot finish has already put in place: the
// directory is this package's own and holds nothing but a document it wrote a
// moment ago.
func (p *PinnedVerification) abort(err error) error {
	if cerr := p.Close(); cerr != nil {
		err = errors.Join(err, cerr)
	}
	os.Remove(filepath.Join(p.dir, verifyResultFile))
	os.Remove(p.dir)
	return err
}

func (p *PinnedVerification) pinnedChecks() []VerificationCheck {
	checks := make([]VerificationCheck, 0, len(p.checks))
	for _, c := range p.checks {
		checks = append(checks, c.safe)
	}
	return checks
}

func (p *PinnedVerification) document(status, reason string, checks []VerificationCheck, warnings []VerificationWarning) VerificationResult {
	return VerificationResult{
		Status:       status,
		Reason:       reason,
		Attribution:  p.opts.Attribution,
		Config:       p.configRel,
		ConfigSHA256: p.configSum,
		Checks:       checks,
		Warnings:     warnings,
	}
}

// Run executes the pinned checks, after the provider has stopped, and answers
// the pending status document with what they found. The configuration is
// checked first and the repository is measured either side, so that a
// verification that was quietly rewritten, or one that changed the work it was
// judging, says so.
func (p *PinnedVerification) Run(ctx context.Context) (VerificationResult, error) {
	switch {
	case p.ran:
		return VerificationResult{}, errors.New("evidence: verification already run")
	case p.closed:
		return VerificationResult{}, errors.New("evidence: verification already closed")
	}
	p.ran = true

	res, err := p.run(ctx)
	if werr := p.write(res); werr != nil {
		err = errors.Join(err, werr)
	}
	if err != nil {
		// A verdict is only ever returned when it was both reached and
		// recorded: half of either is not evidence that the checks passed.
		return VerificationResult{}, err
	}
	return res, nil
}

func (p *PinnedVerification) run(ctx context.Context) (VerificationResult, error) {
	if err := ctx.Err(); err != nil {
		return p.document(statusError, reasonCancelled, p.pinnedChecks(), nil), fmt.Errorf("evidence: run verification: %w", err)
	}

	// The configuration as it is now, against the one that was pinned. A
	// rewritten, removed or replaced configuration is not the reviewed one, so
	// nothing from it is executed and the run says why.
	raw, err := readVerifyConfig(p.configPath)
	if err != nil || hashOf(raw) != p.configSum {
		return p.document(statusTainted, reasonConfigChanged, p.pinnedChecks(), nil), nil
	}

	before, err := p.snapshot(ctx)
	if err != nil {
		// Nothing runs against a repository this package could not measure:
		// a check's effect on the work is the thing it must be able to report.
		return p.document(statusError, reasonSnapshotFailed, p.pinnedChecks(), nil), err
	}

	checks := make([]VerificationCheck, 0, len(p.checks))
	var runErr error
	for _, pc := range p.checks {
		out, err := p.execute(ctx, pc)
		if err != nil {
			return p.document(statusError, "", checks, nil), err
		}
		checks = append(checks, out)
		// An ordinary failure is a result; a cancelled run is the operator
		// having stopped asking, and the remaining checks are not started.
		if cerr := ctx.Err(); cerr != nil {
			runErr = fmt.Errorf("evidence: run verification: %w", cerr)
			break
		}
	}
	if runErr != nil {
		return p.document(statusError, reasonCancelled, checks, nil), runErr
	}

	after, err := p.snapshot(ctx)
	if err != nil {
		return p.document(statusError, reasonSnapshotFailed, checks, nil), err
	}

	status := statusPassed
	for _, c := range checks {
		if c.Status != statusPassed {
			status = statusFailed
			break
		}
	}

	warnings, err := p.mutationWarning(before, after)
	if err != nil {
		return p.document(statusError, "", checks, nil), err
	}
	return p.document(status, "", checks, warnings), nil
}

// mutationWarning reports the repository having changed while the checks ran.
// The checks' own results are left as they are: what a check said about the
// work is its answer, and that it also changed the work is a separate thing to
// know about the run.
func (p *PinnedVerification) mutationWarning(before, after repoSnapshot) ([]VerificationWarning, error) {
	changed := changedPaths(before, after)
	if len(changed) == 0 && before.head == after.head && before.index == after.index {
		return nil, nil
	}
	safe := make([]string, 0, len(changed))
	for _, path := range changed {
		s, err := p.opts.Sanitize(path)
		if err != nil {
			return nil, fmt.Errorf("evidence: sanitize a changed path: %w", err)
		}
		safe = append(safe, s)
	}
	slices.Sort(safe)
	return []VerificationWarning{{Code: warnMutatedRepository, Paths: safe}}, nil
}

// execute runs one pinned check and reports how it ended. Only a failure to
// carry what it produced is an error: a check that failed, timed out or could
// not be started is a result about that check, and the ones after it still run.
func (p *PinnedVerification) execute(ctx context.Context, pc pinnedCheck) (VerificationCheck, error) {
	out := pc.safe

	runCtx, cancel := context.WithTimeout(ctx, pc.timeout)
	defer cancel()

	// The argv is this package's own copy, handed to the process directly:
	// there is no shell between the two, so punctuation in an argument is an
	// argument and nothing else.
	cmd := exec.CommandContext(runCtx, pc.argv[0], pc.argv[1:]...)
	cmd.Dir = p.repoRoot
	stdout := &boundedBuffer{limit: p.opts.MaxOutputBytes}
	stderr := &boundedBuffer{limit: p.opts.MaxOutputBytes}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// Its own process group, so that what ends the check ends everything it
	// started: a test runner's children would otherwise go on writing to the
	// repository after the run had reported it measured.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			// It ended on its own between the deadline and the signal, which is
			// the outcome this wanted.
			return nil
		}
		return err
	}
	cmd.WaitDelay = verifyWaitDelay

	start := time.Now()
	err := cmd.Run()
	end := time.Now()
	out.StartedAt, out.EndedAt = start.UTC(), end.UTC()
	out.DurationMS = end.Sub(start).Milliseconds()

	var exit *exec.ExitError
	switch {
	case err == nil:
		out.Status = statusPassed
		code := 0
		out.ExitCode = &code
	case errors.As(err, &exit):
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			out.Signal = ws.Signal().String()
		} else {
			code := exit.ExitCode()
			out.ExitCode = &code
		}
		out.Status = statusFailed
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			out.Status = statusTimeout
		}
	default:
		// The process never started, so there is no exit code and no signal to
		// record. The error names the executable it could not run, which
		// nothing has sanitized, so it stays out of the document.
		out.Status = statusError
	}

	if out.Stdout, out.StdoutReason, out.StdoutTruncated, err = p.text(stdout); err != nil {
		return VerificationCheck{}, err
	}
	if out.Stderr, out.StderrReason, out.StderrTruncated, err = p.text(stderr); err != nil {
		return VerificationCheck{}, err
	}
	return out, nil
}

// text is what a check's stream may be written down as. Output that is not
// valid UTF-8 is reported as such rather than sanitized: every invalid byte
// would become U+FFFD in the JSON, and the document would then differ from
// what the check printed while still reading as it.
func (p *PinnedVerification) text(b *boundedBuffer) (string, string, bool, error) {
	raw := b.text()
	if !utf8.Valid(raw) {
		return "", reasonNotUTF8, b.truncated, nil
	}
	safe, err := p.opts.Sanitize(string(raw))
	if err != nil {
		return "", "", false, fmt.Errorf("evidence: sanitize verification output: %w", err)
	}
	truncated := b.truncated
	// The bound is a promise about what lands on disk, and sanitizing can make
	// text longer than the bytes that were read.
	if int64(len(safe)) > p.opts.MaxOutputBytes {
		safe, truncated = truncateText(safe, p.opts.MaxOutputBytes), true
	}
	return safe, "", truncated, nil
}

// write installs the status document. The pending one is written through the
// ordinary path; every one after it replaces what pend installed, and only
// while that is still the file this verification wrote.
func (p *PinnedVerification) write(doc VerificationResult) error {
	if p.root == nil {
		return fmt.Errorf("evidence: write %s: the verification directory is not open", verifyResultFile)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("evidence: encode %s: %w", verifyResultFile, err)
	}
	data := append(raw, '\n')
	if p.resultInfo == nil {
		return writeFileAt(p.root, verifyResultFile, data)
	}
	info, err := replaceFileAt(p.root, verifyResultFile, p.resultInfo, data)
	if err != nil {
		return err
	}
	p.resultInfo = info
	return nil
}

// Close gives up the verification directory. It is safe to call more than once
// and reports the same outcome every time, so a deferred close cannot turn one
// failure into a different one.
func (p *PinnedVerification) Close() error {
	if p.closed {
		return p.closeErr
	}
	p.closed = true
	if p.resultHold != nil {
		if err := p.resultHold.Close(); err != nil {
			p.closeErr = fmt.Errorf("evidence: close held %s: %w", verifyResultFile, err)
		}
		p.resultHold = nil
	}
	if p.root != nil {
		if err := p.root.Close(); err != nil {
			p.closeErr = errors.Join(p.closeErr, fmt.Errorf("evidence: close %s: %w", strconv.Quote(p.dir), err))
		}
		p.root = nil
	}
	return p.closeErr
}

// repoSnapshot is the repository as it stood at one moment: what HEAD was,
// what the index held, and the content and mode of every file the repository
// tracks or has been left holding. It stays in memory — only the names of what
// changed are ever written down.
type repoSnapshot struct {
	head  string
	index string
	files map[string]string
}

// snapshot measures the repository. Content is compared rather than Git's own
// summary of it, because a file that was already modified before the run is
// reported as modified after it too: the summary is the same either side of a
// change the checks made.
func (p *PinnedVerification) snapshot(ctx context.Context) (repoSnapshot, error) {
	return p.snapshotWithWorkers(ctx, snapshotFingerprintWorkers)
}

func (p *PinnedVerification) snapshotWithWorkers(ctx context.Context, fingerprintWorkers int) (repoSnapshot, error) {
	snap := repoSnapshot{files: map[string]string{}}

	head, err := gitAt(ctx, p.repoRoot, maxSmallBytes, "rev-parse", "--verify", "HEAD")
	switch {
	case err != nil && ctx.Err() != nil:
		return repoSnapshot{}, fmt.Errorf("evidence: read HEAD: %w", errors.Join(err, ctx.Err()))
	case err == nil:
		snap.head = string(bytes.TrimRight(head, "\n"))
	}

	// Keep the logical entry representation for stable semantic coverage, and
	// include the raw index bytes so extensions and storage topology changes such
	// as enabling split-index cannot disappear behind identical entries.
	index, err := gitAt(ctx, p.repoRoot, maxListBytes, "ls-files", "--stage", "-v", "-z")
	if err != nil {
		return repoSnapshot{}, fmt.Errorf("evidence: read the index: %w", err)
	}
	rawIndex, err := gitIndexFingerprint(ctx, p.repoRoot)
	if err != nil {
		return repoSnapshot{}, err
	}
	index = append(index, 0)
	index = append(index, rawIndex...)
	snap.index = hashOf(index)

	tracked, err := gitAt(ctx, p.repoRoot, maxListBytes, "ls-files", "-z")
	if err != nil {
		return repoSnapshot{}, fmt.Errorf("evidence: list tracked files: %w", err)
	}
	// Ignored files are excluded: build output and caches are what the operator
	// already decided is not part of the work, and a check that writes them is
	// doing what it was asked to.
	others, err := gitAt(ctx, p.repoRoot, maxListBytes, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return repoSnapshot{}, fmt.Errorf("evidence: list untracked files: %w", err)
	}

	// Every read goes through the repository root, so a path Git listed can
	// only ever reach a file inside it.
	root, err := os.OpenRoot(p.repoRoot)
	if err != nil {
		return repoSnapshot{}, fmt.Errorf("evidence: open repository %s: %w", strconv.Quote(p.repoRoot), err)
	}
	defer root.Close()

	paths := make([]string, 0)
	seen := make(map[string]struct{})
	for _, raw := range [][]byte{tracked, others} {
		for _, name := range bytes.Split(raw, []byte{0}) {
			if len(name) == 0 {
				continue
			}
			path := string(name)
			if _, exists := seen[path]; exists {
				continue
			}
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
	}
	snap.files, err = fingerprintPaths(ctx, root, paths, fingerprintWorkers, fingerprint)
	if err != nil {
		return repoSnapshot{}, fmt.Errorf("evidence: fingerprint repository files: %w", err)
	}
	return snap, nil
}

func gitIndexFingerprint(ctx context.Context, repoRoot string) ([]byte, error) {
	rawPath, err := gitAt(ctx, repoRoot, maxSmallBytes, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return nil, fmt.Errorf("evidence: locate the index: %w", err)
	}
	path := string(bytes.TrimSpace(rawPath))
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("evidence: open the index: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("evidence: inspect the index: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("evidence: index is not a regular file")
	}
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return nil, fmt.Errorf("evidence: hash the index: %w", err)
	}
	return h.Sum(nil), nil
}

const snapshotFingerprintWorkers = 16

type fingerprintFunc func(root *os.Root, path string) string

func fingerprintPaths(ctx context.Context, root *os.Root, paths []string, workerCount int, fingerprintFn fingerprintFunc) (map[string]string, error) {
	files := make(map[string]string, len(paths))
	if len(paths) == 0 {
		return files, nil
	}
	if workerCount <= 0 {
		return nil, fmt.Errorf("invalid fingerprint worker count %d", workerCount)
	}

	workerCount = min(workerCount, len(paths))
	jobs := make(chan string)
	var workers sync.WaitGroup
	var mu sync.Mutex
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for path := range jobs {
				if ctx.Err() != nil {
					continue
				}
				value := fingerprintFn(root, path)
				mu.Lock()
				files[path] = value
				mu.Unlock()
			}
		}()
	}

sendPaths:
	for _, path := range paths {
		select {
		case jobs <- path:
		case <-ctx.Done():
			break sendPaths
		}
	}
	close(jobs)
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return files, nil
}

// fingerprint identifies one path by what it is and what it holds. A symlink
// is described by where it points and never followed: reading through it would
// measure a file the repository does not hold.
func fingerprint(root *os.Root, path string) string {
	info, err := root.Lstat(path)
	if err != nil {
		return "missing"
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := root.Readlink(path)
		if err != nil {
			return "unreadable"
		}
		return "symlink:" + hashOf([]byte(target))
	case !info.Mode().IsRegular():
		return "other:" + info.Mode().Type().String()
	}
	// O_NONBLOCK, because the regular file seen a moment ago can be a FIFO by
	// now, and O_NOFOLLOW because it can be a symlink by now: neither is a file
	// this snapshot may block on or read through.
	f, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "unreadable"
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "unreadable"
	}
	return fmt.Sprintf("file:%o:%s", info.Mode().Perm(), hex.EncodeToString(sum.Sum(nil)))
}

// changedPaths names every path the two snapshots disagree about.
func changedPaths(before, after repoSnapshot) []string {
	changed := []string{}
	for path, was := range before.files {
		if now, ok := after.files[path]; !ok || now != was {
			changed = append(changed, path)
		}
	}
	for path := range after.files {
		if _, ok := before.files[path]; !ok {
			changed = append(changed, path)
		}
	}
	slices.Sort(changed)
	return changed
}

// verifyEntry is one check as the configuration describes it.
type verifyEntry struct {
	Name    string   `yaml:"name"`
	Command []string `yaml:"command"`
	Timeout string   `yaml:"timeout"`
}

type verifyConfigDoc struct {
	Version *int          `yaml:"version"`
	Verify  []verifyEntry `yaml:"verify"`
}

// verifyConfigName accepts the configuration path only when it names a file
// the repository itself holds, directly at its root. The path reaches this
// package from a flag, and a link or a traversal would let a run be verified
// by a configuration that is not the repository's own.
func verifyConfigName(repoRoot, configPath string) (string, error) {
	if !filepath.IsAbs(configPath) {
		return "", fmt.Errorf("evidence: verification config %s is not an absolute path", quote(configPath))
	}
	if filepath.Clean(configPath) != configPath {
		return "", fmt.Errorf("evidence: verification config %s is not a clean path", quote(configPath))
	}
	info, err := os.Lstat(configPath)
	if err != nil {
		return "", fmt.Errorf("evidence: verification config %s: %w", quote(configPath), err)
	}
	// Lstat, so a symlink is refused rather than followed: what it points at is
	// not the file the repository holds under this name.
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("evidence: verification config %s is %s, want a regular file", quote(configPath), info.Mode().Type())
	}
	parent, err := realPath(filepath.Dir(configPath))
	if err != nil {
		return "", err
	}
	root, err := realPath(repoRoot)
	if err != nil {
		return "", err
	}
	if parent != root {
		return "", fmt.Errorf("evidence: verification config %s is not at the repository root %s", quote(configPath), quote(root))
	}
	return filepath.Base(configPath), nil
}

// readVerifyConfig reads the configuration as bytes, refusing one larger than
// the bound and one reached through a link.
func readVerifyConfig(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("evidence: open verification config %s: %w", quote(path), err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("evidence: inspect verification config %s: %w", quote(path), err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("evidence: verification config %s is %s, want a regular file", quote(path), info.Mode().Type())
	}
	// One byte past the bound is enough to know the bound was passed.
	raw, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("evidence: read verification config %s: %w", quote(path), err)
	}
	if int64(len(raw)) > maxConfigBytes {
		return nil, fmt.Errorf("evidence: verification config %s %w (%d bytes)", quote(path), errOutputTooLarge, maxConfigBytes)
	}
	return raw, nil
}

// parseVerifyConfig reads exactly the schema this package documents and
// nothing else. Everything a YAML parser can be asked to do beyond describing
// this schema — an alias, a tag naming another type, a second document, a field
// this version does not know — is refused rather than interpreted, because a
// configuration that means something other than it appears to is one an
// operator reviewed without seeing.
func parseVerifyConfig(raw []byte) ([]verifyEntry, error) {
	// Judged before it is ever a string: YAML is UTF-8, and bytes that are not
	// would reach the sanitizer and the JSON encoder as U+FFFD.
	if !utf8.Valid(raw) {
		return nil, errors.New("evidence: the verification config is not valid UTF-8")
	}

	var node yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&node); err != nil {
		return nil, fmt.Errorf("evidence: read the verification config: %w", err)
	}
	if err := checkYAMLNode(&node); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(yaml.Node)); !errors.Is(err, io.EOF) {
		return nil, errors.New("evidence: the verification config holds more than one document")
	}

	var doc verifyConfigDoc
	strict := yaml.NewDecoder(bytes.NewReader(raw))
	strict.KnownFields(true)
	if err := strict.Decode(&doc); err != nil {
		return nil, fmt.Errorf("evidence: read the verification config: %w", err)
	}
	if doc.Version == nil || *doc.Version != verifyConfigVersion {
		return nil, fmt.Errorf("evidence: the verification config is not version %d", verifyConfigVersion)
	}
	if len(doc.Verify) == 0 {
		return nil, errors.New("evidence: the verification config has no checks")
	}

	seen := map[string]bool{}
	for _, e := range doc.Verify {
		switch {
		case e.Name == "":
			return nil, errors.New("evidence: a verification check has no name")
		case seen[e.Name]:
			return nil, fmt.Errorf("evidence: two verification checks are named %s", quote(e.Name))
		case len(e.Command) == 0:
			return nil, fmt.Errorf("evidence: verification check %s has no command", quote(e.Name))
		case e.Command[0] == "":
			return nil, fmt.Errorf("evidence: verification check %s names no program to run", quote(e.Name))
		}
		seen[e.Name] = true

		timeout, err := time.ParseDuration(e.Timeout)
		if err != nil {
			return nil, fmt.Errorf("evidence: verification check %s has no readable timeout: %w", quote(e.Name), err)
		}
		if timeout <= 0 || timeout > maxCheckTimeout {
			return nil, fmt.Errorf("evidence: verification check %s has a timeout of %s, want more than zero and at most %s", quote(e.Name), timeout, maxCheckTimeout)
		}
	}
	return doc.Verify, nil
}

// checkYAMLNode refuses everything in a document that is not a plain value of
// the kinds this schema is written in.
func checkYAMLNode(n *yaml.Node) error {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.AliasNode {
		return errors.New("evidence: the verification config uses a YAML alias")
	}
	if !slices.Contains(yamlTags, n.Tag) {
		return fmt.Errorf("evidence: the verification config uses the YAML tag %s", quote(n.Tag))
	}
	for _, c := range n.Content {
		if err := checkYAMLNode(c); err != nil {
			return err
		}
	}
	return nil
}

// boundedBuffer keeps the first limit bytes written to it and records that
// there were more. It never fails a write, so a process whose output passed the
// bound goes on being drained rather than blocking on a full pipe.
type boundedBuffer struct {
	buf       []byte
	limit     int64
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	room := b.limit - int64(len(b.buf))
	if room >= int64(len(p)) {
		b.buf = append(b.buf, p...)
		return len(p), nil
	}
	if room > 0 {
		b.buf = append(b.buf, p[:room]...)
	}
	if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

// text is what was kept, ending on a rune boundary. A bound falls wherever it
// falls, and a rune cut in half is not text the check produced.
func (b *boundedBuffer) text() []byte {
	if !b.truncated {
		return b.buf
	}
	return trimPartialRune(b.buf)
}

// truncateText cuts sanitized text to the bound, on a rune boundary.
func truncateText(s string, limit int64) string {
	if int64(len(s)) <= limit {
		return s
	}
	return string(trimPartialRune([]byte(s[:limit])))
}

func trimPartialRune(b []byte) []byte {
	for range utf8.UTFMax - 1 {
		if len(b) == 0 {
			return b
		}
		if r, size := utf8.DecodeLastRune(b); r != utf8.RuneError || size > 1 {
			return b
		}
		b = b[:len(b)-1]
	}
	return b
}

// replaceFileAt answers a document that is already there, and is the only
// write in this package that replaces anything. What it replaces is proved
// first and again as late as it can be: the name must still lead to the very
// file prev describes, so a document something else deleted, replaced or
// turned into a symlink is refused rather than written through — the file at
// the other end of a planted link is not this run's to overwrite.
func replaceFileAt(root *os.Root, name string, prev os.FileInfo, data []byte) (os.FileInfo, error) {
	if !filepath.IsLocal(name) {
		return nil, fmt.Errorf("evidence: %s is not a name inside the run directory", strconv.Quote(name))
	}
	same := func() error {
		info, err := root.Lstat(name)
		if err != nil {
			return fmt.Errorf("evidence: inspect %s: %w", name, err)
		}
		if !info.Mode().IsRegular() || !os.SameFile(info, prev) {
			return fmt.Errorf("evidence: %s is %s, want the file this run wrote", name, info.Mode().Type())
		}
		return nil
	}
	if err := same(); err != nil {
		return nil, err
	}

	// A name of this package's own that nothing else writes, created
	// exclusively, so a leftover or a symlink at it is refused rather than
	// written through.
	tmp := name + ".final"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		return nil, fmt.Errorf("evidence: create %s: %w", tmp, err)
	}
	defer root.Remove(tmp)
	// Set again after opening, because the umask masks the mode passed to open.
	if err := f.Chmod(fileMode); err != nil {
		f.Close()
		return nil, fmt.Errorf("evidence: restrict %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return nil, fmt.Errorf("evidence: write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, fmt.Errorf("evidence: sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("evidence: close %s: %w", tmp, err)
	}

	// Asked again with nothing left to do in between: a rename replaces
	// whatever is at the destination, so the destination is proved to be this
	// run's own file as late as it can be.
	if err := same(); err != nil {
		return nil, err
	}
	if err := root.Rename(tmp, name); err != nil {
		return nil, fmt.Errorf("evidence: install %s: %w", name, err)
	}
	if err := syncDirAt(root, filepath.Dir(name)); err != nil {
		return nil, err
	}
	// The document on disk is a different file now, and it is the one any later
	// replacement would have to be replacing.
	info, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("evidence: inspect %s: %w", name, err)
	}
	return info, nil
}
