package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/lock"
	"github.com/seongwoo-choi/agentrec/internal/storage"
)

// A deleted run is moved, not erased: it goes to a trash directory beside the
// runs, under the same data directory, so deleting is a rename and restoring
// is the reverse. The viewer deletes into the trash; only `agentrec trash
// empty` erases.

const (
	trashDirName = "trash"
	trashUsage   = "usage: agentrec trash [restore <run-id> | empty | sweep <age> [--dry-run]]\n"
)

var (
	errRunOpen    = errors.New("cli: the run is still open")
	errRunClosing = errors.New("cli: the run is still being closed out")
	errRunExists  = errors.New("cli: a run with this id is already there")
	errNotInTrash = errors.New("cli: no such run in the trash")
)

// trashRootFor is the trash beside a runs directory.
func trashRootFor(runsRoot string) string {
	return filepath.Join(filepath.Dir(runsRoot), trashDirName)
}

// openTrashParent holds the data root and refuses a trash entry that is not a
// directory in its own right. Mutations through the returned root cannot
// follow a trash symlink outside the data directory.
func openTrashParent(runsRoot string, create bool) (*os.Root, error) {
	parent, err := os.OpenRoot(filepath.Dir(runsRoot))
	if err != nil {
		return nil, fmt.Errorf("cli: open data directory: %w", err)
	}
	info, err := parent.Lstat(trashDirName)
	if errors.Is(err, os.ErrNotExist) && create {
		err = parent.Mkdir(trashDirName, 0o700)
		if err == nil {
			info, err = parent.Lstat(trashDirName)
		}
	}
	if err != nil {
		parent.Close()
		return nil, fmt.Errorf("cli: open trash: %w", err)
	}
	if !info.IsDir() {
		parent.Close()
		return nil, errors.New("cli: trash is not a directory")
	}
	return parent, nil
}

func openTrashRoot(runsRoot string) (*os.Root, error) {
	parent, err := openTrashParent(runsRoot, false)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	root, err := parent.OpenRoot(trashDirName)
	if err != nil {
		return nil, fmt.Errorf("cli: open trash: %w", err)
	}
	return root, nil
}

func checkRunID(runID string) error {
	if runID == "" || validateRunID(runID) != nil || path.Base(runID) != runID {
		return errors.New("cli: invalid run id")
	}
	return nil
}

// trashRun moves a run out of the store. A run whose recorder still holds
// its session is refused: what it records next would land in the trash.
func trashRun(root, runID string) error {
	if err := checkRunID(runID); err != nil {
		return err
	}
	runRoot, err := openRunRoot(root, runID)
	if err != nil {
		return err
	}
	if m, err := readManifestFromRoot(runRoot); err == nil {
		if err := runOpenFromRoot(root, runRoot, m); err != nil {
			runRoot.Close()
			return err
		}
	}
	verificationLock, err := acquirePosthocVerificationLock(runRoot)
	if err != nil {
		return err
	}
	defer verificationLock.Close()
	parent, err := openTrashParent(root, true)
	if err != nil {
		return err
	}
	defer parent.Close()
	dst := filepath.Join(trashDirName, runID)
	if _, err := parent.Lstat(dst); err == nil {
		return errRunExists
	}
	if err := parent.Rename(filepath.Join(filepath.Base(root), runID), dst); err != nil {
		return err
	}
	removeViewRunIndexEntry(root, runID)
	return nil
}

// closeOutGrace is how long after a run's recorded end its close-out — the
// repository measurement, the checks, the report — is assumed to still be
// running when the report is not there yet. After it, a missing report is a
// close-out that died, and the run may go.
const closeOutGrace = time.Hour

// runOpen says whether a run is still being written: a session whose
// recorder holds the session lock, a trace whose repository lock is held,
// a run with no recorded ending whose recorder may still be alive, or one
// whose close-out has not filed the report yet.
func runOpen(root, dir string, m storage.Manifest) error {
	runRoot, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer runRoot.Close()
	return runOpenFromRoot(root, runRoot, m)
}

func runOpenFromRoot(root string, runRoot *os.Root, m storage.Manifest) error {
	if m.Mode == storage.ModeSession && sessionRecorderAlive(m.SessionID) {
		return errRunOpen
	}
	if m.ExitReason == "" {
		if m.Mode != storage.ModeSession && traceRunning(root, m.CWD) {
			return errRunOpen
		}
		// No ending and no live recorder: the recorder died. The run may
		// go — once nothing has touched it for the close-out grace.
		if info, err := runRoot.Stat(manifestFile); err == nil && time.Since(info.ModTime()) < closeOutGrace {
			return errRunOpen
		}
		return nil
	}
	if _, err := runRoot.Stat(reportFile); err != nil && m.EndedAt != nil && time.Since(*m.EndedAt) < closeOutGrace {
		return errRunClosing
	}
	return nil
}

// traceRunning reports whether a traced run is being recorded in cwd's
// repository: the recorder holds the repository lock while it runs.
func traceRunning(root, cwd string) bool {
	if cwd == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	repo, err := lock.Acquire(ctx, filepath.Join(filepath.Dir(root), locksDirName), cwd)
	if errors.Is(err, lock.ErrLocked) {
		return true
	}
	if err == nil {
		repo.Release()
	}
	return false
}

