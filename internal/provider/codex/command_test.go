package codex

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubProbe reports a fixed version string so command-shaping tests do not
// depend on a locally installed Codex binary.
func stubProbe(output string) VersionProbe {
	return func(context.Context, string) (string, error) { return output, nil }
}

// okProbe reports a supported version for tests that only exercise argument
// shaping.
func okProbe() VersionProbe { return stubProbe("codex-cli 0.144.6") }

func TestPrepareCommandRequiresExecMode(t *testing.T) {
	// Codex only streams machine-readable events in exec mode. Guessing the
	// mode through arbitrary global flags would silently launch an
	// interactive session agentrec cannot record.
	for _, args := range [][]string{
		{},
		{"--json"},
		{"login"},
		{"exec-with-typo"},
		{"--cd", "/repo", "exec"},
	} {
		_, err := PrepareCommand(context.Background(), args, okProbe(), Options{})
		if err == nil {
			t.Fatalf("PrepareCommand(%q) = nil error, want error", args)
		}
		if !strings.Contains(err.Error(), "exec") {
			t.Errorf("PrepareCommand(%q) error %q does not name the required subcommand", args, err)
		}
	}
}

func TestPrepareCommandAcceptsExecMode(t *testing.T) {
	cmd, err := PrepareCommand(context.Background(), []string{"exec"}, okProbe(), Options{})
	if err != nil {
		t.Fatalf("PrepareCommand error: %v", err)
	}
	if cmd.Executable != "codex" {
		t.Errorf("PrepareCommand().Executable = %q, want %q", cmd.Executable, "codex")
	}
}

func TestPrepareCommandInjectsJSONRightAfterExec(t *testing.T) {
	// Codex reads the prompt as a positional; anything appended after it
	// would be parsed as part of the prompt rather than as an option.
	args := []string{"exec", "--model", "gpt-5-codex", "summarize the repo"}

	cmd, err := PrepareCommand(context.Background(), args, okProbe(), Options{})
	if err != nil {
		t.Fatalf("PrepareCommand error: %v", err)
	}

	want := []string{"exec", "--json", "--model", "gpt-5-codex", "summarize the repo"}
	if !equalArgs(cmd.Args, want) {
		t.Errorf("PrepareCommand().Args = %q, want %q", cmd.Args, want)
	}
}

func TestPrepareCommandAddsNoDuplicateJSONFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "already first",
			args: []string{"exec", "--json", "prompt"},
			want: []string{"exec", "--json", "prompt"},
		},
		{
			name: "already present later",
			args: []string{"exec", "--model", "gpt-5-codex", "--json", "prompt"},
			want: []string{"exec", "--model", "gpt-5-codex", "--json", "prompt"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := PrepareCommand(context.Background(), tc.args, okProbe(), Options{})
			if err != nil {
				t.Fatalf("PrepareCommand error: %v", err)
			}
			if !equalArgs(cmd.Args, tc.want) {
				t.Errorf("PrepareCommand().Args = %q, want %q", cmd.Args, tc.want)
			}
		})
	}
}

func TestPrepareCommandIgnoresJSONAfterSeparator(t *testing.T) {
	// After a literal "--" every token is a positional, so a "--json" there
	// is prompt text and not the option agentrec needs.
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "json is prompt text",
			args: []string{"exec", "--", "--json"},
			want: []string{"exec", "--json", "--", "--json"},
		},
		{
			name: "separator before options",
			args: []string{"exec", "--", "--model", "--json", "prompt"},
			want: []string{"exec", "--json", "--", "--model", "--json", "prompt"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := PrepareCommand(context.Background(), tc.args, okProbe(), Options{})
			if err != nil {
				t.Fatalf("PrepareCommand error: %v", err)
			}
			if !equalArgs(cmd.Args, tc.want) {
				t.Errorf("PrepareCommand().Args = %q, want %q", cmd.Args, tc.want)
			}
		})
	}
}

