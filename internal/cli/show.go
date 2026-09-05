package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/seongwoo-choi/agentrec/internal/action"
	"github.com/seongwoo-choi/agentrec/internal/evidence"
	"github.com/seongwoo-choi/agentrec/internal/report"
	"github.com/seongwoo-choi/agentrec/internal/storage"
	usageartifact "github.com/seongwoo-choi/agentrec/internal/usage"
)

// Bundle members this command reads. Provider events are never read: they are
// raw provider material, and a report is built only from normalized actions and
// the run's own bookkeeping.
const (
	manifestFile      = "manifest.json"
	actionsFile       = "actions.jsonl"
	processDir        = "process"
	resultFile        = "result.json"
	gitDir            = "git"
	verifyDir         = "verification"
	verifyResults     = "results.json"
	providerUsageFile = "provider-usage.json"
	// unparsedFile is named in the report rather than read: the lines in it are
	// provider material this command does not render, and pointing at the file is
	// how a reader is told where to find them.
	unparsedFile = "provider-stdout.unparsed.log"
)

// Reading bounds. A bundle is written by this tool but read back from the
// filesystem, where anything may have replaced it, so every read is bounded
// rather than trusted: whole documents are capped, one action line is capped,
// and a run may hold only so many actions.
const (
	maxDocumentBytes     = 1 << 20
	maxActionBytes       = storage.MaxStreamLineBytes
	maxActionStreamBytes = storage.MaxStreamBytes
	maxActions           = storage.MaxStreamEntries
	maxUnparsedLines     = storage.MaxStreamEntries
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
	if len(args) < 1 || len(args) > 2 || len(args) == 2 && args[1] != "--failures-only" {
		fmt.Fprint(stderr, "usage: agentrec show <run-id>|latest [--failures-only]\n")
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

	rep, err := readRunWithOptions(root, runID, showOptions{failuresOnly: len(args) == 2})
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

type showOptions struct {
	failuresOnly bool
}

// newestRunID names the run to show when the operator asked for the latest one.
// A run directory that will not say when it started makes the ordering unknown
// rather than shorter, so the difficulty is reported instead of the newest run
// that happened to be readable.
func newestRunID(root string) (string, error) {
	runs, unreadable, err := listRuns(root, "")
	if err != nil {
		return "", err
	}
	if unreadable > 0 {
		return "", fmt.Errorf("cli: %d run(s) under %s are unreadable, so the latest one cannot be told: name a run id", unreadable, root)
	}
	if len(runs) == 0 {
		return "", fmt.Errorf("%w under %s", errNoRuns, root)
	}
	return runs[0].ID, nil
}

// errNoRuns reports an empty runs root. Readers of one run refuse it; the
// viewer, which shows the list of runs, opens on the empty list instead.
var errNoRuns = errors.New("cli: no runs recorded")

// readRun builds the report for one run out of one held run-directory identity.
func readRun(root, runID string) (report.Report, error) {
	return readRunWithOptions(root, runID, showOptions{})
}

func readRunWithOptions(root, runID string, opts showOptions) (report.Report, error) {
	if _, err := runDir(root, runID); err != nil {
		return report.Report{}, err
	}
	runRoot, err := openRunRoot(root, runID)
	if err != nil {
		return report.Report{}, err
	}
	defer runRoot.Close()
	return readRunFromRootWithOptions(runRoot, opts)
}

func readRunFromRoot(root *os.Root) (report.Report, error) {
	return readRunFromRootWithOptions(root, showOptions{})
}

func readRunFromRootWithOptions(root *os.Root, opts showOptions) (report.Report, error) {
	manifest, err := readManifestFromRoot(root)
	if err != nil {
		return report.Report{}, err
	}
	if err := validateUnparsedStreamFromRoot(root, manifest.UnparsedLines); err != nil {
		return report.Report{}, err
	}
	actions, err := readActionsFromRoot(root)
	if err != nil {
		return report.Report{}, err
	}
	result, err := readProcessResultFromRoot(root)
	if err != nil {
		return report.Report{}, err
	}
	git, err := readGitResultFromRoot(root)
	if err != nil {
		return report.Report{}, err
	}
	verification, err := readVerificationFromRoot(root)
	if err != nil {
		return report.Report{}, err
	}
	reportedUsage, err := readProviderUsageFromRoot(root, manifest.Provider)
	if err != nil {
		return report.Report{}, err
	}
	ownVerificationRan := verification != nil
	supervisorFailure := false
	if opts.failuresOnly {
		actions = slices.DeleteFunc(actions, func(a action.Action) bool { return !report.ActionFailed(a) })
		supervisorFailure = supervisorFailed(manifest, result)
		verification = failureVerification(verification)
		reportedUsage = nil
	}
	rep := report.Report{
		Actions:       actions,
		ProviderUsage: providerUsageFields(reportedUsage),
		Supervisor:    supervisorFields(manifest, result),
		Repository:    repositoryFields(git),
		Verification:  verificationFields(verification),
	}
	if opts.failuresOnly && !supervisorFailure {
		rep.Supervisor = nil
	}
	rep.Repository, rep.Verification = appendSessionEvidence(manifest, rep.Repository, rep.Verification)
	posthoc, meta, err := readPosthocVerificationFromRoot(root)
	if err != nil {
		return report.Report{}, err
	}
	if opts.failuresOnly {
		posthoc = failureVerification(posthoc)
	}
	rep.Verification = append(rep.Verification, posthocFields(posthoc, meta, ownVerificationRan)...)
	return rep, nil
}

func supervisorFailed(manifest storage.Manifest, result *processResult) bool {
	if viewStatusClass(exitReason(manifest, result)) == "fail" {
		return true
	}
	if result == nil {
		return false
	}
	return result.ExitCode != nil && *result.ExitCode != 0 || result.Signal != ""
}

func failureVerification(result *evidence.VerificationResult) *evidence.VerificationResult {
	if result == nil {
		return nil
	}
	failedChecks := slices.DeleteFunc(slices.Clone(result.Checks), func(check evidence.VerificationCheck) bool {
		return !isFailureVerificationStatus(check.Status)
	})
	if !isFailureVerificationStatus(result.Status) && len(failedChecks) == 0 {
		return nil
	}
	filtered := *result
	filtered.Checks = failedChecks
	if !isFailureVerificationStatus(result.Status) && len(failedChecks) > 0 {
		filtered.Status = "inconsistent"
	}
	return &filtered
}

func isFailureVerificationStatus(status string) bool {
	switch strings.ToLower(status) {
	case "", evidence.VerificationPassed, "pending":
		return false
	default:
		return true
	}
}

// What a session-mode bundle says where a traced run reports its process and
// its evidence window. The supervisor never saw a process, the baseline was
// pinned when the session's first hook arrived rather than before anything
// ran, and the reader is told both before reading anything else.
const (
	sessionSupervisorStatus = "NOT OBSERVED (interactive session: agentrec was not the parent process; exit code and signal unknown)"
	sessionRepositoryWindow = "baseline pinned at the SessionStart hook, not before the process started; measured after the session ended; the checkout was open to the operator in between"
	sessionVerificationPin  = "at the SessionStart hook; run after the session ended"
)

// sessionEndedBy names what ended a session-mode run. The session's own end is
// a hook's report — anything running as the operator could have sent it — and
// the reader is told that where they read the exit reason.
func sessionEndedBy(reason string) string {
	switch reason {
	case reasonSessionEnded:
		return "the provider's SessionEnd hook, as reported; agentrec did not observe the process end"
	case reasonSessionLost:
		return "the recorder, after no hook delivery for the idle timeout or on a signal; the session's own end was not seen"
	case reasonRunning:
		return "nothing yet: the session is still open and its recorder is running"
	}
	return "nothing recorded: the recorder ended without writing how the session ended"
}

// appendSessionEvidence tells the reader of a session bundle over what window
// the evidence was measured, beside the evidence itself. Every reader — the
// terminal report and the viewer — goes through here, so they say the same
// thing. A traced run's fields are returned as they are.
func appendSessionEvidence(m storage.Manifest, repository, verification []report.Field) ([]report.Field, []report.Field) {
	if m.Mode != storage.ModeSession {
		return repository, verification
	}
	repository = append(repository, report.Field{Name: "Window", Value: sessionRepositoryWindow})
	if len(verification) > 0 {
		verification = append(verification, report.Field{Name: "Pinned", Value: sessionVerificationPin})
	}
	return repository, verification
}

func providerUsageFields(reported *usageartifact.Report) []report.Field {
	if reported == nil {
		return nil
	}
	fields := []report.Field{
		{Name: "Attribution", Value: reported.Attribution},
		{Name: "Provider", Value: reported.Provider},
		{Name: "Scope", Value: reported.Scope},
	}
	if reported.Source == usageartifact.SourceTranscript {
		fields = append(fields, report.Field{Name: "Source", Value: "the provider's transcript, read at session end (the provider's own format, undocumented)"})
	}
	if reported.Model != "" {
		fields = append(fields, report.Field{Name: "Model", Value: reported.Model})
	}
	appendTokens := func(name string, value *int64) {
		if value != nil {
			fields = append(fields, report.Field{Name: name, Value: strconv.FormatInt(*value, 10)})
		}
	}
	appendTokens("Input Tokens", reported.InputTokens)
	appendTokens("Cached Input Tokens", reported.CachedInputTokens)
	appendTokens("Cache Creation Input Tokens", reported.CacheCreationInputTokens)
	appendTokens("Output Tokens", reported.OutputTokens)
	if reported.CostUSD != nil {
		fields = append(fields, report.Field{Name: "Cost USD", Value: strconv.FormatFloat(*reported.CostUSD, 'f', -1, 64)})
	}
	return fields
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
	return decodeGitResult(raw, err)
}

func readGitResultFromRoot(root *os.Root) (*gitResult, error) {
	raw, err := readDocumentFromRoot(root, filepath.Join(gitDir, resultFile))
	return decodeGitResult(raw, err)
}

func decodeGitResult(raw []byte, err error) (*gitResult, error) {
	name := filepath.Join(gitDir, resultFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var res gitResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("cli: read %s: %w", name, err)
	}
	if res.Status == "" {
		return nil, fmt.Errorf("cli: %s does not say whether the collection ran", name)
	}
	if res.Attribution != evidence.Attribution {
		return nil, fmt.Errorf("cli: %s claims %q, want the recorded attribution", name, res.Attribution)
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
	return decodeVerification(raw, name)
}

func readVerificationFromRoot(root *os.Root) (*evidence.VerificationResult, error) {
	name := filepath.Join(verifyDir, verifyResults)
	raw, err := readDocumentFromRoot(root, name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeVerification(raw, name)
}

func decodeVerification(raw []byte, name string) (*evidence.VerificationResult, error) {
	return decodeVerificationAs(raw, name, evidence.VerificationAttribution)
}

// decodeVerificationAs reads a verification document that must carry the
// attribution the reader expects: the run-end one, or a later one.
func decodeVerificationAs(raw []byte, name, attribution string) (*evidence.VerificationResult, error) {
	var res evidence.VerificationResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("cli: read %s: %w", name, err)
	}
	switch {
	case res.Status == "":
		return nil, fmt.Errorf("cli: %s does not say how the verification ended", name)
	case res.Attribution != attribution:
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
	fields := []report.Field{{Name: "Status", Value: repositoryStatusLabel(res.Status)}}
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

// verificationNotRun is the verification column and tile of a run that was
// asked for no verification. Three absences get three words, because they
// mean three things: NOT RUN (no checks were requested), NOT OBSERVED (no
// process was supervised), NOT RECORDED (no repository measurement was made).
const verificationNotRun = "NOT RUN"

// repositoryStatusLabel spells the repository evidence status as recorded.
func repositoryStatusLabel(status string) string {
	if status == "unavailable" {
		return "NOT RECORDED"
	}
	return strings.ToUpper(status)
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
	return decodeManifest(raw)
}

func readManifestFromRoot(root *os.Root) (storage.Manifest, error) {
	raw, err := readDocumentFromRoot(root, manifestFile)
	if err != nil {
		return storage.Manifest{}, err
	}
	return decodeManifest(raw)
}

func decodeManifest(raw []byte) (storage.Manifest, error) {
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
	return decodeProcessResult(raw, err)
}

func readProcessResultFromRoot(root *os.Root) (*processResult, error) {
	raw, err := readDocumentFromRoot(root, filepath.Join(processDir, resultFile))
	return decodeProcessResult(raw, err)
}

func decodeProcessResult(raw []byte, err error) (*processResult, error) {
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

// readProviderUsage reads the optional provider-reported usage artifact. Runs
// recorded before the artifact existed legitimately have no such file.
func readProviderUsage(dir, provider string) (*usageartifact.Report, error) {
	raw, err := readDocument(dir, providerUsageFile)
	return decodeProviderUsage(raw, err, provider)
}

func readProviderUsageFromRoot(root *os.Root, provider string) (*usageartifact.Report, error) {
	raw, err := readDocumentFromRoot(root, providerUsageFile)
	return decodeProviderUsage(raw, err, provider)
}

func decodeProviderUsage(raw []byte, err error, provider string) (*usageartifact.Report, error) {
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var reported usageartifact.Report
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reported); err != nil {
		return nil, fmt.Errorf("cli: read %s: %w", providerUsageFile, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("more than one JSON value")
		}
		return nil, fmt.Errorf("cli: read %s: %w", providerUsageFile, err)
	}
	if err := reported.Validate(); err != nil {
		return nil, fmt.Errorf("cli: read %s: %w", providerUsageFile, err)
	}
	if reported.Provider != provider {
		return nil, fmt.Errorf("cli: read %s: provider %q does not match manifest provider %q", providerUsageFile, reported.Provider, provider)
	}
	return &reported, nil
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

func readDocumentFromRoot(root *os.Root, name string) ([]byte, error) {
	f, err := openRegularFromRoot(root, name)
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
	return decodeActions(f)
}

func readActionsFromRoot(root *os.Root) ([]action.Action, error) {
	f, err := openRegularFromRoot(root, actionsFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return decodeActions(f)
}

func decodeActions(r io.Reader) ([]action.Action, error) {
	scanner := bufio.NewScanner(r)
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

// validateUnparsedStream proves the manifest's claim that non-event stdout was
// kept in the named artifact. The provider material is not rendered, but the
// file is still opened through the same confined path as every artifact show
// reads, and both its size and line count are bounded before the claim is shown.
func validateUnparsedStream(dir string, want int) error {
	if err := validateUnparsedCount(want); err != nil || want == 0 {
		return err
	}
	f, err := openRegular(dir, unparsedFile)
	if err != nil {
		return err
	}
	defer f.Close()
	return validateUnparsedFile(f, want)
}

func validateUnparsedStreamFromRoot(root *os.Root, want int) error {
	if err := validateUnparsedCount(want); err != nil || want == 0 {
		return err
	}
	f, err := openRegularFromRoot(root, unparsedFile)
	if err != nil {
		return err
	}
	defer f.Close()
	return validateUnparsedFile(f, want)
}

func validateUnparsedCount(want int) error {
	switch {
	case want < 0:
		return fmt.Errorf("cli: manifest has a negative unparsed line count")
	case want == 0:
		return nil
	case want > maxUnparsedLines:
		return fmt.Errorf("cli: manifest holds more than %d unparsed lines", maxUnparsedLines)
	}
	return nil
}

func validateUnparsedFile(f *os.File, want int) error {
	return validateUnparsedFileContext(context.Background(), f, want)
}

func validateUnparsedFileContext(ctx context.Context, f *os.File, want int) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("cli: read %s: %w", unparsedFile, err)
	}
	return validateUnparsedReaderContext(ctx, f, info.Size(), want)
}

func validateUnparsedReaderContext(ctx context.Context, reader io.Reader, size int64, want int) error {
	if size > maxActionStreamBytes {
		return fmt.Errorf("cli: %s is larger than %d bytes", unparsedFile, maxActionStreamBytes)
	}

	scanner := bufio.NewScanner(&viewContextReader{ctx: ctx, reader: reader})
	scanner.Buffer(nil, maxActionBytes)
	got := 0
	for scanner.Scan() {
		got++
		if got > want {
			return fmt.Errorf("cli: %s holds %d lines, manifest records %d", unparsedFile, got, want)
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return fmt.Errorf("cli: %s holds a line longer than %d bytes", unparsedFile, maxActionBytes)
		}
		return fmt.Errorf("cli: read %s: %w", unparsedFile, err)
	}
	if got != want {
		return fmt.Errorf("cli: %s holds %d lines, manifest records %d", unparsedFile, got, want)
	}
	return nil
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
	return openRegularFromRoot(root, name)
}

func openRegularFromRoot(root *os.Root, name string) (*os.File, error) {
	base := filepath.Base(name)
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
	var fields []report.Field
	// A session was never supervised: the section keeps its place so a reader
	// looks for the process outcome where it always is, and finds it said to be
	// missing rather than absent.
	if m.Mode == storage.ModeSession {
		fields = append(fields,
			report.Field{Name: "Status", Value: sessionSupervisorStatus},
			report.Field{Name: "Session", Value: m.SessionID},
		)
	}
	fields = append(fields, report.Field{Name: "Provider", Value: m.Provider})
	if m.ProviderVersion != "" {
		// A version the parser does not claim to understand is never shown as a
		// bare version: what follows in the timeline was read by a parser written
		// for a different stream, and the reader is told that where they read it.
		version := m.ProviderVersion
		if m.VersionUnverified {
			version += "  (unsupported; timeline may be incomplete)"
		}
		fields = append(fields, report.Field{Name: "Version", Value: version})
	}
	reason := exitReason(m, result)
	fields = append(fields, report.Field{Name: "Exit Reason", Value: reason})
	if m.Mode == storage.ModeSession {
		// Who said the session ended matters: a traced run's end was observed,
		// a session's end is a hook's word — or the recorder giving up.
		fields = append(fields, report.Field{Name: "Ended By", Value: sessionEndedBy(reason)})
	}
	if result != nil && result.ExitCode != nil {
		fields = append(fields, report.Field{Name: "Exit Code", Value: strconv.Itoa(*result.ExitCode)})
	}
	if result != nil && result.Signal != "" {
		fields = append(fields, report.Field{Name: "Signal", Value: result.Signal})
	}
	fields = append(fields,
		report.Field{Name: "Duration", Value: runDuration(m, result)},
		report.Field{Name: "Warnings", Value: strconv.Itoa(m.WarningCount)},
	)
	// Shown only when there were any: a run whose provider emitted nothing but
	// events has no line here, rather than a zero to read past.
	if m.UnparsedLines > 0 {
		fields = append(fields, report.Field{Name: "Unparsed", Value: fmt.Sprintf("%d stdout line(s) kept in %s", m.UnparsedLines, unparsedFile)})
	}
	return fields
}

// exitReason prefers the manifest, which is what the recorder concluded about
// the run as a whole, and falls back to what the supervisor saw the process do.
func exitReason(m storage.Manifest, result *processResult) string {
	switch {
	case m.ExitReason != "":
		return m.ExitReason
	case result != nil && result.ExitReason != "":
		return result.ExitReason
	case m.Mode == storage.ModeSession && sessionRecorderAlive(m.SessionID):
		return reasonRunning
	}
	return unknownValue
}

// reasonRunning is what a session bundle reports while its recorder still
// holds the session lock: not an ending, and not an unknown one either.
const reasonRunning = "running"

// sessionRecorderAlive reports whether a recorder currently holds the lock for
// this session. The lock is exclusive while a recorder serves the session and
// released by the kernel the moment it is gone, so a shared lock that would
// block is a live recorder and anything else is not. A session recorded under
// another socket directory or another user reads as not running, which is the
// honest answer from here.
func sessionRecorderAlive(sessionID string) bool {
	socket, err := sessionSocketPath(sessionID)
	if err != nil {
		return false
	}
	lock, err := os.OpenFile(sessionLockPath(socket), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer lock.Close()
	err = syscall.Flock(int(lock.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	if err == nil {
		syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		return false
	}
	return errors.Is(err, syscall.EWOULDBLOCK)
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
