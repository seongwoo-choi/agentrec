package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// runsDirName holds the recorded runs inside the application data directory.
const runsDirName = "runs"

// listHeader names the columns; listUsage is the one accepted command shape.
const (
	listHeader = "RUN ID  PROVIDER  PROJECT  STARTED  EXIT  VERIFICATION"
	listUsage  = "usage: agentrec list [--cwd <path>] [--exit-reason <reason>] [--verification-status <status>]\n"
)

// runSummary is one row of the run table.
type runSummary struct {
	ID                   string
	Provider             string
	Project              string
	StartedAt            time.Time
	Exit                 string
	Verification         string
	VerificationWarnings int
}

// runList prints the recorded runs, newest first.
func runList(args []string, stdout, stderr io.Writer) int {
	cwd := ""
	cwdSet := false
	exitReasonFilter := ""
	exitReasonSet := false
	verificationFilter := ""
	verificationSet := false
	for len(args) > 0 {
		if len(args) < 2 {
			fmt.Fprint(stderr, listUsage)
			return 2
		}
		switch args[0] {
		case "--cwd":
			if cwdSet {
				fmt.Fprint(stderr, listUsage)
				return 2
			}
			cwdSet = true
			var err error
			cwd, err = filepath.Abs(args[1])
			if err != nil {
				fmt.Fprintf(stderr, "cli: resolve working directory: %v\n", err)
				return 1
			}
		case "--exit-reason":
			if exitReasonSet {
				fmt.Fprint(stderr, listUsage)
				return 2
			}
			exitReasonSet = true
			exitReasonFilter = args[1]
		case "--verification-status":
			if verificationSet {
				fmt.Fprint(stderr, listUsage)
				return 2
			}
			verificationSet = true
			verificationFilter = args[1]
		default:
			fmt.Fprint(stderr, listUsage)
			return 2
		}
		args = args[2:]
	}
	root, err := runsRoot()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	runs, unreadable, err := listRunsForTable(root, cwd, exitReasonSet, exitReasonFilter)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if verificationSet {
		runs = slices.DeleteFunc(runs, func(run runSummary) bool {
			return oneLine(run.Verification) != verificationFilter
		})
	}

	if len(runs) == 0 {
		fmt.Fprint(stdout, "No runs.\n")
	} else {
		fmt.Fprintln(stdout, listHeader)
		for _, r := range runs {
			fmt.Fprintln(stdout, strings.Join([]string{
				oneLine(r.ID),
				oneLine(r.Provider),
				oneLine(r.Project),
				r.StartedAt.UTC().Format(time.RFC3339),
				oneLine(r.Exit),
				oneLine(r.Verification),
			}, "  "))
		}
	}
	// Entries that could not be read are counted rather than hidden: a run
	// missing from the table is exactly what an operator needs to know about. It
	// is a diagnostic about the table and not a row of it, so it goes to stderr,
	// where it cannot be mistaken for a run by whatever reads the table.
	if unreadable > 0 {
		fmt.Fprintf(stderr, "Warnings: %d unreadable run(s).\n", unreadable)
	}
	return 0
}

// listRunsForTable reads the manifest and verification for a row through one
// held run root. Exit filtering happens before verification is opened, so a
// narrowed list does not inspect evidence for rows it will discard.
func listRunsForTable(root, cwd string, exitReasonSet bool, exitReasonFilter string) ([]runSummary, int, error) {
	return scanRuns(root, cwd, func(runRoot *os.Root, run *runSummary) (bool, error) {
		if exitReasonSet && oneLine(run.Exit) != exitReasonFilter {
			return false, nil
		}
		verification, err := readVerificationFromRoot(runRoot)
		if err != nil {
			return false, err
		}
		run.Verification = verificationNotRun
		if verification != nil {
			run.Verification = verdict(verification.Status)
			run.VerificationWarnings = len(verification.Warnings)
		}
		return true, nil
	})
}

