package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunHelpListsCoreCommands(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"-h"}} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer

		exitCode := Run(args, &stdout, &stderr)

		if exitCode != 0 {
			t.Fatalf("Run(%q) exit code = %d, want 0", args, exitCode)
		}
		for _, command := range []string{"trace", "list", "show"} {
			if !strings.Contains(stdout.String(), command) {
				t.Errorf("Run(%q) help output does not contain %q", args, command)
			}
		}
		if !strings.Contains(stdout.String(), "agentrec list [--cwd <path>]") {
			t.Errorf("Run(%q) help output does not document the cwd filter", args)
		}
		if stderr.Len() != 0 {
			t.Errorf("Run(%q) stderr = %q, want empty", args, stderr.String())
		}
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Run([]string{"unknown"}, &stdout, &stderr)

	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if !strings.Contains(stderr.String(), `unknown command: "unknown"`) {
		t.Errorf("stderr = %q, want it to name the unknown command", stderr.String())
	}
	if !strings.Contains(stderr.String(), "--help") {
		t.Errorf("stderr = %q, want it to point at --help", stderr.String())
	}
}

func TestRunQuotesUnknownCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Run([]string{"\x1b[31munknown"}, &stdout, &stderr)

	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if strings.ContainsRune(stderr.String(), '\x1b') {
		t.Fatalf("stderr contains a terminal escape: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), `"\x1b[31munknown"`) {
		t.Errorf("stderr = %q, want a quoted command", stderr.String())
	}
}
