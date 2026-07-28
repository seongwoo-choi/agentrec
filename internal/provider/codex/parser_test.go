package codex

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/action"
)

// fixturePath resolves a Codex fixture relative to this package's source
// directory so tests do not depend on the process working directory.
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test source directory")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "testdata", "codex", name)
}

func parseFixture(t *testing.T, name string) ParseResult {
	t.Helper()
	f, err := os.Open(fixturePath(t, name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	res, err := Parse(f)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return res
}

func TestParseCommandExecutionYieldsOneShellExecAction(t *testing.T) {
	res := parseFixture(t, "command-execution.jsonl")

	var shell []action.Action
	for _, a := range res.Actions {
		if a.Type == action.TypeShellExec {
			shell = append(shell, a)
		}
	}
	if len(shell) != 1 {
		t.Fatalf("shell.exec action count = %d, want 1", len(shell))
	}
	if shell[0].ID != "item_fixture_cmd_01" {
		t.Errorf("ID = %q, want %q", shell[0].ID, "item_fixture_cmd_01")
	}
}

func TestParseCommandExecutionCompletionFields(t *testing.T) {
	var got action.Action
	for _, a := range parseFixture(t, "command-execution.jsonl").Actions {
		if a.Type == action.TypeShellExec {
			got = a
		}
	}

	if got.Provider != "codex" {
		t.Errorf("Provider = %q, want %q", got.Provider, "codex")
	}
	if got.Assurance != action.AssuranceProviderReported {
		t.Errorf("Assurance = %q, want %q", got.Assurance, action.AssuranceProviderReported)
	}
	if got.Status != "completed" {
		t.Errorf("Status = %q, want %q", got.Status, "completed")
	}

	var input struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(got.Input, &input); err != nil {
		t.Fatalf("unmarshal Input: %v", err)
	}
	if input.Command != "go build ./..." {
		t.Errorf("Input.command = %q, want %q", input.Command, "go build ./...")
	}

	var result struct {
		AggregatedOutput string `json:"aggregatedOutput"`
		ExitCode         *int   `json:"exitCode"`
	}
	if err := json.Unmarshal(got.Result, &result); err != nil {
		t.Fatalf("unmarshal Result: %v", err)
	}
	if result.AggregatedOutput != "ok\n" {
		t.Errorf("Result.aggregatedOutput = %q, want %q", result.AggregatedOutput, "ok\n")
	}
	// Exit code 0 is the success case and must survive serialization rather
	// than being elided as a zero value.
	if result.ExitCode == nil {
		t.Fatalf("Result.exitCode missing, want 0")
	}
	if *result.ExitCode != 0 {
		t.Errorf("Result.exitCode = %d, want 0", *result.ExitCode)
	}
}

// TestParseAgentMessagesAndOrder covers the two agent_message items in the
// fixture, which bracket the command: actions keep the order their items first
// appear in the stream.
func TestParseAgentMessagesAndOrder(t *testing.T) {
	res := parseFixture(t, "command-execution.jsonl")

	want := []struct {
		id         string
		actionType string
	}{
		{"item_fixture_msg_01", action.TypeAgentMessage},
		{"item_fixture_cmd_01", action.TypeShellExec},
		{"item_fixture_msg_02", action.TypeAgentMessage},
	}
	if len(res.Actions) != len(want) {
		t.Fatalf("action count = %d, want %d", len(res.Actions), len(want))
	}
	for i, w := range want {
		if res.Actions[i].ID != w.id {
			t.Errorf("action[%d].ID = %q, want %q", i, res.Actions[i].ID, w.id)
		}
		if res.Actions[i].Type != w.actionType {
			t.Errorf("action[%d].Type = %q, want %q", i, res.Actions[i].Type, w.actionType)
		}
	}

	msg := res.Actions[0]
	if msg.Provider != "codex" {
		t.Errorf("message Provider = %q, want %q", msg.Provider, "codex")
	}
	if msg.Assurance != action.AssuranceProviderReported {
		t.Errorf("message Assurance = %q, want %q", msg.Assurance, action.AssuranceProviderReported)
	}
	// A message arrives already finished: Codex emits only item.completed for it.
	if msg.Status != "completed" {
		t.Errorf("message Status = %q, want %q", msg.Status, "completed")
	}
	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		t.Fatalf("unmarshal message Result: %v", err)
	}
	if result.Text != "Running the build." {
		t.Errorf("message Result.text = %q, want %q", result.Text, "Running the build.")
	}
}

func TestParseProviderErrorsAsActionsWithoutParserWarnings(t *testing.T) {
	stream := `{"type":"item.completed","item":{"id":"item_error_1","type":"error","message":"unstable feature enabled"}}
{"type":"item.completed","item":{"id":"item_error_2","type":"error","message":"skill context budget shortened"}}
`
	res, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if res.WarningCount != 0 {
		t.Errorf("WarningCount = %d, want 0 for recognized provider errors", res.WarningCount)
	}
	if len(res.Actions) != 2 {
		t.Fatalf("action count = %d, want 2", len(res.Actions))
	}
	for i, want := range []string{"unstable feature enabled", "skill context budget shortened"} {
		got := res.Actions[i]
		if got.Type != action.TypeProviderError {
			t.Errorf("action[%d].Type = %q, want %q", i, got.Type, action.TypeProviderError)
		}
		if got.Status != statusFailed {
			t.Errorf("action[%d].Status = %q, want %q", i, got.Status, statusFailed)
		}
		var input struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(got.Input, &input); err != nil {
			t.Fatalf("unmarshal action[%d] Input: %v", i, err)
		}
		if input.Message != want {
			t.Errorf("action[%d].Input.message = %q, want %q", i, input.Message, want)
		}
	}
}

// TestParseCollabToolCallWaitIsKnownMetadata covers collaboration bookkeeping
// emitted around wait, which does not describe an action taken in the run.
func TestParseCollabToolCallWaitIsKnownMetadata(t *testing.T) {
	stream := `{"type":"item.started","item":{"id":"item_wait_01","type":"collab_tool_call","tool":"wait","status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_wait_01","type":"collab_tool_call","tool":"wait","status":"completed"}}
`
	res, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if res.WarningCount != 0 {
		t.Errorf("WarningCount = %d, want 0", res.WarningCount)
	}
	if len(res.Actions) != 0 {
		t.Errorf("actions = %+v, want none", res.Actions)
	}
}

func TestParseMCPToolCallYieldsOneCompletedMCPAction(t *testing.T) {
	stream := `{"type":"item.started","item":{"id":"item_mcp_01","type":"mcp_tool_call","server":"codegraph","tool":"codegraph_explore","arguments":{"query":"parser"},"status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_mcp_01","type":"mcp_tool_call","server":"codegraph","tool":"codegraph_explore","arguments":{"query":"parser"},"result":{"content":"ok"},"error":null,"status":"completed"}}
`
	res, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if res.WarningCount != 0 || len(res.Actions) != 1 {
		t.Fatalf("warnings = %d, actions = %d, want 0 and 1", res.WarningCount, len(res.Actions))
	}
	got := res.Actions[0]
	if got.ID != "item_mcp_01" || got.Type != action.TypeMCPCall || got.Status != statusCompleted {
		t.Errorf("action = %+v", got)
	}
	if got.Assurance != action.AssuranceProviderReported || !got.FinishedAt.IsZero() {
		t.Errorf("assurance = %q, finished = %v", got.Assurance, got.FinishedAt)
	}
	var input struct {
		Server    string          `json:"server"`
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(got.Input, &input); err != nil {
		t.Fatal(err)
	}
	if input.Server != "codegraph" || input.Tool != "codegraph_explore" || string(input.Arguments) != `{"query":"parser"}` {
		t.Errorf("input = %+v", input)
	}
	var result struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(got.Result, &result); err != nil {
		t.Fatal(err)
	}
	if string(result.Result) != `{"content":"ok"}` || string(result.Error) != "null" {
		t.Errorf("result = %s, error = %s", result.Result, result.Error)
	}
}

func TestParseTodoListLifecycleIsKnownMetadata(t *testing.T) {
	stream := `{"type":"item.started","item":{"id":"item_todo_01","type":"todo_list","items":[]}}
{"type":"item.updated","item":{"id":"item_todo_01","type":"todo_list","items":[]}}
{"type":"item.completed","item":{"id":"item_todo_01","type":"todo_list","items":[]}}
`
	res, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if res.WarningCount != 0 || len(res.Actions) != 0 {
		t.Errorf("warnings = %d, actions = %+v, want 0 and none", res.WarningCount, res.Actions)
	}
}

// TestParseFileChangeYieldsOneCompletedFileEditAction covers the real Codex
// file_change lifecycle. Its structured paths are provider-reported evidence,
// not timestamps that the source did not provide.
func TestParseFileChangeYieldsOneCompletedFileEditAction(t *testing.T) {
	stream := `{"type":"item.started","item":{"id":"item_change_01","type":"file_change","status":"in_progress","changes":[{"path":"internal/provider/codex/parser.go","kind":"update"}]}}
{"type":"item.completed","item":{"id":"item_change_01","type":"file_change","status":"completed","changes":[{"path":"internal/provider/codex/parser.go","kind":"update"}]}}
`
	res, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if res.WarningCount != 0 {
		t.Errorf("WarningCount = %d, want 0", res.WarningCount)
	}
	if len(res.Actions) != 1 {
		t.Fatalf("action count = %d, want 1", len(res.Actions))
	}

	got := res.Actions[0]
	if got.ID != "item_change_01" {
		t.Errorf("ID = %q, want %q", got.ID, "item_change_01")
	}
	if got.Type != action.TypeFileEdit {
		t.Errorf("Type = %q, want %q", got.Type, action.TypeFileEdit)
	}
	if got.Provider != providerName {
		t.Errorf("Provider = %q, want %q", got.Provider, providerName)
	}
	if got.Assurance != action.AssuranceProviderReported {
		t.Errorf("Assurance = %q, want %q", got.Assurance, action.AssuranceProviderReported)
	}
	if got.Status != statusCompleted {
		t.Errorf("Status = %q, want %q", got.Status, statusCompleted)
	}
	if !got.StartedAt.IsZero() {
		t.Errorf("StartedAt = %v, want zero when the source omits timestamps", got.StartedAt)
	}
	if !got.FinishedAt.IsZero() {
		t.Errorf("FinishedAt = %v, want zero when the source omits timestamps", got.FinishedAt)
	}
	if len(got.Result) != 0 {
		t.Errorf("Result = %s, want omitted", got.Result)
	}

	var input map[string]json.RawMessage
	if err := json.Unmarshal(got.Input, &input); err != nil {
		t.Fatalf("unmarshal Input: %v", err)
	}
	if len(input) != 2 {
		t.Errorf("Input field count = %d, want 2 (path and changes)", len(input))
	}
	var path string
	if err := json.Unmarshal(input["path"], &path); err != nil {
		t.Fatalf("unmarshal Input.path: %v", err)
	}
	if path != "internal/provider/codex/parser.go" {
		t.Errorf("Input.path = %q, want %q", path, "internal/provider/codex/parser.go")
	}
	var changes []map[string]json.RawMessage
	if err := json.Unmarshal(input["changes"], &changes); err != nil {
		t.Fatalf("unmarshal Input.changes: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("Input.changes count = %d, want 1", len(changes))
	}
	if len(changes[0]) != 2 {
		t.Errorf("Input.changes[0] field count = %d, want 2 (path and kind)", len(changes[0]))
	}
	var structuredChanges []struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(input["changes"], &structuredChanges); err != nil {
		t.Fatalf("unmarshal Input.changes values: %v", err)
	}
	change := structuredChanges[0]
	if change.Path != "internal/provider/codex/parser.go" {
		t.Errorf("Input.changes[0].path = %q, want %q", change.Path, "internal/provider/codex/parser.go")
	}
	if change.Kind != "update" {
		t.Errorf("Input.changes[0].kind = %q, want %q", change.Kind, "update")
	}
}

func TestParseFailedFileChangeKeepsProviderStatus(t *testing.T) {
	stream := `{"type":"item.completed","item":{"id":"item_change_failed_01","type":"file_change","status":"failed","changes":[{"path":"internal/provider/codex/parser.go","kind":"update"}]}}
`
	res, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if res.WarningCount != 0 {
		t.Errorf("WarningCount = %d, want 0", res.WarningCount)
	}
	if len(res.Actions) != 1 {
		t.Fatalf("action count = %d, want 1", len(res.Actions))
	}
	if got := res.Actions[0].Status; got != statusFailed {
		t.Errorf("Status = %q, want %q", got, statusFailed)
	}
}

// TestParseUnknownItemTypeWarnsWithoutError covers forward compatibility: an
// item kind this parser does not model is counted and skipped, not fatal.
func TestParseUnknownItemTypeWarnsWithoutError(t *testing.T) {
	res := parseFixture(t, "unknown-event.jsonl")

	if res.WarningCount != 1 {
		t.Errorf("WarningCount = %d, want 1", res.WarningCount)
	}
	if len(res.Actions) != 0 {
		t.Errorf("actions = %+v, want none", res.Actions)
	}
}

// TestParseSkipsMalformedAndUnknownEventsAndKeepsParsing covers stream
// resilience: a line that is not JSON and an event kind this parser does not
// model each cost one warning, while the known metadata events cost none and a
// later valid item still parses.
func TestParseSkipsMalformedAndUnknownEventsAndKeepsParsing(t *testing.T) {
	stream := `{"type":"thread.started","thread_id":"thread_resilient_01"}
{"type":"turn.started"}
this line is not json at all
{"type":"future.unknown_event","payload":{"note":"unmodeled top-level event"}}
{"type":"item.started","item":{"id":"item_resilient_cmd_01","type":"command_execution","command":"go vet ./...","status":"in_progress"}}
{"type":"turn.completed","usage":{"input_tokens":12,"output_tokens":3}}
`
	res, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if res.WarningCount != 2 {
		t.Errorf("WarningCount = %d, want 2 (malformed line + unknown event)", res.WarningCount)
	}
	if len(res.Actions) != 1 {
		t.Fatalf("action count = %d, want 1", len(res.Actions))
	}
	if res.Actions[0].ID != "item_resilient_cmd_01" {
		t.Errorf("ID = %q, want %q", res.Actions[0].ID, "item_resilient_cmd_01")
	}
	if res.Actions[0].Status != "in_progress" {
		t.Errorf("Status = %q, want %q", res.Actions[0].Status, "in_progress")
	}
}

// TestParseDuplicateCommandStartKeepsFirst covers a repeated item.started for
// one item ID: the first start defines the action's input and position, and the
// repeat is warned about rather than creating a second action.
func TestParseDuplicateCommandStartKeepsFirst(t *testing.T) {
	stream := `{"type":"item.started","timestamp":"2026-01-01T00:00:01.000Z","item":{"id":"item_dup_start_01","type":"command_execution","command":"go build ./...","status":"in_progress"}}
{"type":"item.started","timestamp":"2026-01-01T00:00:02.000Z","item":{"id":"item_dup_start_01","type":"command_execution","command":"rm -rf /","status":"in_progress"}}
`
	res, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Actions) != 1 {
		t.Fatalf("action count = %d, want 1", len(res.Actions))
	}
	if res.WarningCount != 1 {
		t.Errorf("WarningCount = %d, want 1", res.WarningCount)
	}

	got := res.Actions[0]
	var input struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(got.Input, &input); err != nil {
		t.Fatalf("unmarshal Input: %v", err)
	}
	if input.Command != "go build ./..." {
		t.Errorf("Input.command = %q, want the first start's %q", input.Command, "go build ./...")
	}
	wantStart := time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC)
	if !got.StartedAt.Equal(wantStart) {
		t.Errorf("StartedAt = %v, want the first start's %v", got.StartedAt, wantStart)
	}
}

// TestParseDuplicateCommandCompletionKeepsFirst covers a repeated
// item.completed for one item ID: the first completion's result, status and
// finish time stand, and the repeat is warned about rather than overwriting it.
func TestParseDuplicateCommandCompletionKeepsFirst(t *testing.T) {
	stream := `{"type":"item.started","timestamp":"2026-01-01T00:00:01.000Z","item":{"id":"item_dup_done_01","type":"command_execution","command":"go build ./...","status":"in_progress"}}
{"type":"item.completed","timestamp":"2026-01-01T00:00:05.000Z","item":{"id":"item_dup_done_01","type":"command_execution","command":"go build ./...","aggregated_output":"ok\n","exit_code":0,"status":"completed"}}
{"type":"item.completed","timestamp":"2026-01-01T00:00:09.000Z","item":{"id":"item_dup_done_01","type":"command_execution","command":"go build ./...","aggregated_output":"boom\n","exit_code":1,"status":"failed"}}
`
	res, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Actions) != 1 {
		t.Fatalf("action count = %d, want 1", len(res.Actions))
	}
	if res.WarningCount != 1 {
		t.Errorf("WarningCount = %d, want 1", res.WarningCount)
	}

	got := res.Actions[0]
	if got.Status != "completed" {
		t.Errorf("Status = %q, want the first completion's %q", got.Status, "completed")
	}
	wantFinish := time.Date(2026, 1, 1, 0, 0, 5, 0, time.UTC)
	if !got.FinishedAt.Equal(wantFinish) {
		t.Errorf("FinishedAt = %v, want the first completion's %v", got.FinishedAt, wantFinish)
	}
	var result struct {
		AggregatedOutput string `json:"aggregatedOutput"`
		ExitCode         *int   `json:"exitCode"`
	}
	if err := json.Unmarshal(got.Result, &result); err != nil {
		t.Fatalf("unmarshal Result: %v", err)
	}
	if result.AggregatedOutput != "ok\n" {
		t.Errorf("Result.aggregatedOutput = %q, want the first completion's %q", result.AggregatedOutput, "ok\n")
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Errorf("Result.exitCode = %v, want the first completion's 0", result.ExitCode)
	}
}

