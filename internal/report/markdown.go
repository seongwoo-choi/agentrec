package report

import (
	"fmt"
	"io"
	"strings"
)

// Markdown section headings mirror the terminal titles in title case.
const (
	mdTimeline     = "# Action Timeline"
	mdProvider     = "## Provider-Reported Actions"
	mdSupervisor   = "## Supervisor-Observed Result"
	mdRepository   = "## Repository-Observed Changes"
	mdVerification = "## Verification-Observed Result"
)

// RenderMarkdown writes the report as Markdown headings and bullets. Every
// dynamic value is wrapped in a code span wide enough to contain it, so no
// provider string can open a heading, a list or a fence of its own.
func RenderMarkdown(w io.Writer, report Report) error {
	out := &lineWriter{w: w}
	out.line(mdTimeline)
	out.line("")
	out.line(mdProvider)
	out.line("")

	if len(report.Actions) == 0 {
		out.line(none)
	}
	for i := range report.Actions {
		if out.err != nil {
			break
		}
		if i > 0 {
			out.line("")
		}
		for _, line := range markdownAction(i+1, viewOf(report.Actions[i])) {
			out.line(line)
		}
	}

	for _, section := range markdownSections(report) {
		if out.err != nil {
			break
		}
		out.line("")
		out.line(section.title)
		out.line("")
		if len(section.fields) == 0 {
			out.line(none)
			continue
		}
		for _, f := range section.fields {
			out.line(bullet(f.Name, f.Value))
		}
	}
	return out.err
}

// markdownAction renders one action under a positional heading, so the heading
// itself carries no provider text.
func markdownAction(number int, v actionView) []string {
	lines := []string{
		fmt.Sprintf("### Action %d", number),
		"",
		bullet("Time", v.Time),
		bullet("Action", v.Label),
	}
	if v.Detail != "" {
		lines = append(lines, bullet("Detail", v.Detail))
	}
	for _, f := range v.Fields {
		lines = append(lines, bullet(f.Name, f.Value))
	}
	return lines
}

func markdownSections(report Report) []section {
	return []section{
		{mdSupervisor, report.Supervisor},
		{mdRepository, report.Repository},
		{mdVerification, report.Verification},
	}
}

func bullet(name, value string) string {
	return "- " + code(name) + ": " + code(value)
}

// code wraps a value in a code span whose fence is longer than any backtick run
// inside it, padding when the value would otherwise touch the fence. The value
// is escaped first, so it can never contain a newline that would end the span.
func code(value string) string {
	value = safe(value)
	fence := strings.Repeat("`", longestBacktickRun(value)+1)

	if strings.HasPrefix(value, "`") || strings.HasSuffix(value, "`") || strings.TrimSpace(value) == "" {
		return fence + " " + value + " " + fence
	}
	return fence + value + fence
}

func longestBacktickRun(s string) int {
	longest, run := 0, 0
	for _, r := range s {
		if r != '`' {
			run = 0
			continue
		}
		run++
		if run > longest {
			longest = run
		}
	}
	return longest
}
