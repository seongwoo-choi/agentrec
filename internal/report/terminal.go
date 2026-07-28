// Package report renders a recorded run as a deterministic human-readable
// timeline. Renderers never emit raw provider JSON: an action is reduced to a
// label, one allowlisted detail and a fixed set of summary fields, and every
// value that came from a provider is escaped so it cannot forge lines or drive
// the terminal it is printed to.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/seongwoo-choi/agentrec/internal/action"
)

// Field is one named summary value in an evidence section.
type Field struct {
	Name  string
	Value string
}

// Report is everything a rendered run shows: the provider-reported actions in
// the order they were recorded, plus the summary fields observed by each of the
// other evidence sources.
type Report struct {
	Actions      []action.Action
	Supervisor   []Field
	Repository   []Field
	Verification []Field
}

// Section titles, kept identical between renderers so a terminal timeline and a
// Markdown one describe the same evidence sources.
const (
	titleTimeline     = "ACTION TIMELINE"
	titleProvider     = "PROVIDER-REPORTED ACTIONS"
	titleSupervisor   = "SUPERVISOR-OBSERVED RESULT"
	titleRepository   = "REPOSITORY-OBSERVED CHANGES"
	titleVerification = "VERIFICATION-OBSERVED RESULT"
)

// none marks a section with nothing to report, so an empty section is stated
// rather than silently missing.
const none = "(none)"

// zeroClock states that the provider supplied no action timestamp. It must not
// look like an observed clock value.
const zeroClock = "not reported"

// maxValueRunes bounds how much of any dynamic value is displayed. Long values
// are cut to this width including the ellipsis, so one pathological path or
// command cannot flood the timeline.
const maxValueRunes = 160

const ellipsis = "..."

// RenderTerminal writes the report as an aligned plain-text timeline.
func RenderTerminal(w io.Writer, report Report) error {
	out := &lineWriter{w: w}
	out.line(titleTimeline)
	out.line("")
	out.line(titleProvider)

	if len(report.Actions) == 0 {
		out.line("  " + none)
	}
	for i := range report.Actions {
		if out.err != nil {
			break
		}
		if i > 0 {
			out.line("")
		}
		for _, line := range terminalAction(viewOf(report.Actions[i])) {
			out.line(line)
		}
	}

	for _, section := range sections(report) {
		if out.err != nil {
			break
		}
		out.line("")
		out.line(section.title)
		if len(section.fields) == 0 {
			out.line("  " + none)
			continue
		}
		for _, f := range section.fields {
			out.line(terminalField(f))
		}
	}
	return out.err
}

// lineWriter writes a report a line at a time, so a rendering is never held in
// memory at the size of the run it describes. The first write error is kept and
// every write after it is skipped, and the renderers stop at it rather than
// rendering the rest of a report nothing is receiving.
type lineWriter struct {
	w   io.Writer
	err error
}

func (l *lineWriter) line(s string) {
	l.write(s)
	l.write("\n")
}

func (l *lineWriter) write(s string) {
	if l.err != nil {
		return
	}
	_, l.err = io.WriteString(l.w, s)
}

// terminalAction renders one action as a header line plus its summary fields.
func terminalAction(v actionView) []string {
	header := v.Time + "  " + safe(v.Label)
	if v.Detail != "" {
		header += "  " + safe(v.Detail)
	}

	lines := []string{header}
	for _, f := range v.Fields {
		lines = append(lines, terminalField(f))
	}
	return lines
}

// terminalField renders a name/value pair in the aligned field column. Values
// wider than the column push the value right rather than truncating the name.
func terminalField(f Field) string {
	return strings.TrimRight(fmt.Sprintf("  %-12s %s", safe(f.Name), safe(f.Value)), " ")
}

// section pairs an evidence source's title with its fields.
type section struct {
	title  string
	fields []Field
}

func sections(report Report) []section {
	return []section{
		{titleSupervisor, report.Supervisor},
		{titleRepository, report.Repository},
		{titleVerification, report.Verification},
	}
}

// actionView is the rendered shape of one action: everything both renderers
// need, and nothing that came straight out of provider JSON.
type actionView struct {
	Time   string
	Label  string
	Detail string
	Fields []Field
}

// viewOf reduces an action to its displayable summary. It reads the action by
// value and never writes back, so a caller's slice is left untouched.
func viewOf(a action.Action) actionView {
	v := actionView{Time: clock(a.StartedAt), Label: label(a.Type), Detail: detail(a)}

	if a.Provider != "" {
		v.Fields = append(v.Fields, Field{"Source", a.Provider})
	}
	if a.Assurance != "" {
		v.Fields = append(v.Fields, Field{"Assurance", string(a.Assurance)})
	}
	if a.ParentID != "" {
		v.Fields = append(v.Fields, Field{"Parent", a.ParentID})
	}
	if name, value := outcome(a); value != "" {
		v.Fields = append(v.Fields, Field{name, value})
	}
	if d, ok := elapsed(a); ok {
		v.Fields = append(v.Fields, Field{"Duration", d})
	}
	return v
}

