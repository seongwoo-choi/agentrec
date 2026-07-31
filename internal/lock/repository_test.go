package lock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// blockTimeout is how long a second acquisition may take before it is judged to
// have blocked: the lock is non-blocking, so an acquisition that has not
// answered by now never will.
const blockTimeout = 10 * time.Second

// runGit runs one Git command in dir, isolated from the operator's own
// configuration so a machine's global settings cannot decide what these tests
// observe.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"LC_ALL=C",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// gitRepo creates a temporary repository holding one commit and returns its
// root as the filesystem finally resolves it, which is the path the lock is
// named after.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "agentrec test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")

	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	return real
}

// locksRoot names a lock directory that does not exist yet, which is what an
// operator who has never recorded a run has.
func locksRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "locks")
}

// acquireResult reports what a second acquisition answered, so a test can tell
// a refusal from a wait.
func acquireResult(t *testing.T, locks, cwd string) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		held, err := Acquire(context.Background(), locks, cwd)
		if held != nil {
			done <- held.Release()
			return
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(blockTimeout):
		t.Fatalf("Acquire blocked on a held lock, want an immediate refusal")
		return nil
	}
}

func TestAcquireHoldsTheRepositoryRootAgainstASecondRun(t *testing.T) {
	root := gitRepo(t)
	locks := locksRoot(t)

	held, err := Acquire(context.Background(), locks, root)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer held.Release()

	if held.Root() != root {
		t.Errorf("Root() = %q, want %q", held.Root(), root)
	}
	sum := sha256.Sum256([]byte(root))
	want := filepath.Join(locks, hex.EncodeToString(sum[:])+".lock")
	if held.Path() != want {
		t.Errorf("Path() = %q, want %q", held.Path(), want)
	}

	// The same repository reached by a different path inside it is the same
	// repository, and a run there must be refused rather than waited for.
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatalf("create subdirectory: %v", err)
	}
	if err := acquireResult(t, locks, sub); !errors.Is(err, ErrLocked) {
		t.Errorf("second acquire = %v, want %v", err, ErrLocked)
	}
}

func TestReleasePermitsTheRepositoryToBeAcquiredAgain(t *testing.T) {
	root := gitRepo(t)
	locks := locksRoot(t)

	held, err := Acquire(context.Background(), locks, root)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	if err := acquireResult(t, locks, root); err != nil {
		t.Errorf("acquire after release = %v, want it to succeed", err)
	}
}

// A repository reached through a symlink is the same repository, so it must
// hash to — and lock — the same file.
func TestAcquireResolvesASymlinkAliasToTheSameLock(t *testing.T) {
	root := gitRepo(t)
	locks := locksRoot(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatalf("create alias: %v", err)
	}

	held, err := Acquire(context.Background(), locks, alias)
	if err != nil {
		t.Fatalf("acquire through alias: %v", err)
	}
	defer held.Release()

	if held.Root() != root {
		t.Errorf("Root() = %q, want the resolved root %q", held.Root(), root)
	}
	if err := acquireResult(t, locks, root); !errors.Is(err, ErrLocked) {
		t.Errorf("acquire through the real path = %v, want %v", err, ErrLocked)
	}
}

func TestAcquireLocksDistinctRepositoriesIndependently(t *testing.T) {
	first, second := gitRepo(t), gitRepo(t)
	locks := locksRoot(t)

	held, err := Acquire(context.Background(), locks, first)
	if err != nil {
		t.Fatalf("acquire first: %v", err)
	}
	defer held.Release()

	other, err := Acquire(context.Background(), locks, second)
	if err != nil {
		t.Fatalf("acquire second: %v", err)
	}
	defer other.Release()

	if held.Path() == other.Path() {
		t.Errorf("both repositories locked %q", held.Path())
	}
}

