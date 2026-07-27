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

// listHeader names the columns of the run table.
const listHeader = "RUN ID  PROVIDER  STARTED  EXIT"

// runSummary is one row of the run table.
type runSummary struct {
	ID        string
	Provider  string
	StartedAt time.Time
	Exit      string
}

// runList prints the recorded runs, newest first.
func runList(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprint(stderr, "usage: agentrec list\n")
		return 2
	}
	root, err := runsRoot()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	runs, unreadable, err := listRuns(root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if len(runs) == 0 {
		fmt.Fprint(stdout, "No runs.\n")
	} else {
		fmt.Fprintln(stdout, listHeader)
		for _, r := range runs {
			fmt.Fprintln(stdout, strings.Join([]string{
				oneLine(r.ID),
				oneLine(r.Provider),
				r.StartedAt.UTC().Format(time.RFC3339),
				oneLine(r.Exit),
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

// listRuns summarizes every readable run under root, newest first, and reports
// how many run directories it could not read. An entry that never was a run —
// a stray file, a symlink, a name no run could have — is passed over silently:
// only a run directory that will not open is a run missing from the table. A
// runs directory that does not exist yet is a tool that has recorded nothing,
// not a failure.
func listRuns(root string) ([]runSummary, int, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("cli: read runs directory: %w", err)
	}

	var runs []runSummary
	unreadable := 0
	for _, entry := range entries {
		if err := validateRunID(entry.Name()); err != nil {
			continue
		}
		dir, err := runDir(root, entry.Name())
		if err != nil {
			continue
		}
		manifest, err := readManifest(dir)
		if err != nil {
			unreadable++
			continue
		}
		runs = append(runs, runSummary{
			ID:        entry.Name(),
			Provider:  manifest.Provider,
			StartedAt: manifest.StartedAt,
			Exit:      exitReason(manifest, nil),
		})
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

// oneLine makes a recorded value safe to print in a table row: control
// characters — newlines that would forge rows, escapes that would drive the
// terminal — become visible escapes, and printable text is left as it is.
func oneLine(s string) string {
	quoted := strconv.QuoteToGraphic(s)
	return quoted[1 : len(quoted)-1]
}
