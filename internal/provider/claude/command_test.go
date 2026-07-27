package claude

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubProbe reports a fixed version string so command-shaping tests do not
// depend on a locally installed Claude Code binary.
func stubProbe(output string) VersionProbe {
	return func(context.Context, string) (string, error) { return output, nil }
}

// okProbe reports a supported version for tests that only exercise argument
// shaping.
func okProbe() VersionProbe { return stubProbe("2.1.220 (Claude Code)") }

func TestPrepareCommandRejectsMissingPrintFlag(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--verbose"},
		{"-print"},
		{"--printer"},
		{"-pq"},
	} {
		_, err := PrepareCommand(context.Background(), args, okProbe())
		if err == nil {
			t.Fatalf("PrepareCommand(%q) = nil error, want error", args)
		}
		if !strings.Contains(err.Error(), "--print") {
			t.Errorf("PrepareCommand(%q) error %q does not name the required flag", args, err)
		}
	}
}

func TestPrepareCommandInjectsRequiredOptionsBeforeUserArgs(t *testing.T) {
	args := []string{"-p", "summarize the repo", "--", "extra"}

	cmd, err := PrepareCommand(context.Background(), args, okProbe())
	if err != nil {
		t.Fatalf("PrepareCommand error: %v", err)
	}

	want := []string{
		"--output-format", "stream-json",
		"--verbose",
		"--include-hook-events",
		"-p", "summarize the repo", "--", "extra",
	}
	if !equalArgs(cmd.Args, want) {
		t.Errorf("PrepareCommand().Args = %q, want %q", cmd.Args, want)
	}
}

func TestPrepareCommandKeepsExistingStreamJSONAndAddsNoDuplicates(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "split form",
			args: []string{"--output-format", "stream-json", "-p"},
			want: []string{"--verbose", "--include-hook-events", "--output-format", "stream-json", "-p"},
		},
		{
			name: "equals form",
			args: []string{"--output-format=stream-json", "-p"},
			want: []string{"--verbose", "--include-hook-events", "--output-format=stream-json", "-p"},
		},
		{
			name: "everything already present",
			args: []string{"-p", "--verbose", "--output-format=stream-json", "--include-hook-events"},
			want: []string{"-p", "--verbose", "--output-format=stream-json", "--include-hook-events"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := PrepareCommand(context.Background(), tc.args, okProbe())
			if err != nil {
				t.Fatalf("PrepareCommand error: %v", err)
			}
			if !equalArgs(cmd.Args, tc.want) {
				t.Errorf("PrepareCommand().Args = %q, want %q", cmd.Args, tc.want)
			}
		})
	}
}

func TestPrepareCommandRejectsConflictingOutputFormat(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"split text", []string{"-p", "--output-format", "text"}, "text"},
		{"split json", []string{"-p", "--output-format", "json"}, "json"},
		{"equals text", []string{"-p", "--output-format=text"}, "text"},
		{"equals empty", []string{"-p", "--output-format="}, "--output-format"},
		{"value is another flag", []string{"-p", "--output-format", "--verbose"}, "--verbose"},
		{"missing split value", []string{"-p", "--output-format"}, "--output-format"},
		{"value swallowed by separator", []string{"-p", "--output-format", "--", "text"}, "--output-format"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := PrepareCommand(context.Background(), tc.args, okProbe())
			if err == nil {
				t.Fatalf("PrepareCommand(%q) = nil error, want error", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("PrepareCommand(%q) error %q does not mention %q", tc.args, err, tc.want)
			}
			if !strings.Contains(err.Error(), "stream-json") {
				t.Errorf("PrepareCommand(%q) error %q does not name the required format", tc.args, err)
			}
		})
	}
}

func TestPrepareCommandNeverInjectsPermissionBypass(t *testing.T) {
	cmd, err := PrepareCommand(context.Background(), []string{"-p", "audit"}, okProbe())
	if err != nil {
		t.Fatalf("PrepareCommand error: %v", err)
	}
	for _, arg := range cmd.Args {
		if strings.Contains(arg, "dangerously") || strings.Contains(arg, "permission-mode") {
			t.Errorf("PrepareCommand().Args = %q, injected permission bypass %q", cmd.Args, arg)
		}
	}
}

func TestPrepareCommandPreservesUserArgsIncludingBypass(t *testing.T) {
	args := []string{
		"--model", "opus",
		"-p",
		"--dangerously-skip-permissions",
		"--allowed-tools", "Bash(git status)",
		"--",
		"--verbose",
	}

	cmd, err := PrepareCommand(context.Background(), args, okProbe())
	if err != nil {
		t.Fatalf("PrepareCommand error: %v", err)
	}

	want := append([]string{"--output-format", "stream-json", "--verbose", "--include-hook-events"}, args...)
	if !equalArgs(cmd.Args, want) {
		t.Errorf("PrepareCommand().Args = %q, want %q", cmd.Args, want)
	}
}