// The lock says which repository is being recorded, which is not something to
// leave readable to everyone on the machine.
func TestAcquireKeepsTheLockRootAndFilePrivate(t *testing.T) {
	root := gitRepo(t)
	locks := locksRoot(t)
	if err := os.MkdirAll(locks, 0o700); err != nil {
		t.Fatalf("create lock root: %v", err)
	}
	if err := os.Chmod(locks, 0o777); err != nil {
		t.Fatalf("relax lock root: %v", err)
	}

	held, err := Acquire(context.Background(), locks, root)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer held.Release()

	dir, err := os.Lstat(locks)
	if err != nil {
		t.Fatalf("stat lock root: %v", err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("lock root mode = %v, want 0700", dir.Mode().Perm())
	}
	file, err := os.Lstat(held.Path())
	if err != nil {
		t.Fatalf("stat lock file: %v", err)
	}
	if !file.Mode().IsRegular() {
		t.Errorf("lock file mode = %v, want a regular file", file.Mode())
	}
	if file.Mode().Perm() != 0o600 {
		t.Errorf("lock file mode = %v, want 0600", file.Mode().Perm())
	}
}

// A lock root that is not a directory this process created is a lock root
// somebody else decides the meaning of, so it is refused rather than used.
func TestAcquireRefusesALockRootItDoesNotOwn(t *testing.T) {
	root := gitRepo(t)

	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		locks := filepath.Join(dir, "locks")
		if err := os.Symlink(filepath.Join(dir, "elsewhere"), locks); err != nil {
			t.Fatalf("create symlinked lock root: %v", err)
		}
		if err := os.Mkdir(filepath.Join(dir, "elsewhere"), 0o700); err != nil {
			t.Fatalf("create link target: %v", err)
		}

		held, err := Acquire(context.Background(), locks, root)
		if err == nil {
			held.Release()
			t.Fatalf("acquire succeeded, want a refusal")
		}
		if !strings.Contains(err.Error(), "locks") {
			t.Errorf("error = %v, want it to name the refused lock root", err)
		}
	})

	t.Run("file", func(t *testing.T) {
		locks := filepath.Join(t.TempDir(), "locks")
		if err := os.WriteFile(locks, []byte("not a directory\n"), 0o600); err != nil {
			t.Fatalf("create lock root file: %v", err)
		}

		held, err := Acquire(context.Background(), locks, root)
		if err == nil {
			held.Release()
			t.Fatalf("acquire succeeded, want a refusal")
		}
	})
}

func TestAcquireRefusesALockRootInsideRepositoryBeforeWriting(t *testing.T) {
	root := gitRepo(t)
	locks := filepath.Join(root, ".agentrec", "locks")

	held, err := Acquire(context.Background(), locks, root)
	if err == nil {
		held.Release()
		t.Fatal("acquire succeeded, want refusal before creating a lock")
	}
	if !strings.Contains(err.Error(), "data directory") || !strings.Contains(err.Error(), root) {
		t.Errorf("error = %q, want data-directory refusal naming %q", err, root)
	}
	if _, err := os.Lstat(filepath.Join(root, ".agentrec")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("agentrec directory = %v, want it absent", err)
	}
}

// The lock file is the lock: deleting it would let a later run take a lock
// nobody else can see, so releasing leaves it exactly where it was.
func TestReleaseKeepsTheLockFileAndIsIdempotent(t *testing.T) {
	root := gitRepo(t)
	locks := locksRoot(t)

	held, err := Acquire(context.Background(), locks, root)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := held.Release(); err != nil {
		t.Errorf("second release = %v, want it to report the first outcome", err)
	}

	info, err := os.Lstat(held.Path())
	if err != nil {
		t.Fatalf("stat lock file after release: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("lock file mode = %v, want the file left in place", info.Mode())
	}
}

func TestAcquireOutsideARepositoryExplainsItself(t *testing.T) {
	dir := t.TempDir()

	held, err := Acquire(context.Background(), locksRoot(t), dir)
	if err == nil {
		held.Release()
		t.Fatalf("acquire succeeded outside a repository, want a refusal")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error = %v, want it to name %q", err, dir)
	}
	if !strings.Contains(err.Error(), "repository") {
		t.Errorf("error = %v, want it to say what is missing", err)
	}
}

// A path is reported back to a terminal, so a path carrying control characters
// is quoted rather than replayed.
func TestAcquireQuotesPathsItReportsBack(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "re\x1b[31mpo")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("create directory: %v", err)
	}

	held, err := Acquire(context.Background(), locksRoot(t), dir)
	if err == nil {
		held.Release()
		t.Fatalf("acquire succeeded outside a repository, want a refusal")
	}
	if strings.Contains(err.Error(), "\x1b") {
		t.Errorf("error = %q, want the control character quoted", err.Error())
	}
}

func TestCheckCleanAcceptsACleanRepository(t *testing.T) {
	root := gitRepo(t)

	if err := CheckClean(context.Background(), root); err != nil {
		t.Errorf("CheckClean = %v, want nil", err)
	}
}

