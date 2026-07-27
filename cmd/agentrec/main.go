package main

import (
	"fmt"
	"io"
	"os"
)

const usage = `agentrec records coding-agent execution as a replayable action timeline.

Usage:
  agentrec trace <provider> -- <args...>
  agentrec list
  agentrec show <run-id>
`

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		return 0
	}

	fmt.Fprintf(stderr, "unknown command: %s\n", args[0])
	return 2
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
