package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/seongwoo-choi/agentrec/internal/action"
	"github.com/seongwoo-choi/agentrec/internal/report"
	"github.com/seongwoo-choi/agentrec/internal/storage"
)

// Bundle members this command reads. Provider events are never read: they are
// raw provider material, and a report is built only from normalized actions and
// the run's own bookkeeping.
const (
	manifestFile = "manifest.json"
	actionsFile  = "actions.jsonl"
	processDir   = "process"
	resultFile   = "result.json"
)

// Reading bounds. A bundle is written by this tool but read back from the
// filesystem, where anything may have replaced it, so every read is bounded
// rather than trusted: whole documents are capped, one action line is capped,
// and a run may hold only so many actions.
const (
	maxDocumentBytes     = 1 << 20
	maxActionBytes       = 4 << 20
	maxActionStreamBytes = 64 << 20
	maxActions           = 100000
)

// latestRun names the newest run instead of one particular run.
const latestRun = "latest"

// unknownValue stands in for a field a run has not recorded, so an unfinished
// or interrupted run reports what is missing rather than an empty column.
const unknownValue = "unknown"

// processResult mirrors process/result.json. ExitCode is a pointer because a
// process killed by a signal has none, which is different from having exited 0.
type processResult struct {
	DurationMillis int64  `json:"durationMillis"`
	ExitCode       *int   `json:"exitCode"`
	Signal         string `json:"signal"`
	ExitReason     string `json:"exitReason"`
}

