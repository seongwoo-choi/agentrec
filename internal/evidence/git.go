// Package evidence preserves what a recorded run left behind in the repository
// it ran against: the commits and worktree changes measured from the baseline
// the run started at, and the files it created that Git does not track.
//
// What this package records is what the repository looked like before and
// after, which is not the same as proof that the agent caused the difference —
// hence the attribution every result carries. The repository itself is only
// ever read: the one thing written to it is a temporary ref pinning the
// baseline commit, and that is removed again when the capture ends.
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
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// Artifacts of one capture, all under the run's git directory.
const (
	gitDirName       = "git"
	baselineFile     = "baseline.json"
	patchFile        = "tracked.patch"
	trackedStatFile  = "tracked-stat.json"
	untrackedFile    = "untracked.json"
	untrackedDirName = "untracked"
	resultFile       = "result.json"
)

// refPrefix namespaces the temporary ref, so what a capture plants in a
// repository is recognizable and cannot collide with an operator's own refs.
const refPrefix = "refs/agentrec/"

// Statuses and reasons are recorded rather than inferred by a reader: evidence
// that could not be collected says so, and says why, in the same document that
// would otherwise hold it.
const (
	statusAvailable   = "available"
	statusUnavailable = "unavailable"
	// statusPending is written before the run starts and replaced when it has
	// been measured, so evidence that was never collected — because the process
	// was killed partway, or the machine went down — reads as unfinished rather
	// than as a run that changed nothing.
	statusPending = "pending"

	reasonBaselineUnreachable = "baseline_unreachable"
	reasonPatchTooLarge       = "patch_too_large"
	reasonPatchNotUTF8        = "patch_not_utf8"
	reasonBinary              = "binary"
	reasonFileTooLarge        = "file_too_large"
	reasonStorageLimit        = "storage_limit_reached"
	reasonUnreadable          = "unreadable"
	reasonCollectionFailed    = "collection_failed"
)

// What an untracked entry's hash was taken over. A hash is only ever recorded
// alongside the basis it was taken on, because the two answer different
// questions: one identifies a body this bundle holds, the other identifies a
// file it deliberately does not.
const (
	// hashBasisSanitized covers the bytes that were stored, or would have been
	// but for a limit. Hashing the text as read would publish every secret the
	// sanitizer had just removed: a short or low-entropy secret is recovered
	// from its own SHA256 by guessing.
	hashBasisSanitized = "sanitized"
	// hashBasisRawBinary covers a binary file exactly as it was read. Its body
	// is never stored and never sanitized, so the hash is the only thing said
	// about it — and there is no redacted text for it to give back.
	hashBasisRawBinary = "raw_binary"
)

// Kinds of untracked entry. Only a regular file has a body worth hashing; the
// rest are described and left alone, and one that could not be inspected at all
// is still named so that a reader sees the gap rather than a shorter list.
const (
	kindFile        = "file"
	kindSymlink     = "symlink"
	kindOther       = "other"
	kindUnavailable = "unavailable"
)

// Attribution is what a repository difference does and does not mean, carried
// on every result so that no reader has to supply it themselves.
const Attribution = "observed during run, not causal proof"

// The evidence may hold whole file contents from a private repository, so it is
// readable only by the user who recorded it.
const (
	dirMode  os.FileMode = 0o700
	fileMode os.FileMode = 0o600
)

// Defaults bound what one run may store. A patch is one document and is either
// stored whole or not at all; untracked text is bounded twice, once per file
// and once in total, so that neither one enormous file nor very many small ones
// can fill the operator's disk.
const (
	defaultMaxPatchBytes      int64 = 64 << 20
	defaultMaxTextFileBytes   int64 = 1 << 20
	defaultMaxStoredTextBytes int64 = 10 << 20
)

// Bounds on what Git itself may hand back. A list of names or a stat summary
// this large is already beyond anything a reviewable run produced.
const (
	maxListBytes   int64 = 16 << 20
	maxSmallBytes  int64 = 64 << 10
	maxStderrBytes       = 8 << 10
)

// sniffBytes is how much of a file decides whether it is text. It is a prefix
// because the answer is needed before the whole file is in memory.
const sniffBytes = 8000

// closeTimeout bounds the cleanup that removes the temporary ref. Cleanup runs
// on this deadline rather than the run's context, because a run that ended by
// being cancelled is exactly when the ref most needs taking back out.
const closeTimeout = 10 * time.Second

// errOutputTooLarge reports that a Git command produced more than the caller
// allowed. It is a bound being reached, not a failure of the command.
var errOutputTooLarge = errors.New("output exceeds the configured limit")

// Options bound what a capture stores and how it sanitizes what it stores. A
// zero Options is the default policy, and a nil Sanitize stores text as it was
// found.
type Options struct {
	MaxPatchBytes      int64
	MaxTextFileBytes   int64
	MaxStoredTextBytes int64
	Sanitize           func(string) (string, error)
}

func (o Options) withDefaults() Options {
	if o.MaxPatchBytes <= 0 {
		o.MaxPatchBytes = defaultMaxPatchBytes
	}
	if o.MaxTextFileBytes <= 0 {
		o.MaxTextFileBytes = defaultMaxTextFileBytes
	}
	if o.MaxStoredTextBytes <= 0 {
		o.MaxStoredTextBytes = defaultMaxStoredTextBytes
	}
	if o.Sanitize == nil {
		o.Sanitize = func(s string) (string, error) { return s, nil }
	}
	return o
}

