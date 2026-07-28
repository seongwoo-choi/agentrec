package claude

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

// fixturePath resolves a Claude fixture relative to this package's source
// directory so tests do not depend on the process working directory.
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test source directory")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "testdata", "claude", name)
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

func TestParseReadAndBashYieldsTwoActionsInOrder(t *testing.T) {
	res := parseFixture(t, "read-and-bash.jsonl")

	want := []string{"toolu_fixture_read_01", "toolu_fixture_bash_01"}
	if len(res.Actions) != len(want) {
		t.Fatalf("action count = %d, want %d", len(res.Actions), len(want))
	}
	for i, id := range want {
		if res.Actions[i].ID != id {
			t.Errorf("action[%d].ID = %q, want %q", i, res.Actions[i].ID, id)
		}
	}
}

func TestParseReadActionFields(t *testing.T) {
	got := parseFixture(t, "read-and-bash.jsonl").Actions[0]

	if got.Type != action.TypeFileRead {
		t.Errorf("Type = %q, want %q", got.Type, action.TypeFileRead)
	}
	if got.Provider != "claude" {
		t.Errorf("Provider = %q, want %q", got.Provider, "claude")
	}
	if got.Assurance != action.AssuranceProviderReported {
		t.Errorf("Assurance = %q, want %q", got.Assurance, action.AssuranceProviderReported)
	}
	if got.Status != "completed" {
		t.Errorf("Status = %q, want %q", got.Status, "completed")
	}
	wantStart := time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC)
	if !got.StartedAt.Equal(wantStart) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, wantStart)
	}
	wantFinish := time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC)
	if !got.FinishedAt.Equal(wantFinish) {
		t.Errorf("FinishedAt = %v, want %v", got.FinishedAt, wantFinish)
	}

	var input struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(got.Input, &input); err != nil {
		t.Fatalf("unmarshal Input: %v", err)
	}
	if input.FilePath != "/workspace/project/main.go" {
		t.Errorf("Input.file_path = %q, want %q", input.FilePath, "/workspace/project/main.go")
	}

	var result struct {
		Content       string `json:"content"`
		ToolUseResult struct {
			File struct {
				FilePath string `json:"filePath"`
			} `json:"file"`
		} `json:"toolUseResult"`
	}
	if err := json.Unmarshal(got.Result, &result); err != nil {
		t.Fatalf("unmarshal Result: %v", err)
	}
	if result.Content != "package main\n" {
		t.Errorf("Result.content = %q, want %q", result.Content, "package main\n")
	}
	if result.ToolUseResult.File.FilePath != "/workspace/project/main.go" {
		t.Errorf("Result.toolUseResult.file.filePath = %q, want %q",
			result.ToolUseResult.File.FilePath, "/workspace/project/main.go")
	}
}

func TestParseBashActionCapturesCommandAndStdout(t *testing.T) {
	got := parseFixture(t, "read-and-bash.jsonl").Actions[1]

	if got.Type != action.TypeShellExec {
		t.Errorf("Type = %q, want %q", got.Type, action.TypeShellExec)
	}
	if got.Status != "completed" {
		t.Errorf("Status = %q, want %q", got.Status, "completed")
	}
	// Duration comes from the event timestamps: tool_use at 00:00:03,
	// tool_result at 00:00:04.
	if d := got.FinishedAt.Sub(got.StartedAt); d != time.Second {
		t.Errorf("FinishedAt-StartedAt = %v, want %v", d, time.Second)
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
		ToolUseResult struct {
			Stdout string `json:"stdout"`
		} `json:"toolUseResult"`
	}
	if err := json.Unmarshal(got.Result, &result); err != nil {
		t.Fatalf("unmarshal Result: %v", err)
	}
	if result.ToolUseResult.Stdout != "ok\n" {
		t.Errorf("Result.toolUseResult.stdout = %q, want %q", result.ToolUseResult.Stdout, "ok\n")
	}
}

func TestParseErrorToolResultMarksActionFailed(t *testing.T) {
	stream := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_err_01","name":"Bash","input":{"command":"false"}}]},"timestamp":"2026-01-01T00:00:01.000Z"}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_err_01","content":"exit status 1","is_error":true}]},"timestamp":"2026-01-01T00:00:02.000Z"}
