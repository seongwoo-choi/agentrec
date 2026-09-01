// Package lock serializes recorded runs per repository and refuses to record a
// repository that is not in a state a run can be told apart from.
//
// A run's evidence is the difference the agent made to a repository, which only
// means something when one run at a time is making differences and when the
// repository started from a state nothing else was already changing.
package lock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ErrLocked reports that another run already holds this repository. It is a
// refusal and not a failure: the operator's answer is to wait for that run.
var ErrLocked = errors.New("lock: repository is already being recorded")

// Repository is a held lock on one repository root. It is released by Release
// and by nothing else — in particular the lock file is never removed, because a
// file removed under a lock is a lock a later run cannot see.
type Repository struct {
	root string
	path string
	file *os.File

	// released records the first Release outcome, so every later call reports
	// the same thing rather than a second, meaningless error.
	released bool
	err      error
}

// Root is the repository this lock covers, as the filesystem finally resolves
// it.
func (r *Repository) Root() string { return r.root }

// Path is the lock file held for that repository.
func (r *Repository) Path() string { return r.path }

// Acquire takes the lock for the repository containing cwd, without waiting: a
// repository another run already holds is reported as ErrLocked rather than
// queued behind it, because a recorder that silently waits records a run the
// operator thinks already started.
func Acquire(ctx context.Context, locksRoot, cwd string) (*Repository, error) {
	root, err := repoRoot(ctx, cwd)
	if err != nil {
		return nil, err
	}
	if insideRepository(locksRoot, root) {
		return nil, fmt.Errorf("lock: the agentrec data directory %s is inside the repository %s being recorded: set AGENTREC_HOME to a directory outside it",
			strconv.Quote(filepath.Dir(locksRoot)), strconv.Quote(root))
	}
	if err := privateDir(locksRoot); err != nil {
		return nil, err
	}

	sum := sha256.Sum256([]byte(root))
	path := filepath.Join(locksRoot, hex.EncodeToString(sum[:])+".lock")

	// O_NOFOLLOW so a symlink planted in the lock directory cannot redirect the
	// lock — and, with it, this process's writes — somewhere else.
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock: open lock file %s: %w", strconv.Quote(path), err)
	}
	if err := verifyLockFile(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("lock: %s: %w", strconv.Quote(path), err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, strconv.Quote(root))
		}
		return nil, fmt.Errorf("lock: lock %s: %w", strconv.Quote(path), err)
	}
	return &Repository{root: root, path: path, file: file}, nil
}

// Release gives the lock up. It is safe to call more than once and reports the
// first outcome every time, so a deferred release cannot turn one failure into
// a different one.
func (r *Repository) Release() error {
	if r.released {
		return r.err
	}
	r.released = true
	if err := syscall.Flock(int(r.file.Fd()), syscall.LOCK_UN); err != nil {
		r.err = fmt.Errorf("lock: unlock %s: %w", strconv.Quote(r.path), err)
	}
	if err := r.file.Close(); err != nil && r.err == nil {
		r.err = fmt.Errorf("lock: close %s: %w", strconv.Quote(r.path), err)
	}
	return r.err
}

// verifyLockFile refuses anything that is not a plain file, so a device or a
// directory left where the lock belongs is not mistaken for one.
func verifyLockFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat lock file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("lock file is %s, want a regular file", info.Mode().Type())
	}
	// A lock file left readable by an earlier version, or by a different umask,
	// is tightened on the handle already checked rather than by name.
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("restrict lock file: %w", err)
	}
	return nil
}

// insideRepository reports whether path is the repository root or inside it.
// Existing symlinks are resolved so a future lock cannot write into the subject
// repository through an alias.
func insideRepository(path, root string) bool {
	rel, err := filepath.Rel(resolved(root), resolved(path))
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}

