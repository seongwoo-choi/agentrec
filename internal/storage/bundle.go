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

	"github.com/seongwoo-choi/agentrec/internal/action"
	"github.com/seongwoo-choi/agentrec/internal/redaction"
)

// Bundle files. Later phases add process, git and verification material
// alongside them.
const (
	manifestFile = "manifest.json"
	promptFile   = "prompt.txt"
	actionsFile  = "actions.jsonl"
	eventsFile   = "provider-events.sanitized.jsonl"
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
	Provider             string     `json:"provider"`
	ProviderVersion      string     `json:"providerVersion,omitempty"`
	Argv                 []string   `json:"argv"`
	CWD                  string     `json:"cwd"`
	StartedAt            time.Time  `json:"startedAt"`
	EndedAt              *time.Time `json:"endedAt,omitempty"`
	ExitReason           string     `json:"exitReason,omitempty"`
	WarningCount         int        `json:"warningCount"`
	RedactionRuleVersion string     `json:"redactionRuleVersion"`
}

// Finalization is what only the end of a run knows.
type Finalization struct {
	EndedAt      time.Time
	ExitReason   string
	WarningCount int
}

// ErrFinalized is returned by every write once the run has been finalized, and
// by a second Finalize. The manifest on disk describes a finished run, so
// anything arriving after it belongs to a run that is no longer being recorded.
var ErrFinalized = errors.New("storage: run already finalized")

// Bundle is an open run directory. It holds one redactor for the whole run, so
// a secret seen in argv, in the prompt, in an action and in a provider event is
// recorded under a single marker. A Bundle is for one goroutine only and is not
// safe for concurrent use: its caller must serialize writes and finalization.
type Bundle struct {
	dir       string
	red       *redaction.Redactor
	manifest  Manifest
	actions   *os.File
	events    *os.File
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
	b := &Bundle{dir: dir, red: redaction.New(), manifest: manifest}
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
	wrapped, err := json.Marshal(map[string]string{"prompt": prompt})
	if err != nil {
		return fmt.Errorf("storage: encode prompt: %w", err)
	}
	safe, err := b.red.RedactJSON(wrapped)
	if err != nil {
		return fmt.Errorf("storage: redact prompt: %w", err)
	}
	var unwrapped struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(safe, &unwrapped); err != nil {
		return fmt.Errorf("storage: decode redacted prompt: %w", err)
	}

	f, err := createFile(b.path(promptFile))
	if err != nil {
		return err
	}
	if _, err := f.WriteString(unwrapped.Prompt + "\n"); err != nil {
		f.Close()
		return fmt.Errorf("storage: write %s: %w", promptFile, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("storage: close %s: %w", promptFile, err)
	}
	return nil
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
	}{{actionsFile, b.actions}, {eventsFile, b.events}} {
		if err := syncClose(s.file); err != nil && streamErr == nil {
			streamErr = fmt.Errorf("storage: finish %s: %w", s.name, err)
		}
	}

	b.manifest.EndedAt = &f.EndedAt
	b.manifest.ExitReason = f.ExitReason
	b.manifest.WarningCount = f.WarningCount
	if err := b.writeManifest(); err != nil && streamErr == nil {
		return err
	}
	return streamErr
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

// appendLine writes one sanitized JSON line to an already-open stream.
func (b *Bundle) appendLine(f *os.File, name string, line []byte) error {
	if _, err := f.Write(append(line, '\n')); err != nil {
		b.writeErr = fmt.Errorf("storage: append to %s: %w", name, err)
		return b.writeErr
	}
	return nil
}

// discard undoes a failed Create. It removes the files this Create made, named
// one by one, and then the run directory itself: a recursive delete of a
// caller-supplied path is not something a failed create should ever perform,
// and a directory holding anything unexpected is left standing.
func (b *Bundle) discard() {
	for _, f := range []*os.File{b.actions, b.events} {
		if f != nil {
			f.Close()
		}
	}
	for _, name := range []string{eventsFile, actionsFile, manifestFile} {
		os.Remove(b.path(name))
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

	tmp := b.path(manifestFile + ".tmp")
	f, err := createFile(tmp)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if _, err := f.Write(append(safe, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("storage: write manifest: %w", err)
	}
	// Synced before the rename, so the manifest a crash leaves behind is the
	// one this rename promised rather than an empty file.
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("storage: sync manifest: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("storage: close manifest: %w", err)
	}
	if err := os.Rename(tmp, b.path(manifestFile)); err != nil {
		return fmt.Errorf("storage: install manifest: %w", err)
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
