// Package storage persists one recorded run as a self-contained bundle:
// a private directory holding the run's manifest and its append-only streams,
// every byte of which passes through the run's redactor on the way in.
package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/seongwoo-choi/agentrec/internal/action"
	"github.com/seongwoo-choi/agentrec/internal/redaction"
	"github.com/seongwoo-choi/agentrec/internal/usage"
)

// Bundle files. Later phases add process, git and verification material
// alongside them.
const (
	manifestFile = "manifest.json"
	promptFile   = "prompt.txt"
	actionsFile  = "actions.jsonl"
	eventsFile   = "provider-events.sanitized.jsonl"
	// unparsedFile holds the stdout lines that were not provider events at all —
	// an update banner, a deprecation warning, anything the provider printed
	// beside its event stream. They are kept apart from the events because they
	// are not events, and kept rather than dropped because a line the recorder
	// could not read is still something the provider said.
	unparsedFile = "provider-stdout.unparsed.log"
	usageFile    = "provider-usage.json"
)

// Process artifacts describe the provider process itself and are written once,
// after it has ended. They live in their own directory so that what the run
// recorded stays separate from how the run was executed.
const (
	processDirName = "process"
	stderrFile     = "stderr.sanitized.log"
	resultFile     = "result.json"
)

// A run may hold prompts, credentials in argv and whole file contents, so the
// bundle is readable only by the user who recorded it. The modes are applied
// explicitly rather than left to the process umask, which can only ever remove
// bits and is not the recorder's to trust.
const (
	dirMode  os.FileMode = 0o700
	fileMode os.FileMode = 0o600
)

// ModeSession marks a bundle recorded from an interactive session's hooks rather
// than from a process agentrec supervised. The trace mode has no marker: a
// manifest without one was written by a recorder that was the parent process.
const ModeSession = "session"

// Manifest describes the run as a whole. RedactionRuleVersion records the rules
// that produced the markers in this bundle, so it is set from the redaction
// package rather than by the caller.
type Manifest struct {
	Provider        string   `json:"provider"`
	ProviderVersion string   `json:"providerVersion,omitempty"`
	Argv            []string `json:"argv"`
	CWD             string   `json:"cwd"`
	CanonicalCWD    string   `json:"canonicalCwd,omitempty"`
	RepoRoot        string   `json:"repoRoot,omitempty"`
	// VersionUnverified records that the provider's version was outside the
	// range agentrec's parser was written against and the run was recorded
	// anyway, on the operator's explicit say-so. What the run reports about
	// itself was read by a parser that does not claim to understand this
	// version's event stream, and every reader of this bundle is told so.
	VersionUnverified bool `json:"versionUnverified,omitempty"`
	// Mode names how the run was recorded. Empty means trace: agentrec launched
	// the provider itself and supervised the process. ModeSession means an
	// interactive session whose own hooks reported to agentrec: there was no
	// supervised process, and SessionID names the provider's session so the
	// bundle can be told apart from a run agentrec started.
	Mode                 string     `json:"mode,omitempty"`
	SessionID            string     `json:"sessionId,omitempty"`
	StartedAt            time.Time  `json:"startedAt"`
	EndedAt              *time.Time `json:"endedAt,omitempty"`
	ExitReason           string     `json:"exitReason,omitempty"`
	WarningCount         int        `json:"warningCount"`
	UnparsedLines        int        `json:"unparsedLines,omitempty"`
	RedactionRuleVersion string     `json:"redactionRuleVersion"`
}

// Finalization is what only the end of a run knows.
type Finalization struct {
	EndedAt       time.Time
	ExitReason    string
	WarningCount  int
	UnparsedLines int
}

// ErrFinalized is returned by every write once the run has been finalized, and
// by a second Finalize. The manifest on disk describes a finished run, so
// anything arriving after it belongs to a run that is no longer being recorded.
var ErrFinalized = errors.New("storage: run already finalized")

