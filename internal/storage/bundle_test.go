package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/action"
	"github.com/seongwoo-choi/agentrec/internal/redaction"
	"github.com/seongwoo-choi/agentrec/internal/usage"
)

// testManifest is a representative manifest holding nothing sensitive.
func testManifest() Manifest {
	return Manifest{
		Provider:        "claude",
		ProviderVersion: "1.2.3",
		Argv:            []string{"agentrec", "trace", "--", "claude", "-p", "hello"},
		CWD:             "/workspace/project",
		StartedAt:       time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC),
	}
}

func TestInstallAtSyncsFileBeforeRenameAndDirectoryAfter(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var stages []string
	syncFile := func(file *os.File) error {
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if info.IsDir() {
			if _, err := root.Lstat(manifestFile); err != nil {
				t.Fatalf("directory synced before install: %v", err)
			}
			if _, err := root.Lstat(manifestFile + ".tmp"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("temporary manifest at directory sync: %v", err)
			}
			stages = append(stages, "directory")
			return nil
		}
		if _, err := root.Lstat(manifestFile); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("manifest installed before file sync: %v", err)
		}
		stages = append(stages, "file")
		return nil
	}

	if err := installAtWithSync(root, manifestFile, []byte("{}\n"), syncFile); err != nil {
		t.Fatalf("installAtWithSync: %v", err)
	}
	if got := strings.Join(stages, ","); got != "file,directory" {
		t.Fatalf("sync stages = %q, want file,directory", got)
	}
}

func TestInstallAtReportsTemporaryCleanupFailure(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	syncErr := errors.New("injected file sync failure")
	removeErr := errors.New("injected temporary cleanup failure")
	err = installAtWithOps(
		root,
		manifestFile,
		[]byte("manifest"),
		func(*os.File) error { return syncErr },
		func(string) error { return removeErr },
	)
	if !errors.Is(err, syncErr) {
		t.Fatalf("error lost sync failure: %v", err)
	}
	if !errors.Is(err, removeErr) {
		t.Fatalf("error lost cleanup failure: %v", err)
	}
}

func TestInstallNewAtSyncsFileBeforeLinkAndDirectoryAfter(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var stages []string
	syncFile := func(file *os.File) error {
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if info.IsDir() {
			if _, err := root.Lstat(usageFile); err != nil {
				t.Fatalf("directory synced before link: %v", err)
			}
			if _, err := root.Lstat(usageFile + ".tmp"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("temporary link at directory sync: %v", err)
			}
			stages = append(stages, "directory")
			return nil
		}
		if _, err := root.Lstat(usageFile); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("artifact linked before file sync: %v", err)
		}
		stages = append(stages, "file")
		return nil
	}

	if err := installNewAtWithSync(root, usageFile, []byte("{}\n"), syncFile); err != nil {
		t.Fatalf("installNewAtWithSync: %v", err)
	}
	if got := strings.Join(stages, ","); got != "file,directory" {
		t.Fatalf("sync stages = %q, want file,directory", got)
	}
}

func TestFinishNewFileAtSyncsFileBeforeDirectory(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := createFileAt(root, promptFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("prompt\n"); err != nil {
		t.Fatal(err)
	}
	var stages []string
	syncFile := func(target *os.File) error {
		info, err := target.Stat()
		if err != nil {
			return err
		}
		if info.IsDir() {
			stages = append(stages, "directory")
		} else {
			stages = append(stages, "file")
		}
		return nil
	}

	if err := finishNewFileAtWithSync(root, promptFile, file, syncFile); err != nil {
		t.Fatalf("finishNewFileAtWithSync: %v", err)
	}
	if got := strings.Join(stages, ","); got != "file,directory" {
		t.Fatalf("sync stages = %q, want file,directory", got)
	}
}

func TestCreateRunRootSyncsParentAfterDirectoryCreation(t *testing.T) {
	parent, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	var synced bool
	syncFile := func(file *os.File) error {
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if !info.IsDir() {
			t.Fatal("parent sync target is not a directory")
		}
		if child, err := parent.Lstat("run-durable"); err != nil || !child.IsDir() {
			t.Fatalf("run directory before parent sync = %v, %v", child, err)
		}
		synced = true
		return nil
	}

	runRoot, err := createRunRootAtWithSync(parent, "run-durable", syncFile)
	if err != nil {
		t.Fatalf("createRunRootAtWithSync: %v", err)
	}
	defer runRoot.Close()
	if !synced {
		t.Fatal("parent directory was not synced")
	}
}

func TestEnsureRootSyncsEveryNewDirectoryEntry(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "agentrec", "runs")
	var synced []string
	syncFile := func(file *os.File) error {
		synced = append(synced, filepath.Base(file.Name()))
		return nil
	}

	if err := ensureRootWithSync(root, syncFile); err != nil {
		t.Fatalf("ensureRootWithSync: %v", err)
	}
	if got := len(synced); got != 3 {
		t.Fatalf("synced directories = %v, want anchor parent plus two new-entry syncs", synced)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("root = %v, %v", info, err)
	}
}

func TestEnsureRootRetryResyncsExistingAncestorParent(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "agentrec", "runs")
	failFirst := true
	err := ensureRootWithSync(root, func(*os.File) error {
		if failFirst {
			failFirst = false
			return errors.New("injected sync failure")
		}
		return nil
	})
	if err == nil {
		t.Fatal("first root creation ignored sync failure")
	}
	var syncs int
	if err := ensureRootWithSync(root, func(*os.File) error {
		syncs++
		return nil
	}); err != nil {
		t.Fatalf("retry ensureRootWithSync: %v", err)
	}
	if syncs != 3 {
		t.Fatalf("retry sync count = %d, want anchor parent plus two new directory entries", syncs)
	}
}

func TestCreateProcessRootSyncsRunDirectory(t *testing.T) {
	runRoot, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer runRoot.Close()
	var synced bool
	processRoot, err := createProcessRootAtWithSync(runRoot, func(file *os.File) error {
		if _, err := runRoot.Lstat(processDirName); err != nil {
			t.Fatalf("process directory before parent sync: %v", err)
		}
		synced = true
		return nil
	})
	if err != nil {
		t.Fatalf("createProcessRootAtWithSync: %v", err)
	}
	defer processRoot.Close()
	if !synced {
		t.Fatal("run directory was not synced")
	}
}

