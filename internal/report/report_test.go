package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/action"
)

var goldenStart = time.Date(2026, 7, 28, 13, 45, 57, 0, time.UTC)

// goldenReport exercises one action per rendering branch — a mapped label with
// a path detail, a shell action carrying an exit code, and an unknown type with
// a zero timestamp and a parent — alongside all four evidence sources.
func goldenReport() Report {
	return Report{
		Actions: []action.Action{
			{
				ID:         "a1",
				Type:       action.TypeFileRead,
				Provider:   "claude",
				Assurance:  action.AssuranceProviderReported,
				StartedAt:  goldenStart,
				FinishedAt: goldenStart.Add(5 * time.Millisecond),
				Status:     "completed",
				Input:      json.RawMessage(`{"file_path":"README.md"}`),
			},
			{
				ID:         "a2",
				Type:       action.TypeShellExec,
				Provider:   "claude",
				Assurance:  action.AssuranceProviderReported,
				StartedAt:  goldenStart.Add(time.Second),
				FinishedAt: goldenStart.Add(time.Second + 226*time.Millisecond),
				Status:     "completed",
				Input:      json.RawMessage(`{"command":"pwd"}`),
				Result:     json.RawMessage(`{"exitCode":0,"stdout":"/tmp"}`),
			},
			{
				ID:        "a3",
				ParentID:  "a2",
				Type:      "custom.tool.probe",
				Provider:  "codex",
				Assurance: action.AssuranceProviderReported,
				Status:    "in_progress",
				Input:     json.RawMessage(`{"name":"probe","depth":3}`),
			},
		},
		Supervisor: []Field{{Name: "Exit Code", Value: "0"}, {Name: "Signal", Value: "none"}},
		Repository: []Field{
			{Name: "Status", Value: "AVAILABLE"},
			{Name: "Files", Value: "3 (2 tracked, 1 untracked)"},
			{Name: "Diff", Value: "+32/-8, 0 binary"},
			{Name: "Stored Text", Value: "1"},
			{Name: "Baseline", Value: goldenBaseline},
			{Name: "Attribution", Value: "observed during run, not causal proof"},
		},
		Verification: []Field{
			{Name: "Status", Value: "PASS"},
			{Name: "Config", Value: ".agentrec.yaml"},
			{Name: "Config SHA-256", Value: goldenConfigSum},
			{Name: "Check", Value: `PASS test  "./gradlew" "test"  8.21s  exit 0`},
			{Name: "Attribution", Value: "verification_observed"},
		},
	}
}

// Digests as the evidence records them: whole, so a reader can compare one
// against the repository rather than against a prefix of it.
const (
	goldenBaseline  = "1f0c2f2a2b6b4f2d9c8e7a6b5c4d3e2f1a0b9c8d"
	goldenConfigSum = "3b1d2c4e5f60718293a4b5c6d7e8f90112233445566778899aabbccddeeff001"
)

const goldenTerminal = `ACTION TIMELINE

PROVIDER-REPORTED ACTIONS
13:45:57  READ  README.md
  Source       claude
  Assurance    provider_reported
  Result       success
  Duration     5ms

13:45:58  SHELL  pwd
  Source       claude
  Assurance    provider_reported
  Exit         0
  Duration     226ms

not reported  PROBE  probe
  Source       codex
  Assurance    provider_reported
  Parent       a2
  Result       in progress

SUPERVISOR-OBSERVED RESULT
  Exit Code    0
  Signal       none

REPOSITORY-OBSERVED CHANGES
  Status       AVAILABLE
  Files        3 (2 tracked, 1 untracked)
  Diff         +32/-8, 0 binary
  Stored Text  1
  Baseline     ` + goldenBaseline + `
  Attribution  observed during run, not causal proof

VERIFICATION-OBSERVED RESULT
  Status       PASS
  Config       .agentrec.yaml
  Config SHA-256 ` + goldenConfigSum + `
  Check        PASS test  "./gradlew" "test"  8.21s  exit 0
  Attribution  verification_observed
`

