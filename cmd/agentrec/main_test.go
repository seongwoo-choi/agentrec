package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunHelpListsCoreCommands(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run([]string{"--help"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	for _, command := range []string{"trace", "list", "show"} {
		if !strings.Contains(stdout.String(), command) {
			t.Errorf("help output does not contain %q", command)
		}
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run([]string{"unknown"}, &stdout, &stderr)

	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if !strings.Contains(stderr.String(), "unknown command: unknown") {
		t.Errorf("stderr = %q", stderr.String())
	}
}