// TestParseUnmatchedCommandCompletionWarns covers a completion whose start was
// never seen — a truncated or resumed stream. There is no action to close out,
// so nothing is invented and the orphan is counted.
func TestParseUnmatchedCommandCompletionWarns(t *testing.T) {
	stream := `{"type":"item.completed","timestamp":"2026-01-01T00:00:05.000Z","item":{"id":"item_orphan_01","type":"command_execution","command":"go build ./...","aggregated_output":"ok\n","exit_code":0,"status":"completed"}}
`
	res, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Actions) != 0 {
		t.Errorf("actions = %+v, want none", res.Actions)
	}
	if res.WarningCount != 1 {
		t.Errorf("WarningCount = %d, want 1", res.WarningCount)
	}
}

// TestParseEmptyItemIDsWarnAndYieldNoActions covers items that arrive without an
// ID. An empty ID cannot be correlated, and emitting it would collide with every
// other ID-less action, so each such event is counted and dropped.
func TestParseEmptyItemIDsWarnAndYieldNoActions(t *testing.T) {
	stream := `{"type":"item.started","item":{"id":"","type":"command_execution","command":"go build ./...","status":"in_progress"}}
{"type":"item.completed","item":{"id":"","type":"agent_message","text":"anonymous message"}}
`
	res, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Actions) != 0 {
		t.Errorf("actions = %+v, want none", res.Actions)
	}
	if res.WarningCount != 2 {
		t.Errorf("WarningCount = %d, want 2 (one per ID-less item)", res.WarningCount)
	}
}

