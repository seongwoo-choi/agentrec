package worktree_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seongwoo-choi/agentrec/internal/worktree"
)

// --- git stand-in ------------------------------------------------------------
//
// Git creates the administration entry for a linked worktree and checks the
// worktree out before it is finished, so a failure in the last part of the work
// leaves a whole checkout behind. That moment cannot be provoked from outside
// Git, so the test binary stands in for it: every question is handed to the real
// Git and answered exactly as Git answered it, except the one addition a test
// asks to fail — which is failed after the real Git has carried it out.

// gitStandInName is the name the test binary is symlinked under when it stands
// in for Git.
const gitStandInName = "git"

const (
	// gitRealEnv names the Git the stand-in delegates to.
	gitRealEnv = "AGENTREC_TEST_WORKTREE_GIT"
	// gitFailAddEnv asks the stand-in to report `worktree add` as failed once
	// the real Git has already created the worktree.
	gitFailAddEnv = "AGENTREC_TEST_WORKTREE_FAIL_ADD"
	gitLogEnv     = "AGENTREC_TEST_WORKTREE_GIT_LOG"
)

// standInExit is what the stand-in reports when it could not do its own job,
// which is not the same as Git having failed.
const standInExit = 3

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == gitStandInName {
		os.Exit(gitStandIn(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func gitStandIn(args []string) int {
	real := os.Getenv(gitRealEnv)
	if real == "" {
		fmt.Fprintln(os.Stderr, "git stand-in: no git to delegate to")
		return standInExit
	}
	if path := os.Getenv(gitLogEnv); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			fmt.Fprintln(os.Stderr, "git stand-in:", err)
			return standInExit
		}
		_, err = fmt.Fprintln(f, strings.Join(args, "\x00"))
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			fmt.Fprintln(os.Stderr, "git stand-in:", errors.Join(err, closeErr))
			return standInExit
		}
	}
	cmd := exec.Command(real, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	if os.Getenv(gitFailAddEnv) != "" && isWorktreeAdd(args) {
		// The worktree the real Git just made is really there, and the answer is
		// a failure regardless: this is Git reporting that it could not finish a
		// checkout it had already begun.
		fmt.Fprintln(os.Stderr, "fatal: could not finish checking out the worktree")
		return 128
	}
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	fmt.Fprintln(os.Stderr, "git stand-in:", err)
	return standInExit
}

// isWorktreeAdd reports whether these are the arguments of the one command the
// stand-in is asked to fail. Every command this package runs names the
// repository with -C first, so the operation is the pair after it.
func isWorktreeAdd(args []string) bool {
	return len(args) > 3 && args[0] == "-C" && args[2] == "worktree" && args[3] == "add"
}

// standInGit puts the stand-in on PATH ahead of the real Git and points it at
// the Git it delegates to, so everything launched from here on goes through it —
// including whatever tidying a failed add does.
func standInGit(t *testing.T) {
	t.Helper()
	real, err := exec.LookPath(gitStandInName)
	if err != nil {
		t.Fatalf("locate git: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	dir := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(dir, gitStandInName)); err != nil {
		t.Fatalf("stand in for git: %v", err)
	}
	t.Setenv(gitRealEnv, real)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// The repositories below are real: what this package does is Git's own
// bookkeeping, and a fake would prove nothing about it.

// newRepo makes a repository holding two commits and returns its root. Two,
// because a worktree pinned at the first one proves the commit it was asked for
// was used rather than whatever HEAD happened to be.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "README.md"), "first\n")
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "agentrec test"},
		{"add", "README.md"},
		{"commit", "-m", "first"},
	} {
		git(t, dir, args...)
	}
	write(t, filepath.Join(dir, "README.md"), "second\n")
	git(t, dir, "add", "README.md")
	git(t, dir, "commit", "-m", "second")

	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	return real
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// git runs one Git command in dir and returns its output with the trailing
// newline removed. The environment is fixed so a developer's own configuration
// cannot change what these tests observe.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitErr(dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func gitErr(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"LC_ALL=C",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimRight(string(out), "\n"), err
}