func TestPrepareCommandNeverInjectsSandboxOrApprovalFlags(t *testing.T) {
	cmd, err := PrepareCommand(context.Background(), []string{"exec", "audit"}, okProbe(), Options{})
	if err != nil {
		t.Fatalf("PrepareCommand error: %v", err)
	}
	for _, arg := range cmd.Args {
		for _, banned := range []string{"sandbox", "approval", "bypass", "dangerously", "full-auto"} {
			if strings.Contains(arg, banned) {
				t.Errorf("PrepareCommand().Args = %q, injected %q flag %q", cmd.Args, banned, arg)
			}
		}
	}
}

func TestPrepareCommandPreservesUserArgsIncludingSandboxAndApproval(t *testing.T) {
	args := []string{
		"exec",
		"--sandbox", "danger-full-access",
		"--dangerously-bypass-approvals-and-sandbox",
		"-c", "model_reasoning_effort=high",
		"--full-auto",
		"prompt",
		"--",
		"--json",
	}

	cmd, err := PrepareCommand(context.Background(), args, okProbe(), Options{})
	if err != nil {
		t.Fatalf("PrepareCommand error: %v", err)
	}

	want := append([]string{"exec", "--json"}, args[1:]...)
	if !equalArgs(cmd.Args, want) {
		t.Errorf("PrepareCommand().Args = %q, want %q", cmd.Args, want)
	}
}

func TestPrepareCommandDoesNotMutateCallerArgs(t *testing.T) {
	// Extra capacity lets a careless append write through into the caller's
	// backing array.
	args := make([]string, 0, 8)
	args = append(args, "exec", "prompt")
	snapshot := []string{"exec", "prompt"}

	cmd, err := PrepareCommand(context.Background(), args, okProbe(), Options{})
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

func TestPrepareCommandProbesTheCodexExecutable(t *testing.T) {
	var probed []string
	probe := func(_ context.Context, name string) (string, error) {
		probed = append(probed, name)
		return "codex-cli 0.144.6", nil
	}

	if _, err := PrepareCommand(context.Background(), []string{"exec"}, probe, Options{}); err != nil {
		t.Fatalf("PrepareCommand error: %v", err)
	}
	if !equalArgs(probed, []string{"codex"}) {
		t.Errorf("probe called with %q, want exactly one call for %q", probed, "codex")
	}
}

func TestPrepareCommandReportsNormalizedVersion(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{"codex-cli prefix", "codex-cli 0.144.6", "0.144.6"},
		{"codex prefix", "codex 0.144.6", "0.144.6"},
		{"trailing newline", "codex-cli 0.150.0\n", "0.150.0"},
		{"lowest supported", "codex-cli 0.144.0", "0.144.0"},
		{"prerelease suffix", "codex-cli 0.145.0-alpha.1", "0.145.0"},
		{"padded components", "codex-cli 0.144.06", "0.144.6"},
		{"first version wins", "codex-cli 0.144.6 (rust 1.90.0)", "0.144.6"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := PrepareCommand(context.Background(), []string{"exec"}, stubProbe(tc.output), Options{})
			if err != nil {
				t.Fatalf("PrepareCommand(%q) error: %v", tc.output, err)
			}
			if cmd.Version != tc.want {
				t.Errorf("PrepareCommand(%q).Version = %q, want %q", tc.output, cmd.Version, tc.want)
			}
		})
	}
}

func TestPrepareCommandFailsWhenProbeFails(t *testing.T) {
	// A missing or broken binary must surface as-is: recording a run against a
	// Codex agentrec could not identify would produce evidence nobody can trust.
	notInstalled := errors.New(`exec: "codex": executable file not found in $PATH`)
	probe := func(context.Context, string) (string, error) { return "", notInstalled }

	cmd, err := PrepareCommand(context.Background(), []string{"exec", "prompt"}, probe, Options{})
	if err == nil {
		t.Fatalf("PrepareCommand() = %+v, nil error; want probe failure", cmd)
	}
	if !errors.Is(err, notInstalled) {
		t.Errorf("PrepareCommand() error %v does not wrap the probe failure", err)
	}
	if !strings.Contains(err.Error(), "codex") {
		t.Errorf("PrepareCommand() error %q does not name the executable", err)
	}
	assertNoCommand(t, cmd)
}