func TestCheckCleanRejectsAWorktreeThatHasChanges(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dirty func(t *testing.T, root string)
	}{
		{"tracked modification", func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("changed\n"), 0o600); err != nil {
				t.Fatalf("modify tracked file: %v", err)
			}
		}},
		{"staged change", func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("changed\n"), 0o600); err != nil {
				t.Fatalf("modify tracked file: %v", err)
			}
			runGit(t, root, "add", "README.md")
		}},
		{"untracked file", func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "scratch.txt"), []byte("scratch\n"), 0o600); err != nil {
				t.Fatalf("write untracked file: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := gitRepo(t)
			tc.dirty(t, root)

			err := CheckClean(context.Background(), root)
			if err == nil {
				t.Fatalf("CheckClean = nil, want a refusal")
			}
			if !strings.Contains(err.Error(), "commit") && !strings.Contains(err.Error(), "stash") {
				t.Errorf("error = %v, want it to say what to do about it", err)
			}
		})
	}
}

// An interrupted Git operation is a repository mid-edit, whose changes belong
// to that operation and not to the run about to be recorded — so it is refused
// even when the worktree itself reports nothing.
func TestCheckCleanRejectsAnUnfinishedGitOperation(t *testing.T) {
	for _, tc := range []struct {
		marker    string
		directory bool
		operation string
	}{
		{marker: "MERGE_HEAD", operation: "merge"},
		{marker: "rebase-merge", directory: true, operation: "rebase"},
		{marker: "rebase-apply", directory: true, operation: "rebase"},
		{marker: "CHERRY_PICK_HEAD", operation: "cherry-pick"},
		{marker: "BISECT_LOG", operation: "bisect"},
		{marker: "BISECT_START", operation: "bisect"},
	} {
		t.Run(tc.marker, func(t *testing.T) {
			root := gitRepo(t)
			path := filepath.Join(root, ".git", tc.marker)
			if tc.directory {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatalf("create %s: %v", tc.marker, err)
				}
			} else if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatalf("create %s: %v", tc.marker, err)
			}

			err := CheckClean(context.Background(), root)
			if err == nil {
				t.Fatalf("CheckClean = nil, want a refusal")
			}
			if !strings.Contains(err.Error(), tc.operation) {
				t.Errorf("error = %v, want it to name the %s in progress", err, tc.operation)
			}
		})
	}
}

// stubGitDir puts a stand-in for git ahead of the real one on PATH, answering
// every question with answer. It is how a test hands CheckClean a git directory
// the real git would never name.
func stubGitDir(t *testing.T, answer string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' " + strconv.Quote(answer) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o700); err != nil {
		t.Fatalf("write git stand-in: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A marker that cannot be looked at is not a marker that is absent. Reading
// through the difference would record a run against a repository nobody
// established was idle, so the check refuses and says which path defeated it.
func TestCheckCleanReportsAMarkerItCannotLookAt(t *testing.T) {
	// A regular file standing where the git directory belongs makes every marker
	// underneath it unreachable — ENOTDIR, which is not "does not exist".
	notADir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatalf("create file: %v", err)
	}
	gitDir := filepath.Join(notADir, "gitdir")
	stubGitDir(t, gitDir)
	root := t.TempDir()

	err := CheckClean(context.Background(), root)
	if err == nil {
		t.Fatalf("CheckClean = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), strconv.Quote(filepath.Join(gitDir, "MERGE_HEAD"))) {
		t.Errorf("error = %v, want it to quote the marker it could not look at", err)
	}
	if !strings.Contains(err.Error(), strconv.Quote(root)) {
		t.Errorf("error = %v, want it to quote the repository %q", err, root)
	}
}

func TestCheckCleanOutsideARepositoryExplainsItself(t *testing.T) {
	dir := t.TempDir()

	err := CheckClean(context.Background(), dir)
	if err == nil {
		t.Fatalf("CheckClean = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "repository") {
		t.Errorf("error = %v, want it to say what is missing", err)
	}
}

// Checking a repository is reading it, and reading evidence must leave it as it
// was found.
func TestCheckCleanDoesNotMutateTheRepository(t *testing.T) {
	root := gitRepo(t)
	before := snapshot(t, filepath.Join(root, ".git"))

	if err := CheckClean(context.Background(), root); err != nil {
		t.Fatalf("CheckClean = %v, want nil", err)
	}

	after := snapshot(t, filepath.Join(root, ".git"))
	if len(before) != len(after) {
		t.Fatalf("git directory holds %d files after the check, had %d", len(after), len(before))
	}
	for name, content := range before {
		if after[name] != content {
			t.Errorf("%s changed during the check", name)
		}
	}
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[path] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", dir, err)
	}
	return files
}