func TestActionFailed(t *testing.T) {
	tests := []struct {
		name   string
		action action.Action
		want   bool
	}{
		{name: "provider error", action: action.Action{Type: action.TypeProviderError}, want: true},
		{name: "nonzero shell", action: action.Action{Type: action.TypeShellExec, Status: "completed", Result: json.RawMessage(`{"exitCode":7}`)}, want: true},
		{name: "zero shell", action: action.Action{Type: action.TypeShellExec, Status: "completed", Result: json.RawMessage(`{"exitCode":0}`)}},
		{name: "failed shell with zero exit", action: action.Action{Type: action.TypeShellExec, Status: "failed", Result: json.RawMessage(`{"exitCode":0}`)}, want: true},
		{name: "failed status", action: action.Action{Type: action.TypeFileRead, Status: "failed"}, want: true},
		{name: "unknown status", action: action.Action{Type: action.TypeFileRead, Status: "future"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ActionFailed(test.action); got != test.want {
				t.Errorf("ActionFailed() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestRenderTerminalGolden(t *testing.T) {
	got := renderTerminal(t, goldenReport())

	if got != goldenTerminal {
		t.Errorf("RenderTerminal() =\n%s\nwant\n%s", got, goldenTerminal)
	}
}

const goldenMarkdown = "# Action Timeline\n" + `
## Provider-Reported Actions

### Action 1

- ` + "`Time`" + `: ` + "`13:45:57`" + `
- ` + "`Action`" + `: ` + "`READ`" + `
- ` + "`Detail`" + `: ` + "`README.md`" + `
- ` + "`Source`" + `: ` + "`claude`" + `
- ` + "`Assurance`" + `: ` + "`provider_reported`" + `
- ` + "`Result`" + `: ` + "`success`" + `
- ` + "`Duration`" + `: ` + "`5ms`" + `

### Action 2

- ` + "`Time`" + `: ` + "`13:45:58`" + `
- ` + "`Action`" + `: ` + "`SHELL`" + `
- ` + "`Detail`" + `: ` + "`pwd`" + `
- ` + "`Source`" + `: ` + "`claude`" + `
- ` + "`Assurance`" + `: ` + "`provider_reported`" + `
- ` + "`Exit`" + `: ` + "`0`" + `
- ` + "`Duration`" + `: ` + "`226ms`" + `

### Action 3

- ` + "`Time`" + `: ` + "`not reported`" + `
- ` + "`Action`" + `: ` + "`PROBE`" + `
- ` + "`Detail`" + `: ` + "`probe`" + `
- ` + "`Source`" + `: ` + "`codex`" + `
- ` + "`Assurance`" + `: ` + "`provider_reported`" + `
- ` + "`Parent`" + `: ` + "`a2`" + `
- ` + "`Result`" + `: ` + "`in progress`" + `

## Supervisor-Observed Result

- ` + "`Exit Code`" + `: ` + "`0`" + `
- ` + "`Signal`" + `: ` + "`none`" + `

## Repository-Observed Changes

- ` + "`Status`" + `: ` + "`AVAILABLE`" + `
- ` + "`Files`" + `: ` + "`3 (2 tracked, 1 untracked)`" + `
- ` + "`Diff`" + `: ` + "`+32/-8, 0 binary`" + `
- ` + "`Stored Text`" + `: ` + "`1`" + `
- ` + "`Baseline`" + `: ` + "`" + goldenBaseline + "`" + `
- ` + "`Attribution`" + `: ` + "`observed during run, not causal proof`" + `

## Verification-Observed Result

- ` + "`Status`" + `: ` + "`PASS`" + `
- ` + "`Config`" + `: ` + "`.agentrec.yaml`" + `
- ` + "`Config SHA-256`" + `: ` + "`" + goldenConfigSum + "`" + `
- ` + "`Check`" + `: ` + "`PASS test  \"./gradlew\" \"test\"  8.21s  exit 0`" + `
- ` + "`Attribution`" + `: ` + "`verification_observed`" + `
`

func TestRenderMarkdownGolden(t *testing.T) {
	got := renderMarkdown(t, goldenReport())

	if got != goldenMarkdown {
		t.Errorf("RenderMarkdown() =\n%s\nwant\n%s", got, goldenMarkdown)
	}
}

func renderTerminal(t *testing.T, report Report) string {
	t.Helper()

	var buf strings.Builder
	if err := RenderTerminal(&buf, report); err != nil {
		t.Fatalf("RenderTerminal() error = %v", err)
	}
	return buf.String()
}

func renderMarkdown(t *testing.T, report Report) string {
	t.Helper()

	var buf strings.Builder
	if err := RenderMarkdown(&buf, report); err != nil {
		t.Fatalf("RenderMarkdown() error = %v", err)
	}
	return buf.String()
}

const emptyTerminal = `ACTION TIMELINE

PROVIDER-REPORTED ACTIONS
  (none)

SUPERVISOR-OBSERVED RESULT
  (none)

REPOSITORY-OBSERVED CHANGES
  (none)

VERIFICATION-OBSERVED RESULT
  (none)
`

const emptyMarkdown = `# Action Timeline

## Provider-Reported Actions

(none)

## Supervisor-Observed Result

(none)

## Repository-Observed Changes

(none)

## Verification-Observed Result

(none)
`

func TestEmptyReportStatesEverySectionIsEmpty(t *testing.T) {
	if got := renderTerminal(t, Report{}); got != emptyTerminal {
		t.Errorf("RenderTerminal() =\n%s\nwant\n%s", got, emptyTerminal)
	}
	if got := renderMarkdown(t, Report{}); got != emptyMarkdown {
		t.Errorf("RenderMarkdown() =\n%s\nwant\n%s", got, emptyMarkdown)
	}
}

func TestMissingProviderTimestampIsExplicitlyUnavailable(t *testing.T) {
	report := Report{Actions: []action.Action{{Type: action.TypeShellExec}}}
	for _, got := range []string{renderTerminal(t, report), renderMarkdown(t, report)} {
		if !strings.Contains(got, "not reported") {
			t.Errorf("rendering =\n%s\nwant missing provider time stated as not reported", got)
		}
		if strings.Contains(got, "--:--:--") {
			t.Errorf("rendering =\n%s\nmust not show a clock-like placeholder for missing evidence", got)
		}
	}
}

func TestProviderErrorMessageIsVisibleAsBoundedActionDetail(t *testing.T) {
	report := Report{Actions: []action.Action{{
		Type:   action.TypeProviderError,
		Status: "failed",
		Input:  json.RawMessage(`{"message":"skill context budget shortened"}`),
	}}}
	for _, got := range []string{renderTerminal(t, report), renderMarkdown(t, report)} {
		if !strings.Contains(got, "ERROR") || !strings.Contains(got, "skill context budget shortened") {
			t.Errorf("rendering =\n%s\nwant provider error label and message", got)
		}
	}
}

// What a report says is only as good as which source is being quoted, so the
// four sources are named apart from one another, in the same order, in both
// renderings — never merged into one verdict about the run.
func TestEveryEvidenceSourceIsNamedDistinctlyAndInOrder(t *testing.T) {
	for _, tt := range []struct {
		name   string
		got    string
		titles []string
	}{
		{"terminal", renderTerminal(t, goldenReport()), []string{titleProvider, titleSupervisor, titleRepository, titleVerification}},
		{"markdown", renderMarkdown(t, goldenReport()), []string{mdProvider, mdSupervisor, mdRepository, mdVerification}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			seen := map[string]bool{}
			at := -1
			for _, title := range tt.titles {
				if seen[title] {
					t.Fatalf("title %q names two evidence sources", title)
				}
				seen[title] = true

				found := strings.Index(tt.got, "\n"+title+"\n")
				if found < 0 {
					t.Fatalf("rendering =\n%s\nwant a %q section", tt.got, title)
				}
				if found < at {
					t.Errorf("title %q is out of order in\n%s", title, tt.got)
				}
				if strings.Count(tt.got, "\n"+title+"\n") != 1 {
					t.Errorf("title %q appears more than once in\n%s", title, tt.got)
				}
				at = found
			}
		})
	}
}

func TestRenderersKeepCallerOrderAndLeaveActionsUnchanged(t *testing.T) {
	// Timestamps run backwards so any sorting by time would be visible.
	actions := []action.Action{
		{Type: action.TypeFileRead, StartedAt: goldenStart.Add(2 * time.Hour), Input: json.RawMessage(`{"path":"third.txt"}`)},
		{Type: action.TypeFileRead, StartedAt: goldenStart.Add(time.Hour), Input: json.RawMessage(`{"path":"second.txt"}`)},
		{Type: action.TypeFileRead, StartedAt: goldenStart, Input: json.RawMessage(`{"path":"first.txt"}`)},
	}
	before, err := json.Marshal(actions)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	for _, render := range []func(*testing.T, Report) string{renderTerminal, renderMarkdown} {
		got := render(t, Report{Actions: actions})

		third, second, first := strings.Index(got, "third.txt"), strings.Index(got, "second.txt"), strings.Index(got, "first.txt")
		if third < 0 || !(third < second && second < first) {
			t.Errorf("rendered detail order = %d/%d/%d, want input order in\n%s", third, second, first, got)
		}
	}

	after, err := json.Marshal(actions)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("actions mutated:\n got %s\nwant %s", after, before)
	}
}

func TestLabelsCoverKnownTypesAndFallBackToLastComponent(t *testing.T) {
	tests := []struct {
		actionType string
		want       string
	}{
		{action.TypeFileRead, "READ"},
		{action.TypeFileWrite, "WRITE"},
		{action.TypeFileEdit, "EDIT"},
		{action.TypeShellExec, "SHELL"},
		{action.TypeSearch, "SEARCH"},
		{action.TypeWebFetch, "WEB"},
		{action.TypeMCPCall, "MCP"},
		{action.TypeSubagentSpawn, "SUBAGENT"},
		{action.TypeAgentMessage, "MESSAGE"},
		{"vendor.thing.probe", "PROBE"},
		{"probe", "PROBE"},
		{"trailing.", "ACTION"},
		{"", "ACTION"},
	}

	for _, tt := range tests {
		t.Run(tt.actionType, func(t *testing.T) {
			got := renderTerminal(t, Report{Actions: []action.Action{{Type: tt.actionType}}})

			want := zeroClock + "  " + tt.want
			if !strings.Contains(got, "\n"+want+"\n") {
				t.Errorf("RenderTerminal() =\n%s\nwant header %q", got, want)
			}
		})
	}
}

func TestOnlyAllowlistedInputKeysAreRendered(t *testing.T) {
	report := Report{Actions: []action.Action{{
		Type:   action.TypeFileRead,
		Status: "completed",
		Input:  json.RawMessage(`{"api_key":"sk-live-LEAKED","authorization":"Bearer LEAKED","file_path":"notes.txt","content":"LEAKED body"}`),
		Result: json.RawMessage(`{"password":"LEAKED","bytes":42}`),
	}}}

	for _, got := range []string{renderTerminal(t, report), renderMarkdown(t, report)} {
		if !strings.Contains(got, "notes.txt") {
			t.Errorf("output lost the allowlisted detail:\n%s", got)
		}
		for _, forbidden := range []string{"LEAKED", "api_key", "authorization", "password", "content", "42"} {
			if strings.Contains(got, forbidden) {
				t.Errorf("output leaked %q:\n%s", forbidden, got)
			}
		}
	}
}

func TestMalformedPayloadsAreOmitted(t *testing.T) {
	payloads := []json.RawMessage{
		nil,
		json.RawMessage(``),
		json.RawMessage(`{"file_path":`),
		json.RawMessage(`[{"file_path":"a.txt"}]`),
		json.RawMessage(`"file_path"`),
		json.RawMessage(`null`),
		json.RawMessage(`{"file_path":42}`),
		json.RawMessage(`{"file_path":{"nested":"a.txt"}}`),
	}

	for _, payload := range payloads {
		t.Run(string(payload), func(t *testing.T) {
			got := renderTerminal(t, Report{Actions: []action.Action{{Type: action.TypeFileRead, Input: payload}}})

			if !strings.Contains(got, "\n"+zeroClock+"  READ\n") {
				t.Errorf("RenderTerminal() =\n%s\nwant a bare READ header", got)
			}
		})
	}
}

func TestShellExitCodeOnlyComesFromAnIntegerExitCode(t *testing.T) {
	tests := []struct {
		name   string
		result json.RawMessage
		want   string
	}{
		{"integer", json.RawMessage(`{"exitCode":0}`), "  Exit         0"},
		{"nonzero", json.RawMessage(`{"exitCode":137}`), "  Exit         137"},
		{"negative", json.RawMessage(`{"exitCode":-1}`), "  Exit         -1"},
		{"string", json.RawMessage(`{"exitCode":"0"}`), "  Result       failed"},
		{"fractional", json.RawMessage(`{"exitCode":1.5}`), "  Result       failed"},
		{"missing", json.RawMessage(`{"stdout":"hi"}`), "  Result       failed"},
		{"malformed", json.RawMessage(`{"exitCode":`), "  Result       failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderTerminal(t, Report{Actions: []action.Action{
				{Type: action.TypeShellExec, Status: "failed", Result: tt.result},
			}})

			if !strings.Contains(got, "\n"+tt.want+"\n") {
				t.Errorf("RenderTerminal() =\n%s\nwant line %q", got, tt.want)
			}
		})
	}
}

