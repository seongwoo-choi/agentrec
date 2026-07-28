package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/seongwoo-choi/agentrec/internal/action"
	"github.com/seongwoo-choi/agentrec/internal/evidence"
	"github.com/seongwoo-choi/agentrec/internal/report"
	"github.com/seongwoo-choi/agentrec/internal/storage"
)

// Bundle members this command reads. Provider events are never read: they are
// raw provider material, and a report is built only from normalized actions and
// the run's own bookkeeping.
const (
	manifestFile  = "manifest.json"
	actionsFile   = "actions.jsonl"
	processDir    = "process"
	resultFile    = "result.json"
	gitDir        = "git"
	verifyDir     = "verification"
	verifyResults = "results.json"
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
	// maxEvidenceItems bounds the lists inside one evidence document. A run that
	// pinned more checks than this, or a warning naming more paths, is past
	// anything a reviewable verification produced.
	maxEvidenceItems = 1000
	// Counts and durations are arithmetic inputs below, not merely strings to
	// display. Refuse values no recorder run can produce before they overflow.
	maxRepositoryCount      = 1_000_000_000
	maxVerificationDuration = 2 * time.Hour
	maxVerificationExitCode = 255
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
	git, err := readGitResult(dir)
	if err != nil {
		return report.Report{}, err
	}
	verification, err := readVerification(dir)
	if err != nil {
		return report.Report{}, err
	}
	return report.Report{
		Actions:      actions,
		Supervisor:   supervisorFields(manifest, result),
		Repository:   repositoryFields(git),
		Verification: verificationFields(verification),
	}, nil
}

// gitResult mirrors git/result.json: what the repository capture measured, and
// what it says that measurement does and does not mean.
type gitResult struct {
	Status      string `json:"status"`
	Reason      string `json:"reason"`
	Attribution string `json:"attribution"`
	Baseline    string `json:"baseline"`

	TrackedFiles  int `json:"trackedFiles"`
	Added         int `json:"added"`
	Deleted       int `json:"deleted"`
	BinaryTracked int `json:"binaryTracked"`

	UntrackedFiles  int `json:"untrackedFiles"`
	StoredTextFiles int `json:"storedTextFiles"`
}

// gitAvailable is the one recorded status whose counts are a finished
// measurement. Every other status describes a collection that did not produce
// them, so its zeros are absence rather than evidence of a run that changed
// nothing.
const gitAvailable = "available"

