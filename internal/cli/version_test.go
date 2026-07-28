package cli

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunVersionReportsDevelopmentFallback(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer

		exitCode := Run(args, &stdout, &stderr)

		if exitCode != 0 {
			t.Fatalf("Run(%q) exit code = %d, want 0", args, exitCode)
		}
		const want = "agentrec dev\ncommit unknown\nbuilt unknown\n"
		if stdout.String() != want {
			t.Errorf("Run(%q) stdout = %q, want %q", args, stdout.String(), want)
		}
		if stderr.Len() != 0 {
			t.Errorf("Run(%q) stderr = %q, want empty", args, stderr.String())
		}
	}
}

func TestRunVersionRejectsExtraArguments(t *testing.T) {
	for _, args := range [][]string{{"version", "extra"}, {"--version", "extra"}} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer

		exitCode := Run(args, &stdout, &stderr)

		if exitCode != 2 {
			t.Fatalf("Run(%q) exit code = %d, want 2", args, exitCode)
		}
		if stdout.Len() != 0 {
			t.Errorf("Run(%q) stdout = %q, want empty", args, stdout.String())
		}
		if !strings.Contains(stderr.String(), "extra") {
			t.Errorf("Run(%q) stderr = %q, want it to name the unexpected argument", args, stderr.String())
		}
		if !strings.Contains(stderr.String(), "usage: agentrec version") {
			t.Errorf("Run(%q) stderr = %q, want it to show the version usage", args, stderr.String())
		}
	}
}

func TestRunVersionQuotesUnexpectedArguments(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Run([]string{"version", "\x1b[31msecret"}, &stdout, &stderr)

	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if strings.ContainsRune(stderr.String(), '\x1b') {
		t.Fatalf("stderr contains a terminal escape: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), `"\x1b[31msecret"`) {
		t.Errorf("stderr = %q, want a quoted argument", stderr.String())
	}
}

func TestRunHelpDocumentsVersion(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if exitCode := Run(nil, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	if !strings.Contains(stdout.String(), "agentrec version") {
		t.Errorf("help output = %q, want it to document the version command", stdout.String())
	}
}

// TestVersionReportsInjectedBuildMetadata links a real binary the way a release
// build does and runs it, so the ldflags symbol paths are verified rather than
// assumed.
func TestVersionReportsInjectedBuildMetadata(t *testing.T) {
	const pkg = "github.com/seongwoo-choi/agentrec/internal/cli"
	binary := filepath.Join(t.TempDir(), "agentrec")

	build := exec.Command("go", "build",
		"-ldflags", "-X "+pkg+".version=v9.9.9 -X "+pkg+".commit=0123456789abcdef0123456789abcdef01234567 -X "+pkg+".built=2026-07-28T00:00:00Z",
		"-o", binary, "./cmd/agentrec",
	)
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build with injected metadata: %v\n%s", err, out)
	}

	for _, args := range [][]string{{"version"}, {"--version"}} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd := exec.Command(binary, args...)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			t.Fatalf("%s %q: %v (stderr %q)", binary, args, err, stderr.String())
		}
		const want = "agentrec v9.9.9\ncommit 0123456789abcdef0123456789abcdef01234567\nbuilt 2026-07-28T00:00:00Z\n"
		if stdout.String() != want {
			t.Errorf("%q stdout = %q, want %q", args, stdout.String(), want)
		}
		if stderr.Len() != 0 {
			t.Errorf("%q stderr = %q, want empty", args, stderr.String())
		}
	}
}
