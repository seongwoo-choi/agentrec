// Package storage persists one recorded run as a self-contained bundle:
// a private directory holding the run's manifest and its append-only streams,
// every byte of which passes through the run's redactor on the way in.
package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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

// Manifest describes the run as a whole. RedactionRuleVersion records the rules
// that produced the markers in this bundle, so it is set from the redaction
// package rather than by the caller.
type Manifest struct {
	Provider        string   `json:"provider"`
	ProviderVersion string   `json:"providerVersion,omitempty"`
	Argv            []string `json:"argv"`
	CWD             string   `json:"cwd"`
	// VersionUnverified records that the provider's version was outside the
	// range agentrec's parser was written against and the run was recorded
	// anyway, on the operator's explicit say-so. What the run reports about
	// itself was read by a parser that does not claim to understand this
	// version's event stream, and every reader of this bundle is told so.
	VersionUnverified    bool       `json:"versionUnverified,omitempty"`
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
	dir      string
	red      *redaction.Redactor
	manifest Manifest
	dirRoot  *os.Root
	actions  *os.File
	events   *os.File
	// unparsed is opened on the first line that was not an event, so a run whose
	// provider only ever emitted events leaves no empty file claiming otherwise.
	unparsed  *os.File
	writeErr  error
	finalized bool
}

// Create makes the run directory under root, writes the initial manifest and
// opens the append-only streams. It never writes into an existing run.
func Create(root, runID string, manifest Manifest) (*Bundle, error) {
	if err := validateRunID(runID); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, dirMode); err != nil {
		return nil, fmt.Errorf("storage: create root %s: %w", root, err)
	}
	dir := filepath.Join(root, runID)
	// Mkdir, not MkdirAll: an existing run must collide here rather than be
	// written into.
	if err := os.Mkdir(dir, dirMode); err != nil {
		return nil, fmt.Errorf("storage: create run directory: %w", err)
	}
	if err := os.Chmod(dir, dirMode); err != nil {
		os.Remove(dir)
		return nil, fmt.Errorf("storage: set run directory mode: %w", err)
	}

	manifest.RedactionRuleVersion = redaction.RuleVersion
	dirRoot, err := os.OpenRoot(dir)
	if err != nil {
		os.Remove(dir)
		return nil, fmt.Errorf("storage: open run directory: %w", err)
	}
	b := &Bundle{dir: dir, dirRoot: dirRoot, red: redaction.New(), manifest: manifest}
	if err := b.start(); err != nil {
		b.discard()
		return nil, err
	}
	return b, nil
}

// Dir returns the run directory.
func (b *Bundle) Dir() string { return b.dir }

