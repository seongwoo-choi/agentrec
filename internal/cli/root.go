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
  agentrec show <run-id>|latest [--failures-only]
  agentrec events <run-id>|latest [--json]
  agentrec view [<run-id>|latest] [--listen <loopback-address>] [--no-open] [--allow-run]
  agentrec setup [--claude] [--codex] [--verify] [--project] [--uninstall]
  agentrec start [--listen <loopback-address>] [--no-open] [--allow-run]
  agentrec stop
  agentrec status
  agentrec trash [restore <run-id> | empty | sweep <age>]
  agentrec verify <run-id>|latest
  agentrec hooks print --claude|--codex [--verify]
  agentrec version

Recording an interactive session: 'agentrec setup' installs the hooks into your
Claude Code settings and your Codex hooks file ('hooks print' shows the fragment
instead of installing it). Each new session is then recorded by 'agentrec hook
<provider>' (run by the provider) and 'agentrec session serve' (started by the
first hook), neither of which is meant to be typed by hand.
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
	case "verify":
		return runVerify(args[1:], stdout, stderr)
	case "list":
		return runList(args[1:], stdout, stderr)
	case "show":
		return runShow(args[1:], stdout, stderr)
	case "events":
		return runEvents(args[1:], stdout, stderr)
	case "view":
		return runView(args[1:], stdout, stderr)
	case "setup":
		return runSetup(args[1:], stdout, stderr)
	case "start":
		return runStart(args[1:], stdout, stderr)
	case "stop":
		return runStop(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "trash":
		return runTrash(args[1:], stdout, stderr)
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