// runShow renders one recorded run. Usage failures are told apart from reading
// failures by their exit code: 2 means the command was called wrongly, 1 means
// the run could not be read.
func runShow(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprint(stderr, "usage: agentrec show <run-id>|latest\n")
		return 2
	}
	if args[0] != latestRun {
		if err := validateRunID(args[0]); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}

	root, err := runsRoot()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	runID := args[0]
	if runID == latestRun {
		if runID, err = newestRunID(root); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	rep, err := readRun(root, runID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := report.RenderTerminal(stdout, rep); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// newestRunID names the run to show when the operator asked for the latest one.
// A run directory that will not say when it started makes the ordering unknown
// rather than shorter, so the difficulty is reported instead of the newest run
// that happened to be readable.
func newestRunID(root string) (string, error) {
	runs, unreadable, err := listRuns(root)
	if err != nil {
		return "", err
	}
	if unreadable > 0 {
		return "", fmt.Errorf("cli: %d run(s) under %s are unreadable, so the latest one cannot be told: name a run id", unreadable, root)
	}
	if len(runs) == 0 {
		return "", fmt.Errorf("cli: no runs recorded under %s", root)
	}
	return runs[0].ID, nil
}

// readRun builds the report for one run out of its manifest, its action stream
// and, when the run has ended, its process result.
func readRun(root, runID string) (report.Report, error) {
	dir, err := runDir(root, runID)
	if err != nil {
		return report.Report{}, err
	}
	manifest, err := readManifest(dir)
	if err != nil {
		return report.Report{}, err
	}
	actions, err := readActions(dir)
	if err != nil {
		return report.Report{}, err
	}
	result, err := readProcessResult(dir)
	if err != nil {
		return report.Report{}, err
	}
	// Repository and verification evidence are recorded by later phases; until
	// then those sections have nothing to report and say so.
	return report.Report{Actions: actions, Supervisor: supervisorFields(manifest, result)}, nil
}

// runDir locates a run directory. Lstat, so a symlink standing where a run
// should be is refused rather than followed out of the runs root.
func runDir(root, runID string) (string, error) {
	dir := filepath.Join(root, runID)
	info, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("cli: read run %q: %w", runID, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("cli: run %q is not a directory", runID)
	}
	return dir, nil
}

func readManifest(dir string) (storage.Manifest, error) {
	raw, err := readDocument(dir, manifestFile)
	if err != nil {
		return storage.Manifest{}, err
	}
	var manifest storage.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return storage.Manifest{}, fmt.Errorf("cli: read %s: %w", manifestFile, err)
	}
	return manifest, nil
}

// readProcessResult reads how the provider process ended. The file is absent
// for a run that is still going, or was recorded before the supervisor wrote
// one, so a missing result is not an error.
func readProcessResult(dir string) (*processResult, error) {
	raw, err := readDocument(dir, filepath.Join(processDir, resultFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result processResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("cli: read %s: %w", resultFile, err)
	}
	return &result, nil
}

// readDocument reads one whole bundle file, bounded. The size is taken from the
// open file itself, so what is read is what was measured.
func readDocument(dir, name string) ([]byte, error) {
	f, err := openRegular(dir, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	raw, err := io.ReadAll(io.LimitReader(f, maxDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("cli: read %s: %w", filepath.Base(name), err)
	}
	if len(raw) > maxDocumentBytes {
		return nil, fmt.Errorf("cli: %s is larger than %d bytes", filepath.Base(name), maxDocumentBytes)
	}
	return raw, nil
}

// readActions streams the action timeline. It is read line by line, and both
// the line length and the number of actions are bounded, so a bundle that grew
// pathologically is reported rather than loaded.
func readActions(dir string) ([]action.Action, error) {
	f, err := openRegular(dir, actionsFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(nil, maxActionBytes)

	var actions []action.Action
	scanned := 0
	for line := 1; scanner.Scan(); line++ {
		// The newline the scanner dropped is counted too, so what is measured is
		// the stream on disk rather than the part of it that was kept.
		scanned += len(scanner.Bytes()) + 1
		if scanned > maxActionStreamBytes {
			return nil, fmt.Errorf("cli: %s is larger than %d bytes", actionsFile, maxActionStreamBytes)
		}
		if len(strings.TrimSpace(scanner.Text())) == 0 {
			continue
		}
		if len(actions) == maxActions {
			return nil, fmt.Errorf("cli: %s holds more than %d actions", actionsFile, maxActions)
		}
		var a action.Action
		if err := json.Unmarshal(scanner.Bytes(), &a); err != nil {
			return nil, fmt.Errorf("cli: %s line %d is not a recorded action: %w", actionsFile, line, err)
		}
		actions = append(actions, a)
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, fmt.Errorf("cli: %s holds a line longer than %d bytes", actionsFile, maxActionBytes)
		}
		return nil, fmt.Errorf("cli: read %s: %w", actionsFile, err)
	}
	return actions, nil
}

// openRegular opens a bundle file, confined to the run directory it belongs to.
// The path is resolved inside a root that cannot be escaped, every component is
// checked as itself so a symlink anywhere along it is refused rather than
// followed, and the handle that comes back is proved to be the file that was
// checked, so a name pointed somewhere else in between is caught.
func openRegular(dir, name string) (*os.File, error) {
	base := filepath.Base(name)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("cli: read %s: %w", base, err)
	}
	defer root.Close()

	checked, err := lstatConfined(root, name)
	if err != nil {
		return nil, err
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("cli: read %s: %w", base, err)
	}
	opened, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("cli: read %s: %w", base, err)
	}
	if !os.SameFile(checked, opened) {
		f.Close()
		return nil, fmt.Errorf("cli: %s changed while it was being opened", name)
	}
	return f, nil
}

// lstatConfined walks a bundle-relative path one component at a time and
// reports the last one as it is on disk. Every component is required to be what
// a bundle holds — directories on the way, a regular file at the end — so a
// symlink standing anywhere along the path is refused as itself instead of
// leading the read out of the run.
func lstatConfined(root *os.Root, name string) (os.FileInfo, error) {
	parts := strings.Split(filepath.ToSlash(name), "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("cli: %q does not name a file inside the run", name)
		}
	}
	for i := 1; i < len(parts); i++ {
		at := strings.Join(parts[:i], "/")
		info, err := root.Lstat(at)
		if err != nil {
			return nil, fmt.Errorf("cli: read %s: %w", at, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("cli: %s is not a directory", at)
		}
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("cli: read %s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("cli: %s is not a regular file", name)
	}
	return info, nil
}

// supervisorFields summarizes how the run was executed, in a fixed order. The
// fields that only a finished run has are omitted when it has none, rather than
// reported as zero.
func supervisorFields(m storage.Manifest, result *processResult) []report.Field {
	fields := []report.Field{{Name: "Provider", Value: m.Provider}}
	if m.ProviderVersion != "" {
		fields = append(fields, report.Field{Name: "Version", Value: m.ProviderVersion})
	}
	fields = append(fields, report.Field{Name: "Exit Reason", Value: exitReason(m, result)})
	if result != nil && result.ExitCode != nil {
		fields = append(fields, report.Field{Name: "Exit Code", Value: strconv.Itoa(*result.ExitCode)})
	}
	if result != nil && result.Signal != "" {
		fields = append(fields, report.Field{Name: "Signal", Value: result.Signal})
	}
	return append(fields,
		report.Field{Name: "Duration", Value: runDuration(m, result)},
		report.Field{Name: "Warnings", Value: strconv.Itoa(m.WarningCount)},
	)
}

// exitReason prefers the manifest, which is what the recorder concluded about
// the run as a whole, and falls back to what the supervisor saw the process do.
func exitReason(m storage.Manifest, result *processResult) string {
	switch {
	case m.ExitReason != "":
		return m.ExitReason
	case result != nil && result.ExitReason != "":
		return result.ExitReason
	}
	return unknownValue
}

// runDuration reports how long the run took: the supervisor's measurement when
// there is one, otherwise the manifest's own two ends. A run with neither is
// one still going, or one whose ending was never recorded.
func runDuration(m storage.Manifest, result *processResult) string {
	if result != nil {
		return (time.Duration(result.DurationMillis) * time.Millisecond).String()
	}
	if m.EndedAt != nil && !m.EndedAt.Before(m.StartedAt) {
		return m.EndedAt.Sub(m.StartedAt).String()
	}
	return unknownValue
}

// validateRunID accepts exactly one clean, printable path component. A run ID
// comes off the command line and is reported back on the terminal, so anything
// that could name a directory other than a run under the runs root, or that
// could drive the terminal it is printed to, is refused rather than cleaned.
func validateRunID(runID string) error {
	switch {
	case runID == "":
		return errors.New("cli: empty run id")
	case runID == "." || runID == "..":
		return fmt.Errorf("cli: run id %q is not a name", runID)
	case strings.ContainsFunc(runID, isControlOrFormat):
		return fmt.Errorf("cli: run id %q holds a control character", runID)
	case strings.ContainsRune(runID, '/') || strings.ContainsRune(runID, os.PathSeparator):
		return fmt.Errorf("cli: run id %q contains a path separator", runID)
	case filepath.Clean(runID) != runID:
		return fmt.Errorf("cli: run id %q is not a clean path component", runID)
	}
	return nil
}

// isControlOrFormat reports the characters a run ID may not hold: the control
// characters a terminal acts on, and the format characters — bidirectional
// overrides among them — that make a name read as something other than itself.
func isControlOrFormat(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}
