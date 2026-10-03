package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/action"
	"github.com/seongwoo-choi/agentrec/internal/storage"
)

// Claude Code reports a turn that ended on an API error (rate limit,
// authentication, billing...) through StopFailure, not Stop. Without it a
// session that never ran looks the same as one that has not answered yet.
func TestHooksPrintRegistersClaudeStopFailure(t *testing.T) {
	home(t)
	restore := sessionExecutable
	t.Cleanup(func() { sessionExecutable = restore })
	sessionExecutable = func() (string, error) { return "/usr/local/bin/agentrec", nil }

	code, stdout, stderr := run(t, "hooks", "print", "--claude")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	var settings hookSettings
	if err := json.Unmarshal([]byte(stdout), &settings); err != nil {
		t.Fatalf("stdout is not a settings fragment: %v", err)
	}
	groups := settings.Hooks["StopFailure"]
	if len(groups) != 1 || len(groups[0].Hooks) != 1 || groups[0].Hooks[0].Command != "/usr/local/bin/agentrec hook claude" {
		t.Fatalf("StopFailure groups = %+v, want the recorder's hook", groups)
	}

	code, stdout, _ = run(t, "hooks", "print", "--codex")
	if code != 0 {
		t.Fatalf("codex exit code = %d", code)
	}
	if strings.Contains(stdout, "StopFailure") {
		t.Errorf("the Codex fragment registers StopFailure, which Codex never sends")
	}
}

// The failure is filed as the provider's own error, with the error type,
// details and message it reported, verbatim and under its turn.
func TestSessionServeRecordsStopFailureAsProviderError(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	sessionSocketHome(t)
	const sessionID = "session-stop-failure"

	socket, done, stderr := serveInProcess(t, sessionID, repo)
	deliver(t, socket, sessionEvent(t, sessionID, repo, hookSessionStart, map[string]any{"source": "startup"}))
	deliver(t, socket, sessionEvent(t, sessionID, repo, hookUserPromptSubmit, map[string]any{"prompt": "do it", "prompt_id": "p-1"}))
	deliver(t, socket, sessionEvent(t, sessionID, repo, "StopFailure", map[string]any{
		"prompt_id":              "p-1",
		"error":                  "authentication_failed",
		"error_details":          "401 OAuth access token has been revoked",
		"last_assistant_message": "API Error: 401",
	}))
	deliver(t, socket, sessionEvent(t, sessionID, repo, hookSessionEnd, map[string]any{"reason": "other"}))
	if code := waitExit(t, done); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr.String())
	}

	actions := readActionsFile(t, onlyRunDir(t, root))
	if len(actions) != 2 {
		t.Fatalf("actions = %+v, want the prompt and the provider error", actions)
	}
	got := actions[1]
	if got.Type != action.TypeProviderError || got.Status != hookStatusFailed || got.ID != "failure-p-1" ||
		got.Provider != "claude" || got.Assurance != action.AssuranceProviderReported {
		t.Errorf("failure action = %s %s %s %s %s, want provider.error failed failure-p-1 claude provider_reported",
			got.ID, got.Type, got.Status, got.Provider, got.Assurance)
	}
	var input struct {
		Message      string `json:"message"`
		Error        string `json:"error"`
		ErrorDetails string `json:"errorDetails"`
	}
	if err := json.Unmarshal(got.Input, &input); err != nil || input.Error != "authentication_failed" ||
		input.ErrorDetails != "401 OAuth access token has been revoked" || input.Message != "API Error: 401" {
		t.Errorf("failure input = %s (%v), want the provider's error, details and message verbatim", got.Input, err)
	}
	if r := string(got.Result); !strings.Contains(r, `"source":"hook.StopFailure"`) || !strings.Contains(r, `"turn":"p-1"`) {
		t.Errorf("failure result = %s, want its hook source and turn", r)
	}
}

// The detail names the last recorded provider error and where it sits, so a
// run that ended on one can be told from a run that has not answered yet. It
// is the provider's report, not a verdict on the run.
func TestViewRunDetailCarriesLastProviderError(t *testing.T) {
	root := home(t)
	startedAt := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	const runID = "20261003T090000.000000000Z-eeeeeeee"
	b, err := storage.Create(root, runID, storage.Manifest{Provider: "claude", CWD: "/tmp", StartedAt: startedAt})
	if err != nil {
		t.Fatal(err)
	}
	prompt := action.Action{ID: "prompt-p-1", Type: action.TypeUserPrompt, Provider: "claude", Assurance: action.AssuranceProviderReported,
		StartedAt: startedAt, Status: "completed", Input: json.RawMessage(`{"prompt":"do it"}`)}
	failure := action.Action{ID: "failure-p-1", Type: action.TypeProviderError, Provider: "claude", Assurance: action.AssuranceProviderReported,
		StartedAt: startedAt.Add(time.Second), Status: "failed",
		Input: json.RawMessage(`{"message":"API Error: 401","error":"authentication_failed","errorDetails":"401 OAuth access token has been revoked"}`)}
	for _, act := range []action.Action{prompt, failure} {
		if err := b.WriteAction(act); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Finalize(storage.Finalization{EndedAt: startedAt.Add(2 * time.Second), ExitReason: "session_ended"}); err != nil {
		t.Fatal(err)
	}
	writeRun(t, root, "20261003T100000.000000000Z-ffffffff", "claude", startedAt.Add(time.Hour), "completed")

	handler := newViewHandler(root, "latest", false)
	t.Cleanup(func() { _ = handler.Close() })
	var detail struct {
		ActionCount   int `json:"actionCount"`
		ProviderError *struct {
			ActionID     string `json:"actionId"`
			Position     int    `json:"position"`
			Offset       int64  `json:"offset"`
			Error        string `json:"error"`
			ErrorDetails string `json:"errorDetails"`
			Message      string `json:"message"`
		} `json:"providerError"`
	}
	viewJSONRequest(t, handler, "/api/runs/"+runID, &detail)
	pe := detail.ProviderError
	if pe == nil {
		t.Fatal("run that ended on a provider error has no providerError")
	}
	if pe.ActionID != "failure-p-1" || pe.Position != 2 || detail.ActionCount != 2 || pe.Offset <= 0 ||
		pe.Error != "authentication_failed" || pe.ErrorDetails != "401 OAuth access token has been revoked" || pe.Message != "API Error: 401" {
		t.Errorf("providerError = %+v (of %d), want the recorded failure verbatim at position 2", *pe, detail.ActionCount)
	}

	var without struct {
		ProviderError *json.RawMessage `json:"providerError"`
	}
	viewJSONRequest(t, handler, "/api/runs/20261003T100000.000000000Z-ffffffff", &without)
	if without.ProviderError != nil {
		t.Errorf("run without provider errors must omit providerError, got %s", *without.ProviderError)
	}
}
