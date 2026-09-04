package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/evidence"
	"github.com/seongwoo-choi/agentrec/internal/redaction"
	"github.com/seongwoo-choi/agentrec/internal/report"
)

// A run verified at its end measures the tree the agent left behind. A run
// can also be verified later: the repository's committed verification config
// is run now, in the run's repository, and the result is filed beside the
// run under its own directory, attributed as a later measurement. It never
// replaces the run-end verification, and it says whether HEAD moved since.
const (
	verifyPosthocDir = "verification-posthoc"
	// A later verification is built beside the one already filed and only
	// takes its place once it has finished: a re-run that cannot even be
	// pinned must not destroy the measurement that is already there.
	verifyPosthocPending = verifyPosthocDir + ".pending"
	verifyPosthocMeta    = "meta.json"
	verifyPosthocCurrent = "current"
	verifyPosthocLock    = ".verification-posthoc.lock"
	posthocAttribution   = evidence.VerificationAttribution + " (post-hoc)"
)

var (
	errVerifyNoRepo              = errors.New("cli: the run recorded no repository to verify in")
	errVerifyRepoGone            = errors.New("cli: the run's repository is no longer there")
	errVerifyNoConfig            = errors.New("cli: the repository has no committed " + verifyConfigFile)
	errVerifyBusy                = errors.New("cli: another verification is running")
	errVerifyPublishedNotDurable = errors.New("cli: later verification was published but directory sync failed")
	pinPosthocVerification       = evidence.PinVerificationBytesRoot
	syncPosthocDirectory         = syncPosthocDir
	openPosthocRunRoot           = openRunRoot
	acquireVerifyPosthocLock     = acquirePosthocVerificationLock
)

type posthocMeta struct {
	MeasuredAt     time.Time `json:"measuredAt"`
	HeadAtRun      string    `json:"headAtRun,omitempty"`
	HeadNow        string    `json:"headNow,omitempty"`
	HeadMovedSince *bool     `json:"headMovedSince,omitempty"`
}