// A worktree is checked out at the commit it was pinned to, with no branch of
// its own and nothing already changed in it: it is the state a recorded run has
// to start from, and any difference in it would be measured as the run's own.
func TestAddChecksOutThePinnedCommitDetachedAndClean(t *testing.T) {
	repo := newRepo(t)
	first := git(t, repo, "rev-parse", "HEAD~1")
	path := filepath.Join(t.TempDir(), "leg")

	w, err := worktree.Add(context.Background(), repo, path, first)
	if err != nil {
		t.Fatalf("add worktree: %v", err)
	}
	t.Cleanup(func() { w.Remove(context.Background()) })

	if w.Path() != path {
		t.Errorf("path = %q, want %q", w.Path(), path)
	}
	if head := git(t, path, "rev-parse", "HEAD"); head != first {
		t.Errorf("HEAD = %q, want the pinned commit %q", head, first)
	}
	if status := git(t, path, "status", "--porcelain=v1", "--untracked-files=normal"); status != "" {
		t.Errorf("status = %q, want a clean worktree", status)
	}
	if branch, err := gitErr(path, "symbolic-ref", "--quiet", "HEAD"); err == nil {
		t.Errorf("HEAD is on branch %q, want a detached worktree", branch)
	}
	// The bytes are the pinned commit's own, not the tip's: a worktree that
	// checked out the wrong commit would record a run against the wrong baseline.
	if content := read(t, filepath.Join(path, "README.md")); content != "first\n" {
		t.Errorf("README.md = %q, want the pinned commit's content", content)
	}
}

// A recorded run leaves work behind in the worktree it ran in — that is the
// whole point of it — so removal has to take a dirty checkout with it. What the
// run left is already evidence in its own bundle, and an administration entry
// left in the source repository is a leak the next run would find.
func TestRemoveDeletesADirtyWorktreeAndItsAdministration(t *testing.T) {
	repo := newRepo(t)
	path := filepath.Join(t.TempDir(), "leg")

	w, err := worktree.Add(context.Background(), repo, path, git(t, repo, "rev-parse", "HEAD"))
	if err != nil {
		t.Fatalf("add worktree: %v", err)
	}
	// Changed the way a recorded agent changes a checkout: a tracked file
	// rewritten and an untracked one created.
	write(t, filepath.Join(path, "README.md"), "changed by the run\n")
	write(t, filepath.Join(path, "notes.txt"), "left behind\n")

	if err := w.Remove(context.Background()); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("stat %s = %v, want the worktree removed", path, err)
	}
	wantOnlyTheSourceWorktree(t, repo)
}

// An addition that fails leaves the source repository as it found it. Git
// writes the administration entry for a linked worktree before it has finished
// checking one out, so a failure partway through is exactly where an entry
// naming a worktree that does not exist would be left behind — and the next run
// would find it.
func TestAddLeavesNothingBehindWhenItFails(t *testing.T) {
	for _, tc := range []struct {
		name   string
		commit func(t *testing.T, repo string) string
		path   func(t *testing.T) string
	}{
		{
			name:   "commit that is not in the repository",
			commit: func(*testing.T, string) string { return strings.Repeat("0", 40) },
			path:   func(t *testing.T) string { return filepath.Join(t.TempDir(), "leg") },
		},
		{
			name:   "path that cannot be created",
			commit: func(t *testing.T, repo string) string { return git(t, repo, "rev-parse", "HEAD") },
			path: func(t *testing.T) string {
				// A directory nothing may be created in, so the checkout fails
				// where Git has already begun preparing for it.
				dir := filepath.Join(t.TempDir(), "sealed")
				if err := os.Mkdir(dir, 0o500); err != nil {
					t.Fatalf("seal directory: %v", err)
				}
				t.Cleanup(func() { os.Chmod(dir, 0o700) })
				return filepath.Join(dir, "leg")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newRepo(t)
			path := tc.path(t)

			w, err := worktree.Add(context.Background(), repo, path, tc.commit(t, repo))

			if err == nil {
				w.Remove(context.Background())
				t.Fatalf("add worktree = nil, want a refusal")
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Errorf("stat %s = %v, want nothing left where the worktree would have been", path, err)
			}
			wantOnlyTheSourceWorktree(t, repo)
		})
	}
}