func TestPrepareCommandRejectsUnreadableVersion(t *testing.T) {
	// agentrec keys its parser on the Codex version, so an unreadable one is a
	// hard stop rather than a value to guess at.
	for _, output := range []string{
		"",
		"\n",
		"codex-cli",
		"codex-cli 0.144",
		"codex-cli unknown",
		"codex-cli v.x.y",
	} {
		cmd, err := PrepareCommand(context.Background(), []string{"exec"}, stubProbe(output), Options{})
		if err == nil {
			t.Fatalf("PrepareCommand(%q) = %+v, nil error; want malformed-version error", output, cmd)
		}
		if !strings.Contains(err.Error(), "0.144.0") {
			t.Errorf("PrepareCommand(%q) error %q does not state the required version %q", output, err, "0.144.0")
		}
		assertNoCommand(t, cmd)
	}
}

func TestPrepareCommandRejectsVersionsOlderThanMinimum(t *testing.T) {
	// 0.144.0 is the first release whose exec JSONL events agentrec can read.
	for _, tc := range []struct{ output, version string }{
		{"codex-cli 0.143.9", "0.143.9"},
		{"codex-cli 0.143.0", "0.143.0"},
		{"codex-cli 0.99.99", "0.99.99"},
		{"codex-cli 0.0.1", "0.0.1"},
	} {
		cmd, err := PrepareCommand(context.Background(), []string{"exec"}, stubProbe(tc.output), Options{})
		if err == nil {
			t.Fatalf("PrepareCommand(%q) = %+v, nil error; want too-old error", tc.output, cmd)
		}
		for _, want := range []string{tc.version, "0.144.0", "upgrade"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("PrepareCommand(%q) error %q does not contain %q", tc.output, err, want)
			}
		}
		assertNoCommand(t, cmd)
	}
}

func TestPrepareCommandRejectsUnsupportedMajorVersions(t *testing.T) {
	// A major bump may reshape the event stream, so agentrec refuses rather
	// than record events it cannot prove it understood.
	for _, tc := range []struct{ output, version string }{
		{"codex-cli 1.0.0", "1.0.0"},
		{"codex-cli 1.2.3", "1.2.3"},
		{"codex-cli 2.0.0-beta.1", "2.0.0"},
	} {
		cmd, err := PrepareCommand(context.Background(), []string{"exec"}, stubProbe(tc.output), Options{})
		if err == nil {
			t.Fatalf("PrepareCommand(%q) = %+v, nil error; want unsupported-major error", tc.output, cmd)
		}
		for _, want := range []string{tc.version, "1.0.0"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("PrepareCommand(%q) error %q does not contain %q", tc.output, err, want)
			}
		}
		assertNoCommand(t, cmd)
	}
}

// assertNoCommand fails when a rejected invocation still handed back something
// a caller could run.
func assertNoCommand(t *testing.T, cmd Command) {
	t.Helper()
	if cmd.Executable != "" || cmd.Args != nil || cmd.Version != "" {
		t.Errorf("rejected PrepareCommand returned runnable command %+v, want zero Command", cmd)
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

// The adapter carries the override through and stamps the command with what it
// cost: a run recorded against a version this parser was not written for must
// say so everywhere it is read, starting here.
func TestPrepareCommandRecordsAnUnsupportedVersionOnlyWhenAllowed(t *testing.T) {
	args := []string{"exec", "do the thing"}

	if _, err := PrepareCommand(context.Background(), args, stubProbe("codex-cli 2.0.0"), Options{}); err == nil {
		t.Fatal("PrepareCommand error = nil for an unsupported version, want the refusal")
	}

	cmd, err := PrepareCommand(context.Background(), args, stubProbe("codex-cli 2.0.0"), Options{AllowUnsupportedVersion: true})
	if err != nil {
		t.Fatalf("PrepareCommand error = %v, want the run prepared", err)
	}
	if cmd.Version != "2.0.0" || !cmd.VersionUnverified {
		t.Errorf("PrepareCommand = (%q, unverified %v), want (\"2.0.0\", true)", cmd.Version, cmd.VersionUnverified)
	}

	cmd, err = PrepareCommand(context.Background(), args, okProbe(), Options{AllowUnsupportedVersion: true})
	if err != nil || cmd.VersionUnverified {
		t.Errorf("PrepareCommand on a supported version = (unverified %v, %v), want it not stamped", cmd.VersionUnverified, err)
	}
}