// verifyRunLater runs the committed verification config of the run's
// repository now and files the result under the run. A previous later
// verification is replaced. A run a recorder still holds is refused.
func verifyRunLater(ctx context.Context, root, runID string) (evidence.VerificationResult, posthocMeta, error) {
	var none evidence.VerificationResult
	if err := checkRunID(runID); err != nil {
		return none, posthocMeta{}, err
	}
	runRoot, err := openPosthocRunRoot(root, runID)
	if err != nil {
		return none, posthocMeta{}, fmt.Errorf("cli: open run for verification: %w", err)
	}
	m, err := readManifestFromRoot(runRoot)
	if err != nil {
		runRoot.Close()
		return none, posthocMeta{}, err
	}
	if err := runOpenFromRoot(root, runRoot, m); err != nil {
		runRoot.Close()
		return none, posthocMeta{}, err
	}
	lock, err := acquireVerifyPosthocLock(runRoot)
	if err != nil {
		return none, posthocMeta{}, err
	}
	defer lock.Close()
	if err := requireInstalledRun(root, runID, lock.root); err != nil {
		return none, posthocMeta{}, err
	}
	repoRoot := m.RepoRoot
	if repoRoot == "" {
		return none, posthocMeta{}, errVerifyNoRepo
	}
	if info, err := os.Stat(repoRoot); err != nil || !info.IsDir() {
		return none, posthocMeta{}, fmt.Errorf("%w: %s", errVerifyRepoGone, repoRoot)
	}
	configPath := filepath.Join(repoRoot, verifyConfigFile)
	if _, err := os.Lstat(configPath); err != nil {
		return none, posthocMeta{}, errVerifyNoConfig
	}
	pinCtx, cancel := context.WithTimeout(ctx, verifyPinTimeout)
	defer cancel()
	// What will be executed is compared with what HEAD holds byte for byte,
	// rather than with git's opinion of whether the file changed: an index
	// marked assume-unchanged or skip-worktree makes `diff --quiet` agree
	// with a working tree that says something else entirely.
	committed, err := gitAsked(pinCtx, repoRoot, "cat-file", "blob", "HEAD:"+verifyConfigFile)
	if err != nil {
		return none, posthocMeta{}, fmt.Errorf("%w: it is not committed at HEAD", errVerifyNoConfig)
	}
	onDisk, err := os.ReadFile(configPath)
	if err != nil {
		return none, posthocMeta{}, fmt.Errorf("%w: %v", errVerifyNoConfig, err)
	}
	if !bytes.Equal(committed, onDisk) {
		return none, posthocMeta{}, fmt.Errorf("%w: it differs from HEAD", errVerifyNoConfig)
	}
	head, _ := gitAsked(pinCtx, repoRoot, "rev-parse", "HEAD")

	if err := lock.root.RemoveAll(verifyPosthocPending); err != nil {
		return none, posthocMeta{}, fmt.Errorf("cli: clear an unfinished later verification: %w", err)
	}
	red := redaction.New()
	verifier, err := pinPosthocVerification(pinCtx, repoRoot, lock.root, configPath, committed, evidence.VerificationOptions{
		Sanitize:    func(text string) (string, error) { return redactFreeText(red, text) },
		DirName:     verifyPosthocPending,
		Attribution: posthocAttribution,
	})
	if err != nil {
		return none, posthocMeta{}, err
	}
	result, runErr := verifier.Run(ctx)
	if err := verifier.Close(); runErr == nil {
		runErr = err
	}
	if runErr != nil {
		_ = lock.root.RemoveAll(verifyPosthocPending)
		return none, posthocMeta{}, runErr
	}

	meta := posthocMeta{MeasuredAt: time.Now().UTC().Truncate(time.Second), HeadNow: strings.TrimSpace(string(head))}
	if g, err := readGitResultFromRoot(lock.root); err == nil && g != nil {
		meta.HeadAtRun = g.Baseline
	}
	if meta.HeadAtRun != "" && meta.HeadNow != "" {
		moved := meta.HeadAtRun != meta.HeadNow
		meta.HeadMovedSince = &moved
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return none, posthocMeta{}, err
	}
	if err := writePosthocFile(lock.root, filepath.Join(verifyPosthocPending, verifyPosthocMeta), append(raw, '\n')); err != nil {
		_ = lock.root.RemoveAll(verifyPosthocPending)
		return none, posthocMeta{}, fmt.Errorf("cli: file the later verification: %w", err)
	}
	if err := publishPosthocVerification(lock.root); err != nil {
		return none, posthocMeta{}, fmt.Errorf("cli: file the later verification: %w", err)
	}
	return result, meta, nil
}

func writePosthocFile(root *os.Root, name string, raw []byte) (err error) {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	installed := false
	defer func() {
		if installed {
			return
		}
		removeErr := root.Remove(name)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		err = errors.Join(err, removeErr, syncPosthocDirectory(root, filepath.Dir(name)))
	}()
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = syncPosthocDirectory(root, filepath.Dir(name)); err != nil {
		return err
	}
	installed = true
	return nil
}

func publishPosthocVerification(root *os.Root) error {
	if info, err := root.Lstat(verifyPosthocDir); errors.Is(err, os.ErrNotExist) {
		if err := root.Mkdir(verifyPosthocDir, 0o700); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if !info.IsDir() {
		return errors.New("the later verification container is not a directory")
	}
	// Retry the parent sync even when a prior failed attempt left the
	// container entry behind. Its existence is not evidence of durability.
	if err := syncPosthocDirectory(root, "."); err != nil {
		return err
	}

	generation := "generation-" + newViewToken()
	generationPath := filepath.Join(verifyPosthocDir, generation)
	if err := root.Rename(verifyPosthocPending, generationPath); err != nil {
		return err
	}
	if err := syncPosthocDirectory(root, "."); err != nil {
		return err
	}
	if err := syncPosthocDirectory(root, verifyPosthocDir); err != nil {
		return err
	}
	temporaryCurrent := filepath.Join(verifyPosthocDir, ".current-"+newViewToken())
	if err := writePosthocFile(root, temporaryCurrent, []byte(generation+"\n")); err != nil {
		return err
	}
	if err := root.Rename(temporaryCurrent, filepath.Join(verifyPosthocDir, verifyPosthocCurrent)); err != nil {
		removeErr := root.Remove(temporaryCurrent)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		return errors.Join(err, removeErr, syncPosthocDirectory(root, verifyPosthocDir))
	}
	if err := syncPosthocDirectory(root, verifyPosthocDir); err != nil {
		return fmt.Errorf("%w: %v", errVerifyPublishedNotDurable, err)
	}
	return nil
}

func syncPosthocDir(root *os.Root, name string) error {
	dir, err := root.Open(name)
	if err != nil {
		return err
	}
	err = dir.Sync()
	if closeErr := dir.Close(); err == nil {
		err = closeErr
	}
	return err
}

type posthocVerificationLock struct {
	root *os.Root
	file *os.File
}

func acquirePosthocVerificationLock(root *os.Root) (*posthocVerificationLock, error) {
	file, err := root.OpenFile(verifyPosthocLock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("cli: open verification lock: %w", err)
	}
	info, statErr := root.Lstat(verifyPosthocLock)
	held, heldErr := file.Stat()
	if statErr != nil || heldErr != nil || !info.Mode().IsRegular() || !held.Mode().IsRegular() || !os.SameFile(info, held) {
		file.Close()
		root.Close()
		return nil, errors.New("cli: verification lock is not the run's regular lock file")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		root.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errVerifyBusy
		}
		return nil, fmt.Errorf("cli: lock later verification: %w", err)
	}
	return &posthocVerificationLock{root: root, file: file}, nil
}

