package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	for _, test := range []struct {
		args       []string
		unexpected string
	}{
		{[]string{"version", "extra"}, "extra"},
		{[]string{"--version", "extra"}, "extra"},
		{[]string{"version", "--verbose", "extra"}, "extra"},
		{[]string{"--version", "--verbose", "extra"}, "extra"},
	} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer

		exitCode := Run(test.args, &stdout, &stderr)

		if exitCode != 2 {
			t.Fatalf("Run(%q) exit code = %d, want 2", test.args, exitCode)
		}
		if stdout.Len() != 0 {
			t.Errorf("Run(%q) stdout = %q, want empty", test.args, stdout.String())
		}
		if !strings.Contains(stderr.String(), strconv.Quote(test.unexpected)) {
			t.Errorf("Run(%q) stderr = %q, want it to name %q", test.args, stderr.String(), test.unexpected)
		}
		if !strings.Contains(stderr.String(), "usage: agentrec version [--verbose]") {
			t.Errorf("Run(%q) stderr = %q, want it to show the version usage", test.args, stderr.String())
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
	if !strings.Contains(stdout.String(), "agentrec version [--verbose]") {
		t.Errorf("help output = %q, want it to document the verbose version diagnostic", stdout.String())
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

func TestVersionVerboseReportsExecutableIdentity(t *testing.T) {
	const pkg = "github.com/seongwoo-choi/agentrec/internal/cli"
	root := t.TempDir()
	binary := filepath.Join(root, "invoked", "agentrec")
	if err := os.Mkdir(filepath.Dir(binary), 0o700); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build",
		"-ldflags", "-X "+pkg+".version=v9.9.9 -X "+pkg+".commit=0123456789abcdef0123456789abcdef01234567 -X "+pkg+".built=2026-07-28T00:00:00Z",
		"-o", binary, "./cmd/agentrec",
	)
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build with injected metadata: %v\n%s", err, out)
	}
	const prefix = "agentrec v9.9.9\n" +
		"commit 0123456789abcdef0123456789abcdef01234567\n" +
		"built 2026-07-28T00:00:00Z\n"
	run := func(name, dir, pathValue, candidates string) {
		t.Helper()
		cmd := exec.Command(binary, "version", "--verbose")
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + pathValue}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
		want := prefix + "executable " + strconv.Quote(binary) + "\n" + candidates
		if string(out) != want {
			t.Fatalf("%s output = %q, want %q", name, out, want)
		}
	}

	pathDir := filepath.Join(root, "path")
	if err := os.Mkdir(pathDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pathBinary := filepath.Join(pathDir, "agentrec")
	if err := os.WriteFile(pathBinary, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	run("duplicate PATH", "", pathDir+string(os.PathListSeparator)+filepath.Dir(binary),
		"path-candidate "+strconv.Quote(pathBinary)+" other\n"+
			"path-candidate "+strconv.Quote(binary)+" current\n")
	run("matching PATH", "", filepath.Dir(binary),
		"path-candidate "+strconv.Quote(binary)+" current\n")

	aliasDir := filepath.Join(root, "alias")
	if err := os.Mkdir(aliasDir, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(aliasDir, "agentrec")
	if err := os.Symlink(binary, alias); err != nil {
		t.Fatal(err)
	}
	resolvedAliasDir, err := filepath.EvalSymlinks(aliasDir)
	if err != nil {
		t.Fatal(err)
	}
	run("relative symlink PATH", aliasDir, ".",
		"path-candidate "+strconv.Quote(filepath.Join(resolvedAliasDir, "agentrec"))+" current\n")

	deniedPath := filepath.Join(root, "denied")
	if err := os.Mkdir(deniedPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deniedPath, "agentrec"), []byte("#!/bin/sh\n"), 0o010); err != nil {
		t.Fatal(err)
	}
	run("inaccessible PATH candidate", "", deniedPath, "path-candidate unavailable\n")

	emptyPath := filepath.Join(root, "empty")
	if err := os.Mkdir(emptyPath, 0o700); err != nil {
		t.Fatal(err)
	}
	run("PATH without candidate", "", emptyPath, "path-candidate unavailable\n")
}