// TestParseDuplicateMessageIDKeepsFirst covers a repeated agent_message ID:
// messages arrive already finished, so a repeat would append a second action
// under an ID that is meant to be unique.
func TestParseDuplicateMessageIDKeepsFirst(t *testing.T) {
	stream := `{"type":"item.completed","item":{"id":"item_dup_msg_01","type":"agent_message","text":"first"}}
{"type":"item.completed","item":{"id":"item_dup_msg_01","type":"agent_message","text":"second"}}
`
	res, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Actions) != 1 {
		t.Fatalf("action count = %d, want 1", len(res.Actions))
	}
	if res.WarningCount != 1 {
		t.Errorf("WarningCount = %d, want 1", res.WarningCount)
	}
	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(res.Actions[0].Result, &result); err != nil {
		t.Fatalf("unmarshal Result: %v", err)
	}
	if result.Text != "first" {
		t.Errorf("Result.text = %q, want the first message's %q", result.Text, "first")
	}
}

// TestParseNonZeroExitCodeMarksActionFailed covers a command that ran to
// completion but failed: Codex reports the item as "completed" because the
// process finished, so the exit code is what decides the action's outcome. The
// raw code stays in the result for callers that need the specific value.
func TestParseNonZeroExitCodeMarksActionFailed(t *testing.T) {
	stream := `{"type":"item.started","item":{"id":"item_exit7_01","type":"command_execution","command":"go test ./...","status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_exit7_01","type":"command_execution","command":"go test ./...","aggregated_output":"FAIL\n","exit_code":7,"status":"completed"}}
`
	res, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Actions) != 1 {
		t.Fatalf("action count = %d, want 1", len(res.Actions))
	}
	got := res.Actions[0]
	if got.Status != "failed" {
		t.Errorf("Status = %q, want %q", got.Status, "failed")
	}
	var result struct {
		ExitCode *int `json:"exitCode"`
	}
	if err := json.Unmarshal(got.Result, &result); err != nil {
		t.Fatalf("unmarshal Result: %v", err)
	}
	if result.ExitCode == nil || *result.ExitCode != 7 {
		t.Errorf("Result.exitCode = %v, want 7", result.ExitCode)
	}
}