// Result summarizes what the capture observed, for a report that must show the
// difference without claiming to have caused it.
type Result struct {
	Status      string
	Reason      string
	Baseline    string
	Attribution string

	TrackedFiles  int
	Added         int
	Deleted       int
	BinaryTracked int

	UntrackedFiles  int
	StoredTextFiles int
}

// Capture is one run's hold on a baseline. It is created before the provider
// starts and finalized after the provider's process group has been recovered,
// so that what it measures is the repository as the run left it.
type Capture struct {
	repoRoot string
	dir      string
	ref      string
	baseline string
	opts     Options

	// root is the capture directory held open for the length of the capture.
	// Every artifact is installed through it under a relative name, so that a
	// directory replaced or moved during the run cannot decide where the
	// evidence lands: the descriptor still names the directory this package
	// made, wherever it has since been put.
	root *os.Root

	// resultInfo identifies the status document as this capture wrote it, so
	// that the one file this package replaces is only ever replaced when it is
	// still the file it installed and not something put there since.
	resultInfo os.FileInfo

	finalized bool
	closed    bool
	closeErr  error
}

// Baseline is the commit this capture measures from, empty when there was none
// to pin.
func (c *Capture) Baseline() string { return c.baseline }

// Dir is the directory holding this capture's artifacts.
func (c *Capture) Dir() string { return c.dir }

// Documents written into the capture directory. Every field is fixed in order
// and name so that two runs of the same shape produce byte-identical evidence.
type baselineDoc struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Commit string `json:"commit,omitempty"`
	Ref    string `json:"ref,omitempty"`
}

type trackedFile struct {
	Path      string `json:"path"`
	Additions *int   `json:"additions,omitempty"`
	Deletions *int   `json:"deletions,omitempty"`
	Binary    bool   `json:"binary,omitempty"`
}

type trackedTotals struct {
	Files     int `json:"files"`
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	Binary    int `json:"binary"`
}

type trackedStatDoc struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	// Carried in the document itself, so that the file says what it means
	// wherever it is read and whether or not it holds any evidence.
	Attribution string        `json:"attribution"`
	Baseline    string        `json:"baseline,omitempty"`
	Files       []trackedFile `json:"files"`
	Totals      trackedTotals `json:"totals"`
}

type untrackedEntry struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Mode      string `json:"mode"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256,omitempty"`
	HashBasis string `json:"hashBasis,omitempty"`
	Stored    bool   `json:"stored"`
	Reason    string `json:"reason,omitempty"`
	StoredAs  string `json:"storedAs,omitempty"`
}

type untrackedDoc struct {
	Attribution string           `json:"attribution"`
	Count       int              `json:"count"`
	Stored      int              `json:"stored"`
	Files       []untrackedEntry `json:"files"`
}

// resultDoc is the one document that says whether this capture ran at all. It
// is written pending before the provider starts and replaced once, so that
// what a reader finds on disk distinguishes a collection that never finished
// from one that finished with nothing to show and from one that failed.
type resultDoc struct {
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	Attribution string `json:"attribution"`
	Baseline    string `json:"baseline,omitempty"`

	TrackedFiles  int `json:"trackedFiles"`
	Added         int `json:"added"`
	Deleted       int `json:"deleted"`
	BinaryTracked int `json:"binaryTracked"`

	UntrackedFiles  int `json:"untrackedFiles"`
	StoredTextFiles int `json:"storedTextFiles"`
}

// Start pins the repository's current commit for the length of one run. The
// commit is pinned with a ref so that a rebase, an amend or a reset performed
// during the run cannot take the baseline away; if there is no commit to pin,
// that is recorded and the run goes ahead without one rather than against a
// guessed starting point.
func Start(ctx context.Context, repoRoot, runID, runDir string, opts Options) (*Capture, error) {
	if err := existingDir("repository root", repoRoot); err != nil {
		return nil, err
	}
	if err := existingDir("run directory", runDir); err != nil {
		return nil, err
	}
	if err := validateRunID(runID); err != nil {
		return nil, err
	}
	ref := refPrefix + runID
	if _, err := gitAt(ctx, "", maxSmallBytes, "check-ref-format", ref); err != nil {
		return nil, fmt.Errorf("evidence: run id %s does not name a Git ref: %w", strconv.Quote(runID), err)
	}
	// Everything below is measured from this directory, and Git answers every
	// question relative to it: a subdirectory would scope the diff and the
	// untracked listing to part of the work while still calling it the run's.
	if err := checkToplevel(ctx, repoRoot); err != nil {
		return nil, err
	}

	dir := filepath.Join(runDir, gitDirName)
	// Mkdir, not MkdirAll: a directory already there belongs to something else,
	// and a symlink there would put this run's evidence wherever it points.
	if err := os.Mkdir(dir, dirMode); err != nil {
		return nil, fmt.Errorf("evidence: create %s: %w", strconv.Quote(dir), err)
	}

	c := &Capture{repoRoot: repoRoot, dir: dir, opts: opts.withDefaults()}

	// Set again after creation, because the umask masks the mode passed to
	// Mkdir and is not this package's to trust.
	if err := os.Chmod(dir, dirMode); err != nil {
		return nil, c.abort(ctx, fmt.Errorf("evidence: restrict %s: %w", strconv.Quote(dir), err))
	}
	// Held from here to Close, so that every later write names this directory by
	// descriptor rather than by a path something else could take over.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, c.abort(ctx, fmt.Errorf("evidence: hold %s open: %w", strconv.Quote(dir), err))
	}
	c.root = root

	// The bodies directory is made here rather than at the first body, so that
	// what a body is written into is a directory this package created before
	// the run began and not one that appeared while it was running.
	if err := c.root.Mkdir(untrackedDirName, dirMode); err != nil {
		return nil, c.abort(ctx, fmt.Errorf("evidence: create %s: %w", strconv.Quote(untrackedDirName), err))
	}
	if err := c.root.Chmod(untrackedDirName, dirMode); err != nil {
		return nil, c.abort(ctx, fmt.Errorf("evidence: restrict %s: %w", strconv.Quote(untrackedDirName), err))
	}

	baseline, err := c.git(ctx, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// The question was never put to the repository, which says nothing
			// about whether there was a baseline to pin.
			return nil, c.abort(ctx, fmt.Errorf("evidence: read baseline: %w", errors.Join(err, ctxErr)))
		}
		// A repository with no commit yet has no baseline, and there is nothing
		// to invent in its place.
		if err := c.writeJSON(baselineFile, baselineDoc{
			Status: statusUnavailable,
			Reason: reasonBaselineUnreachable,
		}); err != nil {
			return nil, c.abort(ctx, err)
		}
		return c.pend(ctx)
	}

	// The empty old value makes this a create: a ref already at this name is a
	// collision to report, never something to move.
	if _, err := c.git(ctx, "update-ref", ref, baseline, ""); err != nil {
		return nil, c.abort(ctx, fmt.Errorf("evidence: pin baseline at %s: %w", strconv.Quote(ref), err))
	}
	c.ref, c.baseline = ref, baseline

	if err := c.writeJSON(baselineFile, baselineDoc{
		Status: statusAvailable,
		Commit: baseline,
		Ref:    ref,
	}); err != nil {
		return nil, c.abort(ctx, err)
	}
	return c.pend(ctx)
}