func resolved(path string) string {
	rest := ""
	for dir := filepath.Clean(path); ; dir = filepath.Dir(dir) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Clean(path)
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

// privateDir makes sure locksRoot is a directory this user alone can reach. A
// symlink there is refused rather than followed: where the locks live is this
// process's decision and not something a link in the data directory may change.
func privateDir(locksRoot string) error {
	if err := os.MkdirAll(locksRoot, 0o700); err != nil {
		return fmt.Errorf("lock: create lock directory %s: %w", strconv.Quote(locksRoot), err)
	}
	info, err := os.Lstat(locksRoot)
	if err != nil {
		return fmt.Errorf("lock: stat lock directory %s: %w", strconv.Quote(locksRoot), err)
	}
	if !info.Mode().IsDir() {
		return fmt.Errorf("lock: lock directory %s is %s, want a directory", strconv.Quote(locksRoot), info.Mode().Type())
	}
	if err := os.Chmod(locksRoot, 0o700); err != nil {
		return fmt.Errorf("lock: restrict lock directory %s: %w", strconv.Quote(locksRoot), err)
	}
	return nil
}

// repoRoot is the repository containing cwd, resolved to one absolute path with
// no links left in it, so the same repository reached by different names always
// hashes to the same lock.
func repoRoot(ctx context.Context, cwd string) (string, error) {
	out, err := git(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("lock: %s is not inside a Git repository: agentrec records runs against a repository (%w)", strconv.Quote(cwd), err)
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return "", fmt.Errorf("lock: resolve repository root %s: %w", strconv.Quote(out), err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("lock: resolve repository root %s: %w", strconv.Quote(abs), err)
	}
	return real, nil
}

// gitOperations are the operations that leave a repository mid-edit, the marker
// Git leaves behind for each, and how an operator ends it. A repository in one
// of these states holds changes belonging to that operation, which no recorded
// run may claim as its own.
var gitOperations = []struct {
	marker    string
	operation string
	remedy    string
}{
	{"MERGE_HEAD", "merge", "git merge --abort"},
	{"rebase-merge", "rebase", "git rebase --abort"},
	{"rebase-apply", "rebase", "git rebase --abort"},
	{"CHERRY_PICK_HEAD", "cherry-pick", "git cherry-pick --abort"},
	{"REVERT_HEAD", "revert", "git revert --abort"},
	{"BISECT_LOG", "bisect", "git bisect reset"},
	{"BISECT_START", "bisect", "git bisect reset"},
}

// CheckClean reports why repoRoot is not a repository a run can be recorded
// against: an operation still in progress, or a worktree that already differs
// from its last commit. Both would be indistinguishable afterwards from changes
// the recorded agent made. It only reads: the repository is evidence.
func CheckClean(ctx context.Context, repoRoot string) error {
	gitDir, err := git(ctx, repoRoot, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return fmt.Errorf("lock: %s is not inside a Git repository: agentrec records runs against a repository (%w)", strconv.Quote(repoRoot), err)
	}
	for _, op := range gitOperations {
		// Only an absent marker means the operation is not in progress. Any other
		// answer means the check could not be made, which is not the same as a
		// clean repository and must not be read as one.
		marker := filepath.Join(gitDir, op.marker)
		_, err := os.Lstat(marker)
		if err == nil {
			return fmt.Errorf("lock: repository %s has an unfinished %s: finish it or run %s before recording a run",
				strconv.Quote(repoRoot), op.operation, op.remedy)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("lock: repository %s cannot be checked for an unfinished operation: stat %s: %w",
				strconv.Quote(repoRoot), strconv.Quote(marker), err)
		}
	}

	status, err := git(ctx, repoRoot, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return fmt.Errorf("lock: read status of repository %s: %w", strconv.Quote(repoRoot), err)
	}
	if status != "" {
		return fmt.Errorf("lock: repository %s has uncommitted changes: commit or stash them before recording a run, so the run's own changes can be told apart",
			strconv.Quote(repoRoot))
	}
	return nil
}

// git asks one question of the repository at dir and returns the answer with
// its trailing newline removed. LC_ALL=C keeps Git's own messages in the
// language this package parses, and GIT_OPTIONAL_LOCKS=0 keeps a question from
// refreshing an index the operator did not ask to have written.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), oneLine(string(exit.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// oneLine makes Git's own words safe to report onward: control characters — the
// escapes that would drive a terminal — become visible, and printable text is
// left as it is.
func oneLine(s string) string {
	quoted := strconv.QuoteToGraphic(strings.TrimSpace(s))
	return quoted[1 : len(quoted)-1]
}