func TestEmptyStatusOmitsTheResultLine(t *testing.T) {
	got := renderTerminal(t, Report{Actions: []action.Action{{Type: action.TypeFileRead, Provider: "claude"}}})

	if strings.Contains(got, "Result") {
		t.Errorf("RenderTerminal() =\n%s\nwant no Result line", got)
	}
}

func TestDurationNeedsTwoTimestampsAndForwardTime(t *testing.T) {
	tests := []struct {
		name     string
		started  time.Time
		finished time.Time
		want     string
	}{
		{"forward", goldenStart, goldenStart.Add(time.Second), "  Duration     1s"},
		{"instant", goldenStart, goldenStart, "  Duration     0s"},
		{"backwards", goldenStart, goldenStart.Add(-time.Second), ""},
		{"no finish", goldenStart, time.Time{}, ""},
		{"no start", time.Time{}, goldenStart, ""},
		{"neither", time.Time{}, time.Time{}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderTerminal(t, Report{Actions: []action.Action{
				{Type: action.TypeFileRead, StartedAt: tt.started, FinishedAt: tt.finished},
			}})

			if tt.want == "" {
				if strings.Contains(got, "Duration") {
					t.Errorf("RenderTerminal() =\n%s\nwant no Duration line", got)
				}
				return
			}
			if !strings.Contains(got, "\n"+tt.want+"\n") {
				t.Errorf("RenderTerminal() =\n%s\nwant line %q", got, tt.want)
			}
		})
	}
}