// MaxStreamLineBytes is the exclusive bound shared by bundle writers and
// readers. bufio.Scanner needs room for the line delimiter inside this bound.
const (
	MaxStreamLineBytes = 4 << 20
	MaxStreamBytes     = 64 << 20
	MaxStreamEntries   = 100000
)

// ErrNotProviderEvent reports that a line offered as a provider event was not
// one JSON object. It is the shape of the line being wrong and not this package
// failing, so a supervisor can keep the line as what it actually is — see
// WriteUnparsedLine — instead of ending the recording over it.
var ErrNotProviderEvent = redaction.ErrNotJSONObject

// Bundle is an open run directory. It holds one redactor for the whole run, so
// a secret seen in argv, in the prompt, in an action and in a provider event is
// recorded under a single marker. A Bundle is for one goroutine only and is not
// safe for concurrent use: its caller must serialize writes and finalization.
type Bundle struct {
	dir         string
	red         *redaction.Redactor
	manifest    Manifest
	parentRoot  *os.Root
	dirRoot     *os.Root
	processRoot *os.Root
	actions     *os.File
	events      *os.File
	// unparsed is opened on the first line that was not an event, so a run whose
	// provider only ever emitted events leaves no empty file claiming otherwise.
	unparsed      *os.File
	actionsState  streamState
	eventsState   streamState
	unparsedState streamState
	writeErr      error
	finalized     bool
}

// Create makes the run directory under root, writes the initial manifest and
// opens the append-only streams. It never writes into an existing run.
func Create(root, runID string, manifest Manifest) (*Bundle, error) {
	if err := validateRunID(runID); err != nil {
		return nil, err
	}
	if err := ensureRoot(root); err != nil {
		return nil, fmt.Errorf("storage: create root %s: %w", root, err)
	}
	dir := filepath.Join(root, runID)
	parentRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("storage: open root directory: %w", err)
	}
	dirRoot, err := createRunRootAt(parentRoot, runID)
	if err != nil {
		return nil, errors.Join(err, parentRoot.Close())
	}

	manifest.RedactionRuleVersion = redaction.RuleVersion
	b := &Bundle{dir: dir, parentRoot: parentRoot, dirRoot: dirRoot, red: redaction.New(), manifest: manifest}
	if err := b.start(); err != nil {
		return nil, errors.Join(err, b.discard())
	}
	return b, nil
}

func ensureRoot(root string) error {
	return ensureRootWithSync(root, func(file *os.File) error { return file.Sync() })
}

func ensureRootWithSync(root string, syncFile func(*os.File) error) error {
	missing := []string{}
	anchor := ""
	for current := filepath.Clean(root); ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("%s exists and is not a directory", current)
			}
			anchor = current
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, current)
		if parent := filepath.Dir(current); parent == current {
			return err
		}
	}
	if parentPath := filepath.Dir(anchor); parentPath != anchor {
		parent, err := os.OpenRoot(parentPath)
		if err != nil {
			return err
		}
		err = errors.Join(syncRootWith(parent, syncFile), parent.Close())
		if err != nil {
			return err
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		path := missing[i]
		parent, err := os.OpenRoot(filepath.Dir(path))
		if err != nil {
			return err
		}
		name := filepath.Base(path)
		err = parent.Mkdir(name, dirMode)
		if errors.Is(err, os.ErrExist) {
			if info, statErr := parent.Lstat(name); statErr != nil || !info.IsDir() {
				err = errors.Join(err, statErr)
			} else {
				err = nil
			}
		}
		if err == nil {
			err = parent.Chmod(name, dirMode)
		}
		if err == nil {
			err = syncRootWith(parent, syncFile)
		}
		err = errors.Join(err, parent.Close())
		if err != nil {
			return err
		}
	}
	return nil
}

func createRunRootAt(parent *os.Root, runID string) (*os.Root, error) {
	return createRunRootAtWithSync(parent, runID, func(file *os.File) error { return file.Sync() })
}

