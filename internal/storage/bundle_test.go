package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/action"
	"github.com/seongwoo-choi/agentrec/internal/redaction"
)

// testManifest is a representative manifest holding nothing sensitive.
func testManifest() Manifest {
	return Manifest{
		Provider:        "claude",
		ProviderVersion: "1.2.3",
		Argv:            []string{"agentrec", "trace", "--", "claude", "-p", "hello"},
		CWD:             "/workspace/project",
		StartedAt:       time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC),
	}
}

func statMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func TestCreateRejectsUnsafeRunIDs(t *testing.T) {
	// Arrange
	root := t.TempDir()

	// Act & Assert: a run ID is one clean path component and nothing else, so
	// none of these may reach the filesystem.
	for _, runID := range []string{"", ".", "..", "../escape", "sub/run", "run/", "./run"} {
		if _, err := Create(root, runID, testManifest()); err == nil {
			t.Errorf("Create(root, %q) succeeded, want error", runID)
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("rejected run IDs left %d entries under root, want none", len(entries))
	}
}

func TestCreateDoesNotOverwriteAnExistingRun(t *testing.T) {
	// Arrange
	root := t.TempDir()
	first, err := Create(root, "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(first.Dir(), "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	// Act
	second, err := Create(root, "run-1", testManifest())

	// Assert
	if err == nil {
		t.Fatalf("Create over existing run %s succeeded, want error", second.Dir())
	}
	after, err := os.ReadFile(filepath.Join(first.Dir(), "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest after refused Create: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("refused Create rewrote the existing manifest")
	}
}

// readLines returns the non-empty lines of a bundle file.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := strings.TrimSuffix(string(raw), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func TestWriteActionAppendsOneLinePerActionWhileTheRunIsOpen(t *testing.T) {
	// Arrange
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	actions := filepath.Join(b.Dir(), "actions.jsonl")

	// Act & Assert: each action must be on disk before the next one is
	// written, so a run interrupted mid-flight keeps everything it recorded.
	for i, a := range []action.Action{
		{ID: "a1", Type: action.TypeFileRead, Assurance: action.AssuranceProviderReported},
		{ID: "a2", Type: action.TypeShellExec, Assurance: action.AssuranceSupervisorObserved},
	} {
		if err := b.WriteAction(a); err != nil {
			t.Fatalf("WriteAction %s: %v", a.ID, err)
		}
		if got := len(readLines(t, actions)); got != i+1 {
			t.Fatalf("after writing %s the file holds %d lines, want %d", a.ID, got, i+1)
		}
	}

	lines := readLines(t, actions)
	for i, want := range []string{"a1", "a2"} {
		var got action.Action
		if err := json.Unmarshal([]byte(lines[i]), &got); err != nil {
			t.Fatalf("line %d is not one JSON action: %v", i+1, err)
		}
		if got.ID != want {
			t.Errorf("line %d has id %q, want %q", i+1, got.ID, want)
		}
	}
}

func TestWriteActionRejectsAnInvalidActionAndWritesNothing(t *testing.T) {
	// Arrange
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Act: no assurance, which action.Writer refuses to encode.
	err = b.WriteAction(action.Action{ID: "a1", Type: action.TypeFileRead})

	// Assert
	if err == nil {
		t.Fatal("WriteAction accepted an action with no assurance, want error")
	}
	if !strings.Contains(err.Error(), "missing assurance") {
		t.Errorf("error %q does not report the failed validation", err)
	}
	if lines := readLines(t, filepath.Join(b.Dir(), "actions.jsonl")); len(lines) != 0 {
		t.Errorf("rejected action left %d lines on disk, want none", len(lines))
	}
}

// fixturePath resolves a fixture relative to this package's source directory so
// tests do not depend on the process working directory.
func fixturePath(t *testing.T, parts ...string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test source directory")
	}
	return filepath.Join(append([]string{filepath.Dir(thisFile), "..", "..", "testdata"}, parts...)...)
}

// fixtureSecrets are the synthetic values planted in the redaction fixture.
// None is a real credential, and none may appear in a bundle.
var fixtureSecrets = []string{
	"synthetic-token-aaaaaaaa",
	"synthetic-secret-bbbbbbbb",
	"synthetic-password-dddddddd",
	"synthetic-env-token-11111111",
	"ghp_syntheticAAAABBBBCCCCDDDD",
	"AKIASYNTHETIC0000000",
	"c3ludGhldGljLXJzYS1rZXktYm9keQ==",
}

func TestProviderEventsFromTheFixtureArePersistedSanitized(t *testing.T) {
	// Arrange
	raw, err := os.ReadFile(fixturePath(t, "redaction", "provider-events.jsonl"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	for _, secret := range fixtureSecrets {
		if !strings.Contains(string(raw), secret) {
			t.Fatalf("fixture does not contain %q, so this test would prove nothing", secret)
		}
	}
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Act: one event per call, as a running provider delivers them.
	events := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if err := b.WriteProviderEvent([]byte(line)); err != nil {
			t.Fatalf("WriteProviderEvent: %v", err)
		}
		events++
	}

	// Assert
	path := filepath.Join(b.Dir(), "provider-events.sanitized.jsonl")
	lines := readLines(t, path)
	if len(lines) != events {
		t.Errorf("wrote %d events but the stream holds %d lines", events, len(lines))
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	for _, secret := range fixtureSecrets {
		if strings.Contains(string(stored), secret) {
			t.Errorf("sanitized stream leaked %q", secret)
		}
		digest := sha256.Sum256([]byte(secret))
		if strings.Contains(string(stored), hex.EncodeToString(digest[:])) {
			t.Errorf("sanitized stream contains the SHA-256 digest of %q", secret)
		}
	}
}

func TestWriteProviderEventFailsClosedOnMalformedEvents(t *testing.T) {
	// Arrange
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	path := filepath.Join(b.Dir(), "provider-events.sanitized.jsonl")

	// Act & Assert: an event this package cannot parse is one it cannot vouch
	// for, so nothing about it reaches the bundle.
	for _, event := range []string{
		`{"type":"tool.call"`,
		`["not","an","object"]`,
		`"a bare string"`,
		`{"a":1} {"b":2}`,
		``,
	} {
		if err := b.WriteProviderEvent([]byte(event)); err == nil {
			t.Errorf("WriteProviderEvent(%q) succeeded, want error", event)
		}
		if lines := readLines(t, path); len(lines) != 0 {
			t.Fatalf("malformed event %q left %d lines on disk, want none", event, len(lines))
		}
	}
}

// markerPattern finds every run-local marker in a bundle file.
var markerPattern = regexp.MustCompile(`\[REDACTED:\d+\]`)

func TestOneSecretGetsOneMarkerAcrossEveryBundleFile(t *testing.T) {
	// Arrange: one synthetic token, reaching the bundle by four routes.
	const secret = "ghp_syntheticEEEEFFFFGGGGHHHH"
	manifest := testManifest()
	manifest.Argv = []string{"agentrec", "trace", "--", "claude", "--token", secret}

	b, err := Create(t.TempDir(), "run-1", manifest)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Act
	if err := b.WritePrompt("push the branch using " + secret + " and report back"); err != nil {
		t.Fatalf("WritePrompt: %v", err)
	}
	if err := b.WriteAction(action.Action{
		ID:        "a1",
		Type:      action.TypeToolCall,
		Assurance: action.AssuranceProviderReported,
		Input:     json.RawMessage(`{"github_token":"` + secret + `"}`),
	}); err != nil {
		t.Fatalf("WriteAction: %v", err)
	}
	if err := b.WriteProviderEvent([]byte(`{"type":"tool.call","command":"git push https://` + secret + `@example.invalid/repo"}`)); err != nil {
		t.Fatalf("WriteProviderEvent: %v", err)
	}
	if err := b.WriteProcessStderr("fatal: authentication failed for " + secret + "\n"); err != nil {
		t.Fatalf("WriteProcessStderr: %v", err)
	}
	if err := b.WriteProcessResult([]byte(`{"type":"result","stderr_tail":"failed for ` + secret + `"}`)); err != nil {
		t.Fatalf("WriteProcessResult: %v", err)
	}

	// Assert: every file carries a marker, none carries the secret or a
	// digest of it, and all six markers are the same one.
	markers := make(map[string]bool)
	files := []string{
		"manifest.json", "prompt.txt", "actions.jsonl", "provider-events.sanitized.jsonl",
		filepath.Join("process", "stderr.sanitized.log"), filepath.Join("process", "result.json"),
	}
	digest := sha256.Sum256([]byte(secret))
	for _, name := range files {
		path := filepath.Join(b.Dir(), name)
		if got := statMode(t, path); got != 0o600 {
			t.Errorf("%s mode = %04o, want 0600", name, got)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(raw), secret) {
			t.Errorf("%s leaked the secret", name)
		}
		if strings.Contains(string(raw), hex.EncodeToString(digest[:])) {
			t.Errorf("%s contains the SHA-256 digest of the secret", name)
		}
		found := markerPattern.FindAllString(string(raw), -1)
		if len(found) == 0 {
			t.Errorf("%s holds no marker, so the secret never reached it", name)
		}
		for _, m := range found {
			markers[m] = true
		}
	}
	if len(markers) != 1 {
		t.Errorf("got %d distinct markers across the bundle, want 1: %v", len(markers), markers)
	}
}

func TestWritePromptStoresTheRedactedPromptOnce(t *testing.T) {
	// Arrange
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	const prompt = "deploy with AKIASYNTHETIC1111111 then stop"

	// Act
	if err := b.WritePrompt(prompt); err != nil {
		t.Fatalf("WritePrompt: %v", err)
	}
	second := b.WritePrompt("another prompt")

	// Assert
	if second == nil {
		t.Error("a second WritePrompt succeeded, want error")
	}
	got, err := os.ReadFile(filepath.Join(b.Dir(), "prompt.txt"))
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	if strings.Contains(string(got), "AKIASYNTHETIC1111111") {
		t.Errorf("prompt.txt leaked the pattern-shaped secret")
	}
	if !strings.HasPrefix(string(got), "deploy with [REDACTED:") ||
		!strings.HasSuffix(strings.TrimSuffix(string(got), "\n"), "] then stop") {
		t.Errorf("prompt.txt = %q, want the prompt text with only the secret replaced", got)
	}
}

// readManifest decodes the bundle's manifest as it stands on disk.
func readManifest(t *testing.T, b *Bundle) Manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(b.Dir(), "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return m
}

func TestFinalizeRecordsTheWholeManifest(t *testing.T) {
	// Arrange
	want := testManifest()
	b, err := Create(t.TempDir(), "run-1", want)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ended := want.StartedAt.Add(90 * time.Second)

	// Act
	if err := b.Finalize(Finalization{EndedAt: ended, ExitReason: "exit:0", WarningCount: 2}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// Assert
	got := readManifest(t, b)
	if got.Provider != want.Provider || got.ProviderVersion != want.ProviderVersion {
		t.Errorf("provider = %q/%q, want %q/%q", got.Provider, got.ProviderVersion, want.Provider, want.ProviderVersion)
	}
	if strings.Join(got.Argv, " ") != strings.Join(want.Argv, " ") {
		t.Errorf("argv = %v, want %v", got.Argv, want.Argv)
	}
	if got.CWD != want.CWD {
		t.Errorf("cwd = %q, want %q", got.CWD, want.CWD)
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("startedAt = %s, want %s", got.StartedAt, want.StartedAt)
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(ended) {
		t.Errorf("endedAt = %v, want %s", got.EndedAt, ended)
	}
	if got.ExitReason != "exit:0" {
		t.Errorf("exitReason = %q, want %q", got.ExitReason, "exit:0")
	}
	if got.WarningCount != 2 {
		t.Errorf("warningCount = %d, want 2", got.WarningCount)
	}
	if got.RedactionRuleVersion != redaction.RuleVersion {
		t.Errorf("redactionRuleVersion = %q, want %q", got.RedactionRuleVersion, redaction.RuleVersion)
	}
	if got := statMode(t, filepath.Join(b.Dir(), "manifest.json")); got != 0o600 {
		t.Errorf("rewritten manifest mode = %04o, want 0600", got)
	}
}

func TestFinalizeCompletesAnInterruptedRun(t *testing.T) {
	// Arrange & Act: a run that ended badly still has to be closed out, and
	// whatever it recorded before the end has to survive.
	for _, reason := range []string{"interrupted", "timeout", "exit:1"} {
		b, err := Create(t.TempDir(), "run-1", testManifest())
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := b.WriteAction(action.Action{ID: "a1", Type: action.TypeShellExec, Assurance: action.AssuranceSupervisorObserved}); err != nil {
			t.Fatalf("WriteAction: %v", err)
		}
		ended := time.Date(2026, 7, 27, 10, 1, 0, 0, time.UTC)

		if err := b.Finalize(Finalization{EndedAt: ended, ExitReason: reason, WarningCount: 1}); err != nil {
			t.Fatalf("Finalize(%s): %v", reason, err)
		}

		// Assert
		if got := readManifest(t, b); got.ExitReason != reason || got.EndedAt == nil {
			t.Errorf("manifest after %s = %+v, want the reason and an end time", reason, got)
		}
		if lines := readLines(t, filepath.Join(b.Dir(), "actions.jsonl")); len(lines) != 1 {
			t.Errorf("%s run kept %d action lines, want 1", reason, len(lines))
		}
	}
}

func TestWritesAfterFinalizeFailAndFinalizeIsNotRepeatable(t *testing.T) {
	// Arrange
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	final := Finalization{EndedAt: time.Date(2026, 7, 27, 10, 1, 0, 0, time.UTC), ExitReason: "exit:0"}
	if err := b.Finalize(final); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// Act & Assert
	writes := map[string]error{
		"WritePrompt":        b.WritePrompt("late prompt"),
		"WriteAction":        b.WriteAction(action.Action{ID: "a1", Type: action.TypeShellExec, Assurance: action.AssuranceProviderReported}),
		"WriteProviderEvent": b.WriteProviderEvent([]byte(`{"type":"late"}`)),
		"Finalize":           b.Finalize(final),
	}
	for name, err := range writes {
		if !errors.Is(err, ErrFinalized) {
			t.Errorf("%s after Finalize returned %v, want ErrFinalized", name, err)
		}
	}
	if lines := readLines(t, filepath.Join(b.Dir(), "actions.jsonl")); len(lines) != 0 {
		t.Errorf("a post-finalize write reached actions.jsonl")
	}
	if _, err := os.Stat(filepath.Join(b.Dir(), "prompt.txt")); !os.IsNotExist(err) {
		t.Errorf("a post-finalize WritePrompt created prompt.txt")
	}
}

func TestAFailedStreamWriteStopsEveryLaterWrite(t *testing.T) {
	// Arrange: closing the action file makes the next append fail the way a
	// full or vanished disk would.
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := b.actions.Close(); err != nil {
		t.Fatalf("close action stream: %v", err)
	}

	// Act
	first := b.WriteAction(action.Action{ID: "a1", Type: action.TypeShellExec, Assurance: action.AssuranceProviderReported})

	// Assert: the failure is reported, then remembered. A stream that lost a
	// line no longer describes the run, so nothing more is appended to any of
	// them.
	if first == nil {
		t.Fatal("WriteAction on a broken stream returned nil, want a write error")
	}
	if !strings.Contains(first.Error(), actionsFile) {
		t.Errorf("error %q does not name the stream that failed", first)
	}
	later := map[string]error{
		"WriteProviderEvent": b.WriteProviderEvent([]byte(`{"type":"after"}`)),
		"WriteAction":        b.WriteAction(action.Action{ID: "a2", Type: action.TypeFileRead, Assurance: action.AssuranceProviderReported}),
		"WriteProcessStderr": b.WriteProcessStderr("after\n"),
		"WriteProcessResult": b.WriteProcessResult([]byte(`{"type":"result"}`)),
	}
	for name, err := range later {
		if !errors.Is(err, first) {
			t.Errorf("%s after a failed write returned %v, want the stored failure %v", name, err, first)
		}
	}
	if lines := readLines(t, filepath.Join(b.Dir(), eventsFile)); len(lines) != 0 {
		t.Errorf("a write after the failure reached %s", eventsFile)
	}
}

// processPath resolves one process artifact inside a bundle.
func processPath(b *Bundle, name string) string {
	return filepath.Join(b.Dir(), "process", name)
}

func TestWriteProcessStderrStoresTheCaptureUnderPrivateModes(t *testing.T) {
	// Arrange: a permissive umask masks nothing, so a too-wide mode shows here.
	defer syscall.Umask(syscall.Umask(0))
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	const text = "starting claude\n  indented detail\n\nexit status 1\n"

	// Act
	if err := b.WriteProcessStderr(text); err != nil {
		t.Fatalf("WriteProcessStderr: %v", err)
	}

	// Assert
	if got := statMode(t, filepath.Join(b.Dir(), "process")); got != 0o700 {
		t.Errorf("process directory mode = %04o, want 0700", got)
	}
	path := processPath(b, "stderr.sanitized.log")
	if got := statMode(t, path); got != 0o600 {
		t.Errorf("%s mode = %04o, want 0600", path, got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if string(raw) != text {
		t.Errorf("stderr = %q, want the capture unchanged: %q", raw, text)
	}
}

func TestWriteProcessStderrRedactsSecretsThatSpanLines(t *testing.T) {
	// Arrange: a key block and an assignment, both of which only make sense to
	// the redactor if the whole capture is sanitized in one pass.
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	const body = "c3ludGhldGljLXJzYS1rZXktYm9keQ==\nMIISYNTHETICKEYBODYSECONDLINE\n"
	const text = "loading key\n" +
		"-----BEGIN RSA PRIVATE KEY-----\n" + body + "-----END RSA PRIVATE KEY-----\n" +
		"AWS_SECRET_ACCESS_KEY=synthetic-secret-bbbbbbbb\ndone\n"

	// Act
	if err := b.WriteProcessStderr(text); err != nil {
		t.Fatalf("WriteProcessStderr: %v", err)
	}

	// Assert: no line of the key survives, in whole or in part, and the
	// ordinary output around it is untouched.
	raw, err := os.ReadFile(processPath(b, "stderr.sanitized.log"))
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	got := string(raw)
	for _, leak := range []string{
		"c3ludGhldGljLXJzYS1rZXktYm9keQ==",
		"MIISYNTHETICKEYBODYSECONDLINE",
		"BEGIN RSA PRIVATE KEY",
		"synthetic-secret-bbbbbbbb",
	} {
		if strings.Contains(got, leak) {
			t.Errorf("sanitized stderr leaked %q:\n%s", leak, got)
		}
	}
	if !strings.HasPrefix(got, "loading key\n") || !strings.HasSuffix(got, "\ndone\n") {
		t.Errorf("sanitized stderr = %q, want the ordinary lines and newlines preserved", got)
	}
	if !strings.Contains(got, "\nAWS_SECRET_ACCESS_KEY=[REDACTED:") {
		t.Errorf("sanitized stderr = %q, want the assignment name kept and its value replaced", got)
	}
	if n := len(markerPattern.FindAllString(got, -1)); n != 2 {
		t.Errorf("sanitized stderr holds %d markers, want 2 (the key block and the assignment)", n)
	}
}

func TestWriteProcessResultInstallsOneSanitizedObject(t *testing.T) {
	// Arrange
	defer syscall.Umask(syscall.Umask(0))
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	raw := []byte(`{"type":"result","is_error":false,"api_key":"synthetic-token-aaaaaaaa","usage":{"input_tokens":12}}`)

	// Act
	if err := b.WriteProcessResult(raw); err != nil {
		t.Fatalf("WriteProcessResult: %v", err)
	}

	// Assert
	path := processPath(b, "result.json")
	if got := statMode(t, path); got != 0o600 {
		t.Errorf("%s mode = %04o, want 0600", path, got)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if strings.Contains(string(stored), "synthetic-token-aaaaaaaa") {
		t.Errorf("result.json leaked the key")
	}
	var got map[string]any
	if err := json.Unmarshal(stored, &got); err != nil {
		t.Fatalf("result.json is not one JSON object: %v", err)
	}
	if got["type"] != "result" || got["is_error"] != false {
		t.Errorf("result.json = %v, want the document's own fields preserved", got)
	}
	if key, _ := got["api_key"].(string); !markerPattern.MatchString(key) {
		t.Errorf("api_key = %v, want a marker", got["api_key"])
	}
	// The install is atomic, so nothing of the temporary file survives it.
	entries, err := os.ReadDir(filepath.Join(b.Dir(), "process"))
	if err != nil {
		t.Fatalf("read process directory: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("process directory holds %d entries, want only result.json", len(entries))
	}
}

func TestWriteProcessResultFailsClosedAndLeavesTheRunWritable(t *testing.T) {
	// Arrange
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	path := processPath(b, "result.json")

	// Act & Assert: a result this package cannot parse as one object is one it
	// cannot vouch for, so nothing about it reaches the bundle.
	for _, bad := range []string{
		`[{"type":"result"}]`,
		`"result"`,
		`42`,
		`null`,
		`{"type":"result"`,
		`{"a":1} {"b":2}`,
		``,
	} {
		if err := b.WriteProcessResult([]byte(bad)); err == nil {
			t.Errorf("WriteProcessResult(%q) succeeded, want error", bad)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("rejected result %q reached disk", bad)
		}
	}

	// Rejected input is not a storage failure: the run still has its evidence,
	// so it stays open.
	if err := b.WriteProcessResult([]byte(`{"type":"result"}`)); err != nil {
		t.Errorf("WriteProcessResult after rejected input: %v", err)
	}
}

// Each process artifact gets its own bundle below, because the first refusal
// poisons the bundle: sharing one would leave the second artifact's own guard
// untested behind the stored failure.
func TestProcessArtifactsAreWrittenOnce(t *testing.T) {
	for name, tc := range map[string]struct {
		file   string
		first  func(*Bundle) error
		second func(*Bundle) error
		kept   string
	}{
		"stderr": {
			file:   "stderr.sanitized.log",
			first:  func(b *Bundle) error { return b.WriteProcessStderr("first capture\n") },
			second: func(b *Bundle) error { return b.WriteProcessStderr("second capture\n") },
			kept:   "first capture",
		},
		"result": {
			file:   "result.json",
			first:  func(b *Bundle) error { return b.WriteProcessResult([]byte(`{"type":"result","attempt":1}`)) },
			second: func(b *Bundle) error { return b.WriteProcessResult([]byte(`{"type":"result","attempt":2}`)) },
			kept:   `"attempt":1`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			// Arrange
			b, err := Create(t.TempDir(), "run-1", testManifest())
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if err := tc.first(b); err != nil {
				t.Fatalf("first write: %v", err)
			}

			// Act: the process ended once, so a second call is a mistake that
			// would destroy what the first one recorded.
			second := tc.second(b)

			// Assert
			if second == nil {
				t.Error("a second write succeeded, want error")
			}
			got, err := os.ReadFile(processPath(b, tc.file))
			if err != nil {
				t.Fatalf("read %s: %v", tc.file, err)
			}
			if !strings.Contains(string(got), tc.kept) {
				t.Errorf("%s = %q, want what the first write stored", tc.file, got)
			}
		})
	}
}

func TestProcessWritesAfterFinalizeFail(t *testing.T) {
	// Arrange
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := b.Finalize(Finalization{EndedAt: time.Date(2026, 7, 27, 10, 1, 0, 0, time.UTC), ExitReason: "exit:0"}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// Act & Assert
	writes := map[string]error{
		"WriteProcessStderr": b.WriteProcessStderr("late capture\n"),
		"WriteProcessResult": b.WriteProcessResult([]byte(`{"type":"result"}`)),
	}
	for name, err := range writes {
		if !errors.Is(err, ErrFinalized) {
			t.Errorf("%s after Finalize returned %v, want ErrFinalized", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(b.Dir(), "process")); !os.IsNotExist(err) {
		t.Errorf("a post-finalize process write created the process directory")
	}
}

func TestAFailedProcessWritePoisonsTheBundleAndRemovesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	// Arrange: a process directory that already holds evidence and can no
	// longer be written into, the way a full or read-only filesystem fails.
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	dir := filepath.Join(b.Dir(), "process")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("pre-create process directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("earlier evidence"), 0o600); err != nil {
		t.Fatalf("pre-create process artifact: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("seal process directory: %v", err)
	}
	defer os.Chmod(dir, 0o700)

	// Act
	first := b.WriteProcessStderr("capture\n")

	// Assert: the failure is reported, then remembered, and what was already
	// in the process directory is still there.
	if first == nil {
		t.Fatal("WriteProcessStderr into a sealed directory returned nil, want an error")
	}
	later := b.WriteAction(action.Action{ID: "a1", Type: action.TypeFileRead, Assurance: action.AssuranceProviderReported})
	if !errors.Is(later, first) {
		t.Errorf("WriteAction after a failed process write returned %v, want the stored failure %v", later, first)
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.txt")); err != nil {
		t.Errorf("a failed process write removed what the directory already held: %v", err)
	}
}

func TestProcessArtifactsNeverFollowASymlink(t *testing.T) {
	// Arrange: somewhere outside the bundle for a symlink to point at.
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "target.log")
	if err := os.WriteFile(outsideFile, []byte("untouched\n"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	// Act & Assert: every entry a process write opens is refused when it is a
	// symlink or is not the kind of thing it should be. Each writer gets its
	// own bundle, since the first refusal poisons the one it was made against.
	for _, tc := range []struct {
		name    string
		planted string
		plant   func(t *testing.T, b *Bundle)
	}{
		{"process is a symlink", "process", func(t *testing.T, b *Bundle) {
			if err := os.Symlink(outside, filepath.Join(b.Dir(), "process")); err != nil {
				t.Fatalf("plant symlink: %v", err)
			}
		}},
		{"process is a regular file", "process", func(t *testing.T, b *Bundle) {
			if err := os.WriteFile(filepath.Join(b.Dir(), "process"), nil, 0o600); err != nil {
				t.Fatalf("plant file: %v", err)
			}
		}},
		{"stderr is a symlink", filepath.Join("process", "stderr.sanitized.log"), plantArtifactSymlink(outsideFile, "stderr.sanitized.log")},
		{"result is a symlink", filepath.Join("process", "result.json"), plantArtifactSymlink(outsideFile, "result.json")},
	} {
		for name, write := range map[string]func(*Bundle) error{
			"WriteProcessStderr": func(b *Bundle) error { return b.WriteProcessStderr("capture\n") },
			"WriteProcessResult": func(b *Bundle) error { return b.WriteProcessResult([]byte(`{"type":"result"}`)) },
		} {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				b, err := Create(t.TempDir(), "run-1", testManifest())
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				tc.plant(t, b)
				planted, err := os.Lstat(filepath.Join(b.Dir(), tc.planted))
				if err != nil {
					t.Fatalf("stat planted entry: %v", err)
				}

				// A write that names the planted entry has to be refused; one
				// that names the other artifact may be refused too, but must
				// never reach through the plant.
				_ = write(b)

				if strings.HasSuffix(tc.planted, filepath.Base(tc.planted)) {
					after, err := os.Lstat(filepath.Join(b.Dir(), tc.planted))
					if err != nil {
						t.Fatalf("stat planted entry after the write: %v", err)
					}
					if after.Mode().Type() != planted.Mode().Type() {
						t.Errorf("%s changed from %s to %s, so the write replaced what was already there",
							tc.planted, planted.Mode().Type(), after.Mode().Type())
					}
				}
				got, err := os.ReadFile(outsideFile)
				if err != nil {
					t.Fatalf("read outside file: %v", err)
				}
				if string(got) != "untouched\n" {
					t.Errorf("the write followed the symlink and wrote %q outside the bundle", got)
				}
				entries, err := os.ReadDir(outside)
				if err != nil {
					t.Fatalf("read outside directory: %v", err)
				}
				if len(entries) != 1 {
					t.Errorf("the write left %d entries outside the bundle, want only the planted target", len(entries))
				}
			})
		}
	}
}

// plantArtifactSymlink puts a real process directory in the bundle with one
// artifact name already taken by a symlink out of it.
func plantArtifactSymlink(target, name string) func(*testing.T, *Bundle) {
	return func(t *testing.T, b *Bundle) {
		t.Helper()
		dir := filepath.Join(b.Dir(), "process")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("plant directory: %v", err)
		}
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatalf("plant symlink: %v", err)
		}
	}
}

func TestProcessArtifactsUseAnExistingProcessDirectory(t *testing.T) {
	// Arrange: a real directory is the one thing that may already be there.
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.Mkdir(filepath.Join(b.Dir(), "process"), 0o700); err != nil {
		t.Fatalf("pre-create process directory: %v", err)
	}

	// Act & Assert
	if err := b.WriteProcessStderr("capture\n"); err != nil {
		t.Errorf("WriteProcessStderr into an existing process directory: %v", err)
	}
	if err := b.WriteProcessResult([]byte(`{"type":"result"}`)); err != nil {
		t.Errorf("WriteProcessResult into an existing process directory: %v", err)
	}
}

func TestCreateUsesExactPermissionsUnderPermissiveUmask(t *testing.T) {
	// Arrange: a permissive umask masks nothing, so any mode the bundle asks
	// for lands as-is and a too-wide request shows up here.
	defer syscall.Umask(syscall.Umask(0))
	root := filepath.Join(t.TempDir(), "runs")

	// Act
	b, err := Create(root, "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Assert
	if got := statMode(t, root); got != 0o700 {
		t.Errorf("root mode = %04o, want 0700", got)
	}
	if got := statMode(t, b.Dir()); got != 0o700 {
		t.Errorf("run directory mode = %04o, want 0700", got)
	}
	for _, name := range []string{"manifest.json", "actions.jsonl", "provider-events.sanitized.jsonl"} {
		if got := statMode(t, filepath.Join(b.Dir(), name)); got != 0o600 {
			t.Errorf("%s mode = %04o, want 0600", name, got)
		}
	}
}

// Repository evidence is measured after the run has been finalized, so
// sanitizing it is the one thing a finished bundle still does. A secret the
// prompt and the actions already named is the same secret when the evidence
// carries it, and it has to read as one secret across the whole bundle.
func TestSanitizeTextReusesTheRunsMarkerAfterFinalize(t *testing.T) {
	// Arrange: one synthetic token, recorded first by the routes a run writes
	// while it is open.
	const secret = "ghp_syntheticIIIIJJJJKKKKLLLL"
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := b.WritePrompt("push the branch using " + secret); err != nil {
		t.Fatalf("WritePrompt: %v", err)
	}
	if err := b.WriteAction(action.Action{
		ID:        "a1",
		Type:      action.TypeToolCall,
		Assurance: action.AssuranceProviderReported,
		Input:     json.RawMessage(`{"github_token":"` + secret + `"}`),
	}); err != nil {
		t.Fatalf("WriteAction: %v", err)
	}
	if err := b.Finalize(Finalization{
		EndedAt:    time.Date(2026, 7, 27, 10, 1, 0, 0, time.UTC),
		ExitReason: "completed",
	}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// Act: what a patch collected from the repository afterwards would carry.
	safe, err := b.SanitizeText("+token = " + secret + "\n+kept = plain\n")
	if err != nil {
		t.Fatalf("SanitizeText: %v", err)
	}

	// Assert
	if strings.Contains(safe, secret) {
		t.Errorf("SanitizeText = %q, want the secret replaced", safe)
	}
	if !strings.Contains(safe, "+kept = plain") {
		t.Errorf("SanitizeText = %q, want the surrounding text preserved", safe)
	}
	recorded, err := os.ReadFile(filepath.Join(b.Dir(), "prompt.txt"))
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	want := markerPattern.FindAllString(string(recorded), -1)
	if len(want) != 1 {
		t.Fatalf("prompt.txt holds %d markers, want 1", len(want))
	}
	if got := markerPattern.FindAllString(safe, -1); len(got) != 1 || got[0] != want[0] {
		t.Errorf("SanitizeText markers = %v, want the prompt's %q", got, want[0])
	}
}

// Text that is not valid UTF-8 is refused rather than sanitized. The redactor
// reads JSON, and a JSON encoder replaces every invalid byte with U+FFFD: a
// caller handed that back would store a mangled copy of what it collected while
// believing it had stored what it read.
func TestSanitizeTextRefusesTextThatIsNotUTF8(t *testing.T) {
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	safe, err := b.SanitizeText("caf\xe9 cr\xe8me\n")

	if err == nil {
		t.Fatalf("SanitizeText = %q, want it refused", safe)
	}
	if safe != "" {
		t.Errorf("SanitizeText = %q, want nothing alongside the refusal", safe)
	}
	if strings.Contains(err.Error(), "�") {
		t.Errorf("SanitizeText error = %v, want it not to carry a coerced copy", err)
	}
	if valid, err := b.SanitizeText("plain text\n"); err != nil || valid != "plain text\n" {
		t.Errorf("SanitizeText(%q) = %q, %v, want valid text still sanitized", "plain text\n", valid, err)
	}
}
