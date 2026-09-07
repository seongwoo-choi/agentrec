package cli

import (
	"bytes"
	"os"
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
		for _, command := range []string{"trace", "list", "show", "events", "view"} {
			if !strings.Contains(stdout.String(), command) {
				t.Errorf("Run(%q) help output does not contain %q", args, command)
			}
		}
		if !strings.Contains(stdout.String(), "agentrec list [--cwd <path>] [--exit-reason <reason>] [--verification-status <status>] [--failures-only]") {
			t.Errorf("Run(%q) help output does not document the list filters", args)
		}
		if stderr.Len() != 0 {
			t.Errorf("Run(%q) stderr = %q, want empty", args, stderr.String())
		}
	}
}

func TestRunPublicCommandHelpSucceedsWithoutSideEffects(t *testing.T) {
	commands := []string{
		"trace", "shadow", "verify", "list", "show", "changes", "events",
		"view", "setup", "start", "stop", "status", "trash", "hooks", "version",
	}
	for _, command := range commands {
		for _, flag := range []string{"--help", "-h"} {
			t.Run(command+"/"+flag, func(t *testing.T) {
				home := t.TempDir()
				t.Setenv("HOME", home)
				t.Setenv("AGENTREC_HOME", home)
				var stdout bytes.Buffer
				var stderr bytes.Buffer

				exitCode := Run([]string{command, flag}, &stdout, &stderr)

				if exitCode != 0 {
					t.Errorf("exit code = %d, want 0", exitCode)
				}
				if !strings.Contains(stdout.String(), "agentrec "+command) {
					t.Errorf("stdout = %q, want command usage", stdout.String())
				}
				if command == "trash" && !strings.Contains(stdout.String(), "--dry-run") {
					t.Errorf("stdout = %q, want trash sweep dry-run option", stdout.String())
				}
				wantLines := 2
				if command == "shadow" {
					wantLines = 3
				}
				if got := len(strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")); got != wantLines {
					t.Errorf("stdout has %d lines, want %d command-specific lines: %q", got, wantLines, stdout.String())
				}
				if stderr.Len() != 0 {
					t.Errorf("stderr = %q, want empty", stderr.String())
				}
				entries, err := os.ReadDir(home)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Fatalf("help created files under AGENTREC_HOME: %v", entries)
				}
			})
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

func TestRunDoesNotTreatUnknownCommandAsHelpPrefix(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Run([]string{"s", "--help"}, &stdout, &stderr)

	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), `unknown command: "s"`) {
		t.Errorf("stderr = %q, want unknown command", stderr.String())
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