func createRunRootAtWithSync(parent *os.Root, runID string, syncFile func(*os.File) error) (*os.Root, error) {
	// Mkdir, not MkdirAll: an existing run must collide here rather than be
	// written into.
	if err := parent.Mkdir(runID, dirMode); err != nil {
		return nil, fmt.Errorf("storage: create run directory: %w", err)
	}
	fail := func(err error) (*os.Root, error) {
		removeErr := parent.Remove(runID)
		if removeErr == nil {
			removeErr = syncRoot(parent)
		}
		return nil, errors.Join(err, removeErr)
	}
	if err := parent.Chmod(runID, dirMode); err != nil {
		return fail(fmt.Errorf("storage: set run directory mode: %w", err))
	}
	runRoot, err := parent.OpenRoot(runID)
	if err != nil {
		return fail(fmt.Errorf("storage: open run directory: %w", err))
	}
	if err := syncRootWith(parent, syncFile); err != nil {
		return fail(errors.Join(fmt.Errorf("storage: sync root directory: %w", err), runRoot.Close()))
	}
	return runRoot, nil
}

// Dir returns the run directory.
func (b *Bundle) Dir() string { return b.dir }

// start writes the first manifest and opens the streams.
func (b *Bundle) start() error {
	if err := b.writeManifest(); err != nil {
		return err
	}
	var err error
	if b.actions, err = createFileAt(b.dirRoot, actionsFile); err != nil {
		return err
	}
	b.events, err = createFileAt(b.dirRoot, eventsFile)
	return err
}

// WritePrompt stores the run's prompt, redacted. A prompt is free text, and
// the redactor only reads JSON, so the prompt is handed over as the one field
// of a throwaway object: that puts it through the same pattern rules, and the
// same marker assignments, as every other string in the run. It may be called
// once per run.
func (b *Bundle) WritePrompt(prompt string) error {
	if err := b.writable(); err != nil {
		return err
	}
	safe, err := b.redactFreeText("prompt", prompt)
	if err != nil {
		return err
	}

	f, err := createFileAt(b.dirRoot, promptFile)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(safe + "\n"); err != nil {
		f.Close()
		return fmt.Errorf("storage: write %s: %w", promptFile, err)
	}
	if err := finishNewFileAt(b.dirRoot, promptFile, f); err != nil {
		return err
	}
	return nil
}

// redactFreeText puts free text through the run's redactor. The redactor only
// reads JSON, so the text is handed over as the one field of a throwaway object
// under a name that means nothing to the pattern rules: that puts it through
// the same rules, and the same marker assignments, as every other string in the
// run. The text goes over in a single call, whole: a private key or a
// credential written across several lines is one secret, and redacting a line
// at a time would judge each fragment alone and publish the rest.
func (b *Bundle) redactFreeText(field, text string) (string, error) {
	wrapped, err := json.Marshal(map[string]string{field: text})
	if err != nil {
		return "", fmt.Errorf("storage: encode %s: %w", field, err)
	}
	safe, err := b.red.RedactJSON(wrapped)
	if err != nil {
		return "", fmt.Errorf("storage: redact %s: %w", field, err)
	}
	var unwrapped map[string]string
	if err := json.Unmarshal(safe, &unwrapped); err != nil {
		return "", fmt.Errorf("storage: decode redacted %s: %w", field, err)
	}
	return unwrapped[field], nil
}

// SanitizeText puts text through this run's redactor and hands it back. It
// writes nothing: the caller is the one holding the text, and what it does with
// the sanitized copy is its own. It is deliberately allowed after Finalize,
// because repository evidence can only be measured once the run has ended, and
// going through this bundle's redactor is what makes a secret named there the
// same secret the argv, the prompt, the actions and the events already named.
//
// Text that is not valid UTF-8 is refused rather than sanitized. The redactor
// reads JSON, and a JSON encoder does not fail on invalid bytes — it replaces
// each one with U+FFFD. Handing that back would give the caller a mangled copy
// of what it collected under the name of a sanitized one, so what cannot be
// carried is reported as such and the caller decides what to record instead.
func (b *Bundle) SanitizeText(text string) (string, error) {
	if !utf8.ValidString(text) {
		return "", errors.New("storage: text is not valid UTF-8 and cannot be sanitized")
	}
	return b.redactFreeText("evidence", text)
}