// labels name the action kinds agentrec normalizes. Anything else falls back to
// the uppercased last component of its type.
var labels = map[string]string{
	action.TypeFileRead:      "READ",
	action.TypeFileWrite:     "WRITE",
	action.TypeFileEdit:      "EDIT",
	action.TypeShellExec:     "SHELL",
	action.TypeSearch:        "SEARCH",
	action.TypeWebFetch:      "WEB",
	action.TypeMCPCall:       "MCP",
	action.TypeSubagentSpawn: "SUBAGENT",
	action.TypeAgentMessage:  "MESSAGE",
	action.TypeProviderError: "ERROR",
}

func label(actionType string) string {
	if known, ok := labels[actionType]; ok {
		return known
	}
	last := actionType[strings.LastIndex(actionType, ".")+1:]
	if last == "" {
		return "ACTION"
	}
	return strings.ToUpper(last)
}

// detailKeys is the allowlist of input keys that may be shown, in the order
// each action kind prefers them. No other key is ever read for display.
var detailKeys = map[string][]string{
	action.TypeFileRead:      {"file_path", "path"},
	action.TypeFileWrite:     {"file_path", "path"},
	action.TypeFileEdit:      {"file_path", "path"},
	action.TypeShellExec:     {"command"},
	action.TypeSearch:        {"query", "pattern", "path"},
	action.TypeWebFetch:      {"url"},
	action.TypeMCPCall:       {"tool", "name"},
	action.TypeSubagentSpawn: {"name"},
	action.TypeProviderError: {"message"},
}

// fallbackKeys orders the whole allowlist for action kinds without a preference
// of their own, so an unrecognized type still summarizes deterministically.
var fallbackKeys = []string{"file_path", "path", "command", "query", "pattern", "url", "tool", "name"}

// detail picks the first nonempty allowlisted string in an action's input.
// Malformed input, non-object input and non-string values yield no detail.
func detail(a action.Action) string {
	fields, ok := object(a.Input)
	if !ok {
		return ""
	}

	keys, ok := detailKeys[a.Type]
	if !ok {
		keys = fallbackKeys
	}
	for _, key := range keys {
		var value string
		if err := json.Unmarshal(fields[key], &value); err == nil && value != "" {
			return value
		}
	}
	return ""
}

// outcome names and renders how an action ended: a shell action reports the
// exit code it carries, anything else reports its status in words.
func outcome(a action.Action) (string, string) {
	if a.Type == action.TypeShellExec {
		if code, ok := exitCode(a.Result); ok {
			return "Exit", code
		}
	}
	return "Result", statusText(a.Status)
}

var statuses = map[string]string{
	"completed":   "success",
	"failed":      "failed",
	"in_progress": "in progress",
}

func statusText(status string) string {
	if text, ok := statuses[status]; ok {
		return text
	}
	return status
}

// exitCode reads an integer `exitCode` from a shell action's result. The raw
// JSON token is parsed directly because decoding would accept a quoted number
// too, and a string exit code is not an observation worth reporting as one. Any
// other shape — missing, fractional, quoted, malformed — reports no code.
func exitCode(result json.RawMessage) (string, bool) {
	fields, ok := object(result)
	if !ok {
		return "", false
	}

	code, err := strconv.ParseInt(string(fields["exitCode"]), 10, 64)
	if err != nil {
		return "", false
	}
	return strconv.FormatInt(code, 10), true
}

// object decodes a raw payload as a JSON object, reporting failure for anything
// that is not one rather than surfacing the payload itself.
func object(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, false
	}
	return fields, true
}

func clock(t time.Time) string {
	if t.IsZero() {
		return zeroClock
	}
	return t.Format("15:04:05")
}

// elapsed reports an action's duration only when both ends were recorded and
// the clock ran forward, so a missing or nonsensical pair shows nothing rather
// than a fabricated number.
func elapsed(a action.Action) (string, bool) {
	if a.StartedAt.IsZero() || a.FinishedAt.IsZero() || a.FinishedAt.Before(a.StartedAt) {
		return "", false
	}
	return a.FinishedAt.Sub(a.StartedAt).String(), true
}

// safe makes an arbitrary provider string printable on one line: control
// characters — newlines that would forge timeline rows, escapes that would
// drive the terminal, invisible format characters — become visible Go-style
// escapes, printable Unicode survives intact, and the result is capped so no
// single value can flood the output.
func safe(s string) string {
	escaped := make([]string, 0, len(s))
	width := 0
	for _, r := range s {
		token := escapeRune(r)
		escaped = append(escaped, token)
		width += utf8.RuneCountInString(token)
	}
	if width <= maxValueRunes {
		return strings.Join(escaped, "")
	}

	var b strings.Builder
	width = 0
	for _, token := range escaped {
		if width+utf8.RuneCountInString(token) > maxValueRunes-len(ellipsis) {
			break
		}
		b.WriteString(token)
		width += utf8.RuneCountInString(token)
	}
	b.WriteString(ellipsis)
	return b.String()
}

func escapeRune(r rune) string {
	switch r {
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	case '\t':
		return `\t`
	}
	if unicode.IsPrint(r) {
		return string(r)
	}
	if r < 0x100 {
		return fmt.Sprintf(`\x%02x`, r)
	}
	if r > 0xffff {
		return fmt.Sprintf(`\U%08x`, r)
	}
	return fmt.Sprintf(`\u%04x`, r)
}
