package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

type failingOutputWriter struct {
	err error
}

func (w failingOutputWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type shortOutputWriter struct{}

func (shortOutputWriter) Write(p []byte) (int, error) {
	return len(p) - 1, nil
}

func TestRunFailsWhenStandardOutputCannotBeWritten(t *testing.T) {
	wantErr := errors.New("injected stdout failure")
	for _, args := range [][]string{{"version"}, {"list"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			t.Setenv("AGENTREC_HOME", t.TempDir())
			var stderr bytes.Buffer

			exitCode := Run(args, failingOutputWriter{err: wantErr}, &stderr)

			if exitCode != 1 {
				t.Fatalf("exit code = %d, want 1", exitCode)
			}
			if !strings.Contains(stderr.String(), "write stdout: "+wantErr.Error()) {
				t.Errorf("stderr = %q, want contextual stdout error", stderr.String())
			}
		})
	}
}

func TestRunFailsOnShortStandardOutputWrite(t *testing.T) {
	var stderr bytes.Buffer

	exitCode := Run([]string{"version"}, shortOutputWriter{}, &stderr)

	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), io.ErrShortWrite.Error()) {
		t.Errorf("stderr = %q, want %q", stderr.String(), io.ErrShortWrite)
	}
}

func TestRunPreservesCommandFailureAndReportsStandardOutputFailure(t *testing.T) {
	t.Setenv("AGENTREC_HOME", t.TempDir())
	wantErr := errors.New("injected stdout failure")
	var stderr bytes.Buffer

	exitCode := Run([]string{"list", "--json"}, failingOutputWriter{err: wantErr}, &stderr)

	if exitCode != 1 {
		t.Fatalf("exit code = %d, want existing command failure 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "write stdout: "+wantErr.Error()) {
		t.Errorf("stderr = %q, want contextual stdout error", stderr.String())
	}
}

func TestFinalizeOutputPreservesExistingNonzeroExitCode(t *testing.T) {
	wantErr := errors.New("injected stdout failure")
	var stderr bytes.Buffer

	exitCode := finalizeOutput(2, wantErr, &stderr)

	if exitCode != 2 {
		t.Fatalf("exit code = %d, want existing command failure 2", exitCode)
	}
	if !strings.Contains(stderr.String(), "write stdout: "+wantErr.Error()) {
		t.Errorf("stderr = %q, want contextual stdout error", stderr.String())
	}
}

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
				} else if command == "trash" {
					wantLines = 4
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

func TestRunPublicNestedCommandHelpSucceedsWithoutSideEffects(t *testing.T) {
	commands := [][]string{
		{"shadow", "run"},
		{"shadow", "show"},
		{"hooks", "print"},
		{"trash", "restore"},
		{"trash", "sweep"},
		{"trash", "empty"},
	}
	for _, command := range commands {
		for _, flag := range []string{"--help", "-h"} {
			name := strings.Join(command, "/") + "/" + flag
			t.Run(name, func(t *testing.T) {
				home := t.TempDir()
				t.Setenv("HOME", home)
				t.Setenv("AGENTREC_HOME", home)
				var stdout bytes.Buffer
				var stderr bytes.Buffer

				args := append(append([]string(nil), command...), flag)
				exitCode := Run(args, &stdout, &stderr)

				if exitCode != 0 {
					t.Errorf("exit code = %d, want 0", exitCode)
				}
				wantUsage := "agentrec " + strings.Join(command, " ")
				if !strings.Contains(stdout.String(), wantUsage) {
					t.Errorf("stdout = %q, want %q", stdout.String(), wantUsage)
				}
				if got := len(strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")); got != 2 {
					t.Errorf("stdout has %d lines, want 2 command-specific lines: %q", got, stdout.String())
				}
				if stderr.Len() != 0 {
					t.Errorf("stderr = %q, want empty", stderr.String())
				}
				entries, err := os.ReadDir(home)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Fatalf("help created files under HOME: %v", entries)
				}
			})
		}
	}
}

func TestRunTrailingHelpAfterArgumentsSucceedsWithoutSideEffects(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantOutput string
	}{
		{name: "show operand", args: []string{"show", "latest", "--help"}, wantOutput: "Usage:\n  agentrec show <run-id>|latest [--failures-only] [--json]\n"},
		{name: "events operand short flag", args: []string{"events", "latest", "-h"}, wantOutput: "Usage:\n  agentrec events <run-id>|latest [--json]\n"},
		{name: "list option", args: []string{"list", "--json", "--help"}, wantOutput: "Usage:\n  agentrec list [--cwd <path>] [--exit-reason <reason>] [--verification-status <status>] [--failures-only] [--json]\n"},
		{name: "nested operand", args: []string{"trash", "restore", "run-123", "--help"}, wantOutput: "Usage:\n  agentrec trash restore <run-id>\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("AGENTREC_HOME", home)
			var stdout bytes.Buffer
			var stderr bytes.Buffer

			exitCode := Run(tt.args, &stdout, &stderr)

			if exitCode != 0 {
				t.Errorf("exit code = %d, want 0", exitCode)
			}
			if stdout.String() != tt.wantOutput {
				t.Errorf("stdout = %q, want %q", stdout.String(), tt.wantOutput)
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

func TestRunHelpTakesPriorityAfterResolvedCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Run([]string{"show", "latest", "--bogus", "--help"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	want := "Usage:\n  agentrec show <run-id>|latest [--failures-only] [--json]\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestTrailingHelpRequestedStopsAtArgumentSeparator(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "long help before separator", args: []string{"show", "latest", "--help"}, want: true},
		{name: "short help before separator", args: []string{"events", "latest", "-h"}, want: true},
		{name: "long help after separator", args: []string{"trace", "claude", "--", "--help"}, want: false},
		{name: "short help after separator", args: []string{"trace", "claude", "--", "-h"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trailingHelpRequested(tt.args); got != tt.want {
				t.Fatalf("trailingHelpRequested(%q) = %t, want %t", tt.args, got, tt.want)
			}
		})
	}
}

func TestRunDoesNotInterceptHelpAfterArgumentSeparator(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Run([]string{"show", "latest", "--", "--help"}, &stdout, &stderr)

	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Error("stderr is empty, want show usage error")
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

func TestRunDoesNotTreatNestedCommandPrefixAsHelp(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Run([]string{"shadow", "s", "--help"}, &stdout, &stderr)

	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Error("stderr is empty, want shadow usage error")
	}
}

func TestRunDoesNotJoinCommandTokensForHelp(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Run([]string{"shadow run", "--help"}, &stdout, &stderr)

	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), `unknown command: "shadow run"`) {
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