// WriteProcessStderr stores everything the provider process wrote to stderr,
// sanitized as one capture. Stderr is free text carrying whatever the process
// printed, so the ordinary output is preserved exactly and only the secret runs
// in it are replaced. It may be called once per run.
func (b *Bundle) WriteProcessStderr(text string) error {
	if err := b.writable(); err != nil {
		return err
	}
	safe, err := b.redactFreeText("stderr", text)
	if err != nil {
		return err
	}
	root, err := b.processDir()
	if err != nil {
		return b.fail(err)
	}
	f, err := createFileAt(root, stderrFile)
	if err != nil {
		return b.fail(err)
	}
	if _, err := f.WriteString(safe); err != nil {
		f.Close()
		return b.fail(fmt.Errorf("storage: write %s: %w", stderrFile, err))
	}
	if err := finishNewFileAt(root, stderrFile, f); err != nil {
		return b.fail(err)
	}
	return nil
}

// WriteProcessResult stores the provider's final result document, sanitized.
// It fails closed the way a provider event does: a result that is not exactly
// one JSON object is one this package cannot read, so it is never persisted in
// any form rather than guessed at. It may be called once per run.
func (b *Bundle) WriteProcessResult(rawJSON []byte) error {
	if err := b.writable(); err != nil {
		return err
	}
	safe, err := b.red.RedactJSON(rawJSON)
	if err != nil {
		return fmt.Errorf("storage: redact process result: %w", err)
	}
	root, err := b.processDir()
	if err != nil {
		return b.fail(err)
	}
	if err := installNewAt(root, resultFile, append(safe, '\n')); err != nil {
		return b.fail(err)
	}
	return nil
}

// WriteUsage stores one normalized provider-reported resource summary. It is a
// separate artifact, never an action, and cannot replace any existing entry.
func (b *Bundle) WriteUsage(reported usage.Report) error {
	if err := b.writable(); err != nil {
		return err
	}
	if err := reported.Validate(); err != nil {
		return fmt.Errorf("storage: validate provider usage: %w", err)
	}
	if reported.Provider != b.manifest.Provider {
		return fmt.Errorf("storage: provider usage claims %q, want %q", reported.Provider, b.manifest.Provider)
	}
	raw, err := json.Marshal(reported)
	if err != nil {
		return fmt.Errorf("storage: encode provider usage: %w", err)
	}
	if err := installNewAt(b.dirRoot, usageFile, append(raw, '\n')); err != nil {
		return b.fail(err)
	}
	return nil
}

// processDir returns the directory holding the process artifacts, creating it
// on the first one. An entry that is already there is accepted only if it is a
// real directory: a symlink standing in its place would put the run's evidence
// wherever it points, so it is refused rather than followed. A directory this
// call created and could not then write into is left where it is; removing a
// directory tree is not something a failed write should ever perform.
func (b *Bundle) processDir() (*os.Root, error) {
	if b.processRoot != nil {
		return b.processRoot, nil
	}
	root, err := createProcessRootAt(b.dirRoot)
	if err != nil {
		return nil, err
	}
	b.processRoot = root
	return root, nil
}

func createProcessRootAt(parent *os.Root) (*os.Root, error) {
	return createProcessRootAtWithSync(parent, func(file *os.File) error { return file.Sync() })
}