// pend installs the status document that says this capture has begun and has
// not yet been measured. It is written before the provider starts, so that a
// run killed partway leaves evidence that reads as unfinished rather than as a
// run that changed nothing.
//
// The file it installed is identified here and remembered, so that the one
// document this package later replaces is only ever replaced when it is still
// the file this capture wrote. The context is only for undoing a Start that
// cannot finish.
func (c *Capture) pend(ctx context.Context) (*Capture, error) {
	if err := c.writeJSON(resultFile, resultDoc{Status: statusPending, Attribution: Attribution}); err != nil {
		return nil, c.abort(ctx, err)
	}
	info, err := c.root.Lstat(resultFile)
	if err != nil {
		return nil, c.abort(ctx, fmt.Errorf("evidence: inspect %s: %w", resultFile, err))
	}
	if !info.Mode().IsRegular() {
		return nil, c.abort(ctx, fmt.Errorf("evidence: %s is %s, want the regular file this capture just wrote", resultFile, info.Mode().Type()))
	}
	c.resultInfo = info
	return c, nil
}

// abort undoes what a Start that cannot finish has already put in place. The
// ref exists only to serve a capture that is not going to happen, and the
// directories are this package's own and still empty — a directory holding a
// document that was written is left alone rather than emptied.
func (c *Capture) abort(ctx context.Context, err error) error {
	if cerr := c.Close(ctx); cerr != nil {
		err = errors.Join(err, cerr)
	}
	os.Remove(c.path(untrackedDirName))
	os.Remove(c.dir)
	return err
}

// checkToplevel accepts repoRoot only when Git agrees it is the repository's own
// top level. Both paths are resolved the same way first, so that a symlinked
// temporary directory or a differently spelled absolute path is not read as a
// different repository.
func checkToplevel(ctx context.Context, repoRoot string) error {
	out, err := gitAt(ctx, repoRoot, maxSmallBytes, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("evidence: read the repository root of %s: %w", quote(repoRoot), err)
	}
	top, err := realPath(strings.TrimRight(string(out), "\n"))
	if err != nil {
		return err
	}
	root, err := realPath(repoRoot)
	if err != nil {
		return err
	}
	if top != root {
		return fmt.Errorf("evidence: %s is not the repository root, which is %s", quote(repoRoot), quote(top))
	}
	return nil
}

func realPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("evidence: resolve %s: %w", quote(path), err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("evidence: resolve %s: %w", quote(path), err)
	}
	return real, nil
}