// An add can fail after Git has already checked the worktree out, which is the
// one failure that leaves a whole copy of the repository on disk and an entry
// naming it in the source repository. What the caller is given back is a
// refusal, so the checkout has to be taken back out here: a refusal that left a
// worktree behind is one whose caller cannot know there is anything to clean up.
func TestAddTakesBackAWorktreeCreatedBeforeTheFailure(t *testing.T) {
	repo := newRepo(t)
	path := filepath.Join(t.TempDir(), "leg")
	head := git(t, repo, "rev-parse", "HEAD")
	standInGit(t)
	t.Setenv(gitFailAddEnv, "1")
	before := sourceSnapshot(t, repo)

	w, err := worktree.Add(context.Background(), repo, path, head)

	if err == nil {
		w.Remove(context.Background())
		t.Fatalf("add worktree = nil, want the failure reported")
	}
	if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
		t.Errorf("stat %s = %v, want the checkout the failed add created taken back out", path, statErr)
	}
	wantOnlyTheSourceWorktree(t, repo)
	after := sourceSnapshot(t, repo)
	for _, name := range snapshotNames {
		if before[name] != after[name] {
			t.Errorf("%s after the failed add =\n%s\nwant\n%s", name, after[name], before[name])
		}
	}
}

// Where a run is executed is this package's decision. Anything already standing
// at that path — a directory, a file, or a symlink pointing somewhere else
// entirely — is refused as itself rather than checked out into, written through
// or emptied: a worktree created over something that was already there is a run
// recorded in a checkout nobody described.
func TestAddRefusesAPathThatIsAlreadyThere(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, path string)
	}{
		{"directory", func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatalf("plant directory: %v", err)
			}
		}},
		{"file", func(t *testing.T, path string) { write(t, path, "already here\n") }},
		{"symlink", func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), "elsewhere")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatalf("plant target: %v", err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatalf("plant symlink: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newRepo(t)
			path := filepath.Join(t.TempDir(), "leg")
			tc.plant(t, path)
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatalf("stat the planted path: %v", err)
			}

			w, err := worktree.Add(context.Background(), repo, path, git(t, repo, "rev-parse", "HEAD"))

			if err == nil {
				w.Remove(context.Background())
				t.Fatalf("add worktree = nil, want a refusal of %s", tc.name)
			}
			after, statErr := os.Lstat(path)
			if statErr != nil {
				t.Fatalf("stat the planted path: %v", statErr)
			}
			if after.Mode().Type() != before.Mode().Type() {
				t.Errorf("planted path is now %s, want it left as %s", after.Mode().Type(), before.Mode().Type())
			}
			wantOnlyTheSourceWorktree(t, repo)
		})
	}
}

// Cleanup is deferred by every caller and is also done on the way out of a
// failure, so it happens more than once. A second removal reports what the
// first one did rather than the fresh complaint that the worktree it was asked
// about is already gone, which a caller would read as a cleanup that failed.
func TestRemoveReportsItsFirstOutcomeEveryTime(t *testing.T) {
	repo := newRepo(t)
	path := filepath.Join(t.TempDir(), "leg")

	w, err := worktree.Add(context.Background(), repo, path, git(t, repo, "rev-parse", "HEAD"))
	if err != nil {
		t.Fatalf("add worktree: %v", err)
	}
	if err := w.Remove(context.Background()); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	if err := w.Remove(context.Background()); err != nil {
		t.Errorf("second remove = %v, want the first outcome", err)
	}
	wantOnlyTheSourceWorktree(t, repo)
}

// Cleanup owns one exact linked worktree. Repository-global pruning can remove
// unrelated stale administration entries, so normal cleanup must not invoke it.
func TestRemoveDoesNotPruneTheRepository(t *testing.T) {
	standInGit(t)
	log := filepath.Join(t.TempDir(), "git.log")
	t.Setenv(gitLogEnv, log)
	repo := newRepo(t)
	path := filepath.Join(t.TempDir(), "leg")
	w, err := worktree.Add(context.Background(), repo, path, git(t, repo, "rev-parse", "HEAD"))
	if err != nil {
		t.Fatalf("add worktree: %v", err)
	}
	if err := w.Remove(context.Background()); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read Git command log: %v", err)
	}
	if strings.Contains(string(raw), "worktree\x00prune") {
		t.Errorf("Git commands = %q, want no repository-global worktree prune", raw)
	}
}