// readGitResult reads what the run left in the repository. A run recorded
// before this evidence existed has no document, which is different from a
// document that cannot be read: the first has nothing to report and says so,
// and the second is refused.
func readGitResult(dir string) (*gitResult, error) {
	raw, err := readDocument(dir, filepath.Join(gitDir, resultFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var res gitResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("cli: read %s: %w", filepath.Join(gitDir, resultFile), err)
	}
	if res.Status == "" {
		return nil, fmt.Errorf("cli: %s does not say whether the collection ran", filepath.Join(gitDir, resultFile))
	}
	if res.Attribution != evidence.Attribution {
		return nil, fmt.Errorf("cli: %s claims %q, want the recorded attribution", filepath.Join(gitDir, resultFile), res.Attribution)
	}
	for _, count := range []struct {
		name string
		n    int
	}{
		{"trackedFiles", res.TrackedFiles},
		{"added", res.Added},
		{"deleted", res.Deleted},
		{"binaryTracked", res.BinaryTracked},
		{"untrackedFiles", res.UntrackedFiles},
		{"storedTextFiles", res.StoredTextFiles},
	} {
		if count.n < 0 || count.n > maxRepositoryCount {
			return nil, fmt.Errorf("cli: %s counts %d %s outside the recorded range", filepath.Join(gitDir, resultFile), count.n, count.name)
		}
	}
	return &res, nil
}

// readVerification reads how the pinned checks ended. As with the repository,
// an absent document is a run that asked for no verification, and an unreadable
// one is refused rather than summarized.
func readVerification(dir string) (*evidence.VerificationResult, error) {
	name := filepath.Join(verifyDir, verifyResults)
	raw, err := readDocument(dir, name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var res evidence.VerificationResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("cli: read %s: %w", name, err)
	}
	switch {
	case res.Status == "":
		return nil, fmt.Errorf("cli: %s does not say how the verification ended", name)
	case res.Attribution != evidence.VerificationAttribution:
		return nil, fmt.Errorf("cli: %s claims %q, want the recorded attribution", name, res.Attribution)
	case len(res.Checks) > maxEvidenceItems:
		return nil, fmt.Errorf("cli: %s holds more than %d checks", name, maxEvidenceItems)
	case len(res.Warnings) > maxEvidenceItems:
		return nil, fmt.Errorf("cli: %s holds more than %d warnings", name, maxEvidenceItems)
	}
	for _, check := range res.Checks {
		if check.DurationMS < 0 || check.DurationMS > maxVerificationDuration.Milliseconds() {
			return nil, fmt.Errorf("cli: %s reports check %q as having taken %dms", name, check.Name, check.DurationMS)
		}
		if check.ExitCode != nil && (*check.ExitCode < 0 || *check.ExitCode > maxVerificationExitCode) {
			return nil, fmt.Errorf("cli: %s reports check %q with exit code %d outside the recorded range", name, check.Name, *check.ExitCode)
		}
	}
	for _, warning := range res.Warnings {
		if len(warning.Paths) > maxEvidenceItems {
			return nil, fmt.Errorf("cli: %s holds a warning naming more than %d paths", name, maxEvidenceItems)
		}
	}
	return &res, nil
}

// repositoryFields summarizes what the run left in the repository, in a fixed
// order. The counts are shown only for a collection that finished with them:
// what an unfinished or failed one holds is zeros it never measured, and a
// zero read as a measurement is a run reported to have changed nothing.
func repositoryFields(res *gitResult) []report.Field {
	if res == nil {
		return nil
	}
	fields := []report.Field{{Name: "Status", Value: strings.ToUpper(res.Status)}}
	if res.Reason != "" {
		fields = append(fields, report.Field{Name: "Reason", Value: res.Reason})
	}
	if res.Status == gitAvailable {
		fields = append(fields,
			report.Field{Name: "Files", Value: fmt.Sprintf("%d (%d tracked, %d untracked)", res.TrackedFiles+res.UntrackedFiles, res.TrackedFiles, res.UntrackedFiles)},
			report.Field{Name: "Diff", Value: fmt.Sprintf("+%d/-%d, %d binary", res.Added, res.Deleted, res.BinaryTracked)},
			report.Field{Name: "Stored Text", Value: strconv.Itoa(res.StoredTextFiles)},
		)
	}
	if res.Baseline != "" {
		fields = append(fields, report.Field{Name: "Baseline", Value: res.Baseline})
	}
	// Said on every result: what a difference means is not something a reader
	// should have to supply themselves.
	return append(fields, report.Field{Name: "Attribution", Value: res.Attribution})
}

// verificationVerdicts spell the two endings an operator acts on. Every other
// status is shown as the word it was recorded under, uppercase, because a
// verification this command does not recognize is not one that passed.
var verificationVerdicts = map[string]string{
	evidence.VerificationPassed: "PASS",
	"failed":                    "FAIL",
}

// checkTimedOut is the status of a check that was still running when its own
// timeout ran out, which is the one ending whose bound is worth repeating.
const checkTimedOut = "timeout"

func verdict(status string) string {
	if known, ok := verificationVerdicts[status]; ok {
		return known
	}
	return strings.ToUpper(status)
}

// verificationFields summarizes how the checks ended, in the order they were
// pinned. Each check and each warning is one field: they answer different
// questions — whether the work holds up, and what the checks did while asking —
// and folding either into the other would lose one of them.
func verificationFields(res *evidence.VerificationResult) []report.Field {
	if res == nil {
		return nil
	}
	fields := []report.Field{{Name: "Status", Value: verdict(res.Status)}}
	if res.Reason != "" {
		fields = append(fields, report.Field{Name: "Reason", Value: res.Reason})
	}
	fields = append(fields,
		report.Field{Name: "Config", Value: res.Config},
		report.Field{Name: "Config SHA-256", Value: res.ConfigSHA256},
	)
	for _, check := range res.Checks {
		fields = append(fields, report.Field{Name: "Check", Value: checkSummary(check)})
	}
	for _, warning := range res.Warnings {
		fields = append(fields, report.Field{Name: "Warning", Value: warningSummary(warning)})
	}
	return append(fields, report.Field{Name: "Attribution", Value: res.Attribution})
}

// checkSummary reduces one check to its verdict, the command that produced it
// and how it ended. A check with no status of its own is one that was pinned
// and never reached, which is reported as pending rather than as an absence.
func checkSummary(check evidence.VerificationCheck) string {
	status := "PENDING"
	if check.Status != "" {
		status = verdict(check.Status)
	}

	parts := []string{status + " " + check.Name, quoteArgv(check.Command)}
	if check.DurationMS > 0 {
		parts = append(parts, (time.Duration(check.DurationMS) * time.Millisecond).String())
	}
	if check.ExitCode != nil {
		parts = append(parts, "exit "+strconv.Itoa(*check.ExitCode))
	}
	if check.Signal != "" {
		parts = append(parts, "signal "+check.Signal)
	}
	if check.Status == checkTimedOut && check.Timeout != "" {
		parts = append(parts, "timeout "+check.Timeout)
	}
	return strings.Join(parts, "  ")
}

// quoteArgv shows a command as the list of arguments it was launched as, each
// quoted, so that a reader can tell one argument holding a space from two
// arguments — and so that no argument reads as a shell line that was never one.
func quoteArgv(argv []string) string {
	quoted := make([]string, 0, len(argv))
	for _, arg := range argv {
		quoted = append(quoted, strconv.Quote(arg))
	}
	return strings.Join(quoted, " ")
}

// warningSummary names what was observed and the paths it was observed about,
// sorted, so that two runs that saw the same thing read the same. The recorded
// slice is copied rather than sorted in place: it is evidence this command was
// handed, not its own.
func warningSummary(warning evidence.VerificationWarning) string {
	if len(warning.Paths) == 0 {
		return warning.Code
	}
	paths := slices.Clone(warning.Paths)
	slices.Sort(paths)
	return warning.Code + "  " + strings.Join(paths, ", ")
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