// Finalize measures the repository against the pinned baseline and writes the
// evidence. It is called once, after the provider's process group has ended,
// and the temporary ref is removed whatever the outcome.
func (c *Capture) Finalize(ctx context.Context) (Result, error) {
	if c.finalized {
		return Result{}, errors.New("evidence: capture already finalized")
	}
	// A closed capture has given up the directory it would write into, so it
	// says so rather than failing later on a descriptor that is gone.
	if c.closed {
		return Result{}, errors.New("evidence: capture already closed")
	}
	c.finalized = true

	res, err := c.collect(ctx)
	// The pending document is answered on every ending, before the directory is
	// given up: a collection that failed is evidence of a failed collection, not
	// of a run that was never measured.
	if rerr := c.replaceResult(finalResult(res, err)); rerr != nil {
		err = errors.Join(err, rerr)
	}
	// The ref is this capture's alone and outlives nothing: it is removed on
	// every ending, and a cleanup failure is reported alongside — never
	// instead of — what actually went wrong.
	if cerr := c.Close(ctx); cerr != nil {
		err = errors.Join(err, cerr)
	}
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

// finalResult is what the status document says once the run has been measured.
// A collection that failed says only that: the error names repository paths and
// whatever a sanitizer said, none of which anything has sanitized, so it is
// reported to the caller and never written down.
func finalResult(res Result, err error) resultDoc {
	if err != nil {
		return resultDoc{Status: statusUnavailable, Reason: reasonCollectionFailed, Attribution: Attribution}
	}
	return resultDoc{
		Status:          res.Status,
		Reason:          res.Reason,
		Attribution:     Attribution,
		Baseline:        res.Baseline,
		TrackedFiles:    res.TrackedFiles,
		Added:           res.Added,
		Deleted:         res.Deleted,
		BinaryTracked:   res.BinaryTracked,
		UntrackedFiles:  res.UntrackedFiles,
		StoredTextFiles: res.StoredTextFiles,
	}
}

// replaceResult answers the pending status document, and is the only write in
// this package that replaces anything. What it replaces is proved first: the
// name must still lead to the very file pend installed, so a document something
// else deleted, replaced or turned into a symlink is refused rather than
// written through — the file at the other end of a planted link is not this
// bundle's to overwrite.
func (c *Capture) replaceResult(doc resultDoc) error {
	if c.root == nil {
		return fmt.Errorf("evidence: write %s: the capture directory is not open", resultFile)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("evidence: encode %s: %w", resultFile, err)
	}
	if err := c.sameResult(); err != nil {
		return err
	}

	// A name of this package's own that nothing else writes, created
	// exclusively, so a leftover or a symlink at it is refused rather than
	// written through.
	const tmp = resultFile + ".final"
	f, err := c.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		return fmt.Errorf("evidence: create %s: %w", tmp, err)
	}
	defer c.root.Remove(tmp)
	// Set again after opening, because the umask masks the mode passed to open.
	if err := f.Chmod(fileMode); err != nil {
		f.Close()
		return fmt.Errorf("evidence: restrict %s: %w", tmp, err)
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("evidence: write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("evidence: sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("evidence: close %s: %w", tmp, err)
	}

	// Asked again with nothing left to do in between: a rename replaces
	// whatever is at the destination, so the destination is proved to be this
	// capture's own file as late as it can be.
	if err := c.sameResult(); err != nil {
		return err
	}
	if err := c.root.Rename(tmp, resultFile); err != nil {
		return fmt.Errorf("evidence: install %s: %w", resultFile, err)
	}
	if err := syncDirAt(c.root, "."); err != nil {
		return err
	}
	// The document on disk is a different file now, and it is the one any later
	// replacement would have to be replacing.
	info, err := c.root.Lstat(resultFile)
	if err != nil {
		return fmt.Errorf("evidence: inspect %s: %w", resultFile, err)
	}
	c.resultInfo = info
	return nil
}

// sameResult accepts the status document only while it is still the regular
// file this capture installed.
func (c *Capture) sameResult() error {
	info, err := c.root.Lstat(resultFile)
	if err != nil {
		return fmt.Errorf("evidence: inspect %s: %w", resultFile, err)
	}
	if !info.Mode().IsRegular() || !os.SameFile(info, c.resultInfo) {
		return fmt.Errorf("evidence: %s is %s, want the file this capture wrote", resultFile, info.Mode().Type())
	}
	return nil
}

// Close removes the temporary ref and gives up the capture directory. It is
// safe to call more than once and reports the same outcome every time, so a
// deferred close cannot turn one failure into a different one. The two endings
// are independent: the ref comes out of the repository whether or not the
// directory closed cleanly, and neither failure hides the other.
func (c *Capture) Close(ctx context.Context) error {
	if c.closed {
		return c.closeErr
	}
	c.closed = true

	err := c.removeRef(ctx)
	if c.root != nil {
		if cerr := c.root.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("evidence: close %s: %w", strconv.Quote(c.dir), cerr))
		}
		c.root = nil
	}
	c.closeErr = err
	return err
}

// removeRef takes the temporary ref back out of the repository.
func (c *Capture) removeRef(ctx context.Context) error {
	if c.ref == "" {
		return nil
	}
	// Detached from the run's context: a cancelled run is still a run whose ref
	// has to come back out, and a bounded deadline keeps the cleanup from
	// hanging on a repository that has stopped answering.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer cancel()

	// Deleted with the value this capture put there, so a ref an operator has
	// since moved is left alone rather than removed out from under them.
	_, err := c.git(cleanup, "update-ref", "-d", c.ref, c.baseline)
	if err == nil {
		return nil
	}
	deleteErr := fmt.Errorf("evidence: remove %s: %w", strconv.Quote(c.ref), err)

	// A ref already gone is the outcome this call wanted, but only an answer
	// may say so: for-each-ref reports a missing ref by printing nothing and
	// still succeeding, so a failure here is the question going unanswered
	// rather than the ref being absent.
	out, probeErr := c.git(cleanup, "for-each-ref", "--format=%(refname)", c.ref)
	switch {
	case probeErr != nil:
		c.closeErr = errors.Join(deleteErr, fmt.Errorf("evidence: verify %s was removed: %w", strconv.Quote(c.ref), probeErr))
	case out != "":
		c.closeErr = deleteErr
	}
	return c.closeErr
}