func TestTerminalEscapesControlSequencesOntoOneLine(t *testing.T) {
	hostile := "a\x1b[31mred\nrm -rf /\tb\x00c\u202ed\u009be\rf"
	report := Report{
		Actions:    []action.Action{{Type: action.TypeShellExec, Input: mustInput(t, "command", hostile)}},
		Supervisor: []Field{{Name: "Std\nerr", Value: hostile}},
	}

	got := renderTerminal(t, report)

	want := `a\x1b[31mred\nrm -rf /\tb\x00c\u202ed\x9be`
	if !strings.Contains(got, want) {
		t.Errorf("RenderTerminal() =\n%s\nwant escaped %q", got, want)
	}
	if strings.ContainsAny(got, "\x1b\x00\u202e\u009b") {
		t.Errorf("RenderTerminal() emitted raw control characters:\n%q", got)
	}
	// One action header and one supervisor field stand where the empty report
	// prints "(none)", so the hostile values added no lines of their own.
	if lines := strings.Count(got, "\n"); lines != strings.Count(emptyTerminal, "\n") {
		t.Errorf("RenderTerminal() line count = %d, want the fixed layout, got:\n%s", lines, got)
	}
}

// A supplementary-plane rune has no four-digit form, so \u would print an
// ambiguous escape no Go unquoter accepts; it needs the eight-digit \U.
func TestNonPrintableRunesAboveTheBMPUseTheEightDigitEscape(t *testing.T) {
	for _, tt := range []struct {
		name string
		r    rune
		want string
	}{
		{"just above the BMP", '\U0001000c', `\U0001000c`},
		{"above the BMP", '\U000e0001', `\U000e0001`},
		{"byte", '\u009b', `\x9b`},
		{"inside the BMP", '\u202e', `\u202e`},
		{"printable above the BMP", '\U0001f600', "\U0001f600"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapeRune(tt.r); got != tt.want {
				t.Errorf("escapeRune(%U) = %q, want %q", tt.r, got, tt.want)
			}
		})
	}
}