// restoreRun moves a run from the trash back into the store.
func restoreRun(root, runID string) error {
	if err := checkRunID(runID); err != nil {
		return err
	}
	parent, err := openTrashParent(root, false)
	if errors.Is(err, os.ErrNotExist) {
		return errNotInTrash
	}
	if err != nil {
		return err
	}
	defer parent.Close()
	src := filepath.Join(trashDirName, runID)
	if _, err := parent.Lstat(src); err != nil {
		return errNotInTrash
	}
	dst := filepath.Join(filepath.Base(root), runID)
	if _, err := parent.Lstat(dst); err == nil {
		return errRunExists
	}
	if err := parent.MkdirAll(filepath.Base(root), 0o700); err != nil {
		return fmt.Errorf("cli: create runs directory: %w", err)
	}
	if err := parent.Rename(src, dst); err != nil {
		return err
	}
	restoreViewRunIndexEntry(root, runID)
	return nil
}

func emptyTrash(root string, afterOpen func()) (int, error) {
	parent, err := openTrashParent(root, false)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer parent.Close()
	trashRoot, err := parent.OpenRoot(trashDirName)
	if err != nil {
		return 0, err
	}
	defer trashRoot.Close()
	if afterOpen != nil {
		afterOpen()
	}
	if err := requireOpenDirectoryAt(parent, trashDirName, trashRoot); err != nil {
		return 0, err
	}
	dir, err := trashRoot.Open(".")
	if err != nil {
		return 0, err
	}
	entries, err := dir.ReadDir(-1)
	if closeErr := dir.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return 0, err
	}
	for i, entry := range entries {
		if err := requireOpenDirectoryAt(parent, trashDirName, trashRoot); err != nil {
			return i, err
		}
		if err := parent.RemoveAll(filepath.Join(trashDirName, entry.Name())); err != nil {
			return i, err
		}
	}
	return len(entries), nil
}

func listTrash(root string, afterOpen func()) ([]runSummary, int, error) {
	trashRoot, err := openTrashRoot(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer trashRoot.Close()
	if afterOpen != nil {
		afterOpen()
	}
	return scanRunsFromRoot(trashRoot, "", nil)
}

func runTrash(args []string, stdout, stderr io.Writer) int {
	root, err := runsRoot()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	trash := trashRootFor(root)
	switch {
	case len(args) == 0:
		runs, unreadable, err := listTrash(root, nil)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitFailure
		}
		if len(runs) == 0 && unreadable == 0 {
			fmt.Fprintln(stdout, "The trash is empty.")
			return 0
		}
		fmt.Fprintln(stdout, "RUN ID  PROVIDER  PROJECT  EXIT")
		for _, run := range runs {
			fmt.Fprintf(stdout, "%s  %s  %s  %s\n", run.ID, run.Provider, run.Project, oneLine(run.Exit))
		}
		if unreadable > 0 {
			fmt.Fprintf(stdout, "(%d unreadable)\n", unreadable)
		}
		fmt.Fprintf(stdout, "\n%d in %s. Restore one with 'agentrec trash restore <run-id>'; erase them all with 'agentrec trash empty'. Old runs go there with 'agentrec trash sweep 30d'.\n", len(runs)+unreadable, trash)
		return 0
	case len(args) == 2 && args[0] == "restore":
		if err := restoreRun(root, args[1]); err != nil {
			fmt.Fprintln(stderr, err)
			return exitFailure
		}
		fmt.Fprintf(stdout, "restored %s\n", args[1])
		return 0
	case (len(args) == 2 || len(args) == 3 && args[2] == "--dry-run") && args[0] == "sweep":
		age, err := parseAge(args[1])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitUsage
		}
		dryRun := len(args) == 3
		result, err := sweepRuns(root, age, time.Now(), dryRun)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitFailure
		}
		verb := "moved"
		if dryRun {
			verb = "would move"
		}
		for _, id := range result.Moved {
			fmt.Fprintf(stdout, "%s %s\n", verb, id)
		}
		fmt.Fprintf(stdout, "%s %d run(s) that started more than %s ago to the trash", verb, len(result.Moved), args[1])
		if len(result.Kept) > 0 {
			fmt.Fprintf(stdout, "; kept %d still held by a recorder", len(result.Kept))
		}
		if result.Skipped > 0 {
			fmt.Fprintf(stdout, "; left %d that could not be dated", result.Skipped)
		}
		fmt.Fprintln(stdout, ". Erase the trash with 'agentrec trash empty'.")
		for _, err := range result.Failed {
			fmt.Fprintln(stderr, err)
		}
		if len(result.Failed) > 0 {
			return exitFailure
		}
		return 0
	case len(args) == 1 && args[0] == "empty":
		erased, err := emptyTrash(root, nil)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitFailure
		}
		fmt.Fprintf(stdout, "erased %d run(s) from %s\n", erased, trash)
		return 0
	}
	fmt.Fprint(stderr, trashUsage)
	return exitUsage
}