// collect writes every artifact and reports what they say.
func (c *Capture) collect(ctx context.Context) (Result, error) {
	res := Result{Attribution: Attribution, Status: statusAvailable, Baseline: c.baseline}

	switch reachable, err := c.baselineReachable(ctx); {
	case err != nil:
		return Result{}, err
	case !reachable:
		// A baseline that has been rewritten away leaves nothing to measure
		// against, and a difference from some other commit would not be the
		// run's. The untracked files are still collected: the pre-run clean
		// gate established there were none, so the ones here now arrived
		// during the run.
		res.Status, res.Reason, res.Baseline = statusUnavailable, reasonBaselineUnreachable, ""
		if err := c.writeJSON(trackedStatFile, trackedStatDoc{
			Status:      statusUnavailable,
			Reason:      reasonBaselineUnreachable,
			Attribution: Attribution,
			Files:       []trackedFile{},
		}); err != nil {
			return Result{}, err
		}
	default:
		if err := c.captureTracked(ctx, &res); err != nil {
			return Result{}, err
		}
	}

	if err := c.captureUntracked(ctx, &res); err != nil {
		return Result{}, err
	}
	return res, nil
}

// baselineReachable reports whether the pinned commit is still an object this
// repository holds.
func (c *Capture) baselineReachable(ctx context.Context) (bool, error) {
	if c.baseline == "" {
		return false, nil
	}
	if _, err := c.git(ctx, "cat-file", "-e", c.baseline+"^{commit}"); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// The repository was never asked, so nothing has been established
			// about the commit — least of all that it is gone.
			return false, fmt.Errorf("evidence: check baseline %s: %w", quote(c.baseline), errors.Join(err, ctxErr))
		}
		return false, nil
	}
	return true, nil
}

// captureTracked writes the patch and the per-file statistics for everything
// Git tracks. The diff runs from the baseline to the worktree, so commits made
// during the run, staged changes and unstaged changes are all one difference.
func (c *Capture) captureTracked(ctx context.Context, res *Result) error {
	patch, err := c.gitBytes(ctx, c.opts.MaxPatchBytes,
		"diff", "--binary", "--full-index", "--no-ext-diff", "--no-renames", c.baseline, "--")
	if errors.Is(err, errOutputTooLarge) {
		return c.refusePatch(res, reasonPatchTooLarge)
	}
	if err != nil {
		return fmt.Errorf("evidence: diff from baseline: %w", err)
	}
	// Judged before the bytes are ever a string. A patch of a file Git stores as
	// text but that is not valid UTF-8 is carried through verbatim, and every
	// invalid byte of it would become U+FFFD in the JSON the bundle writes: the
	// patch on disk would then differ from the one the repository produced while
	// still reading as evidence of it. The bundle's own sanitizer refuses such
	// text too, and this is the same judgement made before anything is asked of
	// it — so what the reader gets is the reason, not a sanitizer's error.
	if !utf8.Valid(patch) {
		return c.refusePatch(res, reasonPatchNotUTF8)
	}

	safe, err := c.opts.Sanitize(string(patch))
	if err != nil {
		return fmt.Errorf("evidence: sanitize patch: %w", err)
	}
	// The limit is a promise about what lands on disk, and sanitizing can make
	// a patch longer than the one Git produced.
	if int64(len(safe)) > c.opts.MaxPatchBytes {
		return c.refusePatch(res, reasonPatchTooLarge)
	}
	if err := writeFileAt(c.root, patchFile, []byte(safe)); err != nil {
		return err
	}

	raw, err := c.gitBytes(ctx, maxListBytes, "diff", "--numstat", "--no-renames", "-z", c.baseline, "--")
	if err != nil {
		return fmt.Errorf("evidence: summarize diff from baseline: %w", err)
	}
	files, err := parseNumstat(raw)
	if err != nil {
		return err
	}
	slices.SortFunc(files, func(a, b trackedFile) int { return strings.Compare(a.Path, b.Path) })

	doc := trackedStatDoc{Status: statusAvailable, Attribution: Attribution, Baseline: c.baseline, Files: files}
	for i, f := range files {
		safePath, err := c.opts.Sanitize(f.Path)
		if err != nil {
			return fmt.Errorf("evidence: sanitize path: %w", err)
		}
		files[i].Path = safePath
		switch {
		case f.Binary:
			doc.Totals.Binary++
		default:
			doc.Totals.Additions += *f.Additions
			doc.Totals.Deletions += *f.Deletions
		}
	}
	doc.Totals.Files = len(files)

	res.TrackedFiles = doc.Totals.Files
	res.Added, res.Deleted, res.BinaryTracked = doc.Totals.Additions, doc.Totals.Deletions, doc.Totals.Binary
	return c.writeJSON(trackedStatFile, doc)
}

// refusePatch records a patch that will not be written: one over the limit, or
// one that is not text a JSON document can carry. Half a patch, or a patch with
// its unreadable bytes replaced, is not evidence of anything — so none of it is
// written and the reason is recorded in the statistics instead. The untracked
// capture is a separate question and the caller still answers it.
func (c *Capture) refusePatch(res *Result, reason string) error {
	res.Status, res.Reason = statusUnavailable, reason
	return c.writeJSON(trackedStatFile, trackedStatDoc{
		Status:      statusUnavailable,
		Reason:      reason,
		Attribution: Attribution,
		Baseline:    c.baseline,
		Files:       []trackedFile{},
	})
}