// wantOnlyTheSourceWorktree fails when the repository still knows about a
// linked worktree, by its own list or by the administration directory behind it.
func wantOnlyTheSourceWorktree(t *testing.T, repo string) {
	t.Helper()
	list := git(t, repo, "worktree", "list", "--porcelain")
	for line := range strings.SplitSeq(list, "\n") {
		dir, ok := strings.CutPrefix(line, "worktree ")
		if ok && filepath.Clean(dir) != repo {
			t.Errorf("worktree list still names %q, want only the source checkout %q", dir, repo)
		}
	}
	admin := filepath.Join(repo, ".git", "worktrees")
	entries, err := os.ReadDir(admin)
	if err == nil && len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("%s holds %v, want no administration entry", admin, names)
	}
	if err != nil && !os.IsNotExist(err) {
		t.Errorf("read %s: %v", admin, err)
	}
}

// The source checkout is what an operator is still working in while a run is
// recorded somewhere else, and it is also the evidence the run was started
// from. A whole lifecycle — created, worked in, committed in, removed — leaves
// it exactly as it was: the same HEAD, the same index and status, the same
// tracked bytes and modes, the same refs, and no worktree but its own.
func TestTheSourceCheckoutIsUnchangedByAWorktreeLifecycle(t *testing.T) {
	repo := newRepo(t)
	// Something for the snapshot to notice if it were disturbed: a second ref,
	// and an untracked file of the operator's own.
	git(t, repo, "branch", "side")
	write(t, filepath.Join(repo, "scratch.txt"), "the operator's own\n")
	before := sourceSnapshot(t, repo)

	path := filepath.Join(t.TempDir(), "leg")
	w, err := worktree.Add(context.Background(), repo, path, git(t, repo, "rev-parse", "HEAD"))
	if err != nil {
		t.Fatalf("add worktree: %v", err)
	}
	// Worked in the way a recorded agent works: files changed, and a commit of
	// its own made on the detached HEAD.
	write(t, filepath.Join(path, "README.md"), "changed by the run\n")
	write(t, filepath.Join(path, "notes.txt"), "left behind\n")
	git(t, path, "add", "README.md")
	git(t, path, "-c", "user.email=run@example.com", "-c", "user.name=run", "commit", "-m", "the run's own commit")

	if err := w.Remove(context.Background()); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	for _, name := range snapshotNames {
		if before[name] != sourceSnapshot(t, repo)[name] {
			t.Errorf("%s after the lifecycle =\n%s\nwant\n%s", name, sourceSnapshot(t, repo)[name], before[name])
		}
	}
}

// snapshotNames are the readings taken of the source checkout, named so a
// difference is reported as the thing that differs rather than as one opaque
// digest.
var snapshotNames = []string{"head", "status", "index", "refs", "worktrees", "tracked bytes"}

// sourceSnapshot reads the source checkout the way an operator would check it
// was not touched: where it stands, what it has uncommitted, what its index
// holds, every ref it knows, every worktree it claims, and the bytes and modes
// of the files it has checked out.
func sourceSnapshot(t *testing.T, repo string) map[string]string {
	t.Helper()
	snap := map[string]string{
		"head":      git(t, repo, "rev-parse", "HEAD"),
		"status":    git(t, repo, "status", "--porcelain=v1", "--untracked-files=all"),
		"index":     git(t, repo, "ls-files", "--stage"),
		"refs":      git(t, repo, "for-each-ref", "--format=%(refname) %(objectname)"),
		"worktrees": git(t, repo, "worktree", "list", "--porcelain"),
	}

	var bytes strings.Builder
	for line := range strings.SplitSeq(git(t, repo, "ls-files"), "\n") {
		if line == "" {
			continue
		}
		info, err := os.Lstat(filepath.Join(repo, line))
		if err != nil {
			t.Fatalf("stat %s: %v", line, err)
		}
		sum := sha256.Sum256([]byte(read(t, filepath.Join(repo, line))))
		fmt.Fprintf(&bytes, "%s %s %s\n", line, info.Mode(), hex.EncodeToString(sum[:]))
	}
	snap["tracked bytes"] = bytes.String()
	return snap
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