func createProcessRootAtWithSync(parent *os.Root, syncFile func(*os.File) error) (*os.Root, error) {
	created := false
	switch err := parent.Mkdir(processDirName, dirMode); {
	case err == nil:
		created = true
		// Set again after creation, because the umask masks the mode passed to
		// Mkdir and is not the recorder's to trust.
		if err := parent.Chmod(processDirName, dirMode); err != nil {
			return nil, fmt.Errorf("storage: set mode of %s: %w", processDirName, err)
		}
	case !errors.Is(err, os.ErrExist):
		return nil, fmt.Errorf("storage: create %s directory: %w", processDirName, err)
	}
	// Lstat, not Stat, so a symlink is seen as a symlink rather than as
	// whatever it points at.
	info, err := parent.Lstat(processDirName)
	if err != nil {
		return nil, fmt.Errorf("storage: inspect %s: %w", processDirName, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("storage: %s exists and is not a directory", processDirName)
	}
	root, err := parent.OpenRoot(processDirName)
	if err != nil {
		return nil, fmt.Errorf("storage: open %s directory: %w", processDirName, err)
	}
	if created {
		if err := syncRootWith(parent, syncFile); err != nil {
			return nil, errors.Join(fmt.Errorf("storage: sync run directory after creating %s: %w", processDirName, err), root.Close())
		}
	}
	return root, nil
}

// WriteAction appends one normalized action to the action stream. The action
// goes through action.Writer first, so the stream carries exactly what that
// writer validates and encodes, and the encoded object is then redacted
// structurally: its input and result payloads are provider material.
func (b *Bundle) WriteAction(a action.Action) error {
	if err := b.writable(); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := action.NewWriter(&buf).Write(a); err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	safe, err := b.red.RedactJSON(buf.Bytes())
	if err != nil {
		return fmt.Errorf("storage: redact action %s: %w", a.ID, err)
	}
	return b.appendLine(b.actions, actionsFile, safe, &b.actionsState)
}

// WriteProviderEvent appends one raw provider event, sanitized. The event is
// whatever the provider emitted, so it is redacted structurally and fails
// closed: an event that is not exactly one JSON object is never persisted, in
// any form, because nothing here can tell which part of it was the secret.
func (b *Bundle) WriteProviderEvent(raw []byte) error {
	if err := b.writable(); err != nil {
		return err
	}
	safe, err := b.red.RedactJSON(raw)
	if err != nil {
		return fmt.Errorf("storage: redact provider event: %w", err)
	}
	tokens, err := ValidateProviderEvent(safe, MaxProviderEventTokens-b.eventsState.tokens)
	if err != nil {
		return b.fail(fmt.Errorf("storage: provider event exceeds the bundle contract: %w", err))
	}
	if err := b.appendLine(b.events, eventsFile, safe, &b.eventsState); err != nil {
		return err
	}
	b.eventsState.tokens += tokens
	return nil
}

// unparsedNotUTF8 stands in for a line that cannot be carried as text. The
// bytes are dropped rather than mangled into U+FFFD, which would read as what
// the provider printed while differing from it; the line is still counted, so a
// reader sees that something was said rather than a shorter file.
const unparsedNotUTF8 = "[agentrec: a stdout line was not valid UTF-8 and was not stored]"

// WriteUnparsedLine stores one stdout line that was not a provider event,
// sanitized as free text. It exists so that a line the event reader refuses is
// kept as what it actually is instead of ending the recording: an agent CLI
// that prints an update banner or a deprecation warning beside its event stream
// has still run, and a recorder that threw the whole run away over one such line
// would be destroying the evidence it exists to keep.
//
// The line goes through the run's own redactor, so a secret printed here reads
// as the same secret the events, the argv and the prompt already named.
func (b *Bundle) WriteUnparsedLine(raw []byte) error {
	if err := b.writable(); err != nil {
		return err
	}
	safe := unparsedNotUTF8
	if utf8.Valid(raw) {
		var err error
		if safe, err = b.redactFreeText("stdout", string(raw)); err != nil {
			return b.fail(err)
		}
	}
	if b.unparsed == nil {
		f, err := createFileAt(b.dirRoot, unparsedFile)
		if err != nil {
			return b.fail(err)
		}
		b.unparsed = f
	}
	return b.appendLine(b.unparsed, unparsedFile, []byte(safe), &b.unparsedState)
}

// Finalize closes the streams and rewrites the manifest with how the run
// ended. Every ending is finalizable, including an interrupted or timed-out
// one: a run that stopped badly is exactly the run whose record matters.
func (b *Bundle) Finalize(f Finalization) error {
	if b.finalized {
		return ErrFinalized
	}
	// Set before anything can fail, so a partial Finalize still closes the run
	// to further writes rather than leaving it half open.
	b.finalized = true

	// Both streams are flushed and closed, and the manifest written, whatever
	// any one of them does: the first failure is reported, but a failure early
	// in the sequence must not leave the rest of the run unfinished.
	var streamErr error
	for _, s := range []struct {
		name string
		file *os.File
	}{{actionsFile, b.actions}, {eventsFile, b.events}, {unparsedFile, b.unparsed}} {
		// The unparsed stream is opened only if the run needed it, so a nil file
		// here is a run that had nothing to put in it rather than one to report.
		if s.file == nil {
			continue
		}
		if err := syncClose(s.file); err != nil && streamErr == nil {
			streamErr = fmt.Errorf("storage: finish %s: %w", s.name, err)
		}
	}

	b.manifest.EndedAt = &f.EndedAt
	b.manifest.ExitReason = f.ExitReason
	b.manifest.WarningCount = f.WarningCount
	b.manifest.UnparsedLines = f.UnparsedLines
	manifestErr := b.writeManifest()
	var processRootErr error
	if b.processRoot != nil {
		processRootErr = b.processRoot.Close()
	}
	rootErr := b.dirRoot.Close()
	parentRootErr := b.parentRoot.Close()
	if streamErr != nil {
		return streamErr
	}
	if manifestErr != nil {
		return manifestErr
	}
	if processRootErr != nil {
		return fmt.Errorf("storage: close process directory: %w", processRootErr)
	}
	if rootErr != nil {
		return fmt.Errorf("storage: close run directory: %w", rootErr)
	}
	if parentRootErr != nil {
		return fmt.Errorf("storage: close runs root directory: %w", parentRootErr)
	}
	return nil
}

// syncClose flushes a stream to durable storage and closes it, reporting the
// first failure but always doing both: an unclosed descriptor would outlive the
// run either way.
func syncClose(f *os.File) error {
	return syncCloseWith(f, func(file *os.File) error { return file.Sync() })
}

func syncCloseWith(f *os.File, syncFile func(*os.File) error) error {
	err := syncFile(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func syncRoot(root *os.Root) error {
	return syncRootWith(root, func(file *os.File) error { return file.Sync() })
}

func syncRootWith(root *os.Root, syncFile func(*os.File) error) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	return syncCloseWith(dir, syncFile)
}

func finishNewFileAt(root *os.Root, name string, file *os.File) error {
	return finishNewFileAtWithSync(root, name, file, func(target *os.File) error { return target.Sync() })
}

func finishNewFileAtWithSync(root *os.Root, name string, file *os.File, syncFile func(*os.File) error) error {
	if err := syncCloseWith(file, syncFile); err != nil {
		return fmt.Errorf("storage: finish %s: %w", name, err)
	}
	if err := syncRootWith(root, syncFile); err != nil {
		return fmt.Errorf("storage: sync directory after creating %s: %w", name, err)
	}
	return nil
}

// writable reports what stops the next artifact from being written. A stream
// that has already lost a line no longer describes the run, so the first write
// failure is remembered and handed to every later write rather than letting the
// bundle carry on with a hole in it.
func (b *Bundle) writable() error {
	if b.finalized {
		return ErrFinalized
	}
	return b.writeErr
}

// fail records why an artifact could not be written and returns the failure
// this bundle is now stuck on, which is the first one it saw.
func (b *Bundle) fail(err error) error {
	if b.writeErr == nil {
		b.writeErr = err
	}
	return b.writeErr
}

type streamState struct {
	bytes   int
	entries int
	tokens  int
}

// appendLine writes one sanitized JSON line to an already-open stream.
func (b *Bundle) appendLine(f *os.File, name string, line []byte, state *streamState) error {
	if len(line) >= MaxStreamLineBytes {
		return b.fail(fmt.Errorf("storage: %s line is too large", name))
	}
	if state.entries == MaxStreamEntries {
		return b.fail(fmt.Errorf("storage: %s holds too many entries", name))
	}
	if state.bytes+len(line)+1 > MaxStreamBytes {
		return b.fail(fmt.Errorf("storage: %s exceeds %d bytes", name, MaxStreamBytes))
	}
	start, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return b.fail(fmt.Errorf("storage: locate end of %s: %w", name, err))
	}
	payload := append(line, '\n')
	n, writeErr := f.Write(payload)
	if writeErr != nil || n != len(payload) {
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		if rollbackErr := f.Truncate(start); rollbackErr != nil {
			return b.fail(fmt.Errorf("storage: append to %s: %w", name, errors.Join(writeErr, fmt.Errorf("rollback partial append: %w", rollbackErr))))
		}
		return b.fail(fmt.Errorf("storage: append to %s: %w", name, writeErr))
	}
	state.bytes += len(line) + 1
	state.entries++
	return nil
}

// discard undoes a failed Create. It removes the files this Create made, named
// one by one, and then the run directory itself: a recursive delete of a
// caller-supplied path is not something a failed create should ever perform,
// and a directory holding anything unexpected is left standing.
func (b *Bundle) discard() error {
	var errs []error
	for _, f := range []*os.File{b.actions, b.events, b.unparsed} {
		if f != nil {
			errs = append(errs, f.Close())
		}
	}
	for _, name := range []string{unparsedFile, eventsFile, actionsFile, manifestFile} {
		if b.dirRoot == nil {
			break
		}
		if err := b.dirRoot.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("storage: remove %s during failed create: %w", name, err))
		}
	}
	if b.dirRoot != nil {
		errs = append(errs, b.dirRoot.Close())
	}
	if b.parentRoot != nil {
		errs = append(errs, removeRunDirectoryAt(b.parentRoot, filepath.Base(b.dir)))
		errs = append(errs, b.parentRoot.Close())
	}
	return errors.Join(errs...)
}