// parseNumstat reads `git diff --numstat -z` output. Records are NUL-terminated
// because a path may hold a newline, and a record that does not have exactly
// the shape Git documents is refused rather than interpreted: a miscounted
// diff would be evidence of something that did not happen.
func parseNumstat(raw []byte) ([]trackedFile, error) {
	files := []trackedFile{}
	for len(raw) > 0 {
		end := bytes.IndexByte(raw, 0)
		if end < 0 {
			return nil, fmt.Errorf("evidence: numstat record is not terminated: %s", quote(string(raw)))
		}
		record := raw[:end]
		raw = raw[end+1:]

		fields := bytes.SplitN(record, []byte{'\t'}, 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("evidence: numstat record %s does not have three fields", quote(string(record)))
		}
		path := string(fields[2])
		if path == "" {
			return nil, fmt.Errorf("evidence: numstat record %s names no path", quote(string(record)))
		}
		added, deleted := string(fields[0]), string(fields[1])
		if added == "-" || deleted == "-" {
			// Git marks a binary file by omitting both counts. One of the two
			// alone is a record this parser does not understand.
			if added != "-" || deleted != "-" {
				return nil, fmt.Errorf("evidence: numstat record %s counts only one side", quote(string(record)))
			}
			files = append(files, trackedFile{Path: path, Binary: true})
			continue
		}
		a, err := strconv.Atoi(added)
		if err != nil || a < 0 {
			return nil, fmt.Errorf("evidence: numstat record %s has no addition count", quote(string(record)))
		}
		d, err := strconv.Atoi(deleted)
		if err != nil || d < 0 {
			return nil, fmt.Errorf("evidence: numstat record %s has no deletion count", quote(string(record)))
		}
		files = append(files, trackedFile{Path: path, Additions: &a, Deletions: &d})
	}
	return files, nil
}

// captureUntracked describes every file the run left that Git does not track
// and is not told to ignore. Ignored files are excluded: they are build output
// and caches, which the operator already decided are not part of the work.
func (c *Capture) captureUntracked(ctx context.Context, res *Result) error {
	raw, err := c.gitBytes(ctx, maxListBytes, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return fmt.Errorf("evidence: list untracked files: %w", err)
	}
	paths := []string{}
	for _, p := range bytes.Split(raw, []byte{0}) {
		if len(p) > 0 {
			paths = append(paths, string(p))
		}
	}
	slices.Sort(paths)

	// Every read below goes through the repository root, so a path Git listed
	// can only ever reach a file inside it.
	root, err := os.OpenRoot(c.repoRoot)
	if err != nil {
		return fmt.Errorf("evidence: open repository %s: %w", strconv.Quote(c.repoRoot), err)
	}
	defer root.Close()

	doc := untrackedDoc{Attribution: Attribution, Files: make([]untrackedEntry, 0, len(paths))}
	var storedBytes int64
	for _, path := range paths {
		// Sanitized before anything is written for this file, so a sanitizer
		// that refuses cannot leave a body on disk with no entry naming it.
		safePath, err := c.opts.Sanitize(path)
		if err != nil {
			return fmt.Errorf("evidence: sanitize an untracked path: %w", err)
		}
		entry, raw, err := c.describe(root, path)
		if err != nil {
			// A file that vanished, changed identity or could not be read costs
			// that file's evidence, not the run's. The error itself is not
			// written down: it holds the repository path it failed on, which
			// nothing has sanitized.
			entry, raw = untrackedEntry{Kind: kindUnavailable, Reason: reasonUnreadable}, nil
		}
		if raw != nil {
			safe, err := c.opts.Sanitize(string(raw))
			if err != nil {
				return fmt.Errorf("evidence: sanitize untracked %s: %w", quote(safePath), err)
			}
			// The limits are promises about what lands on disk, so they are
			// measured against the sanitized body rather than the file that
			// was read: sanitizing can make text longer than it was.
			body := []byte(safe)
			switch size := int64(len(body)); {
			case size > c.opts.MaxTextFileBytes:
				// Over the limit even after sanitizing, so no body is stored and
				// none is identified: a hash of text this bundle does not hold
				// only gives a reader back the text.
				entry.Reason = reasonFileTooLarge
			case size > c.opts.MaxStoredTextBytes-storedBytes:
				// The body is what the run's own storage budget could not fit,
				// not text this capture refused to look at: the hash of what was
				// sanitized still identifies it, and gives back only the
				// redacted form.
				entry.SHA256, entry.HashBasis = hashOf(body), hashBasisSanitized
				entry.Reason = reasonStorageLimit
			default:
				name := storedName(path)
				if err := c.storeBody(name, body); err != nil {
					return err
				}
				entry.SHA256, entry.HashBasis = hashOf(body), hashBasisSanitized
				entry.Stored = true
				entry.StoredAs = filepath.Join(untrackedDirName, name)
				storedBytes += size
				doc.Stored++
			}
		}
		entry.Path = safePath
		doc.Files = append(doc.Files, entry)
	}
	doc.Count = len(doc.Files)

	res.UntrackedFiles, res.StoredTextFiles = doc.Count, doc.Stored
	return c.writeJSON(untrackedFile, doc)
}

