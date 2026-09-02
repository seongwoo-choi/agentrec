package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	usageartifact "github.com/seongwoo-choi/agentrec/internal/usage"
)

func writeTranscript(t *testing.T, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The transcript is read for numbers and names only: one API response is
// counted once however many lines it spans, the model and the provider's
// version are kept, and nothing of the conversation is.
func TestReadTranscriptUsageClaude(t *testing.T) {
	path := writeTranscript(t, "claude.jsonl",
		`{"type":"user","version":"2.1.300","message":{"role":"user","content":"secret plan"}}`,
		`{"type":"assistant","requestId":"req_1","message":{"model":"claude-opus-5","usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":5}}}`,
		`{"type":"assistant","requestId":"req_1","message":{"model":"claude-opus-5","usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":5}}}`,
		`not json at all`,
		`{"type":"assistant","requestId":"req_2","message":{"model":"claude-sonnet-5","usage":{"input_tokens":1,"output_tokens":7}}}`,
	)
	report, version, err := readTranscriptUsage("claude", path, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if version != "2.1.300" || report.Model != "claude-opus-5, claude-sonnet-5" || report.Source != usageartifact.SourceTranscript || report.Scope != usageartifact.ScopeSession {
		t.Errorf("version %q model %q source %q scope %q", version, report.Model, report.Source, report.Scope)
	}
	if *report.InputTokens != 11 || *report.CacheCreationInputTokens != 100 || *report.CachedInputTokens != 1000 || *report.OutputTokens != 12 {
		t.Errorf("tokens = in %d create %d read %d out %d; want 11 100 1000 12", *report.InputTokens, *report.CacheCreationInputTokens, *report.CachedInputTokens, *report.OutputTokens)
	}
	if err := report.Validate(); err != nil {
		t.Errorf("report does not validate: %v", err)
	}
}

func TestReadTranscriptUsageCodexTakesTheRunningTotal(t *testing.T) {
	path := writeTranscript(t, "rollout.jsonl",
		`{"type":"session_meta","payload":{"id":"s1","cli_version":"0.150.1"}}`,
		`{"type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.6-sol"}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":5},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":5}}}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":350,"cached_input_tokens":130,"output_tokens":17},"last_token_usage":{"input_tokens":250,"cached_input_tokens":90,"output_tokens":12}}}}`,
	)
	report, version, err := readTranscriptUsage("codex", path, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	// Each token_count's last_token_usage is one response; the total is the
	// file's, not the session's. cache_write was never reported: nil, not 0.
	if version != "0.150.1" || report.Model != "gpt-5.6-sol" || *report.InputTokens != 350 || *report.CachedInputTokens != 130 || report.CacheCreationInputTokens != nil || *report.OutputTokens != 17 {
		t.Errorf("report = in %v cached %v create %v out %v model %q version %q", report.InputTokens, report.CachedInputTokens, report.CacheCreationInputTokens, report.OutputTokens, report.Model, version)
	}
}

func TestReadTranscriptUsageRefusesWhatItCannotRead(t *testing.T) {
	if _, _, err := readTranscriptUsage("claude", "relative.jsonl", time.Time{}); err == nil {
		t.Error("a relative path was accepted")
	}
	if _, _, err := readTranscriptUsage("claude", t.TempDir(), time.Time{}); err == nil {
		t.Error("a directory was accepted")
	}
	empty := writeTranscript(t, "empty.jsonl", `{"type":"user"}`)
	if _, _, err := readTranscriptUsage("claude", empty, time.Time{}); err == nil || !strings.Contains(err.Error(), "no usage") {
		t.Errorf("a transcript without usage = %v, want an error saying so", err)
	}
	if _, _, err := readTranscriptUsage("gemini", empty, time.Time{}); err == nil {
		t.Error("an unknown provider was accepted")
	}
	// A FIFO at the path must not hold the recorder: nobody writes to it.
	fifo := filepath.Join(t.TempDir(), "fifo.jsonl")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := readTranscriptUsage("claude", fifo, time.Time{}); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("FIFO = %v, want a refusal", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO transcript blocked")
	}
}

// Only this session's lines count: a resumed session appends to the same
// transcript, placeholders are not the model's, and the version is the one
// that wrote the last line.
func TestReadTranscriptUsageCountsOnlyThisSession(t *testing.T) {
	path := writeTranscript(t, "resumed.jsonl",
		`{"type":"user","version":"2.1.199","timestamp":"2026-08-01T10:00:00Z"}`,
		`{"type":"assistant","requestId":"old","timestamp":"2026-08-01T10:00:05Z","message":{"model":"claude-opus-4-1","usage":{"input_tokens":999,"output_tokens":999}}}`,
		`{"type":"user","version":"2.1.250","timestamp":"2026-09-02T09:00:00Z"}`,
		`{"type":"assistant","isApiErrorMessage":true,"timestamp":"2026-09-02T09:00:01Z","message":{"model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`{"type":"assistant","requestId":"new","timestamp":"2026-09-02T09:00:02Z","message":{"model":"claude-opus-5","usage":{"input_tokens":3,"output_tokens":4}}}`,
	)
	since := time.Date(2026, 9, 2, 8, 59, 50, 0, time.UTC)
	report, version, err := readTranscriptUsage("claude", path, since)
	if err != nil {
		t.Fatal(err)
	}
	if version != "2.1.250" || report.Model != "claude-opus-5" || *report.InputTokens != 3 || *report.OutputTokens != 4 || report.CachedInputTokens != nil {
		t.Errorf("report = model %q in %v out %v cached %v, version %q", report.Model, report.InputTokens, report.OutputTokens, report.CachedInputTokens, version)
	}
	onlySynthetic := writeTranscript(t, "synthetic.jsonl",
		`{"type":"assistant","isApiErrorMessage":true,"message":{"model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0}}}`,
	)
	if _, _, err := readTranscriptUsage("claude", onlySynthetic, time.Time{}); !errors.Is(err, errTranscriptNoUsage) {
		t.Errorf("placeholders alone = %v, want no usage", err)
	}
}

// At session end the recorder reads the transcript the hooks named and files
// what it says as provider-reported usage, and the provider's version lands
// in the manifest.
func TestSessionFilesTranscriptUsageAtSessionEnd(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	sessionSocketHome(t)
	transcript := writeTranscript(t, "session.jsonl",
		`{"type":"user","version":"2.1.300"}`,
		`{"type":"assistant","requestId":"r1","message":{"model":"claude-opus-5","usage":{"input_tokens":12,"cache_read_input_tokens":300,"output_tokens":40}}}`,
	)
	const sessionID = "session-usage-0001"
	socket, done, stderr := serveInProcess(t, sessionID, repo)
	deliver(t, socket, sessionEvent(t, sessionID, repo, hookSessionStart, map[string]any{"source": "startup", "transcript_path": transcript}))
	deliver(t, socket, sessionEvent(t, sessionID, repo, hookSessionEnd, map[string]any{"reason": "other", "transcript_path": transcript}))
	if code := waitExit(t, done); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	dir := onlyRunDir(t, root)
	if m := readManifestFile(t, dir); m.ProviderVersion != "2.1.300" || m.WarningCount != 0 {
		t.Errorf("manifest version %q warnings %d; want 2.1.300 and none (stderr %q)", m.ProviderVersion, m.WarningCount, stderr.String())
	}
	reported, err := readProviderUsage(dir, "claude")
	if err != nil || reported == nil || *reported.InputTokens != 12 || *reported.CachedInputTokens != 300 || *reported.OutputTokens != 40 || reported.Source != usageartifact.SourceTranscript || reported.Model != "claude-opus-5" {
		t.Fatalf("provider usage = %+v, %v", reported, err)
	}
	_, stdout, _ := run(t, "show", "latest")
	flat := strings.Join(strings.Fields(stdout), " ")
	for _, want := range []string{"Input Tokens 12", "Cached Input Tokens 300", "Output Tokens 40", "Model claude-opus-5", "Source the provider's transcript", "Version 2.1.300"} {
		if !strings.Contains(flat, want) {
			t.Errorf("show output lacks %q:\n%s", want, stdout)
		}
	}
}

// A line the reader cannot hold ends the read with an error, never a crash
// or a partial count presented as whole.
func TestReadTranscriptUsageRefusesAnOversizedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "long.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"type":"assistant","requestId":"r","message":{"model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n")
	f.WriteString(`{"type":"user","content":"`)
	chunk := strings.Repeat("x", 1<<20)
	for i := 0; i < 9; i++ {
		f.WriteString(chunk)
	}
	f.WriteString(`"}` + "\n")
	f.Close()
	if _, _, err := readTranscriptUsage("claude", path, time.Time{}); err == nil || !strings.Contains(err.Error(), "read transcript") {
		t.Errorf("oversized line = %v, want a read error", err)
	}
}

// A transcript that names the version but holds no usage still gives the
// manifest its version, and an unreadable one is counted as a warning.
func TestSessionTranscriptVersionWithoutUsageAndUnreadableTranscript(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	sessionSocketHome(t)
	versionOnly := writeTranscript(t, "version-only.jsonl", `{"type":"user","version":"2.1.301"}`)
	socket, done, stderr := serveInProcess(t, "session-version-only", repo)
	deliver(t, socket, sessionEvent(t, "session-version-only", repo, hookSessionStart, map[string]any{"transcript_path": versionOnly}))
	deliver(t, socket, sessionEvent(t, "session-version-only", repo, hookSessionEnd, map[string]any{"reason": "other"}))
	if code := waitExit(t, done); code != 0 {
		t.Fatalf("exit code = %d (stderr %q)", code, stderr.String())
	}
	dir := onlyRunDir(t, root)
	if m := readManifestFile(t, dir); m.ProviderVersion != "2.1.301" || m.WarningCount != 0 {
		t.Errorf("manifest version %q warnings %d, want 2.1.301 and no warning", m.ProviderVersion, m.WarningCount)
	}
	if reported, err := readProviderUsage(dir, "claude"); err != nil || reported != nil {
		t.Errorf("usage = %+v, %v; want none filed", reported, err)
	}

	root2 := home(t)
	sessionSocketHome(t)
	socket, done, stderr = serveInProcess(t, "session-bad-transcript", repo)
	deliver(t, socket, sessionEvent(t, "session-bad-transcript", repo, hookSessionStart, map[string]any{"transcript_path": t.TempDir()}))
	deliver(t, socket, sessionEvent(t, "session-bad-transcript", repo, hookSessionEnd, map[string]any{"reason": "other"}))
	if code := waitExit(t, done); code != 0 {
		t.Fatalf("exit code = %d (stderr %q)", code, stderr.String())
	}
	if m := readManifestFile(t, onlyRunDir(t, root2)); m.WarningCount != 1 || !strings.Contains(stderr.String(), "not a regular file") {
		t.Errorf("warnings = %d, stderr %q; want the unreadable transcript counted once", m.WarningCount, stderr.String())
	}
}