`
	res, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Actions) != 1 {
		t.Fatalf("action count = %d, want 1", len(res.Actions))
	}
	if res.Actions[0].Status != "failed" {
		t.Errorf("Status = %q, want %q", res.Actions[0].Status, "failed")
	}
}

func TestParsePendingToolUseStaysInProgress(t *testing.T) {
	stream := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_pending_01","name":"Read","input":{"file_path":"/tmp/a"}}]},"timestamp":"2026-01-01T00:00:01.000Z"}
`
	res, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Actions) != 1 {
		t.Fatalf("action count = %d, want 1", len(res.Actions))
	}
	if res.Actions[0].Status != "in_progress" {
		t.Errorf("Status = %q, want %q", res.Actions[0].Status, "in_progress")
	}
	if !res.Actions[0].FinishedAt.IsZero() {
		t.Errorf("FinishedAt = %v, want zero", res.Actions[0].FinishedAt)
	}
}

func TestParseCountsWarningsAndKeepsParsing(t *testing.T) {
	stream := `{"type":"assistant","message":{"content":[{"type":"tool_use"
{"type":"totally_unknown_event","timestamp":"2026-01-01T00:00:01.000Z"}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_never_seen","content":"orphan"}]},"timestamp":"2026-01-01T00:00:02.000Z"}

{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_after_warnings","name":"Read","input":{"file_path":"/tmp/b"}}]},"timestamp":"2026-01-01T00:00:03.000Z"}
`
	res, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if res.WarningCount != 3 {
		t.Errorf("WarningCount = %d, want 3", res.WarningCount)
	}
	if len(res.Actions) != 1 || res.Actions[0].ID != "toolu_after_warnings" {
		t.Fatalf("actions = %+v, want single toolu_after_warnings", res.Actions)
	}
}

func TestParseKnownIgnoredEventsProduceNoWarnings(t *testing.T) {
	stream := `{"type":"system","subtype":"hook_started","hook_id":"hook_a","timestamp":"2026-01-01T00:00:01.000Z"}
{"type":"rate_limit_event","timestamp":"2026-01-01T00:00:02.000Z"}
{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"hello"}]},"timestamp":"2026-01-01T00:00:03.000Z"}
{"type":"result","subtype":"success","timestamp":"2026-01-01T00:00:04.000Z"}
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

// TestParseToolProgressIsKnownMetadata covers Claude's top-level progress
// event, which describes tool activity but is not an action itself.
func TestParseToolProgressIsKnownMetadata(t *testing.T) {
	res, err := Parse(strings.NewReader(`{"type":"tool_progress"}` + "\n"))
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

func TestParseHandlesLineLargerThanDefaultScannerBuffer(t *testing.T) {
	big := strings.Repeat("x", 1<<20) // 1 MiB of file content in one tool_result
	stream := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_big_01","name":"Read","input":{"file_path":"/tmp/big"}}]},"timestamp":"2026-01-01T00:00:01.000Z"}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_big_01","content":"` + big + `"}]},"timestamp":"2026-01-01T00:00:02.000Z"}
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
	if res.Actions[0].Status != "completed" {
		t.Errorf("Status = %q, want %q", res.Actions[0].Status, "completed")
	}
	var result struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(res.Actions[0].Result, &result); err != nil {
		t.Fatalf("unmarshal Result: %v", err)
	}
	if len(result.Content) != len(big) {
		t.Errorf("Result.content length = %d, want %d", len(result.Content), len(big))
	}
}

// failingReader yields one valid line and then fails, standing in for a
// truncated or unreadable transcript.
type failingReader struct {
	line string
	done bool
}

func (fr *failingReader) Read(p []byte) (int, error) {
	if fr.done {
		return 0, errRead
	}
	fr.done = true
	n := copy(p, fr.line)
	return n, nil
}

var errRead = errors.New("simulated read failure")

func TestParseReturnsUnderlyingReadError(t *testing.T) {
	fr := &failingReader{
		line: `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_partial_01","name":"Read","input":{"file_path":"/tmp/c"}}]},"timestamp":"2026-01-01T00:00:01.000Z"}` + "\n",
	}

	_, err := Parse(fr)
	if !errors.Is(err, errRead) {
		t.Fatalf("Parse error = %v, want %v", err, errRead)
	}
}