// TestParseProviderFailureStatusMarksActionFailed covers commands Codex itself
// declares failed. Such an item may carry no exit code at all — a command that
// never launched has none — or a zero one, so the provider's own status has to
// be read rather than inferred from the code alone.
func TestParseProviderFailureStatusMarksActionFailed(t *testing.T) {
	tests := []struct {
		name string
		// completedFields are the exit code and status of the item.completed
		// event, spelled out per case because an absent exit code is meaningful.
		completedFields string
		want            string
	}{
		{"failed without exit code", `"status":"failed"`, "failed"},
		{"failed with zero exit code", `"exit_code":0,"status":"failed"`, "failed"},
		{"error without exit code", `"status":"error"`, "failed"},
		{"error with zero exit code", `"exit_code":0,"status":"error"`, "failed"},
		{"cancelled without exit code", `"status":"cancelled"`, "failed"},
		{"canceled without exit code", `"status":"canceled"`, "failed"},
		{"timeout without exit code", `"status":"timeout"`, "failed"},
		{"timed_out without exit code", `"status":"timed_out"`, "failed"},
		{"interrupted without exit code", `"status":"interrupted"`, "failed"},
		{"completed stays completed", `"exit_code":0,"status":"completed"`, "completed"},
		{"unmodeled status with zero exit code stays completed", `"exit_code":0,"status":"finished_somehow"`, "completed"},
		{"unmodeled status without exit code fails safe", `"status":"finished_somehow"`, "failed"},
		{"empty status without exit code fails safe", `"status":""`, "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := `{"type":"item.started","item":{"id":"item_status_01","type":"command_execution","command":"go build ./...","status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_status_01","type":"command_execution","command":"go build ./...",` + tt.completedFields + `}}
`
			res, err := Parse(strings.NewReader(stream))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(res.Actions) != 1 {
				t.Fatalf("action count = %d, want 1", len(res.Actions))
			}
			if got := res.Actions[0].Status; got != tt.want {
				t.Errorf("Status = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestParseLargeAggregatedOutputExceedingDefaultBuffer covers a single event
// line far past bufio.Scanner's 64 KiB default: a command that printed a lot
// puts all of it on one line, and dropping that line would silently lose the
// action rather than merely truncating its output.
func TestParseLargeAggregatedOutputExceedingDefaultBuffer(t *testing.T) {
	output := strings.Repeat("x", 1<<20)
	completed, err := json.Marshal(map[string]any{
		"type": "item.completed",
		"item": map[string]any{
			"id":                "item_large_01",
			"type":              "command_execution",
			"command":           "go test ./...",
			"aggregated_output": output,
			"exit_code":         0,
			"status":            "completed",
		},
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	stream := `{"type":"item.started","item":{"id":"item_large_01","type":"command_execution","command":"go test ./...","status":"in_progress"}}` + "\n" +
		string(completed) + "\n"

	res, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Actions) != 1 {
		t.Fatalf("action count = %d, want 1", len(res.Actions))
	}
	got := res.Actions[0]
	if got.Status != "completed" {
		t.Errorf("Status = %q, want %q", got.Status, "completed")
	}
	var result struct {
		AggregatedOutput string `json:"aggregatedOutput"`
	}
	if err := json.Unmarshal(got.Result, &result); err != nil {
		t.Fatalf("unmarshal Result: %v", err)
	}
	if len(result.AggregatedOutput) != len(output) {
		t.Errorf("Result.aggregatedOutput length = %d, want %d", len(result.AggregatedOutput), len(output))
	}
}

// TestParseCommandExecutionDecodesOptionalTimestamps covers streams that carry
// event timestamps: Codex omits them in some transports, so they are optional,
// but when present they must time the action.
func TestParseCommandExecutionDecodesOptionalTimestamps(t *testing.T) {
	stream := `{"type":"item.started","timestamp":"2026-01-01T00:00:01.000Z","item":{"id":"item_ts_01","type":"command_execution","command":"go test ./...","status":"in_progress"}}
{"type":"item.completed","timestamp":"2026-01-01T00:00:05.000Z","item":{"id":"item_ts_01","type":"command_execution","command":"go test ./...","aggregated_output":"ok\n","exit_code":0,"status":"completed"}}
`
	res, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Actions) != 1 {
		t.Fatalf("action count = %d, want 1", len(res.Actions))
	}
	got := res.Actions[0]
	wantStart := time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC)
	if !got.StartedAt.Equal(wantStart) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, wantStart)
	}
	wantFinish := time.Date(2026, 1, 1, 0, 0, 5, 0, time.UTC)
	if !got.FinishedAt.Equal(wantFinish) {
		t.Errorf("FinishedAt = %v, want %v", got.FinishedAt, wantFinish)
	}
}

// errStreamBroken is the sentinel a failingReader fails with, so a test can
// assert the read failure reaches the caller identifiable.
var errStreamBroken = errors.New("stream broken")

// failingReader yields one JSONL line and then fails, standing in for a stream
// that dies mid-read: a closed pipe, an unreadable file.
type failingReader struct {
	line string
	sent bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, errStreamBroken
	}
	r.sent = true
	return copy(p, r.line), nil
}

// TestParseReturnsReadError covers a stream that fails partway through. The
// events after the failure are unknown, so the truncated action list must not
// be handed back as if it were the whole recording.
func TestParseReturnsReadError(t *testing.T) {
	r := &failingReader{line: `{"type":"item.completed","item":{"id":"item_err_01","type":"agent_message","text":"hi","status":"completed"}}` + "\n"}

	_, err := Parse(r)
	if !errors.Is(err, errStreamBroken) {
		t.Fatalf("Parse error = %v, want %v", err, errStreamBroken)
	}
}
