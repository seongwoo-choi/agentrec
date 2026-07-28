// Package worktree creates and removes the disposable checkouts a recorded run
// is executed in.
package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Worktree is one detached linked worktree.
type Worktree struct {
	repo string
	path string

	// removed records the first Remove outcome, so every later call reports the
	// same thing rather than a second, meaningless complaint.
	removed bool
	err     error
}

// Path is the directory the worktree was checked out into.
func (w *Worktree) Path() string { return w.path }

// Add checks commit out into path as a detached linked worktree of repoRoot.
// The checkout is detached because a run recorded in it is measured against the
// commit it was given and against nothing a branch might move to underneath it.
func Add(ctx context.Context, repoRoot, path, commit string) (*Worktree, error) {
	// Lstat, so a symlink standing at the path is refused as itself rather than
	// followed: where a run is executed is this package's decision, and a link
	// left in the data directory may not change it. An empty directory is
	// refused too, which Git would otherwise check out into — a worktree created
	// over something already there is a run recorded in a checkout nobody
	// described.
	switch _, err := os.Lstat(path); {
	case err == nil:
		return nil, fmt.Errorf("worktree: %s already exists", strconv.Quote(path))
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("worktree: stat %s: %w", strconv.Quote(path), err)
	}
	if _, err := git(ctx, repoRoot, "worktree", "add", "--detach", path, commit); err != nil {
		return nil, errors.Join(err, tidyPartialAdd(repoRoot, path))
	}
	return &Worktree{repo: repoRoot, path: path}, nil
}

// tidyTimeout bounds the tidying after a failed addition. It is the tidying's
// own, because the deadline the caller gave the addition may be the very thing
// that ended it, and a checkout left behind is worth a bounded second ask.
const tidyTimeout = 30 * time.Second

// tidyPartialAdd takes back whatever a failed `git worktree add` had already
// made. Git writes the administration entry and checks the worktree out before
// it has finished, so a failure in the last part of the work leaves a whole copy
// of the repository behind and an entry naming it — and the caller was handed a
// refusal, so nobody is expecting either.
//
// Both asks are best-effort: an addition that failed before anything existed
// leaves Git rightly complaining that there is nothing to remove, which is not
// something to report onward. What is reported is what is still there
// afterwards, so a caller told the addition was refused is also told when the
// refusal left something of itself behind.
func tidyPartialAdd(repoRoot, path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), tidyTimeout)
	defer cancel()
	git(ctx, repoRoot, "worktree", "remove", "--force", path)
	git(ctx, repoRoot, "worktree", "prune")

	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("worktree: %s was created by the failed add and could not be removed", strconv.Quote(path))
	}
	list, err := git(ctx, repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return err
	}
	for line := range strings.SplitSeq(list, "\n") {
		if dir, ok := strings.CutPrefix(line, "worktree "); ok && filepath.Clean(dir) == filepath.Clean(path) {
			return fmt.Errorf("worktree: %s is still registered in %s after the failed add", strconv.Quote(path), strconv.Quote(repoRoot))
		}
	}
	return nil
}

// git runs one Git command in dir directly, with no shell anywhere: every
// argument here reaches Git as itself. LC_ALL=C keeps Git's own messages in the
// language this package reports onward.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", fmt.Errorf("worktree: git %s: %s", strings.Join(args, " "), oneLine(string(exit.Stderr)))
		}
		return "", fmt.Errorf("worktree: git %s: %w", strings.Join(args, " "), err)
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

// Remove deletes the worktree and the administration entry behind it. The
// removal is forced: a recorded run leaves work behind in the checkout it ran
// in, and that work is already in the run's own bundle. Pruning follows, so a
// removal that could not delete the directory itself still does not leave the
// source repository claiming a worktree that is no longer there.
//
// It is safe to call more than once and reports the first outcome every time:
// cleanup is deferred by every caller and also done on the way out of a
// failure, and a second removal complaining that the worktree is already gone
// would read as a cleanup that did not happen.
func (w *Worktree) Remove(ctx context.Context) error {
	if w.removed {
		return w.err
	}
	w.removed = true
	_, w.err = git(ctx, w.repo, "worktree", "remove", "--force", w.path)
	if _, err := git(ctx, w.repo, "worktree", "prune"); w.err == nil {
		w.err = err
	}
	return w.err
}