// describe reports one untracked entry and, when its body is text small enough
// to be a candidate for storing, that body as it was read. A symlink or a device
// is described from its own metadata and never opened: following it would read a
// file the run never had, possibly outside the repository altogether. Every
// error here is one file's own — the caller records it as such and carries on.
func (c *Capture) describe(root *os.Root, path string) (untrackedEntry, []byte, error) {
	info, err := root.Lstat(path)
	if err != nil {
		return untrackedEntry{}, nil, err
	}
	entry := untrackedEntry{Mode: info.Mode().String(), Size: info.Size()}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		entry.Kind = kindSymlink
		return entry, nil, nil
	case !info.Mode().IsRegular():
		entry.Kind = kindOther
		return entry, nil, nil
	}
	entry.Kind = kindFile

	// O_NONBLOCK, because the regular file seen a moment ago can be a FIFO by
	// now: opening one would wait for a writer that need never arrive, and the
	// capture would hang instead of reporting a file it could not read.
	f, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return untrackedEntry{}, nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return untrackedEntry{}, nil, err
	}
	// The file named a moment ago and the file now open must be the same one:
	// anything else and this description would belong to neither.
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return untrackedEntry{}, nil, errors.New("untracked file changed while being read")
	}

	// The whole file is read, because what it is cannot be told from a prefix
	// and its size is not the size Lstat reported by the time it is open. Only
	// as much as could possibly be stored is kept in memory, plus enough of the
	// start to tell text from binary; the running hash is used only if the
	// answer turns out to be binary.
	buf := &capWriter{limit: max(c.opts.MaxTextFileBytes+1, sniffBytes)}
	sum := sha256.New()
	size, err := io.Copy(io.MultiWriter(sum, buf), f)
	if err != nil {
		return untrackedEntry{}, nil, err
	}
	entry.Size = size

	switch {
	case isBinary(buf.buf):
		// A binary body is not reviewable text, and storing one would put an
		// arbitrary blob from the repository into the bundle. The hash is the
		// only thing said about it, and there is no redacted text behind it for
		// the hash to give back.
		entry.SHA256, entry.HashBasis = hex.EncodeToString(sum.Sum(nil)), hashBasisRawBinary
		entry.Reason = reasonBinary
		return entry, nil, nil
	case size > c.opts.MaxTextFileBytes:
		// Nothing sanitized this file and nothing will, so the hash taken over
		// it stays here: publishing it would hand back every secret the file
		// holds, a short one by guessing.
		entry.Reason = reasonFileTooLarge
		return entry, nil, nil
	}
	// The hash of text is the caller's to record, over the bytes the sanitizer
	// gives back rather than the ones read here.
	return entry, buf.buf[:size], nil
}

// hashOf identifies a body by its contents.
func hashOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// storedName is what an untracked body is filed under: the hash of its path,
// never the path itself. A name from the repository could hold a traversal, a
// secret or anything else a filesystem would take literally.
func storedName(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:]) + ".txt"
}

// storeBody writes one sanitized body into the directory Start created. What is
// at that name is checked before every write, not once: a symlink put there
// during the run would otherwise send bodies wherever it points, and the run is
// what this package is watching.
func (c *Capture) storeBody(name string, body []byte) error {
	if c.root == nil {
		return errors.New("evidence: the capture directory is not open")
	}
	info, err := c.root.Lstat(untrackedDirName)
	if err != nil {
		return fmt.Errorf("evidence: inspect %s: %w", strconv.Quote(untrackedDirName), err)
	}
	if !info.Mode().IsDir() {
		return fmt.Errorf("evidence: %s is %s, want the directory this capture created", strconv.Quote(untrackedDirName), info.Mode().Type())
	}
	return writeFileAt(c.root, filepath.Join(untrackedDirName, name), body)
}

// isBinary judges a file by its first bytes, the way Git does: a NUL byte or
// text that is not valid UTF-8 is not something to store as text.
func isBinary(prefix []byte) bool {
	if bytes.IndexByte(prefix, 0) >= 0 {
		return true
	}
	if len(prefix) < sniffBytes {
		// The whole file is here, so every byte of it is judged.
		return !utf8.Valid(prefix)
	}
	// A file longer than the window ends the window wherever it falls, possibly
	// mid-rune, which says nothing about the file: an incomplete rune at the
	// end is dropped rather than read as invalid text.
	prefix = prefix[:sniffBytes]
	for range utf8.UTFMax - 1 {
		if utf8.Valid(prefix) {
			return false
		}
		prefix = prefix[:len(prefix)-1]
	}
	return !utf8.Valid(prefix)
}

// capWriter keeps the first limit bytes written to it and counts the rest as
// written, so a stream can be hashed whole while only a bounded prefix is held
// in memory.
type capWriter struct {
	buf   []byte
	limit int64
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.limit - int64(len(w.buf)); room > 0 {
		w.buf = append(w.buf, p[:min(room, int64(len(p)))]...)
	}
	return len(p), nil
}

func (c *Capture) path(name string) string { return filepath.Join(c.dir, name) }

// git asks one short question of the repository.
func (c *Capture) git(ctx context.Context, args ...string) (string, error) {
	out, err := gitAt(ctx, c.repoRoot, maxSmallBytes, args...)
	return strings.TrimRight(string(out), "\n"), err
}

// gitBytes reads a whole answer, refusing one larger than limit.
func (c *Capture) gitBytes(ctx context.Context, limit int64, args ...string) ([]byte, error) {
	return gitAt(ctx, c.repoRoot, limit, args...)
}