func removeRunDirectoryAt(parent *os.Root, runID string) error {
	removeErr := parent.Remove(runID)
	if removeErr != nil {
		removeErr = fmt.Errorf("storage: remove run directory: %w", removeErr)
	} else if syncErr := syncRoot(parent); syncErr != nil {
		removeErr = fmt.Errorf("storage: sync root after removing run directory: %w", syncErr)
	}
	return removeErr
}

func (b *Bundle) path(name string) string { return filepath.Join(b.dir, name) }

// writeManifest redacts the manifest and installs it atomically, so a manifest
// on disk is always a complete one.
func (b *Bundle) writeManifest() error {
	manifest := b.manifest
	argv, err := b.red.RedactArgv(manifest.Argv)
	if err != nil {
		return fmt.Errorf("storage: redact manifest argv: %w", err)
	}
	manifest.Argv = argv
	raw, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("storage: encode manifest: %w", err)
	}
	// Argv is the manifest's own secret-bearing field: a credential passed on
	// the command line is in there verbatim.
	safe, err := b.red.RedactJSON(raw)
	if err != nil {
		return fmt.Errorf("storage: redact manifest: %w", err)
	}

	return installAt(b.dirRoot, manifestFile, append(safe, '\n'))
}

func installAt(root *os.Root, name string, data []byte) error {
	return installAtWithSync(root, name, data, func(file *os.File) error { return file.Sync() })
}

