package report

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/seongwoo-choi/agentrec/internal/action"
)

func TestCompletedShellWithoutExitCodeDoesNotClaimSuccess(t *testing.T) {
	tests := []struct {
		name   string
		result string
	}{
		{"missing result", ""},
		{"missing exit code", `{}`},
		{"null result", `null`},
		{"non-object result", `[]`},
		{"malformed result", `{"exitCode":`},
		{"quoted exit code", `{"exitCode":"0"}`},
		{"fractional exit code", `{"exitCode":1.5}`},
		{"null exit code", `{"exitCode":null}`},
		{"boolean exit code", `{"exitCode":false}`},
		{"overflow exit code", `{"exitCode":9223372036854775808}`},
		{"success stdout", `{"stdout":"success: all tests passed; exit code 0"}`},
		{"failure stdout", `{"stdout":"FAILED (failures=3); exit code 1"}`},
		{"codex hook response", `{"source":"hook.PostToolUse","toolResponse":"FAILED (failures=3)"}`},
		{"codex hook success response", `{"source":"hook.PostToolUse","toolResponse":"success: exit code 0"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertShellOutcome(t, "completed", tt.result, "Result", "completed (exit code not reported)", false)
		})
	}
}

func TestShellReportedOutcomesRemainExplicit(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		result     string
		field      string
		value      string
		wantFailed bool
	}{
		{"zero exit", "completed", `{"exitCode":0}`, "Exit", "0", false},
		{"nonzero exit", "completed", `{"exitCode":7}`, "Exit", "7", true},
		{"negative exit", "completed", `{"exitCode":-1}`, "Exit", "-1", true},
		{"failed without exit", "failed", `{}`, "Result", "failed", true},
		{"failed with invalid exit", "failed", `{"exitCode":"0"}`, "Result", "failed", true},
		{"failed hook response", "failed", `{"source":"hook.PostToolUse","toolResponse":"success"}`, "Result", "failed", true},
		{"in progress without exit", "in_progress", `{}`, "Result", "in progress", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertShellOutcome(t, tt.status, tt.result, tt.field, tt.value, tt.wantFailed)
		})
	}
}

func assertShellOutcome(t *testing.T, status, result, field, value string, wantFailed bool) {
	t.Helper()
	a := action.Action{
		Type: action.TypeShellExec, Provider: "codex", Status: status,
		Result: json.RawMessage(result),
	}
	rep := Report{Actions: []action.Action{a}}
	for _, rendering := range []struct {
		name string
		got  string
		want string
	}{
		{"terminal", renderTerminal(t, rep), fmt.Sprintf("  %-12s %s\n", field, value)},
		{"markdown", renderMarkdown(t, rep), fmt.Sprintf("- `%s`: `%s`\n", field, value)},
	} {
		t.Run(rendering.name, func(t *testing.T) {
			if !strings.Contains(rendering.got, rendering.want) {
				t.Errorf("missing outcome %q in:\n%s", rendering.want, rendering.got)
			}
			if strings.Contains(rendering.got, "success") {
				t.Errorf("shell rendering claims success without an explicit success outcome:\n%s", rendering.got)
			}
		})
	}
	t.Run("json", func(t *testing.T) {
		var out strings.Builder
		if err := RenderJSON(&out, "shell-outcome", rep); err != nil {
			t.Fatal(err)
		}
		var got jsonReport
		if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Actions) != 1 {
			t.Fatalf("exported actions = %d, want 1", len(got.Actions))
		}
		if got.Actions[0].Status != status {
			t.Errorf("exported status = %q, want unchanged %q", got.Actions[0].Status, status)
		}
		want := []jsonField{{Name: "Source", Value: "codex"}, {Name: field, Value: value}}
		if !reflect.DeepEqual(got.Actions[0].Fields, want) {
			t.Errorf("exported fields = %#v, want %#v", got.Actions[0].Fields, want)
		}
	})
	if rep.Actions[0].Status != status || string(rep.Actions[0].Result) != result {
		t.Error("rendering changed the stored action status or result")
	}
	if got := ActionFailed(rep.Actions[0]); got != wantFailed {
		t.Errorf("ActionFailed() = %t, want %t", got, wantFailed)
	}
}