// gitAt runs one Git command with no shell between it and this process, and
// reads at most limit bytes of its answer. LC_ALL=C keeps Git's own messages in
// the language this package parses, and GIT_OPTIONAL_LOCKS=0 keeps a question
// from writing an index the operator did not ask to have written.
func gitAt(ctx context.Context, dir string, limit int64, args ...string) ([]byte, error) {
	full := args
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0")
	stderr := &capWriter{limit: maxStderrBytes}
	cmd.Stderr = stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", quote(strings.Join(args, " ")), err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("git %s: %w", quote(strings.Join(args, " ")), err)
	}
	// One byte past the limit is enough to know the limit was passed, and the
	// rest is drained rather than left to stall the command being waited on.
	out, readErr := io.ReadAll(io.LimitReader(stdout, limit+1))
	_, drainErr := io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()

	switch {
	case waitErr != nil:
		if len(stderr.buf) > 0 {
			return nil, fmt.Errorf("git %s: %s", quote(strings.Join(args, " ")), quote(string(stderr.buf)))
		}
		return nil, fmt.Errorf("git %s: %w", quote(strings.Join(args, " ")), waitErr)
	case readErr != nil:
		return nil, fmt.Errorf("git %s: %w", quote(strings.Join(args, " ")), readErr)
	case drainErr != nil:
		return nil, fmt.Errorf("git %s: %w", quote(strings.Join(args, " ")), drainErr)
	case int64(len(out)) > limit:
		return nil, fmt.Errorf("git %s: %w (%d bytes)", quote(strings.Join(args, " ")), errOutputTooLarge, limit)
	}
	return out, nil
}

// quote makes text from the repository or from Git safe to report onward:
// control characters — the escapes that would drive a terminal — become
// visible, and printable text is left as it is.
func quote(s string) string {
	return strconv.QuoteToGraphic(strings.TrimSpace(s))
}

// existingDir accepts one absolute path that is a directory in its own right. A
// symlink is refused rather than followed: where the evidence is read from and
// written to is this process's decision, not a link's.
func existingDir(what, path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("evidence: %s %s is not an absolute path", what, quote(path))
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("evidence: %s %s: %w", what, quote(path), err)
	}
	if !info.IsDir() {
		return fmt.Errorf("evidence: %s %s is %s, want a directory", what, quote(path), info.Mode().Type())
	}
	return nil
}

// validateRunID accepts exactly one clean path component with nothing in it a
// filesystem or a ref name would read as structure. The ID reaches this package
// from a flag or a provider session, so anything that could name a directory or
// a ref other than the intended one is refused rather than cleaned.
func validateRunID(runID string) error {
	switch {
	case runID == "":
		return errors.New("evidence: empty run id")
	case runID == "." || runID == "..":
		return fmt.Errorf("evidence: run id %s is not a name", quote(runID))
	case strings.ContainsRune(runID, '/') || strings.ContainsRune(runID, os.PathSeparator):
		return fmt.Errorf("evidence: run id %s contains a path separator", quote(runID))
	case filepath.Clean(runID) != runID:
		return fmt.Errorf("evidence: run id %s is not a clean path component", quote(runID))
	}
	for _, r := range runID {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("evidence: run id %s contains a control character", quote(runID))
		}
	}
	return nil
}

// writeJSON installs one document under the capture's own directory, encoded
// the same way every time and ending in a newline so that it reads as a
// line-oriented file.
func (c *Capture) writeJSON(name string, doc any) error {
	raw, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("evidence: encode %s: %w", name, err)
	}
	return writeFileAt(c.root, name, append(raw, '\n'))
}

// writeFileAt writes data under root, atomically, through a temporary file
// created exclusively and synced before it is installed, so what a crash leaves
// behind is either nothing or the whole file, never a partial one. Installing
// is a link rather than a rename: a rename replaces whatever is at the
// destination, and evidence already written is not this package's to overwrite.
//
// The name is relative and stays relative: root is a directory this package
// opened and holds, so nothing that happens to the path it was opened by can
// send a write somewhere else.
func writeFileAt(root *os.Root, name string, data []byte) error {
	if root == nil {
		return fmt.Errorf("evidence: write %s: the capture directory is not open", strconv.Quote(name))
	}
	// One of this package's own names, and only ever one: anything that could
	// climb out of the capture directory is refused rather than cleaned.
	if !filepath.IsLocal(name) {
		return fmt.Errorf("evidence: %s is not a name inside the capture directory", strconv.Quote(name))
	}
	tmp := name + ".tmp"
	// O_EXCL, so a symlink or a leftover file at this name is refused rather
	// than written through.
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		return fmt.Errorf("evidence: create %s: %w", name, err)
	}
	defer root.Remove(tmp)
	// Set again after opening, because the umask masks the mode passed to open.
	if err := f.Chmod(fileMode); err != nil {
		f.Close()
		return fmt.Errorf("evidence: restrict %s: %w", name, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("evidence: write %s: %w", name, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("evidence: sync %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("evidence: close %s: %w", name, err)
	}
	// The temporary file is in the destination's own directory, so this is one
	// filesystem's own link: it either installs the file or refuses because
	// something — a file, or a symlink pointing anywhere at all — is there.
	if err := root.Link(tmp, name); err != nil {
		return fmt.Errorf("evidence: install %s: %w", name, err)
	}
	if err := root.Remove(tmp); err != nil {
		return fmt.Errorf("evidence: remove the temporary %s: %w", name, err)
	}
	// The directory entry is persisted too, so that a file that was synced is
	// also found again.
	return syncDirAt(root, filepath.Dir(name))
}

// syncDirAt persists a directory's own entries, through the same root the file
// was installed under.
func syncDirAt(root *os.Root, dir string) error {
	d, err := root.Open(dir)
	if err != nil {
		return fmt.Errorf("evidence: open %s: %w", strconv.Quote(dir), err)
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("evidence: sync %s: %w", strconv.Quote(dir), err)
	}
	return nil
}