func installAtWithSync(root *os.Root, name string, data []byte, syncFile func(*os.File) error) error {
	return installAtWithOps(root, name, data, syncFile, root.Remove)
}

func installAtWithOps(root *os.Root, name string, data []byte, syncFile func(*os.File) error, remove func(string) error) (retErr error) {
	tmp := name + ".tmp"
	f, err := createFileAt(root, tmp)
	if err != nil {
		return err
	}
	tmpPresent := true
	defer func() {
		if !tmpPresent {
			return
		}
		if err := remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
			retErr = errors.Join(retErr, fmt.Errorf("storage: remove temporary %s: %w", tmp, err))
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("storage: write %s: %w", name, err)
	}
	if err := syncCloseWith(f, syncFile); err != nil {
		return fmt.Errorf("storage: finish %s: %w", name, err)
	}
	if err := root.Rename(tmp, name); err != nil {
		return fmt.Errorf("storage: install %s: %w", name, err)
	}
	tmpPresent = false
	if err := syncRootWith(root, syncFile); err != nil {
		return fmt.Errorf("storage: sync directory after installing %s: %w", name, err)
	}
	return nil
}

// installNew atomically installs a new artifact without replacing an existing
// file or symlink. Linking the fully synced temporary file provides no-clobber
// semantics at the final directory entry.
func installNewAt(root *os.Root, name string, data []byte) error {
	return installNewAtWithSync(root, name, data, func(file *os.File) error { return file.Sync() })
}