// start writes the first manifest and opens the streams.
func (b *Bundle) start() error {
	if err := b.writeManifest(); err != nil {
		return err
	}
	var err error
	if b.actions, err = createFile(b.path(actionsFile)); err != nil {
		return err
	}
	b.events, err = createFile(b.path(eventsFile))
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

	f, err := createFile(b.path(promptFile))
	if err != nil {
		return err
	}
	if _, err := f.WriteString(safe + "\n"); err != nil {
		f.Close()
		return fmt.Errorf("storage: write %s: %w", promptFile, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("storage: close %s: %w", promptFile, err)
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
	dir, err := b.processDir()
	if err != nil {
		return b.fail(err)
	}
	f, err := createFile(filepath.Join(dir, stderrFile))
	if err != nil {
		return b.fail(err)
	}
	if _, err := f.WriteString(safe); err != nil {
		f.Close()
		return b.fail(fmt.Errorf("storage: write %s: %w", stderrFile, err))
	}
	if err := syncClose(f); err != nil {
		return b.fail(fmt.Errorf("storage: finish %s: %w", stderrFile, err))
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
	dir, err := b.processDir()
	if err != nil {
		return b.fail(err)
	}
	path := filepath.Join(dir, resultFile)
	// The install below renames over whatever is at path, so a result already
	// there is checked for first: it is evidence from this run, and a second
	// call is the recorder's view of the run having diverged from the disk.
	// Lstat, so a symlink is refused rather than replaced silently.
	if _, err := os.Lstat(path); err == nil {
		return b.fail(fmt.Errorf("storage: %s already exists", resultFile))
	}
	if err := install(path, append(safe, '\n')); err != nil {
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
func (b *Bundle) processDir() (string, error) {
	dir := b.path(processDirName)
	switch err := os.Mkdir(dir, dirMode); {
	case err == nil:
		// Set again after creation, because the umask masks the mode passed to
		// Mkdir and is not the recorder's to trust.
		if err := os.Chmod(dir, dirMode); err != nil {
			return "", fmt.Errorf("storage: set mode of %s: %w", processDirName, err)
		}
		return dir, nil
	case !errors.Is(err, os.ErrExist):
		return "", fmt.Errorf("storage: create %s directory: %w", processDirName, err)
	}
	// Lstat, not Stat, so a symlink is seen as a symlink rather than as
	// whatever it points at.
	info, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("storage: inspect %s: %w", processDirName, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("storage: %s exists and is not a directory", processDirName)
	}
	return dir, nil
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
	return b.appendLine(b.actions, actionsFile, safe)
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
	return b.appendLine(b.events, eventsFile, safe)
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
		f, err := createFile(b.path(unparsedFile))
		if err != nil {
			return b.fail(err)
		}
		b.unparsed = f
	}
	return b.appendLine(b.unparsed, unparsedFile, []byte(safe))
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
	rootErr := b.dirRoot.Close()
	if streamErr != nil {
		return streamErr
	}
	if manifestErr != nil {
		return manifestErr
	}
	if rootErr != nil {
		return fmt.Errorf("storage: close run directory: %w", rootErr)
	}
	return nil
}

// syncClose flushes a stream to durable storage and closes it, reporting the
// first failure but always doing both: an unclosed descriptor would outlive the
// run either way.
func syncClose(f *os.File) error {
	err := f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
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

// appendLine writes one sanitized JSON line to an already-open stream.
func (b *Bundle) appendLine(f *os.File, name string, line []byte) error {
	if _, err := f.Write(append(line, '\n')); err != nil {
		return b.fail(fmt.Errorf("storage: append to %s: %w", name, err))
	}
	return nil
}

// discard undoes a failed Create. It removes the files this Create made, named
// one by one, and then the run directory itself: a recursive delete of a
// caller-supplied path is not something a failed create should ever perform,
// and a directory holding anything unexpected is left standing.
func (b *Bundle) discard() {
	for _, f := range []*os.File{b.actions, b.events, b.unparsed} {
		if f != nil {
			f.Close()
		}
	}
	for _, name := range []string{unparsedFile, eventsFile, actionsFile, manifestFile} {
		os.Remove(b.path(name))
	}
	if b.dirRoot != nil {
		b.dirRoot.Close()
	}
	os.Remove(b.dir)
}

func (b *Bundle) path(name string) string { return filepath.Join(b.dir, name) }

// writeManifest redacts the manifest and installs it atomically, so a manifest
// on disk is always a complete one.
func (b *Bundle) writeManifest() error {
	raw, err := json.Marshal(b.manifest)
	if err != nil {
		return fmt.Errorf("storage: encode manifest: %w", err)
	}
	// Argv is the manifest's own secret-bearing field: a credential passed on
	// the command line is in there verbatim.
	safe, err := b.red.RedactJSON(raw)
	if err != nil {
		return fmt.Errorf("storage: redact manifest: %w", err)
	}

	return install(b.path(manifestFile), append(safe, '\n'))
}

// install writes data to path atomically, through a temporary file that is
// created exclusively and synced before the rename, so what a crash leaves
// behind is either the previous file or the whole new one, never a partial.
func install(path string, data []byte) error {
	name := filepath.Base(path)
	tmp := path + ".tmp"
	f, err := createFile(tmp)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("storage: write %s: %w", name, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("storage: sync %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("storage: close %s: %w", name, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("storage: install %s: %w", name, err)
	}
	return nil
}

// installNew atomically installs a new artifact without replacing an existing
// file or symlink. Linking the fully synced temporary file provides no-clobber
// semantics at the final directory entry.
func installNewAt(root *os.Root, name string, data []byte) error {
	tmp := name + ".tmp"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, fileMode)
	if err != nil {
		return fmt.Errorf("storage: create %s: %w", tmp, err)
	}
	defer root.Remove(tmp)
	if err := f.Chmod(fileMode); err != nil {
		f.Close()
		return fmt.Errorf("storage: set mode of %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("storage: write %s: %w", name, err)
	}
	if err := syncClose(f); err != nil {
		return fmt.Errorf("storage: finish %s: %w", name, err)
	}
	if err := root.Link(tmp, name); err != nil {
		return fmt.Errorf("storage: install %s: %w", name, err)
	}
	return nil
}

// createFile opens name for appending, failing if it already exists. The mode
// is set again after opening because the umask masks the one passed to open.
func createFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, fileMode)
	if err != nil {
		return nil, fmt.Errorf("storage: create %s: %w", filepath.Base(path), err)
	}
	if err := f.Chmod(fileMode); err != nil {
		f.Close()
		return nil, fmt.Errorf("storage: set mode of %s: %w", filepath.Base(path), err)
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