// listRuns summarizes readable runs under root, newest first, and reports how
// many run directories it could not read. When cwd is nonempty, it includes
// only runs recorded there. An entry that never was a run —
// a stray file, a symlink, a name no run could have — is passed over silently:
// only a run directory that will not open is a run missing from the table. A
// runs directory that does not exist yet is a tool that has recorded nothing,
// not a failure.
func listRuns(root, cwd string) ([]runSummary, int, error) {
	return scanRuns(root, cwd, nil)
}

type runEnricher func(*os.Root, *runSummary) (bool, error)

func scanRuns(root, cwd string, enrich runEnricher) ([]runSummary, int, error) {
	runsRoot, err := os.OpenRoot(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("cli: read runs directory: %w", err)
	}
	defer runsRoot.Close()
	return scanRunsFromRoot(runsRoot, cwd, enrich)
}

func scanRunsFromRoot(root *os.Root, cwd string, enrich runEnricher) ([]runSummary, int, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, 0, fmt.Errorf("cli: read runs directory: %w", err)
	}
	entries, err := dir.ReadDir(-1)
	if closeErr := dir.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, 0, fmt.Errorf("cli: read runs directory: %w", err)
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})

	var runs []runSummary
	unreadable := 0
	for _, entry := range entries {
		if err := validateRunID(entry.Name()); err != nil {
			continue
		}
		runRoot, err := openRunRootFromRoot(root, entry.Name())
		if err != nil {
			continue
		}
		manifest, err := readManifestFromRoot(runRoot)
		if err != nil {
			runRoot.Close()
			unreadable++
			continue
		}
		if cwd != "" && (!filepath.IsAbs(manifest.CWD) || filepath.Clean(manifest.CWD) != cwd) {
			runRoot.Close()
			continue
		}
		run := runSummary{
			ID:        entry.Name(),
			Provider:  manifest.Provider,
			Project:   projectName(manifest.CWD),
			StartedAt: manifest.StartedAt,
			Exit:      exitReason(manifest, nil),
		}
		include := true
		if enrich != nil {
			include, err = enrich(runRoot, &run)
		}
		runRoot.Close()
		if err != nil {
			unreadable++
			continue
		}
		if include {
			runs = append(runs, run)
		}
	}

	// Newest first, and runs that started in the same instant are ordered by
	// their ID, so the same bundles always list in the same order.
	slices.SortFunc(runs, func(a, b runSummary) int {
		if !a.StartedAt.Equal(b.StartedAt) {
			return b.StartedAt.Compare(a.StartedAt)
		}
		return strings.Compare(b.ID, a.ID)
	})
	return runs, unreadable, nil
}

// runsRoot is where runs are recorded: under AGENTREC_HOME when the operator
// set one, and under the usual user data directory otherwise.
func runsRoot() (string, error) {
	if home := os.Getenv("AGENTREC_HOME"); home != "" {
		return filepath.Join(home, runsDirName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cli: locate data directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "agentrec", runsDirName), nil
}

// projectName names the checkout a run was recorded in by the last element of
// its working directory, which is what tells runs of different repositories
// apart in the table. Only an absolute path names a directory on the machine
// the run happened on: a recorded value that is not one, or that ends in no
// name of its own, is reported as unknown rather than guessed at, so the column
// is never a claim the manifest did not make.
func projectName(cwd string) string {
	if !filepath.IsAbs(cwd) {
		return unknownValue
	}
	switch base := filepath.Base(filepath.Clean(cwd)); base {
	case string(filepath.Separator), ".", "..":
		return unknownValue
	default:
		return base
	}
}

// oneLine makes a recorded value safe to print in a table row: control
// characters — newlines that would forge rows, escapes that would drive the
// terminal — become visible escapes, and printable text is left as it is.
func oneLine(s string) string {
	quoted := strconv.QuoteToGraphic(s)
	return quoted[1 : len(quoted)-1]
}