func TestActionTypeMapsToolNames(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"Read", action.TypeFileRead},
		{"Bash", action.TypeShellExec},
		{"Write", action.TypeFileWrite},
		{"Edit", action.TypeFileEdit},
		{"Glob", action.TypeSearch},
		{"Grep", action.TypeSearch},
		{"WebFetch", action.TypeWebFetch},
		{"Task", action.TypeSubagentSpawn},
		{"Agent", action.TypeSubagentSpawn},
		{"mcp__github__create_issue", action.TypeMCPCall},
		{"mcp__", action.TypeMCPCall},
		{"NotebookEdit", action.TypeToolCall},
		{"", action.TypeToolCall},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := actionType(tt.name); got != tt.want {
				t.Errorf("actionType(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

// TestParseSubagentToolUseCarriesParentID covers nested subagent work: events
// emitted inside a Task carry the spawning tool_use id at the top level.
func TestParseSubagentToolUseCarriesParentID(t *testing.T) {
	stream := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_parent_01","name":"Task","input":{"prompt":"go"}}]},"timestamp":"2026-01-01T00:00:01.000Z"}
{"type":"assistant","parent_tool_use_id":"toolu_parent_01","message":{"content":[{"type":"tool_use","id":"toolu_child_01","name":"Read","input":{"file_path":"/tmp/a"}}]},"timestamp":"2026-01-01T00:00:02.000Z"}
`
	res, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Actions) != 2 {
		t.Fatalf("action count = %d, want 2", len(res.Actions))
	}
	if res.Actions[0].ParentID != "" {
		t.Errorf("parent action ParentID = %q, want empty", res.Actions[0].ParentID)
	}
	if res.Actions[1].ParentID != "toolu_parent_01" {
		t.Errorf("child action ParentID = %q, want %q", res.Actions[1].ParentID, "toolu_parent_01")
	}
}

// TestParseSkipsToolUseWithEmptyID covers external providers that emit a
// tool_use without an id: it cannot be correlated, so it is counted as a
// warning and dropped, and a tool_result with an empty id must not latch onto
// any action.
func TestParseSkipsToolUseWithEmptyID(t *testing.T) {
	stream := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"","name":"Read","input":{"file_path":"/tmp/a"}}]},"timestamp":"2026-01-01T00:00:01.000Z"}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"","content":"orphan"}]},"timestamp":"2026-01-01T00:00:02.000Z"}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_valid_01","name":"Bash","input":{"command":"true"}}]},"timestamp":"2026-01-01T00:00:03.000Z"}
`
	res, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Actions) != 1 {
		t.Fatalf("action count = %d, want 1", len(res.Actions))
	}
	if res.Actions[0].ID != "toolu_valid_01" {
		t.Errorf("action[0].ID = %q, want %q", res.Actions[0].ID, "toolu_valid_01")
	}
	if res.Actions[0].Status != "in_progress" {
		t.Errorf("Status = %q, want %q", res.Actions[0].Status, "in_progress")
	}
	if res.WarningCount != 2 {
		t.Errorf("WarningCount = %d, want 2 (one for empty tool_use id and one for unmatched empty tool_result id)", res.WarningCount)
	}
}

// TestParseDuplicateToolUseIDKeepsFirstAction covers a replayed tool_use: the
// same id emitted twice must not append a second action nor repoint the
// correlation, so the later tool_result closes out the first action and the
// first input is preserved.
func TestParseDuplicateToolUseIDKeepsFirstAction(t *testing.T) {
	stream := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_dup_01","name":"Read","input":{"file_path":"/tmp/first"}}]},"timestamp":"2026-01-01T00:00:01.000Z"}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_dup_01","name":"Read","input":{"file_path":"/tmp/second"}}]},"timestamp":"2026-01-01T00:00:02.000Z"}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_dup_01","content":"ok"}]},"timestamp":"2026-01-01T00:00:03.000Z"}
`
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
	var input struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(got.Input, &input); err != nil {
		t.Fatalf("unmarshal Input: %v", err)
	}
	if input.FilePath != "/tmp/first" {
		t.Errorf("Input.file_path = %q, want %q (first tool_use wins)", input.FilePath, "/tmp/first")
	}
	wantStart := time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC)
	if !got.StartedAt.Equal(wantStart) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, wantStart)
	}
	if res.WarningCount != 1 {
		t.Errorf("WarningCount = %d, want 1 (one per duplicate tool_use)", res.WarningCount)
	}
}

// TestParseDuplicateToolResultKeepsFirstResult covers a replayed tool_result:
// once an action is closed out, a later result for the same id must not
// overwrite its status, finish time or result payload.
func TestParseDuplicateToolResultKeepsFirstResult(t *testing.T) {
	stream := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_dupres_01","name":"Bash","input":{"command":"true"}}]},"timestamp":"2026-01-01T00:00:01.000Z"}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_dupres_01","content":"first"}]},"timestamp":"2026-01-01T00:00:02.000Z"}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_dupres_01","content":"second","is_error":true}]},"timestamp":"2026-01-01T00:00:09.000Z"}
`
	res, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Actions) != 1 {
		t.Fatalf("action count = %d, want 1", len(res.Actions))
	}
	got := res.Actions[0]
	if got.Status != "completed" {
		t.Errorf("Status = %q, want %q (first result wins)", got.Status, "completed")
	}
	wantFinish := time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC)
	if !got.FinishedAt.Equal(wantFinish) {
		t.Errorf("FinishedAt = %v, want %v", got.FinishedAt, wantFinish)
	}
	var result struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(got.Result, &result); err != nil {
		t.Fatalf("unmarshal Result: %v", err)
	}
	if result.Content != "first" {
		t.Errorf("Result.content = %q, want %q", result.Content, "first")
	}
	if res.WarningCount != 1 {
		t.Errorf("WarningCount = %d, want 1 (one per duplicate tool_result)", res.WarningCount)
	}
}