func requireInstalledRun(root, runID string, held *os.Root) error {
	current, err := openRunRoot(root, runID)
	if err != nil {
		return fmt.Errorf("cli: run moved before verification lock: %w", err)
	}
	defer current.Close()
	heldInfo, err := held.Stat(".")
	if err != nil {
		return fmt.Errorf("cli: inspect held run after verification lock: %w", err)
	}
	currentInfo, err := current.Stat(".")
	if err != nil {
		return fmt.Errorf("cli: inspect installed run after verification lock: %w", err)
	}
	if !os.SameFile(heldInfo, currentInfo) {
		return errors.New("cli: run moved before verification lock")
	}
	return nil
}

func (l *posthocVerificationLock) Close() error {
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return errors.Join(err, l.file.Close(), l.root.Close())
}

// gitAsked reads a fact out of a repository without letting the repository's
// own configuration run anything on the way: these two questions are asked
// before the committed checks have been pinned, so they answer from the
// object store and never refresh the index.
func gitAsked(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return gitOutput(ctx, dir, append([]string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
}

// redactFreeText puts text through a redactor the way a bundle does: as the
// one field of a throwaway JSON object, whole, so a secret spanning lines is
// judged as one.
func redactFreeText(red *redaction.Redactor, text string) (string, error) {
	wrapped, err := json.Marshal(map[string]string{"evidence": text})
	if err != nil {
		return "", err
	}
	safe, err := red.RedactJSON(wrapped)
	if err != nil {
		return "", err
	}
	var unwrapped map[string]string
	if err := json.Unmarshal(safe, &unwrapped); err != nil {
		return "", err
	}
	return unwrapped["evidence"], nil
}

// readPosthocVerification reads a later verification filed under the run,
// nil when there is none.
func readPosthocVerification(dir string) (*evidence.VerificationResult, *posthocMeta, error) {
	read := func(name string) ([]byte, error) { return readDocument(dir, name) }
	return readPosthocVerificationWith(read)
}

func readPosthocVerificationFromRoot(root *os.Root) (*evidence.VerificationResult, *posthocMeta, error) {
	read := func(name string) ([]byte, error) { return readDocumentFromRoot(root, name) }
	return readPosthocVerificationWith(read)
}

func parsePosthocGeneration(name string, raw []byte) (string, error) {
	const prefix = "generation-"
	if len(raw) != len(prefix)+32+1 || raw[len(raw)-1] != '\n' {
		return "", fmt.Errorf("cli: decode %s: invalid generation", name)
	}
	generation := string(raw[:len(raw)-1])
	if filepath.Base(generation) != generation || !strings.HasPrefix(generation, prefix) {
		return "", fmt.Errorf("cli: decode %s: invalid generation", name)
	}
	for _, c := range generation[len(prefix):] {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return "", fmt.Errorf("cli: decode %s: invalid generation", name)
		}
	}
	return generation, nil
}

func readPosthocVerificationWith(read func(string) ([]byte, error)) (*evidence.VerificationResult, *posthocMeta, error) {
	base := verifyPosthocDir
	selected := false
	currentName := filepath.Join(verifyPosthocDir, verifyPosthocCurrent)
	current, err := read(currentName)
	if err == nil {
		generation, err := parsePosthocGeneration(currentName, current)
		if err != nil {
			return nil, nil, err
		}
		base = filepath.Join(verifyPosthocDir, generation)
		selected = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}

	name := filepath.Join(base, verifyResults)
	raw, err := read(name)
	if errors.Is(err, os.ErrNotExist) {
		if selected {
			return nil, nil, fmt.Errorf("cli: read %s selected by %s: %w", name, currentName, err)
		}
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	result, err := decodeVerificationAs(raw, name, posthocAttribution)
	if err != nil {
		return nil, nil, err
	}
	metaName := filepath.Join(base, verifyPosthocMeta)
	metaRaw, metaReadErr := read(metaName)
	if selected && errors.Is(metaReadErr, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("cli: read %s selected by %s: %w", metaName, currentName, metaReadErr)
	}
	meta, err := decodePosthocMeta(metaRaw, metaReadErr)
	return result, meta, err
}

// posthocFields are the rows a later verification adds under the run-end
// one: its verdict and when, its caveat, and each check. A run that was
// never verified at its end says so first, so that a later verdict standing
// alone in the section is not read as the run's own.
func posthocFields(result *evidence.VerificationResult, meta *posthocMeta, ownRan bool) []report.Field {
	if result == nil {
		return nil
	}
	var fields []report.Field
	if !ownRan {
		fields = append(fields, report.Field{Name: "Status", Value: verificationNotRun + " (this run was not verified when it ended)"})
	}
	when := "unknown time"
	if meta != nil && !meta.MeasuredAt.IsZero() {
		when = meta.MeasuredAt.Format(time.RFC3339)
	}
	fields = append(fields, report.Field{Name: "Verified later", Value: verdict(result.Status) + " at " + when + "; " + posthocCaveat(meta)})
	for _, check := range result.Checks {
		fields = append(fields, report.Field{Name: "Later check", Value: checkSummary(check)})
	}
	for _, warning := range result.Warnings {
		fields = append(fields, report.Field{Name: "Later warning", Value: warningSummary(warning)})
	}
	return append(fields, report.Field{Name: "Later attribution", Value: result.Attribution})
}

func decodePosthocMeta(raw []byte, err error) (*posthocMeta, error) {
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var meta posthocMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("cli: decode %s: %w", verifyPosthocMeta, err)
	}
	return &meta, nil
}

// posthocCaveat says what the measurement is, and what is known about the
// repository since. Whether HEAD moved has three answers, not two: a run
// with no baseline of its own cannot be compared, and saying nothing would
// read as "it did not move".
func posthocCaveat(meta *posthocMeta) string {
	const measured = "measured later, against the repository as it is now, not the tree the run left behind"
	if meta == nil || meta.HeadMovedSince == nil {
		return measured + "; whether HEAD moved since the run is not known"
	}
	if *meta.HeadMovedSince {
		return measured + "; HEAD has moved since the run"
	}
	return measured + "; HEAD has not moved since the run"
}

// runVerify is 'agentrec verify <run-id>|latest'.
func runVerify(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: agentrec verify <run-id>|latest")
		return exitUsage
	}
	root, err := runsRoot()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	runID := args[0]
	if runID == latestRun {
		runs, _, err := listRuns(root, "")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitFailure
		}
		if len(runs) == 0 {
			fmt.Fprintln(stderr, "cli: no runs recorded")
			return exitFailure
		}
		runID = runs[0].ID
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, meta, err := verifyRunLater(ctx, root, runID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "%s verified later at %s: %s\n", runID, meta.MeasuredAt.Format(time.RFC3339), verdict(result.Status))
	for _, check := range result.Checks {
		fmt.Fprintln(stdout, "  "+checkSummary(check))
	}
	fmt.Fprintln(stdout, "note: "+posthocCaveat(&meta))
	// The verdict reaches the shell the way a traced run's does: anything
	// but a pass is a failure to whatever ran this.
	if result.Status != evidence.VerificationPassed {
		return exitFailure
	}
	return 0
}