func TestPrepareCommandDoesNotMutateCallerArgs(t *testing.T) {
	// Extra capacity lets a careless append write through into the caller's
	// backing array.
	args := make([]string, 0, 8)
	args = append(args, "-p", "prompt")
	snapshot := []string{"-p", "prompt"}

	cmd, err := PrepareCommand(context.Background(), args, okProbe())
	if err != nil {
		t.Fatalf("PrepareCommand error: %v", err)
	}
	if !equalArgs(args, snapshot) {
		t.Fatalf("caller args mutated: got %q, want %q", args, snapshot)
	}

	cmd.Args[len(cmd.Args)-1] = "tampered"
	if !equalArgs(args, snapshot) {
		t.Errorf("caller args aliased by returned Args: got %q, want %q", args, snapshot)
	}
}

// equalArgs compares argument slices element by element.
func equalArgs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestPrepareCommandProbesTheClaudeExecutableOnce(t *testing.T) {
	calls := 0
	var probed string
	probe := func(_ context.Context, name string) (string, error) {
		calls++
		probed = name
		return "2.1.220 (Claude Code)", nil
	}

	if _, err := PrepareCommand(context.Background(), []string{"-p", "prompt"}, probe); err != nil {
		t.Fatalf("PrepareCommand error: %v", err)
	}
	if calls != 1 {
		t.Errorf("probe called %d times, want 1", calls)
	}
	if probed != "claude" {
		t.Errorf("probe called with %q, want %q", probed, "claude")
	}
}

func TestPrepareCommandNormalizesProbedVersion(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"product name suffix", "2.1.220 (Claude Code)\n", "2.1.220"},
		{"bare version", "2.1.0", "2.1.0"},
		{"prerelease suffix", "2.2.0-beta.3 (Claude Code)", "2.2.0"},
		{"zero-padded component", "2.01.007 (Claude Code)", "2.1.7"},
		{"extra later version in output", "2.1.5 (Claude Code); latest is 2.4.0", "2.1.5"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := PrepareCommand(context.Background(), []string{"-p"}, stubProbe(tc.raw))
			if err != nil {
				t.Fatalf("PrepareCommand error: %v", err)
			}
			if cmd.Version != tc.want {
				t.Errorf("PrepareCommand(%q).Version = %q, want %q", tc.raw, cmd.Version, tc.want)
			}
		})
	}
}

func TestPrepareCommandRejectsUnusableVersions(t *testing.T) {
	failingProbe := func(context.Context, string) (string, error) {
		return "", errors.New("executable file not found in $PATH")
	}

	tests := []struct {
		name  string
		probe VersionProbe
		want  string
	}{
		{"probe failed", failingProbe, "not found"},
		{"no version in output", stubProbe("Claude Code"), "Claude Code"},
		{"malformed version", stubProbe("v2.1 (Claude Code)"), "2.1"},
		{"too old patch line", stubProbe("2.0.44 (Claude Code)"), "2.0.44"},
		{"too old major", stubProbe("1.9.9 (Claude Code)"), "1.9.9"},
		{"unsupported major", stubProbe("3.0.0 (Claude Code)"), "3.0.0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := PrepareCommand(context.Background(), []string{"-p"}, tc.probe)
			if err == nil {
				t.Fatalf("PrepareCommand() = nil error, want error")
			}
			got := err.Error()
			if !strings.Contains(got, tc.want) {
				t.Errorf("error %q does not mention %q", got, tc.want)
			}
			if !strings.Contains(got, "claude") {
				t.Errorf("error %q does not name the executable", got)
			}
			for _, bound := range []string{"2.1.0", "3.0.0"} {
				if !strings.Contains(got, bound) {
					t.Errorf("error %q does not state the supported range bound %q", got, bound)
				}
			}
		})
	}
}

func TestPrepareCommandAcceptsPrintFlag(t *testing.T) {
	for _, args := range [][]string{
		{"-p"},
		{"--print"},
	} {
		cmd, err := PrepareCommand(context.Background(), args, okProbe())
		if err != nil {
			t.Fatalf("PrepareCommand(%q) error: %v", args, err)
		}
		if cmd.Executable != "claude" {
			t.Errorf("PrepareCommand(%q).Executable = %q, want %q", args, cmd.Executable, "claude")
		}
	}
}