func installNewAtWithSync(root *os.Root, name string, data []byte, syncFile func(*os.File) error) (retErr error) {
	tmp := name + ".tmp"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, fileMode)
	if err != nil {
		return fmt.Errorf("storage: create %s: %w", tmp, err)
	}
	tmpPresent := true
	defer func() {
		if !tmpPresent {
			return
		}
		if err := root.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
			retErr = errors.Join(retErr, fmt.Errorf("storage: remove temporary %s: %w", tmp, err))
		}
	}()
	if err := f.Chmod(fileMode); err != nil {
		f.Close()
		return fmt.Errorf("storage: set mode of %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("storage: write %s: %w", name, err)
	}
	if err := syncCloseWith(f, syncFile); err != nil {
		return fmt.Errorf("storage: finish %s: %w", name, err)
	}
	if err := root.Link(tmp, name); err != nil {
		return fmt.Errorf("storage: install %s: %w", name, err)
	}
	if err := root.Remove(tmp); err != nil {
		return fmt.Errorf("storage: remove temporary %s after installing %s: %w", tmp, name, err)
	}
	tmpPresent = false
	if err := syncRootWith(root, syncFile); err != nil {
		return fmt.Errorf("storage: sync directory after installing %s: %w", name, err)
	}
	return nil
}

func createFileAt(root *os.Root, name string) (*os.File, error) {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, fileMode)
	if err != nil {
		return nil, fmt.Errorf("storage: create %s: %w", filepath.Base(name), err)
	}
	if err := f.Chmod(fileMode); err != nil {
		f.Close()
		return nil, fmt.Errorf("storage: set mode of %s: %w", filepath.Base(name), err)
	}
	return f, nil
}

// validateRunID accepts exactly one clean path component. A run ID reaches this
// package from a CLI flag or a provider session ID, so anything that could name
// a directory other than the one intended is refused rather than cleaned.
func validateRunID(runID string) error {
	switch {
	case runID == "":
		return fmt.Errorf("storage: empty run id")
	case runID == "." || runID == "..":
		return fmt.Errorf("storage: run id %q is not a name", runID)
	case strings.ContainsRune(runID, '/') || strings.ContainsRune(runID, os.PathSeparator):
		return fmt.Errorf("storage: run id %q contains a path separator", runID)
	case filepath.Clean(runID) != runID:
		return fmt.Errorf("storage: run id %q is not a clean path component", runID)
	}
	return nil
}