func TestMarkdownEscapesControlSequencesAndFencesBackticks(t *testing.T) {
	report := Report{
		Actions: []action.Action{{
			Type:  action.TypeShellExec,
			Input: mustInput(t, "command", "echo ``` `x` ```\n## Heading"),
		}},
		Supervisor: []Field{{Name: "`Name`", Value: "`"}},
	}

	got := renderMarkdown(t, report)

	for _, want := range []string{
		"- " + "`Action`" + ": " + "`SHELL`",
		// A detail is one line before it is fenced: the line break that would
		// have opened a heading is a space.
		"- " + "`Detail`" + ": " + "````echo ``` `x` ``` ## Heading````",
		"- " + "`` `Name` ``" + ": " + "`` ` ``",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderMarkdown() =\n%s\nwant line %q", got, want)
		}
	}
	if strings.Contains(got, "\n## Heading") {
		t.Errorf("RenderMarkdown() let a value open a heading:\n%s", got)
	}
}

func TestLongValuesAreCappedAtTheDisplayWidth(t *testing.T) {
	long := strings.Repeat("x", 500)
	report := Report{
		Actions:    []action.Action{{Type: action.TypeShellExec, Input: mustInput(t, "command", strings.Repeat("\n", 500))}},
		Supervisor: []Field{{Name: "Long", Value: long}},
	}

	got := renderTerminal(t, report)

	if want := strings.Repeat("x", maxValueRunes-len(ellipsis)) + ellipsis; !strings.Contains(got, want) {
		t.Errorf("RenderTerminal() =\n%s\nwant a value capped to %d runes", got, maxValueRunes)
	}
	for _, line := range strings.Split(got, "\n") {
		if runes := len([]rune(line)); runes > maxValueRunes+20 {
			t.Errorf("line of %d runes exceeds the cap: %q", runes, line)
		}
	}
}