func TestDiscardReportsRunDirectoryCleanupFailure(t *testing.T) {
	root := t.TempDir()
	b, err := Create(root, "run-cleanup", testManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.Dir(), "unexpected"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = b.discard()
	if err == nil || !strings.Contains(err.Error(), "remove run directory") {
		t.Fatalf("discard error = %v, want run-directory cleanup failure", err)
	}
	if _, err := os.Stat(filepath.Join(b.Dir(), "unexpected")); err != nil {
		t.Fatalf("unexpected content was removed: %v", err)
	}
}

func TestDiscardUsesHeldParentAfterRootPathReplacement(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "runs")
	b, err := Create(root, "run-held-parent", testManifest())
	if err != nil {
		t.Fatal(err)
	}
	moved := root + "-moved"
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	replacementRun := filepath.Join(root, "run-held-parent")
	if err := os.MkdirAll(replacementRun, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := b.discard(); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if info, err := os.Stat(replacementRun); err != nil || !info.IsDir() {
		t.Fatalf("replacement run was removed: %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(moved, "run-held-parent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("held original run still exists: %v", err)
	}
}

func TestCreateRedactsSeparateSecretFlagValueInManifest(t *testing.T) {
	manifest := testManifest()
	secret := "tiny"
	manifest.Argv = []string{"claude", "--api-key", secret, "--token", secret, "-p", "hello"}
	originalArgv := slices.Clone(manifest.Argv)

	b, err := Create(t.TempDir(), "run-1", manifest)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	assertRedacted := func(stage string) {
		t.Helper()
		got := readManifest(t, b)
		if slices.Contains(got.Argv, secret) {
			t.Fatalf("%s manifest persisted the separate secret flag value verbatim", stage)
		}
		for _, index := range []int{2, 4} {
			if got.Argv[index] != "[REDACTED:1]" {
				t.Errorf("%s manifest argv[%d] = %q, want [REDACTED:1]", stage, index, got.Argv[index])
			}
		}
		if got.RedactionRuleVersion != "6" {
			t.Errorf("%s redactionRuleVersion = %q, want 6", stage, got.RedactionRuleVersion)
		}
	}
	assertRedacted("initial")
	if !slices.Equal(manifest.Argv, originalArgv) {
		t.Fatalf("Create mutated caller argv to %#v, want %#v", manifest.Argv, originalArgv)
	}

	if err := b.Finalize(Finalization{EndedAt: time.Now(), ExitReason: "completed"}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	assertRedacted("final")
	if !slices.Equal(manifest.Argv, originalArgv) {
		t.Fatalf("Finalize mutated caller argv to %#v, want %#v", manifest.Argv, originalArgv)
	}
}

func TestArgvEstablishedShortSecretStaysRedactedInSecretNamedFields(t *testing.T) {
	tests := []struct {
		name  string
		event string
		want  string
	}{
		{"exact", `{"api_key":"tiny","note":"tiny"}`, `{"api_key":"[REDACTED:1]","note":"tiny"}`},
		{"padded", `{"api_key":" tiny "}`, `{"api_key":" [REDACTED:1] "}`},
		{"quoted", `{"api_key":"\"tiny\""}`, `{"api_key":"\"[REDACTED:1]\""}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := testManifest()
			manifest.Argv = []string{"claude", "--api-key", "tiny"}
			b, err := Create(t.TempDir(), "run-1", manifest)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			if err := b.WriteProviderEvent([]byte(tt.event)); err != nil {
				t.Fatalf("WriteProviderEvent: %v", err)
			}
			lines := readLines(t, filepath.Join(b.Dir(), eventsFile))
			if len(lines) != 1 || lines[0] != tt.want {
				t.Fatalf("provider events = %#v, want %q", lines, tt.want)
			}
		})
	}
}

func TestCreateRedactsInlineSecretFlagValueInManifest(t *testing.T) {
	manifest := testManifest()
	manifest.Argv = []string{"claude", "--api-key=-credential-value", "-p", "hello"}

	b, err := Create(t.TempDir(), "run-1", manifest)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := readManifest(t, b)
	if got.Argv[1] != "--api-key=[REDACTED:1]" {
		t.Errorf("manifest argv secret = %q, want --api-key=[REDACTED:1]", got.Argv[1])
	}
}

func TestCreateRedactsFinalInlineSecretFlagValueInManifest(t *testing.T) {
	manifest := testManifest()
	manifest.Argv = []string{"claude", "--api-key=tiny"}

	b, err := Create(t.TempDir(), "run-1", manifest)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := readManifest(t, b)
	if got.Argv[1] != "--api-key=[REDACTED:1]" {
		t.Errorf("manifest argv secret = %q, want --api-key=[REDACTED:1]", got.Argv[1])
	}
}

func TestCreateDoesNotLetMarkerShapedCredentialsCollide(t *testing.T) {
	tests := []struct {
		name string
		argv []string
	}{
		{"separate", []string{"claude", "--api-key", "[REDACTED:1]", "--token", "different-secret"}},
		{"inline", []string{"claude", "--api-key=[REDACTED:1]", "--token", "different-secret"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := testManifest()
			manifest.Argv = tt.argv
			b, err := Create(t.TempDir(), "run-1", manifest)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			got := readManifest(t, b).Argv
			if got[len(got)-1] != "[REDACTED:2]" {
				t.Errorf("second credential = %q, want [REDACTED:2]", got[len(got)-1])
			}
		})
	}
}

func TestMarkerShapedCredentialKeepsItsOwnMarkerInLaterArtifacts(t *testing.T) {
	manifest := testManifest()
	manifest.Argv = []string{"claude", "--api-key", "normal-secret", "--token", "[REDACTED:1]"}
	b, err := Create(t.TempDir(), "run-1", manifest)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := readManifest(t, b).Argv[4]; got != "[REDACTED:2]" {
		t.Fatalf("marker-shaped credential = %q, want [REDACTED:2]", got)
	}

	if err := b.WriteProviderEvent([]byte(`{"api_key":"[REDACTED:1]"}`)); err != nil {
		t.Fatalf("WriteProviderEvent: %v", err)
	}
	lines := readLines(t, filepath.Join(b.Dir(), eventsFile))
	if len(lines) != 1 || lines[0] != `{"api_key":"[REDACTED:2]"}` {
		t.Fatalf("provider events = %#v, want marker-shaped credential correlation", lines)
	}
}

func TestCreateRejectsAmbiguousSeparateSecretValues(t *testing.T) {
	tests := []struct {
		name string
		argv []string
	}{
		{"missing", []string{"claude", "--api-key"}},
		{"delimiter", []string{"claude", "--api-key", "--", "--token", "visible"}},
		{"another option", []string{"claude", "--api-key", "--verbose", "visible"}},
		{"hyphen-leading value", []string{"claude", "--api-key", "-credential-value"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			manifest := testManifest()
			manifest.Argv = tt.argv

			if _, err := Create(root, "run-1", manifest); err == nil {
				t.Fatal("Create succeeded, want ambiguous secret option rejection")
			} else {
				if !strings.Contains(err.Error(), "use --secret-option=<value>") {
					t.Errorf("Create error = %q, want safe inline-value guidance", err)
				}
				if strings.Contains(err.Error(), "credential-value") {
					t.Errorf("Create error leaked the ambiguous value: %q", err)
				}
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("rejected Create left %d run entries, want none", len(entries))
			}
		})
	}
}

func statMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func TestWriteUsagePersistsOnePrivateCanonicalArtifact(t *testing.T) {
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	input := int64(1200)
	cost := 0.0421
	reported := usage.Report{Schema: 1, Attribution: usage.AttributionProviderReported, Provider: "claude", Scope: usage.ScopeRun, InputTokens: &input, CostUSD: &cost}
	if err := b.WriteUsage(reported); err != nil {
		t.Fatalf("WriteUsage: %v", err)
	}
	path := filepath.Join(b.Dir(), usageFile)
	if got := statMode(t, path); got != fileMode {
		t.Errorf("mode = %04o, want %04o", got, fileMode)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got usage.Report
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode usage: %v", err)
	}
	if err := got.Validate(); err != nil || got.InputTokens == nil || *got.InputTokens != input {
		t.Errorf("persisted usage = %+v, validation %v", got, err)
	}
}

func TestWriteUsageRefusesDuplicateOrSymlinkArtifact(t *testing.T) {
	input := int64(1)
	reported := usage.Report{Schema: 1, Attribution: usage.AttributionProviderReported, Provider: "claude", Scope: usage.ScopeRun, InputTokens: &input}
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, path string)
	}{
		{"duplicate", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("existing\n"), fileMode); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink", func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(target, []byte("outside\n"), fileMode); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := Create(t.TempDir(), "run-1", testManifest())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(b.Dir(), usageFile)
			tc.plant(t, path)
			if err := b.WriteUsage(reported); err == nil {
				t.Fatal("WriteUsage succeeded, want refusal")
			}
		})
	}
}

func TestWriteUsageStaysInOriginalRunDirectoryAfterAncestorReplacement(t *testing.T) {
	root := t.TempDir()
	b, err := Create(root, "run-1", testManifest())
	if err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(root, "original")
	if err := os.Rename(b.Dir(), original); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, b.Dir()); err != nil {
		t.Fatal(err)
	}
	input := int64(1)
	reported := usage.Report{Schema: 1, Attribution: usage.AttributionProviderReported, Provider: "claude", Scope: usage.ScopeRun, InputTokens: &input}
	if err := b.WriteUsage(reported); err != nil {
		t.Fatalf("WriteUsage: %v", err)
	}
	if _, err := os.Stat(filepath.Join(original, usageFile)); err != nil {
		t.Fatalf("usage missing from original directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, usageFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside usage artifact: %v", err)
	}
}

func TestBundleWritesStayInOriginalRunDirectoryAfterAncestorReplacement(t *testing.T) {
	root := t.TempDir()
	b, err := Create(root, "run-1", testManifest())
	if err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(root, "original")
	if err := os.Rename(b.Dir(), original); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, b.Dir()); err != nil {
		t.Fatal(err)
	}

	if err := b.WritePrompt("prompt"); err != nil {
		t.Fatalf("WritePrompt: %v", err)
	}
	if err := b.WriteUnparsedLine([]byte("notice")); err != nil {
		t.Fatalf("WriteUnparsedLine: %v", err)
	}
	if err := b.WriteProcessStderr("stderr"); err != nil {
		t.Fatalf("WriteProcessStderr: %v", err)
	}
	if err := b.WriteProcessResult([]byte(`{"exitReason":"completed"}`)); err != nil {
		t.Fatalf("WriteProcessResult: %v", err)
	}
	if err := b.Finalize(Finalization{EndedAt: time.Now(), ExitReason: "completed", UnparsedLines: 1}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	for _, name := range []string{promptFile, unparsedFile, filepath.Join(processDirName, stderrFile), filepath.Join(processDirName, resultFile), manifestFile} {
		if _, err := os.Stat(filepath.Join(original, name)); err != nil {
			t.Errorf("original artifact %s: %v", name, err)
		}
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("replacement directory received %d artifact(s)", len(entries))
	}
}

func TestProcessWritesStayInHeldDirectoryAfterProcessEntryReplacement(t *testing.T) {
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProcessStderr("stderr"); err != nil {
		t.Fatalf("WriteProcessStderr: %v", err)
	}
	original := filepath.Join(b.Dir(), "process-original")
	if err := os.Rename(filepath.Join(b.Dir(), processDirName), original); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(b.Dir(), processDirName)); err != nil {
		t.Fatal(err)
	}

	if err := b.WriteProcessResult([]byte(`{"exitReason":"completed"}`)); err != nil {
		t.Fatalf("WriteProcessResult: %v", err)
	}
	if _, err := os.Stat(filepath.Join(original, resultFile)); err != nil {
		t.Fatalf("result missing from held process directory: %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("replacement process directory received %d artifact(s)", len(entries))
	}
}

func TestCreateRejectsUnsafeRunIDs(t *testing.T) {
	// Arrange
	root := t.TempDir()

	// Act & Assert: a run ID is one clean path component and nothing else, so
	// none of these may reach the filesystem.
	for _, runID := range []string{"", ".", "..", "../escape", "sub/run", "run/", "./run"} {
		if _, err := Create(root, runID, testManifest()); err == nil {
			t.Errorf("Create(root, %q) succeeded, want error", runID)
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("rejected run IDs left %d entries under root, want none", len(entries))
	}
}

func TestCreateDoesNotOverwriteAnExistingRun(t *testing.T) {
	// Arrange
	root := t.TempDir()
	first, err := Create(root, "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(first.Dir(), "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	// Act
	second, err := Create(root, "run-1", testManifest())

	// Assert
	if err == nil {
		t.Fatalf("Create over existing run %s succeeded, want error", second.Dir())
	}
	after, err := os.ReadFile(filepath.Join(first.Dir(), "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest after refused Create: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("refused Create rewrote the existing manifest")
	}
}

// readLines returns the non-empty lines of a bundle file.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := strings.TrimSuffix(string(raw), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func TestWriteActionAppendsOneLinePerActionWhileTheRunIsOpen(t *testing.T) {
	// Arrange
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	actions := filepath.Join(b.Dir(), "actions.jsonl")

	// Act & Assert: each action must be on disk before the next one is
	// written, so a run interrupted mid-flight keeps everything it recorded.
	for i, a := range []action.Action{
		{ID: "a1", Type: action.TypeFileRead, Assurance: action.AssuranceProviderReported},
		{ID: "a2", Type: action.TypeShellExec, Assurance: action.Assurance("supervisor_observed")},
	} {
		if err := b.WriteAction(a); err != nil {
			t.Fatalf("WriteAction %s: %v", a.ID, err)
		}
		if got := len(readLines(t, actions)); got != i+1 {
			t.Fatalf("after writing %s the file holds %d lines, want %d", a.ID, got, i+1)
		}
	}

	lines := readLines(t, actions)
	for i, want := range []string{"a1", "a2"} {
		var got action.Action
		if err := json.Unmarshal([]byte(lines[i]), &got); err != nil {
			t.Fatalf("line %d is not one JSON action: %v", i+1, err)
		}
		if got.ID != want {
			t.Errorf("line %d has id %q, want %q", i+1, got.ID, want)
		}
	}
}

func TestWriteActionRejectsAnInvalidActionAndWritesNothing(t *testing.T) {
	// Arrange
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Act: no assurance, which action.Writer refuses to encode.
	err = b.WriteAction(action.Action{ID: "a1", Type: action.TypeFileRead})

	// Assert
	if err == nil {
		t.Fatal("WriteAction accepted an action with no assurance, want error")
	}
	if !strings.Contains(err.Error(), "missing assurance") {
		t.Errorf("error %q does not report the failed validation", err)
	}
	if lines := readLines(t, filepath.Join(b.Dir(), "actions.jsonl")); len(lines) != 0 {
		t.Errorf("rejected action left %d lines on disk, want none", len(lines))
	}
}

// fixturePath resolves a fixture relative to this package's source directory so
// tests do not depend on the process working directory.
func fixturePath(t *testing.T, parts ...string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test source directory")
	}
	return filepath.Join(append([]string{filepath.Dir(thisFile), "..", "..", "testdata"}, parts...)...)
}

// fixtureSecrets are the synthetic values planted in the redaction fixture.
// None is a real credential, and none may appear in a bundle.
var fixtureSecrets = []string{
	"synthetic-token-aaaaaaaa",
	"synthetic-secret-bbbbbbbb",
	"synthetic-password-dddddddd",
	"synthetic-env-token-11111111",
	"ghp_syntheticAAAABBBBCCCCDDDD",
	"AKIASYNTHETIC0000000",
	"c3ludGhldGljLXJzYS1rZXktYm9keQ==",
}

func TestProviderEventsFromTheFixtureArePersistedSanitized(t *testing.T) {
	// Arrange
	raw, err := os.ReadFile(fixturePath(t, "redaction", "provider-events.jsonl"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	for _, secret := range fixtureSecrets {
		if !strings.Contains(string(raw), secret) {
			t.Fatalf("fixture does not contain %q, so this test would prove nothing", secret)
		}
	}
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Act: one event per call, as a running provider delivers them.
	events := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if err := b.WriteProviderEvent([]byte(line)); err != nil {
			t.Fatalf("WriteProviderEvent: %v", err)
		}
		events++
	}

	// Assert
	path := filepath.Join(b.Dir(), "provider-events.sanitized.jsonl")
	lines := readLines(t, path)
	if len(lines) != events {
		t.Errorf("wrote %d events but the stream holds %d lines", events, len(lines))
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	for _, secret := range fixtureSecrets {
		if strings.Contains(string(stored), secret) {
			t.Errorf("sanitized stream leaked %q", secret)
		}
		digest := sha256.Sum256([]byte(secret))
		if strings.Contains(string(stored), hex.EncodeToString(digest[:])) {
			t.Errorf("sanitized stream contains the SHA-256 digest of %q", secret)
		}
	}
}

func TestWriteUnparsedLineReplacesInvalidUTF8AndCountsIt(t *testing.T) {
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := b.WriteUnparsedLine([]byte{0xff, 0xfe}); err != nil {
		t.Fatalf("WriteUnparsedLine: %v", err)
	}
	if err := b.Finalize(Finalization{
		EndedAt:       testManifest().StartedAt.Add(time.Second),
		ExitReason:    "completed",
		UnparsedLines: 1,
	}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	lines := readLines(t, filepath.Join(b.Dir(), unparsedFile))
	if len(lines) != 1 || lines[0] != unparsedNotUTF8 {
		t.Errorf("unparsed lines = %q, want only %q", lines, unparsedNotUTF8)
	}
	if got := readManifest(t, b).UnparsedLines; got != 1 {
		t.Errorf("manifest unparsedLines = %d, want 1", got)
	}
}

func TestFinalizeReportsAnUnparsedStreamCloseFailureAfterWritingManifest(t *testing.T) {
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := b.WriteUnparsedLine([]byte("provider banner")); err != nil {
		t.Fatalf("WriteUnparsedLine: %v", err)
	}
	if err := b.unparsed.Close(); err != nil {
		t.Fatalf("close unparsed stream before Finalize: %v", err)
	}

	err = b.Finalize(Finalization{
		EndedAt:       testManifest().StartedAt.Add(time.Second),
		ExitReason:    "storage_error",
		UnparsedLines: 1,
	})
	if err == nil || !strings.Contains(err.Error(), unparsedFile) {
		t.Fatalf("Finalize error = %v, want unparsed stream failure", err)
	}
	manifest := readManifest(t, b)
	if manifest.ExitReason != "storage_error" || manifest.UnparsedLines != 1 {
		t.Errorf("manifest exit = %q, unparsedLines = %d; want storage_error and 1", manifest.ExitReason, manifest.UnparsedLines)
	}
}

func TestWriteProviderEventFailsClosedOnMalformedEvents(t *testing.T) {
	// Arrange
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	path := filepath.Join(b.Dir(), "provider-events.sanitized.jsonl")

	// Act & Assert: an event this package cannot parse is one it cannot vouch
	// for, so nothing about it reaches the bundle.
	for _, event := range []string{
		`{"type":"tool.call"`,
		`["not","an","object"]`,
		`"a bare string"`,
		`{"a":1} {"b":2}`,
		``,
	} {
		if err := b.WriteProviderEvent([]byte(event)); err == nil {
			t.Errorf("WriteProviderEvent(%q) succeeded, want error", event)
		}
		if lines := readLines(t, path); len(lines) != 0 {
			t.Fatalf("malformed event %q left %d lines on disk, want none", event, len(lines))
		}
	}
}

func TestWriteProviderEventRejectsALineTheBundleReadersCannotRead(t *testing.T) {
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	raw := []byte(`{"type":"tool.call","value":"` + strings.Repeat("x", 4<<20) + `"}`)

	err = b.WriteProviderEvent(raw)

	if err == nil || !strings.Contains(err.Error(), "line") {
		t.Fatalf("WriteProviderEvent error = %v, want line-limit error", err)
	}
	if lines := readLines(t, filepath.Join(b.Dir(), eventsFile)); len(lines) != 0 {
		t.Fatalf("oversize event left %d lines on disk, want none", len(lines))
	}
}

func TestWriteProviderEventAcceptsTheLargestBundleReaderLine(t *testing.T) {
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	prefix, suffix := `{"value":"`, `"}`
	raw := []byte(prefix + strings.Repeat("x", MaxStreamLineBytes-1-len(prefix)-len(suffix)) + suffix)

	if err := b.WriteProviderEvent(raw); err != nil {
		t.Fatalf("WriteProviderEvent: %v", err)
	}
	info, err := os.Stat(filepath.Join(b.Dir(), eventsFile))
	if err != nil {
		t.Fatalf("stat event stream: %v", err)
	}
	if info.Size() != MaxStreamLineBytes {
		t.Fatalf("event stream size = %d, want %d including delimiter", info.Size(), MaxStreamLineBytes)
	}
}

func TestAppendLineAcceptsExactStreamAndEntryLimits(t *testing.T) {
	t.Run("stream bytes", func(t *testing.T) {
		b, err := Create(t.TempDir(), "run-1", testManifest())
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		payload := []byte(`{}`)
		start := int64(MaxStreamBytes - len(payload) - 1)
		if err := b.events.Truncate(start); err != nil {
			t.Fatalf("prepare stream boundary: %v", err)
		}
		b.eventsState.bytes = int(start)

		if err := b.appendLine(b.events, eventsFile, payload, &b.eventsState); err != nil {
			t.Fatalf("append to exact byte limit: %v", err)
		}
		if b.eventsState.bytes != MaxStreamBytes {
			t.Fatalf("stream bytes = %d, want %d", b.eventsState.bytes, MaxStreamBytes)
		}
		if err := b.appendLine(b.events, eventsFile, payload, &b.eventsState); err == nil {
			t.Fatal("append beyond byte limit succeeded")
		}
		info, err := b.events.Stat()
		if err != nil {
			t.Fatalf("stat event stream: %v", err)
		}
		if info.Size() != MaxStreamBytes {
			t.Fatalf("event stream size after rejection = %d, want %d", info.Size(), MaxStreamBytes)
		}
	})

	t.Run("entries", func(t *testing.T) {
		b, err := Create(t.TempDir(), "run-1", testManifest())
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		b.eventsState.entries = MaxStreamEntries - 1
		if err := b.appendLine(b.events, eventsFile, []byte(`{}`), &b.eventsState); err != nil {
			t.Fatalf("append exact final entry: %v", err)
		}
		if b.eventsState.entries != MaxStreamEntries {
			t.Fatalf("stream entries = %d, want %d", b.eventsState.entries, MaxStreamEntries)
		}
		if err := b.appendLine(b.events, eventsFile, []byte(`{}`), &b.eventsState); err == nil {
			t.Fatal("append beyond entry limit succeeded")
		}
		if lines := readLines(t, filepath.Join(b.Dir(), eventsFile)); len(lines) != 1 {
			t.Fatalf("event stream holds %d physical entries, want only accepted append", len(lines))
		}
	})
}

func TestActionAndUnparsedWritersRejectLinesTheBundleReadersCannotRead(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		write    func(*Bundle) error
	}{
		{
			name:     "action",
			filename: actionsFile,
			write: func(b *Bundle) error {
				return b.WriteAction(action.Action{
					ID:        "a1",
					Type:      action.TypeToolCall,
					Assurance: action.AssuranceProviderReported,
					Result:    json.RawMessage(`{"value":"` + strings.Repeat("x", MaxStreamLineBytes) + `"}`),
				})
			},
		},
		{
			name:     "unparsed",
			filename: unparsedFile,
			write: func(b *Bundle) error {
				return b.WriteUnparsedLine([]byte(strings.Repeat("x", MaxStreamLineBytes)))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := Create(t.TempDir(), "run-1", testManifest())
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			err = tt.write(b)

			if err == nil || !strings.Contains(err.Error(), "line") {
				t.Fatalf("write error = %v, want line-limit error", err)
			}
			data, readErr := os.ReadFile(filepath.Join(b.Dir(), tt.filename))
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("read stream: %v", readErr)
			}
			if len(data) != 0 {
				t.Fatalf("rejected write left %d bytes on disk, want none", len(data))
			}
		})
	}
}

func TestWriteProviderEventRejectsMoreEntriesThanBundleReadersAccept(t *testing.T) {
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := 0; i < 100000; i++ {
		if err := b.WriteProviderEvent([]byte(`{}`)); err != nil {
			t.Fatalf("WriteProviderEvent %d: %v", i+1, err)
		}
	}

	err = b.WriteProviderEvent([]byte(`{}`))

	if err == nil || !strings.Contains(err.Error(), "events") {
		t.Fatalf("WriteProviderEvent 100001 error = %v, want event-count limit", err)
	}
}

func TestWriteProviderEventRejectsAStreamLargerThanBundleReadersAccept(t *testing.T) {
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	b.eventsState.bytes = (64 << 20) - len("{}\n") + 1

	err = b.WriteProviderEvent([]byte(`{}`))

	if err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("WriteProviderEvent error = %v, want stream-byte limit", err)
	}
	if lines := readLines(t, filepath.Join(b.Dir(), eventsFile)); len(lines) != 0 {
		t.Fatalf("event beyond stream limit left %d lines on disk, want none", len(lines))
	}
}

func TestWriteProviderEventRejectsNestingBeyondTheBundleReaderLimit(t *testing.T) {
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	raw := []byte(strings.Repeat(`{"value":`, 65) + `0` + strings.Repeat(`}`, 65))

	err = b.WriteProviderEvent(raw)

	if err == nil || !strings.Contains(err.Error(), "nesting") {
		t.Fatalf("WriteProviderEvent error = %v, want nesting-limit error", err)
	}
	if lines := readLines(t, filepath.Join(b.Dir(), eventsFile)); len(lines) != 0 {
		t.Fatalf("over-nested event left %d lines on disk, want none", len(lines))
	}
}

func TestWriteProviderEventRejectsMoreJSONTokensThanBundleReadersAccept(t *testing.T) {
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	values := strings.TrimSuffix(strings.Repeat("0,", 128), ",")
	event := []byte(`{"values":[` + values + `]}`)
	tokens, err := ValidateProviderEvent(event, MaxProviderEventTokens)
	if err != nil {
		t.Fatalf("ValidateProviderEvent: %v", err)
	}
	writes := MaxProviderEventTokens/tokens + 1
	for i := 0; i < writes-1; i++ {
		if err := b.WriteProviderEvent(event); err != nil {
			t.Fatalf("WriteProviderEvent %d: %v", i, err)
		}
	}

	err = b.WriteProviderEvent(event)

	if err == nil || !strings.Contains(err.Error(), "tokens") {
		t.Fatalf("WriteProviderEvent error = %v, want token-limit error", err)
	}
}

func TestWriteProviderEventRollsBackAPartialAppend(t *testing.T) {
	if os.Getenv("AGENTREC_PARTIAL_WRITE_HELPER") == "1" {
		// Not t.TempDir: this branch leaves through os.Exit, which skips cleanups.
		// The deferred removal covers the failing paths, which leave through
		// Goexit instead.
		dir, err := os.MkdirTemp("", "agentrec-partial-write")
		if err != nil {
			t.Fatalf("temp dir: %v", err)
		}
		defer os.RemoveAll(dir)
		b, err := Create(dir, "run-1", testManifest())
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := b.WriteProviderEvent([]byte(`{}`)); err != nil {
			t.Fatalf("write prefix: %v", err)
		}
		path := filepath.Join(b.Dir(), eventsFile)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat event stream: %v", err)
		}
		limit := uint64(info.Size() + 5)
		setPartialWriteLimit(t, limit)

		if err := b.WriteProviderEvent([]byte(`{"value":"more"}`)); err == nil {
			t.Fatal("partial WriteProviderEvent succeeded")
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read event stream: %v", err)
		}
		if string(got) != "{}\n" {
			t.Fatalf("event stream after partial write = %q, want intact prefix", got)
		}
		// The file-size limit outlives the test, and a -cover build of this binary
		// writes a coverage meta file at teardown that the limit refuses, failing
		// the child for a reason that is not the one under test. Exiting here skips
		// that teardown; a failure above still reaches the parent as FAIL output
		// and a non-zero exit. Exiting 0 from inside a test is only allowed
		// because the parent below forwards none of its own flags: with
		// -test.paniconexit0, which `go test` always passes, it would panic.
		os.RemoveAll(dir)
		os.Exit(0)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestWriteProviderEventRollsBackAPartialAppend$")
	cmd.Env = append(os.Environ(), "AGENTREC_PARTIAL_WRITE_HELPER=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("partial-write helper: %v\n%s", err, output)
	}
}

// markerPattern finds every run-local marker in a bundle file.
var markerPattern = regexp.MustCompile(`\[REDACTED:\d+\]`)

func TestOneSecretGetsOneMarkerAcrossEveryBundleFile(t *testing.T) {
	// Arrange: one synthetic token, reaching the bundle by four routes.
	const secret = "ghp_syntheticEEEEFFFFGGGGHHHH"
	manifest := testManifest()
	manifest.Argv = []string{"agentrec", "trace", "--", "claude", "--token", secret}

	b, err := Create(t.TempDir(), "run-1", manifest)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Act
	if err := b.WritePrompt("push the branch using " + secret + " and report back"); err != nil {
		t.Fatalf("WritePrompt: %v", err)
	}
	if err := b.WriteAction(action.Action{
		ID:        "a1",
		Type:      action.TypeToolCall,
		Assurance: action.AssuranceProviderReported,
		Input:     json.RawMessage(`{"github_token":"` + secret + `"}`),
	}); err != nil {
		t.Fatalf("WriteAction: %v", err)
	}
	if err := b.WriteProviderEvent([]byte(`{"type":"tool.call","command":"git push https://` + secret + `@example.invalid/repo"}`)); err != nil {
		t.Fatalf("WriteProviderEvent: %v", err)
	}
	if err := b.WriteProcessStderr("fatal: authentication failed for " + secret + "\n"); err != nil {
		t.Fatalf("WriteProcessStderr: %v", err)
	}
	if err := b.WriteProcessResult([]byte(`{"type":"result","stderr_tail":"failed for ` + secret + `"}`)); err != nil {
		t.Fatalf("WriteProcessResult: %v", err)
	}

	// Assert: every file carries a marker, none carries the secret or a
	// digest of it, and all six markers are the same one.
	markers := make(map[string]bool)
	files := []string{
		"manifest.json", "prompt.txt", "actions.jsonl", "provider-events.sanitized.jsonl",
		filepath.Join("process", "stderr.sanitized.log"), filepath.Join("process", "result.json"),
	}
	digest := sha256.Sum256([]byte(secret))
	for _, name := range files {
		path := filepath.Join(b.Dir(), name)
		if got := statMode(t, path); got != 0o600 {
			t.Errorf("%s mode = %04o, want 0600", name, got)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(raw), secret) {
			t.Errorf("%s leaked the secret", name)
		}
		if strings.Contains(string(raw), hex.EncodeToString(digest[:])) {
			t.Errorf("%s contains the SHA-256 digest of the secret", name)
		}
		found := markerPattern.FindAllString(string(raw), -1)
		if len(found) == 0 {
			t.Errorf("%s holds no marker, so the secret never reached it", name)
		}
		for _, m := range found {
			markers[m] = true
		}
	}
	if len(markers) != 1 {
		t.Errorf("got %d distinct markers across the bundle, want 1: %v", len(markers), markers)
	}
}

func TestWritePromptStoresTheRedactedPromptOnce(t *testing.T) {
	// Arrange
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	const prompt = "deploy with AKIASYNTHETIC1111111 then stop"

	// Act
	if err := b.WritePrompt(prompt); err != nil {
		t.Fatalf("WritePrompt: %v", err)
	}
	second := b.WritePrompt("another prompt")

	// Assert
	if second == nil {
		t.Error("a second WritePrompt succeeded, want error")
	}
	got, err := os.ReadFile(filepath.Join(b.Dir(), "prompt.txt"))
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	if strings.Contains(string(got), "AKIASYNTHETIC1111111") {
		t.Errorf("prompt.txt leaked the pattern-shaped secret")
	}
	if !strings.HasPrefix(string(got), "deploy with [REDACTED:") ||
		!strings.HasSuffix(strings.TrimSuffix(string(got), "\n"), "] then stop") {
		t.Errorf("prompt.txt = %q, want the prompt text with only the secret replaced", got)
	}
}

// readManifest decodes the bundle's manifest as it stands on disk.
func readManifest(t *testing.T, b *Bundle) Manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(b.Dir(), "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return m
}

func TestFinalizeRecordsTheWholeManifest(t *testing.T) {
	// Arrange
	want := testManifest()
	b, err := Create(t.TempDir(), "run-1", want)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ended := want.StartedAt.Add(90 * time.Second)

	// Act
	if err := b.Finalize(Finalization{EndedAt: ended, ExitReason: "exit:0", WarningCount: 2}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// Assert
	got := readManifest(t, b)
	if got.Provider != want.Provider || got.ProviderVersion != want.ProviderVersion {
		t.Errorf("provider = %q/%q, want %q/%q", got.Provider, got.ProviderVersion, want.Provider, want.ProviderVersion)
	}
	if strings.Join(got.Argv, " ") != strings.Join(want.Argv, " ") {
		t.Errorf("argv = %v, want %v", got.Argv, want.Argv)
	}
	if got.CWD != want.CWD {
		t.Errorf("cwd = %q, want %q", got.CWD, want.CWD)
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("startedAt = %s, want %s", got.StartedAt, want.StartedAt)
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(ended) {
		t.Errorf("endedAt = %v, want %s", got.EndedAt, ended)
	}
	if got.ExitReason != "exit:0" {
		t.Errorf("exitReason = %q, want %q", got.ExitReason, "exit:0")
	}
	if got.WarningCount != 2 {
		t.Errorf("warningCount = %d, want 2", got.WarningCount)
	}
	if got.RedactionRuleVersion != redaction.RuleVersion {
		t.Errorf("redactionRuleVersion = %q, want %q", got.RedactionRuleVersion, redaction.RuleVersion)
	}
	if got := statMode(t, filepath.Join(b.Dir(), "manifest.json")); got != 0o600 {
		t.Errorf("rewritten manifest mode = %04o, want 0600", got)
	}
}

func TestFinalizeCompletesAnInterruptedRun(t *testing.T) {
	// Arrange & Act: a run that ended badly still has to be closed out, and
	// whatever it recorded before the end has to survive.
	for _, reason := range []string{"interrupted", "timeout", "exit:1"} {
		b, err := Create(t.TempDir(), "run-1", testManifest())
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := b.WriteAction(action.Action{ID: "a1", Type: action.TypeShellExec, Assurance: action.Assurance("supervisor_observed")}); err != nil {
			t.Fatalf("WriteAction: %v", err)
		}
		ended := time.Date(2026, 7, 27, 10, 1, 0, 0, time.UTC)

		if err := b.Finalize(Finalization{EndedAt: ended, ExitReason: reason, WarningCount: 1}); err != nil {
			t.Fatalf("Finalize(%s): %v", reason, err)
		}

		// Assert
		if got := readManifest(t, b); got.ExitReason != reason || got.EndedAt == nil {
			t.Errorf("manifest after %s = %+v, want the reason and an end time", reason, got)
		}
		if lines := readLines(t, filepath.Join(b.Dir(), "actions.jsonl")); len(lines) != 1 {
			t.Errorf("%s run kept %d action lines, want 1", reason, len(lines))
		}
	}
}

func TestWritesAfterFinalizeFailAndFinalizeIsNotRepeatable(t *testing.T) {
	// Arrange
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	final := Finalization{EndedAt: time.Date(2026, 7, 27, 10, 1, 0, 0, time.UTC), ExitReason: "exit:0"}
	if err := b.Finalize(final); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// Act & Assert
	writes := map[string]error{
		"WritePrompt":        b.WritePrompt("late prompt"),
		"WriteAction":        b.WriteAction(action.Action{ID: "a1", Type: action.TypeShellExec, Assurance: action.AssuranceProviderReported}),
		"WriteProviderEvent": b.WriteProviderEvent([]byte(`{"type":"late"}`)),
		"Finalize":           b.Finalize(final),
	}
	for name, err := range writes {
		if !errors.Is(err, ErrFinalized) {
			t.Errorf("%s after Finalize returned %v, want ErrFinalized", name, err)
		}
	}
	if lines := readLines(t, filepath.Join(b.Dir(), "actions.jsonl")); len(lines) != 0 {
		t.Errorf("a post-finalize write reached actions.jsonl")
	}
	if _, err := os.Stat(filepath.Join(b.Dir(), "prompt.txt")); !os.IsNotExist(err) {
		t.Errorf("a post-finalize WritePrompt created prompt.txt")
	}
}

func TestAFailedStreamWriteStopsEveryLaterWrite(t *testing.T) {
	// Arrange: closing the action file makes the next append fail the way a
	// full or vanished disk would.
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := b.actions.Close(); err != nil {
		t.Fatalf("close action stream: %v", err)
	}

	// Act
	first := b.WriteAction(action.Action{ID: "a1", Type: action.TypeShellExec, Assurance: action.AssuranceProviderReported})

	// Assert: the failure is reported, then remembered. A stream that lost a
	// line no longer describes the run, so nothing more is appended to any of
	// them.
	if first == nil {
		t.Fatal("WriteAction on a broken stream returned nil, want a write error")
	}
	if !strings.Contains(first.Error(), actionsFile) {
		t.Errorf("error %q does not name the stream that failed", first)
	}
	later := map[string]error{
		"WriteProviderEvent": b.WriteProviderEvent([]byte(`{"type":"after"}`)),
		"WriteAction":        b.WriteAction(action.Action{ID: "a2", Type: action.TypeFileRead, Assurance: action.AssuranceProviderReported}),
		"WriteProcessStderr": b.WriteProcessStderr("after\n"),
		"WriteProcessResult": b.WriteProcessResult([]byte(`{"type":"result"}`)),
	}
	for name, err := range later {
		if !errors.Is(err, first) {
			t.Errorf("%s after a failed write returned %v, want the stored failure %v", name, err, first)
		}
	}
	if lines := readLines(t, filepath.Join(b.Dir(), eventsFile)); len(lines) != 0 {
		t.Errorf("a write after the failure reached %s", eventsFile)
	}
}

// processPath resolves one process artifact inside a bundle.
func processPath(b *Bundle, name string) string {
	return filepath.Join(b.Dir(), "process", name)
}

func TestWriteProcessStderrStoresTheCaptureUnderPrivateModes(t *testing.T) {
	// Arrange: a permissive umask masks nothing, so a too-wide mode shows here.
	defer syscall.Umask(syscall.Umask(0))
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	const text = "starting claude\n  indented detail\n\nexit status 1\n"

	// Act
	if err := b.WriteProcessStderr(text); err != nil {
		t.Fatalf("WriteProcessStderr: %v", err)
	}

	// Assert
	if got := statMode(t, filepath.Join(b.Dir(), "process")); got != 0o700 {
		t.Errorf("process directory mode = %04o, want 0700", got)
	}
	path := processPath(b, "stderr.sanitized.log")
	if got := statMode(t, path); got != 0o600 {
		t.Errorf("%s mode = %04o, want 0600", path, got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if string(raw) != text {
		t.Errorf("stderr = %q, want the capture unchanged: %q", raw, text)
	}
}

func TestWriteProcessStderrRedactsSecretsThatSpanLines(t *testing.T) {
	// Arrange: a key block and an assignment, both of which only make sense to
	// the redactor if the whole capture is sanitized in one pass.
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	const body = "c3ludGhldGljLXJzYS1rZXktYm9keQ==\nMIISYNTHETICKEYBODYSECONDLINE\n"
	const text = "loading key\n" +
		"-----BEGIN RSA PRIVATE KEY-----\n" + body + "-----END RSA PRIVATE KEY-----\n" +
		"AWS_SECRET_ACCESS_KEY=synthetic-secret-bbbbbbbb\ndone\n"

	// Act
	if err := b.WriteProcessStderr(text); err != nil {
		t.Fatalf("WriteProcessStderr: %v", err)
	}

	// Assert: no line of the key survives, in whole or in part, and the
	// ordinary output around it is untouched.
	raw, err := os.ReadFile(processPath(b, "stderr.sanitized.log"))
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	got := string(raw)
	for _, leak := range []string{
		"c3ludGhldGljLXJzYS1rZXktYm9keQ==",
		"MIISYNTHETICKEYBODYSECONDLINE",
		"BEGIN RSA PRIVATE KEY",
		"synthetic-secret-bbbbbbbb",
	} {
		if strings.Contains(got, leak) {
			t.Errorf("sanitized stderr leaked %q:\n%s", leak, got)
		}
	}
	if !strings.HasPrefix(got, "loading key\n") || !strings.HasSuffix(got, "\ndone\n") {
		t.Errorf("sanitized stderr = %q, want the ordinary lines and newlines preserved", got)
	}
	if !strings.Contains(got, "\nAWS_SECRET_ACCESS_KEY=[REDACTED:") {
		t.Errorf("sanitized stderr = %q, want the assignment name kept and its value replaced", got)
	}
	if n := len(markerPattern.FindAllString(got, -1)); n != 2 {
		t.Errorf("sanitized stderr holds %d markers, want 2 (the key block and the assignment)", n)
	}
}

func TestWriteProcessResultInstallsOneSanitizedObject(t *testing.T) {
	// Arrange
	defer syscall.Umask(syscall.Umask(0))
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	raw := []byte(`{"type":"result","is_error":false,"api_key":"synthetic-token-aaaaaaaa","usage":{"input_tokens":12}}`)

	// Act
	if err := b.WriteProcessResult(raw); err != nil {
		t.Fatalf("WriteProcessResult: %v", err)
	}

	// Assert
	path := processPath(b, "result.json")
	if got := statMode(t, path); got != 0o600 {
		t.Errorf("%s mode = %04o, want 0600", path, got)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if strings.Contains(string(stored), "synthetic-token-aaaaaaaa") {
		t.Errorf("result.json leaked the key")
	}
	var got map[string]any
	if err := json.Unmarshal(stored, &got); err != nil {
		t.Fatalf("result.json is not one JSON object: %v", err)
	}
	if got["type"] != "result" || got["is_error"] != false {
		t.Errorf("result.json = %v, want the document's own fields preserved", got)
	}
	if key, _ := got["api_key"].(string); !markerPattern.MatchString(key) {
		t.Errorf("api_key = %v, want a marker", got["api_key"])
	}
	// The install is atomic, so nothing of the temporary file survives it.
	entries, err := os.ReadDir(filepath.Join(b.Dir(), "process"))
	if err != nil {
		t.Fatalf("read process directory: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("process directory holds %d entries, want only result.json", len(entries))
	}
}

func TestWriteProcessResultFailsClosedAndLeavesTheRunWritable(t *testing.T) {
	// Arrange
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	path := processPath(b, "result.json")

	// Act & Assert: a result this package cannot parse as one object is one it
	// cannot vouch for, so nothing about it reaches the bundle.
	for _, bad := range []string{
		`[{"type":"result"}]`,
		`"result"`,
		`42`,
		`null`,
		`{"type":"result"`,
		`{"a":1} {"b":2}`,
		``,
	} {
		if err := b.WriteProcessResult([]byte(bad)); err == nil {
			t.Errorf("WriteProcessResult(%q) succeeded, want error", bad)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("rejected result %q reached disk", bad)
		}
	}

	// Rejected input is not a storage failure: the run still has its evidence,
	// so it stays open.
	if err := b.WriteProcessResult([]byte(`{"type":"result"}`)); err != nil {
		t.Errorf("WriteProcessResult after rejected input: %v", err)
	}
}

// Each process artifact gets its own bundle below, because the first refusal
// poisons the bundle: sharing one would leave the second artifact's own guard
// untested behind the stored failure.
func TestProcessArtifactsAreWrittenOnce(t *testing.T) {
	for name, tc := range map[string]struct {
		file   string
		first  func(*Bundle) error
		second func(*Bundle) error
		kept   string
	}{
		"stderr": {
			file:   "stderr.sanitized.log",
			first:  func(b *Bundle) error { return b.WriteProcessStderr("first capture\n") },
			second: func(b *Bundle) error { return b.WriteProcessStderr("second capture\n") },
			kept:   "first capture",
		},
		"result": {
			file:   "result.json",
			first:  func(b *Bundle) error { return b.WriteProcessResult([]byte(`{"type":"result","attempt":1}`)) },
			second: func(b *Bundle) error { return b.WriteProcessResult([]byte(`{"type":"result","attempt":2}`)) },
			kept:   `"attempt":1`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			// Arrange
			b, err := Create(t.TempDir(), "run-1", testManifest())
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if err := tc.first(b); err != nil {
				t.Fatalf("first write: %v", err)
			}

			// Act: the process ended once, so a second call is a mistake that
			// would destroy what the first one recorded.
			second := tc.second(b)

			// Assert
			if second == nil {
				t.Error("a second write succeeded, want error")
			}
			got, err := os.ReadFile(processPath(b, tc.file))
			if err != nil {
				t.Fatalf("read %s: %v", tc.file, err)
			}
			if !strings.Contains(string(got), tc.kept) {
				t.Errorf("%s = %q, want what the first write stored", tc.file, got)
			}
		})
	}
}

func TestProcessWritesAfterFinalizeFail(t *testing.T) {
	// Arrange
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := b.Finalize(Finalization{EndedAt: time.Date(2026, 7, 27, 10, 1, 0, 0, time.UTC), ExitReason: "exit:0"}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// Act & Assert
	writes := map[string]error{
		"WriteProcessStderr": b.WriteProcessStderr("late capture\n"),
		"WriteProcessResult": b.WriteProcessResult([]byte(`{"type":"result"}`)),
	}
	for name, err := range writes {
		if !errors.Is(err, ErrFinalized) {
			t.Errorf("%s after Finalize returned %v, want ErrFinalized", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(b.Dir(), "process")); !os.IsNotExist(err) {
		t.Errorf("a post-finalize process write created the process directory")
	}
}

func TestAFailedProcessWritePoisonsTheBundleAndRemovesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	// Arrange: a process directory that already holds evidence and can no
	// longer be written into, the way a full or read-only filesystem fails.
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	dir := filepath.Join(b.Dir(), "process")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("pre-create process directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("earlier evidence"), 0o600); err != nil {
		t.Fatalf("pre-create process artifact: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("seal process directory: %v", err)
	}
	defer os.Chmod(dir, 0o700)

	// Act
	first := b.WriteProcessStderr("capture\n")

	// Assert: the failure is reported, then remembered, and what was already
	// in the process directory is still there.
	if first == nil {
		t.Fatal("WriteProcessStderr into a sealed directory returned nil, want an error")
	}
	later := b.WriteAction(action.Action{ID: "a1", Type: action.TypeFileRead, Assurance: action.AssuranceProviderReported})
	if !errors.Is(later, first) {
		t.Errorf("WriteAction after a failed process write returned %v, want the stored failure %v", later, first)
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.txt")); err != nil {
		t.Errorf("a failed process write removed what the directory already held: %v", err)
	}
}

func TestProcessArtifactsNeverFollowASymlink(t *testing.T) {
	// Arrange: somewhere outside the bundle for a symlink to point at.
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "target.log")
	if err := os.WriteFile(outsideFile, []byte("untouched\n"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	// Act & Assert: every entry a process write opens is refused when it is a
	// symlink or is not the kind of thing it should be. Each writer gets its
	// own bundle, since the first refusal poisons the one it was made against.
	for _, tc := range []struct {
		name    string
		planted string
		plant   func(t *testing.T, b *Bundle)
	}{
		{"process is a symlink", "process", func(t *testing.T, b *Bundle) {
			if err := os.Symlink(outside, filepath.Join(b.Dir(), "process")); err != nil {
				t.Fatalf("plant symlink: %v", err)
			}
		}},
		{"process is a regular file", "process", func(t *testing.T, b *Bundle) {
			if err := os.WriteFile(filepath.Join(b.Dir(), "process"), nil, 0o600); err != nil {
				t.Fatalf("plant file: %v", err)
			}
		}},
		{"stderr is a symlink", filepath.Join("process", "stderr.sanitized.log"), plantArtifactSymlink(outsideFile, "stderr.sanitized.log")},
		{"result is a symlink", filepath.Join("process", "result.json"), plantArtifactSymlink(outsideFile, "result.json")},
	} {
		for name, write := range map[string]func(*Bundle) error{
			"WriteProcessStderr": func(b *Bundle) error { return b.WriteProcessStderr("capture\n") },
			"WriteProcessResult": func(b *Bundle) error { return b.WriteProcessResult([]byte(`{"type":"result"}`)) },
		} {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				b, err := Create(t.TempDir(), "run-1", testManifest())
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				tc.plant(t, b)
				planted, err := os.Lstat(filepath.Join(b.Dir(), tc.planted))
				if err != nil {
					t.Fatalf("stat planted entry: %v", err)
				}

				// A write that names the planted entry has to be refused; one
				// that names the other artifact may be refused too, but must
				// never reach through the plant.
				_ = write(b)

				if strings.HasSuffix(tc.planted, filepath.Base(tc.planted)) {
					after, err := os.Lstat(filepath.Join(b.Dir(), tc.planted))
					if err != nil {
						t.Fatalf("stat planted entry after the write: %v", err)
					}
					if after.Mode().Type() != planted.Mode().Type() {
						t.Errorf("%s changed from %s to %s, so the write replaced what was already there",
							tc.planted, planted.Mode().Type(), after.Mode().Type())
					}
				}
				got, err := os.ReadFile(outsideFile)
				if err != nil {
					t.Fatalf("read outside file: %v", err)
				}
				if string(got) != "untouched\n" {
					t.Errorf("the write followed the symlink and wrote %q outside the bundle", got)
				}
				entries, err := os.ReadDir(outside)
				if err != nil {
					t.Fatalf("read outside directory: %v", err)
				}
				if len(entries) != 1 {
					t.Errorf("the write left %d entries outside the bundle, want only the planted target", len(entries))
				}
			})
		}
	}
}

// plantArtifactSymlink puts a real process directory in the bundle with one
// artifact name already taken by a symlink out of it.
func plantArtifactSymlink(target, name string) func(*testing.T, *Bundle) {
	return func(t *testing.T, b *Bundle) {
		t.Helper()
		dir := filepath.Join(b.Dir(), "process")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("plant directory: %v", err)
		}
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatalf("plant symlink: %v", err)
		}
	}
}

func TestProcessArtifactsUseAnExistingProcessDirectory(t *testing.T) {
	// Arrange: a real directory is the one thing that may already be there.
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.Mkdir(filepath.Join(b.Dir(), "process"), 0o700); err != nil {
		t.Fatalf("pre-create process directory: %v", err)
	}

	// Act & Assert
	if err := b.WriteProcessStderr("capture\n"); err != nil {
		t.Errorf("WriteProcessStderr into an existing process directory: %v", err)
	}
	if err := b.WriteProcessResult([]byte(`{"type":"result"}`)); err != nil {
		t.Errorf("WriteProcessResult into an existing process directory: %v", err)
	}
}

func TestCreateUsesExactPermissionsUnderPermissiveUmask(t *testing.T) {
	// Arrange: a permissive umask masks nothing, so any mode the bundle asks
	// for lands as-is and a too-wide request shows up here.
	defer syscall.Umask(syscall.Umask(0))
	root := filepath.Join(t.TempDir(), "runs")

	// Act
	b, err := Create(root, "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Assert
	if got := statMode(t, root); got != 0o700 {
		t.Errorf("root mode = %04o, want 0700", got)
	}
	if got := statMode(t, b.Dir()); got != 0o700 {
		t.Errorf("run directory mode = %04o, want 0700", got)
	}
	for _, name := range []string{"manifest.json", "actions.jsonl", "provider-events.sanitized.jsonl"} {
		if got := statMode(t, filepath.Join(b.Dir(), name)); got != 0o600 {
			t.Errorf("%s mode = %04o, want 0600", name, got)
		}
	}
}

// Repository evidence is measured after the run has been finalized, so
// sanitizing it is the one thing a finished bundle still does. A secret the
// prompt and the actions already named is the same secret when the evidence
// carries it, and it has to read as one secret across the whole bundle.
func TestSanitizeTextReusesTheRunsMarkerAfterFinalize(t *testing.T) {
	// Arrange: one synthetic token, recorded first by the routes a run writes
	// while it is open.
	const secret = "ghp_syntheticIIIIJJJJKKKKLLLL"
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := b.WritePrompt("push the branch using " + secret); err != nil {
		t.Fatalf("WritePrompt: %v", err)
	}
	if err := b.WriteAction(action.Action{
		ID:        "a1",
		Type:      action.TypeToolCall,
		Assurance: action.AssuranceProviderReported,
		Input:     json.RawMessage(`{"github_token":"` + secret + `"}`),
	}); err != nil {
		t.Fatalf("WriteAction: %v", err)
	}
	if err := b.Finalize(Finalization{
		EndedAt:    time.Date(2026, 7, 27, 10, 1, 0, 0, time.UTC),
		ExitReason: "completed",
	}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// Act: what a patch collected from the repository afterwards would carry.
	safe, err := b.SanitizeText("+token = " + secret + "\n+kept = plain\n")
	if err != nil {
		t.Fatalf("SanitizeText: %v", err)
	}

	// Assert
	if strings.Contains(safe, secret) {
		t.Errorf("SanitizeText = %q, want the secret replaced", safe)
	}
	if !strings.Contains(safe, "+kept = plain") {
		t.Errorf("SanitizeText = %q, want the surrounding text preserved", safe)
	}
	recorded, err := os.ReadFile(filepath.Join(b.Dir(), "prompt.txt"))
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	want := markerPattern.FindAllString(string(recorded), -1)
	if len(want) != 1 {
		t.Fatalf("prompt.txt holds %d markers, want 1", len(want))
	}
	if got := markerPattern.FindAllString(safe, -1); len(got) != 1 || got[0] != want[0] {
		t.Errorf("SanitizeText markers = %v, want the prompt's %q", got, want[0])
	}
}

// Text that is not valid UTF-8 is refused rather than sanitized. The redactor
// reads JSON, and a JSON encoder replaces every invalid byte with U+FFFD: a
// caller handed that back would store a mangled copy of what it collected while
// believing it had stored what it read.
func TestSanitizeTextRefusesTextThatIsNotUTF8(t *testing.T) {
	b, err := Create(t.TempDir(), "run-1", testManifest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	safe, err := b.SanitizeText("caf\xe9 cr\xe8me\n")

	if err == nil {
		t.Fatalf("SanitizeText = %q, want it refused", safe)
	}
	if safe != "" {
		t.Errorf("SanitizeText = %q, want nothing alongside the refusal", safe)
	}
	if strings.Contains(err.Error(), "�") {
		t.Errorf("SanitizeText error = %v, want it not to carry a coerced copy", err)
	}
	if valid, err := b.SanitizeText("plain text\n"); err != nil || valid != "plain text\n" {
		t.Errorf("SanitizeText(%q) = %q, %v, want valid text still sanitized", "plain text\n", valid, err)
	}
}