// TestParseHookResponseBeforeToolResultStillTimesAction covers reordered
// events: a PostToolUse hook_response can land between the tool_use and its
// tool_result. Its duration must be held and stamped onto the result the later
// tool_result creates, with the first positive duration still winning.
func TestParseHookResponseBeforeToolResultStillTimesAction(t *testing.T) {
	stream := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_reorder_01","name":"Read","input":{"file_path":"/tmp/a"}}]},"timestamp":"2026-01-01T00:00:01.000Z"}
{"type":"system","subtype":"hook_response","hook_event":"PostToolUse","output":"{\"tool_use_id\":\"toolu_reorder_01\",\"duration_ms\":7}","timestamp":"2026-01-01T00:00:02.000Z"}
{"type":"system","subtype":"hook_response","hook_event":"PostToolUse","output":"{\"tool_use_id\":\"toolu_reorder_01\",\"duration_ms\":9}","timestamp":"2026-01-01T00:00:03.000Z"}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_reorder_01","content":"body"}]},"timestamp":"2026-01-01T00:00:04.000Z"}
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
	if got.Status != "completed" {
		t.Errorf("Status = %q, want %q", got.Status, "completed")
	}
	var result struct {
		Content    string   `json:"content"`
		DurationMs *float64 `json:"durationMs"`
	}
	if err := json.Unmarshal(got.Result, &result); err != nil {
		t.Fatalf("unmarshal Result: %v", err)
	}
	if result.DurationMs == nil {
		t.Fatalf("Result.durationMs missing, want 7")
	}
	if *result.DurationMs != 7 {
		t.Errorf("Result.durationMs = %v, want 7 (first positive duration wins)", *result.DurationMs)
	}
	if result.Content != "body" {
		t.Errorf("Result.content = %q, want %q", result.Content, "body")
	}
}

// TestParseDuplicateHooksYieldSingleAction guards behavior that already exists:
// hook events arrive as "system" and are ignored, so repeated PreToolUse and
// PostToolUse pairs around one Read must not duplicate or warn.
func TestParseDuplicateHooksYieldSingleAction(t *testing.T) {
	res := parseFixture(t, "duplicate-hooks.jsonl")

	if len(res.Actions) != 1 {
		t.Fatalf("action count = %d, want 1", len(res.Actions))
	}
	got := res.Actions[0]
	if got.ID != "toolu_fixture_read_02" {
		t.Errorf("ID = %q, want %q", got.ID, "toolu_fixture_read_02")
	}
	if got.Type != action.TypeFileRead {
		t.Errorf("Type = %q, want %q", got.Type, action.TypeFileRead)
	}
	if got.Status != "completed" {
		t.Errorf("Status = %q, want %q", got.Status, "completed")
	}
	if res.WarningCount != 0 {
		t.Errorf("WarningCount = %d, want 0", res.WarningCount)
	}

	// The first PostToolUse hook_response carries the completed tool duration;
	// the later one (1 ms) must not overwrite it, and PreToolUse durations
	// (3 ms, 2 ms) measure hook activity, not the tool, so they are ignored.
	var result struct {
		Content       string          `json:"content"`
		ToolUseResult json.RawMessage `json:"toolUseResult"`
		DurationMs    *float64        `json:"durationMs"`
	}
	if err := json.Unmarshal(got.Result, &result); err != nil {
		t.Fatalf("unmarshal Result: %v", err)
	}
	if result.DurationMs == nil {
		t.Fatalf("Result.durationMs missing, want 4")
	}
	if *result.DurationMs != 4 {
		t.Errorf("Result.durationMs = %v, want 4", *result.DurationMs)
	}
	if result.Content != "# project\n" {
		t.Errorf("Result.content = %q, want %q", result.Content, "# project\n")
	}
	if len(result.ToolUseResult) == 0 {
		t.Error("Result.toolUseResult missing, want the provider payload preserved")
	}
}