func TestWriterErrorsPropagate(t *testing.T) {
	wantErr := errors.New("disk full")

	if err := RenderTerminal(failingWriter{wantErr}, goldenReport()); !errors.Is(err, wantErr) {
		t.Errorf("RenderTerminal() error = %v, want %v", err, wantErr)
	}
	if err := RenderMarkdown(failingWriter{wantErr}, goldenReport()); !errors.Is(err, wantErr) {
		t.Errorf("RenderMarkdown() error = %v, want %v", err, wantErr)
	}
}

type failingWriter struct{ err error }

func (f failingWriter) Write([]byte) (int, error) { return 0, f.err }

// A renderer writes as it goes rather than building the whole report first, and
// it stops where the writer stopped: once the destination has refused a write,
// the actions after it are not rendered at all.
func TestRenderersStopAtTheFirstWriterError(t *testing.T) {
	const accepted = 6

	rep := Report{Actions: make([]action.Action, 5000)}
	for i := range rep.Actions {
		rep.Actions[i] = action.Action{
			Type:  action.TypeFileRead,
			Input: mustInput(t, "file_path", fmt.Sprintf("file-%d.md", i)),
		}
	}
	last := "file-4999.md"

	tests := []struct {
		name   string
		render func(io.Writer, Report) error
	}{
		{"terminal", RenderTerminal},
		{"markdown", RenderMarkdown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &stoppingWriter{accept: accepted, err: errors.New("disk full")}

			if err := tt.render(w, rep); !errors.Is(err, w.err) {
				t.Fatalf("render() error = %v, want %v", err, w.err)
			}
			if w.writes != accepted+1 {
				t.Errorf("writes = %d, want the renderer to stop at the refused write (%d)", w.writes, accepted+1)
			}
			if strings.Contains(w.got.String(), last) {
				t.Errorf("output =\n%s\nwant the actions after the refused write left unrendered", w.got.String())
			}
		})
	}
}

// stoppingWriter accepts a fixed number of writes and refuses every one after
// them, counting how many it was asked for.
type stoppingWriter struct {
	accept int
	err    error
	writes int
	got    strings.Builder
}

func (s *stoppingWriter) Write(p []byte) (int, error) {
	s.writes++
	if s.writes > s.accept {
		return 0, s.err
	}
	return s.got.Write(p)
}

func mustInput(t *testing.T, key, value string) json.RawMessage {
	t.Helper()

	raw, err := json.Marshal(map[string]string{key: value})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return raw
}
