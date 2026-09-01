// Package cli implements the agentrec command-line interface.
package cli

import (
	"fmt"
	"io"
)

const usage = `agentrec records coding-agent execution as a replayable action timeline.

Usage:
  agentrec trace <provider> [--verify] [--allow-unsupported-version] [--timeout <duration>] -- <args...>
  agentrec shadow run <task-file> --runner claude --runner codex
  agentrec shadow show <group-id>
  agentrec list [--cwd <path>] [--exit-reason <reason>] [--verification-status <status>]
  agentrec show <run-id>|latest
  agentrec events <run-id>|latest [--json]
  agentrec view [<run-id>|latest] [--listen <loopback-address>] [--no-open]
  agentrec hooks print --claude|--codex [--verify]
  agentrec version

Recording an interactive session: paste the output of 'agentrec hooks print
--claude' into your Claude Code settings, or of '--codex' into your Codex hooks
file. Each new session is then recorded by 'agentrec hook <provider>' (run by the
provider) and 'agentrec session serve' (started by the first hook), neither of
which is meant to be typed by hand.
`

// Run executes the CLI with args (os.Args[1:]) and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		return 0
	}

	switch args[0] {
	case "trace":
		return runTrace(args[1:], stdout, stderr)
	case "shadow":
		if len(args) > 1 && args[1] == "show" {
			return runShadowShow(args[2:], stdout, stderr)
		}
		return runShadow(args[1:], stdout, stderr)
	case "list":
		return runList(args[1:], stdout, stderr)
	case "show":
		return runShow(args[1:], stdout, stderr)
	case "events":
		return runEvents(args[1:], stdout, stderr)
	case "view":
		return runView(args[1:], stdout, stderr)
	case "hooks":
		return runHooks(args[1:], stdout, stderr)
	case "hook":
		return runHook(args[1:], stdout, stderr)
	case "session":
		return runSession(args[1:], stdout, stderr)
	case "version", "--version":
		return runVersion(args[1:], stdout, stderr)
	}

	fmt.Fprintf(stderr, "unknown command: %q\nrun 'agentrec --help' to see the available commands\n", args[0])
	return 2
}
