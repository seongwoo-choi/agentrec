package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
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
	trashUsage   = "usage: agentrec trash [restore <run-id> | empty]\n"
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
	src := filepath.Join(root, runID)
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("cli: %s is not a run", strconv.Quote(runID))
	}
	if m, err := readManifest(src); err == nil {
		if err := runOpen(root, src, m); err != nil {
			return err
		}
	}
	trash := trashRootFor(root)
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return fmt.Errorf("cli: create trash: %w", err)
	}
	dst := filepath.Join(trash, runID)
	if _, err := os.Lstat(dst); err == nil {
		return errRunExists
	}
	return os.Rename(src, dst)
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
	if m.Mode == storage.ModeSession && sessionRecorderAlive(m.SessionID) {
		return errRunOpen
	}
	if m.ExitReason == "" {
		if m.Mode != storage.ModeSession && traceRunning(root, m.CWD) {
			return errRunOpen
		}
		// No ending and no live recorder: the recorder died. The run may
		// go — once nothing has touched it for the close-out grace.
		if info, err := os.Stat(filepath.Join(dir, manifestFile)); err == nil && time.Since(info.ModTime()) < closeOutGrace {
			return errRunOpen
		}
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, reportFile)); err != nil && m.EndedAt != nil && time.Since(*m.EndedAt) < closeOutGrace {
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
	src := filepath.Join(trashRootFor(root), runID)
	if _, err := os.Lstat(src); err != nil {
		return errNotInTrash
	}
	dst := filepath.Join(root, runID)
	if _, err := os.Lstat(dst); err == nil {
		return errRunExists
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("cli: create runs directory: %w", err)
	}
	return os.Rename(src, dst)
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
		runs, unreadable, err := listRuns(trash, "")
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
		fmt.Fprintf(stdout, "\n%d in %s. Restore one with 'agentrec trash restore <run-id>'; erase them all with 'agentrec trash empty'.\n", len(runs)+unreadable, trash)
		return 0
	case len(args) == 2 && args[0] == "restore":
		if err := restoreRun(root, args[1]); err != nil {
			fmt.Fprintln(stderr, err)
			return exitFailure
		}
		fmt.Fprintf(stdout, "restored %s\n", args[1])
		return 0
	case len(args) == 1 && args[0] == "empty":
		entries, err := os.ReadDir(trash)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stderr, err)
			return exitFailure
		}
		erased := 0
		for _, entry := range entries {
			if err := os.RemoveAll(filepath.Join(trash, entry.Name())); err != nil {
				fmt.Fprintln(stderr, err)
				return exitFailure
			}
			erased++
		}
		fmt.Fprintf(stdout, "erased %d run(s) from %s\n", erased, trash)
		return 0
	}
	fmt.Fprint(stderr, trashUsage)
	return exitUsage
}
