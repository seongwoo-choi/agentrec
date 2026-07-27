// Package cli implements the agentrec command-line interface.
package cli

import (
	"fmt"
	"io"
)

const usage = `agentrec records coding-agent execution as a replayable action timeline.

Usage:
  agentrec trace <provider> -- <args...>
  agentrec list
  agentrec show <run-id>
`

// Run executes the CLI with args (os.Args[1:]) and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		return 0
	}

	fmt.Fprintf(stderr, "unknown command: %s\nrun 'agentrec --help' to see the available commands\n", args[0])
	return 2
}
