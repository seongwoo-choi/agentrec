package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// `agentrec hooks print --claude` writes the hook configuration that records
// interactive sessions, for the operator to paste into their Claude Code
// settings. It prints rather than installs: the settings file is the
// operator's, and a recorder that edited it would be hard to tell apart from
// the thing it records.

const hooksUsage = "usage: agentrec hooks print --claude|--codex [--verify]\n"

// hookCommandTimeout bounds each hook, in seconds. The hook only dials the
// recorder and waits for one acknowledgement; longer would hold the session on
// a recorder that is not answering. Codex allows a SessionEnd hook three
// seconds at most and clamps anything longer, so that one is given exactly
// that.
const (
	hookCommandTimeout         = 5
	codexSessionEndHookTimeout = 3
)

// hookEvents are the events the recorder acts on, per provider, in the order
// the snippet lists them. No matcher is given, which both providers read as
// every source, tool or reason. Codex has no PostToolUseFailure: a command
// that failed arrives as a PostToolUse whose response says so.
var hookEvents = map[string][]string{
	"claude": {hookSessionStart, hookUserPromptSubmit, hookPostToolUse, hookPostToolUseFailure, hookSessionEnd},
	"codex":  {hookSessionStart, hookUserPromptSubmit, hookPostToolUse, hookSessionEnd},
}

// hookGuidance tells the operator where the fragment goes and what else the
// provider needs before it runs the hooks.
var hookGuidance = map[string]string{
	"claude": "Merge the \"hooks\" object above into ~/.claude/settings.json, or into a project's .claude/settings.json — or let `agentrec setup` do it.\n",
	"codex":  "Merge the \"hooks\" object above into ~/.codex/hooks.json, or into a project's .codex/hooks.json — or let `agentrec setup` do it — then run /hooks inside Codex once to trust it: Codex skips a hook it has not been told to trust.\n",
}

// The shape of Claude Code's hooks setting, as far as this snippet uses it.
type hookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

type hookGroup struct {
	Hooks []hookCommand `json:"hooks"`
}

type hookSettings struct {
	Hooks map[string][]hookGroup `json:"hooks"`
}

func runHooks(args []string, stdout, stderr io.Writer) int {
	// --verify opts every recorded session into the repository's committed
	// checks, as --verify does for one traced run. It is not the default: the
	// fragment applies to every repository the operator opens, and a check is
	// a command the repository chose.
	verify := false
	provider := ""
	switch {
	case len(args) == 2 && args[0] == "print" && strings.HasPrefix(args[1], "--") && sessionProviders[args[1][2:]]:
		provider = args[1][2:]
	case len(args) == 3 && args[0] == "print" && strings.HasPrefix(args[1], "--") && sessionProviders[args[1][2:]] && args[2] == verifyFlag:
		provider = args[1][2:]
		verify = true
	default:
		fmt.Fprint(stderr, hooksUsage)
		return exitUsage
	}
	exe, err := sessionExecutable()
	if err != nil {
		fmt.Fprintf(stderr, "cli: locate agentrec: %v\n", err)
		return exitFailure
	}
	out, err := json.MarshalIndent(hookFragment(provider, exe, verify), "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "cli: encode hook settings: %v\n", err)
		return exitFailure
	}
	fmt.Fprintln(stdout, string(out))

	// Guidance goes to stderr, so stdout stays the settings fragment alone.
	root, err := runsRoot()
	if err != nil {
		root = "the agentrec runs directory"
	}
	fmt.Fprintf(stderr, hookGuidance[provider]+
		"Sessions already open are not recorded: recording starts with the next session, and each one is filed under %s.\n", root)
	return 0
}

// shellWord makes a path one word for the shell Claude Code hands hook commands
// to. A path spelled from the characters shells leave alone is returned as it
// is; anything else is single-quoted, with a quote inside spelled the POSIX way.
func shellWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
