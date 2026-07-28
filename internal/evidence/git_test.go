package evidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// runGit runs one Git command in dir, isolated from the operator's own
// configuration so a machine's global settings cannot decide what these tests
// observe.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := gitOut(t, dir, args...); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"LC_ALL=C",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimRight(string(out), "\n"), err
}

// gitRepo creates a temporary repository holding one commit — the baseline a
// run starts from — and returns its root as the filesystem finally resolves it.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	runGit(t, real, "init")
	runGit(t, real, "config", "user.email", "test@example.com")
	runGit(t, real, "config", "user.name", "agentrec test")
	write(t, real, "README.md", "hello\n")
	write(t, real, "b.txt", "b0\n")
	write(t, real, "c.txt", "c0\n")
	runGit(t, real, "add", ".")
	runGit(t, real, "commit", "-m", "initial")
	return real
}

// runDir is a fresh, private run directory, which is what storage hands the
// capture.
func runDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	return real
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	writeBytes(t, dir, name, []byte(content))
}

func writeBytes(t *testing.T, dir, name string, content []byte) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create parent of %s: %v", name, err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// start begins a capture with the package defaults, which is how every caller
// but the limit tests uses it.
func start(t *testing.T, repo, run string) *Capture {
	t.Helper()
	return startWith(t, repo, run, Options{})
}

func startWith(t *testing.T, repo, run string, opts Options) *Capture {
	t.Helper()
	c, err := Start(context.Background(), repo, "run-1", run, opts)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

func finalize(t *testing.T, c *Capture) Result {
	t.Helper()
	res, err := c.Finalize(context.Background())
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	return res
}

func readJSON[T any](t *testing.T, path string) T {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.HasSuffix(raw, []byte("\n")) {
		t.Errorf("%s does not end with a newline", path)
	}
	var doc T
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return doc
}

func gitDirOf(run string) string { return filepath.Join(run, "git") }

// assertAbsent proves a string appears nowhere under root — in no file's
// content and in no file's name. It is how a test shows that what was
// sanitized, or never read at all, did not reach the evidence by another path.
func assertAbsent(t *testing.T, root, needle string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(d.Name(), needle) {
			t.Errorf("%s: name contains %q", path, needle)
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(body, []byte(needle)) {
			t.Errorf("%s: content contains %q", path, needle)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

func refName(runID string) string { return refPrefix + runID }

// 1. Commits, staged and unstaged tracked changes all reach the patch.

func TestFinalizeCapturesCommitsAndStagedAndUnstagedChanges(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	c := start(t, repo, run)

	write(t, repo, "a.txt", "a1\n")
	runGit(t, repo, "add", "a.txt")
	runGit(t, repo, "commit", "-m", "add a")
	write(t, repo, "a.txt", "a1\na2\n")
	runGit(t, repo, "commit", "-am", "extend a")
	write(t, repo, "b.txt", "b0\nb1\n")
	runGit(t, repo, "add", "b.txt")
	write(t, repo, "c.txt", "c0\nc1\n")

	res := finalize(t, c)

	if res.Status != statusAvailable || res.Reason != "" {
		t.Fatalf("status = %q reason = %q, want %q and no reason", res.Status, res.Reason, statusAvailable)
	}
	if res.Attribution != "observed during run, not causal proof" {
		t.Errorf("attribution = %q", res.Attribution)
	}
	if res.TrackedFiles != 3 || res.Added != 4 || res.Deleted != 0 {
		t.Errorf("tracked = %d files +%d/-%d, want 3 files +4/-0", res.TrackedFiles, res.Added, res.Deleted)
	}

	patch, err := os.ReadFile(filepath.Join(gitDirOf(run), patchFile))
	if err != nil {
		t.Fatalf("read patch: %v", err)
	}
	for _, want := range []string{"a.txt", "b.txt", "c.txt", "+a1", "+a2", "+b1", "+c1"} {
		if !bytes.Contains(patch, []byte(want)) {
			t.Errorf("patch does not mention %q:\n%s", want, patch)
		}
	}

	stat := readJSON[trackedStatDoc](t, filepath.Join(gitDirOf(run), trackedStatFile))
	if stat.Status != statusAvailable {
		t.Fatalf("stat status = %q", stat.Status)
	}
	if len(stat.Files) != 3 {
		t.Fatalf("stat files = %+v", stat.Files)
	}
	want := map[string][2]int{"a.txt": {2, 0}, "b.txt": {1, 0}, "c.txt": {1, 0}}
	for _, f := range stat.Files {
		w, ok := want[f.Path]
		if !ok {
			t.Errorf("unexpected file %q", f.Path)
			continue
		}
		if f.Additions == nil || f.Deletions == nil {
			t.Errorf("%s: counts missing", f.Path)
			continue
		}
		if *f.Additions != w[0] || *f.Deletions != w[1] {
			t.Errorf("%s: +%d/-%d, want +%d/-%d", f.Path, *f.Additions, *f.Deletions, w[0], w[1])
		}
	}
	baseline := readJSON[baselineDoc](t, filepath.Join(gitDirOf(run), baselineFile))
	if baseline.Status != statusAvailable || baseline.Commit == "" || baseline.Ref != refName("run-1") {
		t.Errorf("baseline = %+v", baseline)
	}
	if res.Baseline != baseline.Commit {
		t.Errorf("result baseline = %q, want %q", res.Baseline, baseline.Commit)
	}
}

// 2. A new untracked text file is stored, sanitized, under a hashed name.

func TestFinalizeStoresUntrackedTextSanitizedUnderHashedName(t *testing.T) {
	const secret = "SECRET-TOKEN-abc123"
	repo, run := gitRepo(t), runDir(t)
	c := startWith(t, repo, run, Options{Sanitize: func(s string) (string, error) {
		return strings.ReplaceAll(s, secret, "[redacted]"), nil
	}})

	write(t, repo, "notes.txt", "token: "+secret+"\n")

	res := finalize(t, c)
	if res.UntrackedFiles != 1 || res.StoredTextFiles != 1 {
		t.Fatalf("untracked = %d, stored = %d, want 1 and 1", res.UntrackedFiles, res.StoredTextFiles)
	}

	doc := readJSON[untrackedDoc](t, filepath.Join(gitDirOf(run), untrackedFile))
	if len(doc.Files) != 1 {
		t.Fatalf("files = %+v", doc.Files)
	}
	f := doc.Files[0]
	if f.Path != "notes.txt" || f.Kind != kindFile || !f.Stored {
		t.Fatalf("entry = %+v", f)
	}
	// The hash covers the body that was stored, not the one that was read: a
	// hash of the plaintext would hand back the secret the sanitizer removed.
	if f.SHA256 != sum("token: [redacted]\n") || f.HashBasis != hashBasisSanitized {
		t.Errorf("entry = %+v, want the sanitized body hashed", f)
	}
	nameSum := sha256.Sum256([]byte("notes.txt"))
	wantName := hex.EncodeToString(nameSum[:]) + ".txt"
	if f.StoredAs != filepath.Join(untrackedDirName, wantName) {
		t.Errorf("storedAs = %q, want %q", f.StoredAs, filepath.Join(untrackedDirName, wantName))
	}

	body, err := os.ReadFile(filepath.Join(gitDirOf(run), untrackedDirName, wantName))
	if err != nil {
		t.Fatalf("read stored body: %v", err)
	}
	if string(body) != "token: [redacted]\n" {
		t.Errorf("stored body = %q", body)
	}
	assertAbsent(t, run, secret)
}

// 3. A binary file is described and hashed, and its body is never stored.

func TestFinalizeRecordsBinaryWithoutItsBody(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	c := start(t, repo, run)

	blob := []byte{0x89, 'P', 'N', 'G', 0x00, 0x01, 0x02, 0xff, 0xfe}
	writeBytes(t, repo, "image.png", blob)

	res := finalize(t, c)
	if res.UntrackedFiles != 1 || res.StoredTextFiles != 0 {
		t.Fatalf("untracked = %d, stored = %d, want 1 and 0", res.UntrackedFiles, res.StoredTextFiles)
	}

	doc := readJSON[untrackedDoc](t, filepath.Join(gitDirOf(run), untrackedFile))
	f := doc.Files[0]
	if f.SHA256 != sumBytes(blob) || f.HashBasis != hashBasisRawBinary {
		t.Errorf("entry = %+v, want the raw hash of a body that is never stored", f)
	}
	if f.Stored || f.Reason != reasonBinary || f.StoredAs != "" {
		t.Errorf("entry = %+v, want an unstored binary", f)
	}
	if f.Size != int64(len(blob)) {
		t.Errorf("size = %d, want %d", f.Size, len(blob))
	}
	if bodies := storedBodies(t, run); len(bodies) != 0 {
		t.Errorf("stored bodies = %v, want none", bodies)
	}
}

// storedBodies names what is on disk under the bodies directory, which exists
// from the start of every capture whether or not anything is filed in it.
func storedBodies(t *testing.T, run string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(gitDirOf(run), untrackedDirName))
	if err != nil {
		t.Fatalf("read stored bodies: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// storedByteTotal is what the bodies actually cost on disk, which is the number
// the aggregate limit is a promise about.
func storedByteTotal(t *testing.T, run string) int64 {
	t.Helper()
	var total int64
	for _, name := range storedBodies(t, run) {
		info, err := os.Stat(filepath.Join(gitDirOf(run), untrackedDirName, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		total += info.Size()
	}
	return total
}

// 4. A symlink is described but never followed, so what it points at stays out
// of the evidence.

func TestFinalizeDescribesSymlinkWithoutReadingItsTarget(t *testing.T) {
	const secret = "OUTSIDE-SECRET-xyz789"
	repo, run := gitRepo(t), runDir(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	c := start(t, repo, run)

	if err := os.Symlink(outside, filepath.Join(repo, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	res := finalize(t, c)
	if res.UntrackedFiles != 1 || res.StoredTextFiles != 0 {
		t.Fatalf("untracked = %d, stored = %d, want 1 and 0", res.UntrackedFiles, res.StoredTextFiles)
	}

	doc := readJSON[untrackedDoc](t, filepath.Join(gitDirOf(run), untrackedFile))
	f := doc.Files[0]
	if f.Path != "link.txt" || f.Kind != kindSymlink {
		t.Fatalf("entry = %+v", f)
	}
	if f.Stored || f.SHA256 != "" || f.StoredAs != "" {
		t.Errorf("entry = %+v, want metadata only", f)
	}
	assertAbsent(t, run, secret)
	assertAbsent(t, run, outside)
}

// 5. An ignored file is not evidence of anything the run did.

func TestFinalizeExcludesIgnoredFiles(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	write(t, repo, ".gitignore", "build/\n*.log\n")
	runGit(t, repo, "add", ".gitignore")
	runGit(t, repo, "commit", "-m", "ignore build")
	c := start(t, repo, run)

	write(t, repo, "build/artifact.bin", "ignored\n")
	write(t, repo, "run.log", "ignored\n")
	write(t, repo, "kept.txt", "kept\n")

	res := finalize(t, c)
	if res.UntrackedFiles != 1 {
		t.Fatalf("untracked = %d, want 1", res.UntrackedFiles)
	}
	doc := readJSON[untrackedDoc](t, filepath.Join(gitDirOf(run), untrackedFile))
	if doc.Files[0].Path != "kept.txt" {
		t.Errorf("files = %+v, want only kept.txt", doc.Files)
	}
}

// 6. A baseline that no longer exists is reported as unreachable, never
// guessed at.

func TestFinalizeReportsAnUnreachableBaselineRatherThanGuessing(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	branch, err := gitOut(t, repo, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		t.Fatalf("read branch: %v", err)
	}
	c := start(t, repo, run)
	baseline := c.Baseline()

	// The ref this capture planted is removed and the baseline commit is made
	// unreachable and pruned, which is a history rewrite landing mid-run.
	runGit(t, repo, "update-ref", "-d", refName("run-1"))
	runGit(t, repo, "checkout", "--orphan", "fresh")
	runGit(t, repo, "commit", "-m", "unrelated root")
	runGit(t, repo, "branch", "-D", branch)
	runGit(t, repo, "reflog", "expire", "--expire=now", "--all")
	runGit(t, repo, "gc", "--prune=now", "--quiet")
	if _, err := gitOut(t, repo, "cat-file", "-e", baseline+"^{commit}"); err == nil {
		t.Skip("baseline object survived pruning; nothing to observe")
	}
	write(t, repo, "left-behind.txt", "still evidence\n")

	res := finalize(t, c)
	if res.Status != statusUnavailable || res.Reason != reasonBaselineUnreachable {
		t.Fatalf("status = %q reason = %q, want %q/%q", res.Status, res.Reason, statusUnavailable, reasonBaselineUnreachable)
	}
	if res.Baseline != "" || res.TrackedFiles != 0 {
		t.Errorf("result = %+v, want no tracked claim", res)
	}
	if _, err := os.Stat(filepath.Join(gitDirOf(run), patchFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a patch was written without a baseline: %v", err)
	}
	stat := readJSON[trackedStatDoc](t, filepath.Join(gitDirOf(run), trackedStatFile))
	if stat.Status != statusUnavailable || stat.Reason != reasonBaselineUnreachable || len(stat.Files) != 0 {
		t.Errorf("stat = %+v", stat)
	}
	// The pre-run clean gate established there were no untracked files, so the
	// ones found now are still the run's own.
	if res.UntrackedFiles != 1 {
		t.Errorf("untracked = %d, want 1", res.UntrackedFiles)
	}
}

// 7. The temporary ref exists only for the length of the capture.

func TestTemporaryRefExistsOnlyBetweenStartAndClose(t *testing.T) {
	t.Run("removed after a successful finalize", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		c := start(t, repo, run)
		head, err := gitOut(t, repo, "rev-parse", "HEAD")
		if err != nil {
			t.Fatalf("rev-parse HEAD: %v", err)
		}
		got, err := gitOut(t, repo, "rev-parse", "--verify", refName("run-1"))
		if err != nil {
			t.Fatalf("temp ref missing during the run: %v", got)
		}
		if got != head {
			t.Errorf("temp ref = %s, want HEAD %s", got, head)
		}

		finalize(t, c)
		if out, err := gitOut(t, repo, "rev-parse", "--verify", refName("run-1")); err == nil {
			t.Errorf("temp ref survived finalize: %s", out)
		}
	})

	t.Run("removed after close without finalize", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		c := start(t, repo, run)
		if err := c.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if out, err := gitOut(t, repo, "rev-parse", "--verify", refName("run-1")); err == nil {
			t.Errorf("temp ref survived close: %s", out)
		}
		if err := c.Close(context.Background()); err != nil {
			t.Errorf("second Close: %v", err)
		}
	})

	t.Run("a second finalize is refused and close stays safe", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		c := start(t, repo, run)
		finalize(t, c)
		if _, err := c.Finalize(context.Background()); err == nil {
			t.Fatal("second Finalize succeeded")
		} else if !strings.Contains(err.Error(), "already finalized") {
			t.Errorf("second Finalize: %v", err)
		}
		if err := c.Close(context.Background()); err != nil {
			t.Errorf("Close after Finalize: %v", err)
		}
	})

	t.Run("an existing ref is a collision, not something to overwrite", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		runGit(t, repo, "update-ref", refName("run-1"), "HEAD")
		before, err := gitOut(t, repo, "rev-parse", refName("run-1"))
		if err != nil {
			t.Fatalf("rev-parse: %v", err)
		}

		c, err := Start(context.Background(), repo, "run-1", run, Options{})
		if err == nil {
			c.Close(context.Background())
			t.Fatal("Start reused an existing ref")
		}
		after, err := gitOut(t, repo, "rev-parse", refName("run-1"))
		if err != nil || after != before {
			t.Errorf("existing ref changed: %q -> %q (%v)", before, after, err)
		}
	})
}

// 8. Every stored byte is bounded.

func TestFinalizeRespectsSizeLimits(t *testing.T) {
	t.Run("a patch over the limit is refused whole", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		c := startWith(t, repo, run, Options{MaxPatchBytes: 64})

		write(t, repo, "big.txt", strings.Repeat("line of tracked change\n", 200))
		runGit(t, repo, "add", "big.txt")

		res := finalize(t, c)
		if res.Status != statusUnavailable || res.Reason != reasonPatchTooLarge {
			t.Fatalf("status = %q reason = %q, want %q/%q", res.Status, res.Reason, statusUnavailable, reasonPatchTooLarge)
		}
		if _, err := os.Stat(filepath.Join(gitDirOf(run), patchFile)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a partial patch was written: %v", err)
		}
		stat := readJSON[trackedStatDoc](t, filepath.Join(gitDirOf(run), trackedStatFile))
		if stat.Status != statusUnavailable || stat.Reason != reasonPatchTooLarge {
			t.Errorf("stat = %+v", stat)
		}
	})

	t.Run("a text file over the per-file limit is described, not stored", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		c := startWith(t, repo, run, Options{MaxTextFileBytes: 16})

		body := strings.Repeat("x", 100) + "\n"
		write(t, repo, "long.txt", body)

		res := finalize(t, c)
		if res.StoredTextFiles != 0 {
			t.Fatalf("stored = %d, want 0", res.StoredTextFiles)
		}
		doc := readJSON[untrackedDoc](t, filepath.Join(gitDirOf(run), untrackedFile))
		f := doc.Files[0]
		if f.Stored || f.Reason != reasonFileTooLarge {
			t.Errorf("entry = %+v", f)
		}
		// Nothing sanitized this file, so there is no hash to record that would
		// not be a hash of whatever it holds.
		if f.SHA256 != "" || f.HashBasis != "" {
			t.Errorf("entry = %+v, want no hash of text that was never sanitized", f)
		}
		assertAbsent(t, run, sum(body))
	})

	t.Run("the aggregate limit stops storing but not describing", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		c := startWith(t, repo, run, Options{MaxStoredTextBytes: 12})

		write(t, repo, "new-a.txt", "0123456789\n")
		write(t, repo, "new-b.txt", "0123456789\n")

		res := finalize(t, c)
		if res.UntrackedFiles != 2 || res.StoredTextFiles != 1 {
			t.Fatalf("untracked = %d stored = %d, want 2 and 1", res.UntrackedFiles, res.StoredTextFiles)
		}
		doc := readJSON[untrackedDoc](t, filepath.Join(gitDirOf(run), untrackedFile))
		if !doc.Files[0].Stored {
			t.Errorf("first file not stored: %+v", doc.Files[0])
		}
		if doc.Files[1].Stored || doc.Files[1].Reason != reasonStorageLimit {
			t.Errorf("second file = %+v, want it described but unstored", doc.Files[1])
		}
	})
}

// 9. Names Git can produce, including ones holding newlines, survive intact and
// name nothing outside the evidence directory.

func TestFinalizeHandlesNamesThatWouldBreakLineParsing(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	c := start(t, repo, run)

	names := []string{"plain.txt", "with\nnewline.txt", "with\ttab.txt", "유니코드.txt", "dir/nested file.txt"}
	for _, n := range names {
		write(t, repo, n, "content of "+strconv.Quote(n)+"\n")
	}

	res := finalize(t, c)
	if res.UntrackedFiles != len(names) {
		t.Fatalf("untracked = %d, want %d", res.UntrackedFiles, len(names))
	}

	doc := readJSON[untrackedDoc](t, filepath.Join(gitDirOf(run), untrackedFile))
	got := make(map[string]untrackedEntry, len(doc.Files))
	for _, f := range doc.Files {
		got[f.Path] = f
	}
	for _, n := range names {
		f, ok := got[n]
		if !ok {
			t.Errorf("missing %q from %+v", n, doc.Files)
			continue
		}
		if !f.Stored {
			t.Errorf("%q not stored: %+v", n, f)
			continue
		}
		name := filepath.Base(f.StoredAs)
		if f.StoredAs != filepath.Join(untrackedDirName, name) {
			t.Errorf("%q stored as %q, outside the evidence directory", n, f.StoredAs)
		}
		if _, err := hex.DecodeString(strings.TrimSuffix(name, ".txt")); err != nil {
			t.Errorf("%q stored under %q, which is not a hash", n, name)
		}
	}

	entries, err := os.ReadDir(filepath.Join(gitDirOf(run), untrackedDirName))
	if err != nil {
		t.Fatalf("read stored bodies: %v", err)
	}
	if len(entries) != len(names) {
		t.Errorf("stored %d bodies, want %d", len(entries), len(names))
	}
}

// 10. The evidence outlives the repository it came from.

func TestArtifactsRemainReadableAfterTheRepositoryIsDeleted(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	c := start(t, repo, run)

	write(t, repo, "c.txt", "c0\nchanged\n")
	write(t, repo, "new.txt", "brand new\n")
	res := finalize(t, c)

	if err := os.RemoveAll(repo); err != nil {
		t.Fatalf("delete repository: %v", err)
	}

	for _, name := range []string{baselineFile, patchFile, trackedStatFile, untrackedFile} {
		if _, err := os.ReadFile(filepath.Join(gitDirOf(run), name)); err != nil {
			t.Errorf("read %s after deletion: %v", name, err)
		}
	}
	doc := readJSON[untrackedDoc](t, filepath.Join(gitDirOf(run), untrackedFile))
	if len(doc.Files) != 1 || !doc.Files[0].Stored {
		t.Fatalf("untracked = %+v", doc.Files)
	}
	body, err := os.ReadFile(filepath.Join(gitDirOf(run), doc.Files[0].StoredAs))
	if err != nil {
		t.Fatalf("read stored body after deletion: %v", err)
	}
	if string(body) != "brand new\n" {
		t.Errorf("stored body = %q", body)
	}
	if res.Status != statusAvailable {
		t.Errorf("status = %q", res.Status)
	}
}

// 11. Modes, refusals and the promise that the repository is only read.

func TestEvidenceIsPrivateToTheOperator(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	c := start(t, repo, run)
	write(t, repo, "new.txt", "stored\n")
	finalize(t, c)

	dirs := []string{gitDirOf(run), filepath.Join(gitDirOf(run), untrackedDirName)}
	for _, d := range dirs {
		info, err := os.Lstat(d)
		if err != nil {
			t.Fatalf("stat %s: %v", d, err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %v, want 0700", d, info.Mode().Perm())
		}
	}
	err := filepath.WalkDir(gitDirOf(run), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", path, info.Mode().Perm())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

func TestStartRefusesArgumentsItCannotTrust(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	cases := []struct {
		name           string
		repo, id, dest string
	}{
		{"relative repository", "relative/path", "run-1", run},
		{"missing repository", filepath.Join(repo, "absent"), "run-1", run},
		{"relative run directory", repo, "run-1", "relative/path"},
		{"missing run directory", repo, "run-1", filepath.Join(run, "absent")},
		{"empty run id", repo, "", run},
		{"run id with a separator", repo, "a/b", run},
		{"run id that is a dot component", repo, "..", run},
		{"run id with a control character", repo, "run\n1", run},
		{"run id git will not accept", repo, "run.lock", run},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Start(context.Background(), tc.repo, tc.id, tc.dest, Options{})
			if err == nil {
				c.Close(context.Background())
				t.Fatal("Start accepted it")
			}
		})
	}
}

func TestStartRefusesADirectoryItDidNotCreate(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	if err := os.Mkdir(gitDirOf(run), 0o700); err != nil {
		t.Fatalf("plant directory: %v", err)
	}
	if c, err := Start(context.Background(), repo, "run-1", run, Options{}); err == nil {
		c.Close(context.Background())
		t.Fatal("Start reused an existing evidence directory")
	}
}

func TestCaptureOnlyReadsTheRepository(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	c := start(t, repo, run)
	write(t, repo, "c.txt", "c0\nchanged\n")
	write(t, repo, "new.txt", "new\n")

	before := snapshot(t, repo)
	finalize(t, c)
	after := snapshot(t, repo)

	for path, sum := range before {
		if after[path] != sum {
			t.Errorf("%s changed during finalize", path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("%s appeared during finalize", path)
		}
	}
	if status, err := gitOut(t, repo, "status", "--porcelain=v1"); err != nil || status == "" {
		t.Errorf("worktree state changed: %q (%v)", status, err)
	}
	if out, err := gitOut(t, repo, "rev-parse", "--verify", refName("run-1")); err == nil {
		t.Errorf("temp ref survived finalize: %s", out)
	}
}

// snapshot digests every file in the repository, including Git's own, so that a
// capture that wrote anything beyond its temporary ref is visible. The
// temporary ref itself is excluded and checked separately: it is the one thing
// a capture is allowed to write, and its removal is what the caller asserts.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	sums := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		// Git rewrites these as a side effect of being asked questions, and
		// they describe no evidence.
		switch filepath.Base(path) {
		case "index", "packed-refs", "FETCH_HEAD":
			return nil
		}
		if strings.HasPrefix(rel, filepath.Join(".git", "refs", "agentrec")+string(filepath.Separator)) {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		sums[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", dir, err)
	}
	return sums
}

func TestParseNumstatRefusesMalformedRecords(t *testing.T) {
	t.Run("accepts what git emits", func(t *testing.T) {
		files, err := parseNumstat([]byte("2\t1\ta.txt\x00-\t-\timage.png\x000\t3\twith\nnewline.txt\x00"))
		if err != nil {
			t.Fatalf("parseNumstat: %v", err)
		}
		if len(files) != 3 {
			t.Fatalf("files = %+v", files)
		}
		if files[0].Additions == nil || *files[0].Additions != 2 || *files[0].Deletions != 1 {
			t.Errorf("first = %+v", files[0])
		}
		if !files[1].Binary || files[1].Additions != nil || files[1].Deletions != nil {
			t.Errorf("second = %+v, want a binary with no counts", files[1])
		}
		if files[2].Path != "with\nnewline.txt" {
			t.Errorf("third path = %q", files[2].Path)
		}
	})

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"missing path", "1\t2\t\x00"},
		{"missing a field", "1\ta.txt\x00"},
		{"non-numeric count", "x\t2\ta.txt\x00"},
		{"half binary", "-\t2\ta.txt\x00"},
		{"unterminated record", "1\t2\ta.txt"},
		{"negative count", "-1\t2\ta.txt\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if files, err := parseNumstat([]byte(tc.raw)); err == nil {
				t.Fatalf("accepted %q as %+v", tc.raw, files)
			}
		})
	}
}

// 12. Cleaning up the ref is not the run's to cancel, and never reports a
// success it did not observe.

func TestCloseRemovesTheRefEvenWhenTheRunWasCancelled(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	c, err := Start(ctx, repo, "run-1", run, Options{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	cancel()

	if err := c.Close(ctx); err != nil {
		t.Fatalf("Close after cancellation: %v", err)
	}
	if out, err := gitOut(t, repo, "rev-parse", "--verify", refName("run-1")); err == nil {
		t.Errorf("temp ref survived a cancelled run: %s", out)
	}
}

func TestCloseReportsARefItCouldNotProveGone(t *testing.T) {
	t.Run("a ref moved out from under the capture", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		c := start(t, repo, run)

		write(t, repo, "moved.txt", "moved\n")
		runGit(t, repo, "add", "moved.txt")
		runGit(t, repo, "commit", "-m", "second")
		runGit(t, repo, "update-ref", refName("run-1"), "HEAD")

		err := c.Close(context.Background())
		if err == nil {
			t.Fatal("Close claimed to have removed a ref it did not")
		}
		if out, gerr := gitOut(t, repo, "rev-parse", "--verify", refName("run-1")); gerr != nil {
			t.Errorf("the operator's ref was removed anyway: %v", out)
		}
		if again := c.Close(context.Background()); again == nil || again.Error() != err.Error() {
			t.Errorf("second Close = %v, want the same failure", again)
		}
	})

	t.Run("a git that answers nothing", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		c := start(t, repo, run)

		// Git is replaced only after the capture has started, so what fails is
		// the cleanup: both the delete and the check of whether it worked.
		stub := t.TempDir()
		if err := os.WriteFile(filepath.Join(stub, "git"), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
			t.Fatalf("write git stand-in: %v", err)
		}
		t.Setenv("PATH", stub)

		if err := c.Close(context.Background()); err == nil {
			t.Fatal("Close reported success without being able to ask")
		}
	})
}

// 13. What sanitizing produced is what the limits are measured against.

func TestFinalizeBoundsWhatSanitizingProduced(t *testing.T) {
	// expand grows every marked byte, standing in for a sanitizer whose
	// replacement is longer than what it replaced.
	expand := func(s string) (string, error) { return strings.ReplaceAll(s, "Z", "ZZ"), nil }

	t.Run("a patch sanitizing grew past the limit is refused whole", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		pad := strings.Repeat("p", 4096)
		c := startWith(t, repo, run, Options{MaxPatchBytes: 2048, Sanitize: func(s string) (string, error) {
			if strings.Contains(s, "diff --git") {
				return s + pad, nil
			}
			return s, nil
		}})

		write(t, repo, "b.txt", "b0\nb1\n")

		res := finalize(t, c)
		if res.Status != statusUnavailable || res.Reason != reasonPatchTooLarge {
			t.Fatalf("status = %q reason = %q, want %q/%q", res.Status, res.Reason, statusUnavailable, reasonPatchTooLarge)
		}
		if _, err := os.Stat(filepath.Join(gitDirOf(run), patchFile)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("an oversized patch was written: %v", err)
		}
		stat := readJSON[trackedStatDoc](t, filepath.Join(gitDirOf(run), trackedStatFile))
		if stat.Status != statusUnavailable || stat.Reason != reasonPatchTooLarge {
			t.Errorf("stat = %+v", stat)
		}
	})

	t.Run("a body sanitizing grew past the per-file limit is not stored", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		c := startWith(t, repo, run, Options{MaxTextFileBytes: 10, Sanitize: expand})

		write(t, repo, "a.md", "ZZZZZ\n")

		res := finalize(t, c)
		if res.StoredTextFiles != 0 {
			t.Fatalf("stored = %d, want 0", res.StoredTextFiles)
		}
		doc := readJSON[untrackedDoc](t, filepath.Join(gitDirOf(run), untrackedFile))
		if f := doc.Files[0]; f.Stored || f.Reason != reasonFileTooLarge {
			t.Errorf("entry = %+v, want it described but unstored", f)
		}
		if total := storedByteTotal(t, run); total != 0 {
			t.Errorf("stored %d bytes, want 0", total)
		}
	})

	t.Run("the aggregate limit counts sanitized bytes", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		c := startWith(t, repo, run, Options{MaxStoredTextBytes: 12, Sanitize: expand})

		write(t, repo, "a.md", "ZZZ\n")
		write(t, repo, "b.md", "ZZZ\n")

		res := finalize(t, c)
		if res.UntrackedFiles != 2 || res.StoredTextFiles != 1 {
			t.Fatalf("untracked = %d stored = %d, want 2 and 1", res.UntrackedFiles, res.StoredTextFiles)
		}
		doc := readJSON[untrackedDoc](t, filepath.Join(gitDirOf(run), untrackedFile))
		if !doc.Files[0].Stored {
			t.Errorf("first file not stored: %+v", doc.Files[0])
		}
		if f := doc.Files[1]; f.Stored || f.Reason != reasonStorageLimit {
			t.Errorf("second file = %+v, want it described but unstored", f)
		}
		// "ZZZ\n" sanitizes to seven bytes, and one file's worth is all that
		// fits under a limit of twelve once the second is measured after
		// sanitizing rather than before.
		if total := storedByteTotal(t, run); total != 7 {
			t.Errorf("stored %d bytes, want 7", total)
		}
	})
}

// 14. One file that cannot be read costs that file's evidence, not the run's.

func TestFinalizeRecordsAnUnreadableFileAndKeepsGoing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode says")
	}
	const secret = "UNREADABLE-BODY-qwe456"
	repo, run := gitRepo(t), runDir(t)
	c := start(t, repo, run)

	write(t, repo, "kept.txt", "kept\n")
	write(t, repo, "shut.txt", secret+"\n")
	shut := filepath.Join(repo, "shut.txt")
	if err := os.Chmod(shut, 0o000); err != nil {
		t.Fatalf("close off shut.txt: %v", err)
	}
	t.Cleanup(func() { os.Chmod(shut, 0o600) })

	res := finalize(t, c)
	if res.UntrackedFiles != 2 || res.StoredTextFiles != 1 {
		t.Fatalf("untracked = %d stored = %d, want 2 and 1", res.UntrackedFiles, res.StoredTextFiles)
	}

	doc := readJSON[untrackedDoc](t, filepath.Join(gitDirOf(run), untrackedFile))
	got := map[string]untrackedEntry{}
	for _, f := range doc.Files {
		got[f.Path] = f
	}
	if f := got["kept.txt"]; !f.Stored || f.Kind != kindFile {
		t.Errorf("kept.txt = %+v, want it captured", f)
	}
	f, ok := got["shut.txt"]
	if !ok {
		t.Fatalf("shut.txt is missing from %+v", doc.Files)
	}
	if f.Kind != kindUnavailable || f.Reason != reasonUnreadable || f.Stored || f.SHA256 != "" || f.StoredAs != "" {
		t.Errorf("shut.txt = %+v, want an unavailable entry", f)
	}
	assertAbsent(t, run, secret)
}

// 15. Where a body lands is this package's decision, and a body already there
// is never written over.

func TestStoredBodiesCannotEscapeAReplacedDirectory(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	outside := runDir(t)
	before, err := os.Lstat(outside)
	if err != nil {
		t.Fatalf("stat outside: %v", err)
	}
	c := start(t, repo, run)

	// The bodies directory is replaced mid-run with a link out of the bundle,
	// which is what a provider that wants the evidence elsewhere would plant.
	bodies := filepath.Join(gitDirOf(run), untrackedDirName)
	if err := os.Remove(bodies); err != nil {
		t.Fatalf("remove bodies directory: %v", err)
	}
	if err := os.Symlink(outside, bodies); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}
	write(t, repo, "new.txt", "body\n")

	if _, err := c.Finalize(context.Background()); err == nil {
		t.Fatal("Finalize wrote through a replaced bodies directory")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("read outside: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("outside directory holds %d entries, want none", len(entries))
	}
	after, err := os.Lstat(outside)
	if err != nil {
		t.Fatalf("stat outside: %v", err)
	}
	if after.Mode() != before.Mode() {
		t.Errorf("outside mode = %v, was %v", after.Mode(), before.Mode())
	}
}

// openRootAt is how a test reaches the root-bound writer directly: every
// artifact this package installs goes through a root opened once, so a test of
// the writer opens one the same way.
func openRootAt(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open root %s: %v", dir, err)
	}
	t.Cleanup(func() { root.Close() })
	return root
}

func TestWriteFileNeverReplacesWhatIsAlreadyThere(t *testing.T) {
	t.Run("an existing file", func(t *testing.T) {
		dir := runDir(t)
		path := filepath.Join(dir, "doc.json")
		if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
			t.Fatalf("plant file: %v", err)
		}

		if err := writeFileAt(openRootAt(t, dir), "doc.json", []byte("replacement\n")); err == nil {
			t.Fatal("writeFileAt replaced an existing file")
		}
		body, err := os.ReadFile(path)
		if err != nil || string(body) != "original\n" {
			t.Errorf("file = %q (%v), want it untouched", body, err)
		}
		if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("temporary file left behind: %v", err)
		}
	})

	t.Run("a symlink pointing out of the bundle", func(t *testing.T) {
		dir, target := runDir(t), runDir(t)
		outside := filepath.Join(target, "outside.txt")
		if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
			t.Fatalf("plant target: %v", err)
		}
		path := filepath.Join(dir, "doc.json")
		if err := os.Symlink(outside, path); err != nil {
			t.Fatalf("plant symlink: %v", err)
		}

		if err := writeFileAt(openRootAt(t, dir), "doc.json", []byte("replacement\n")); err == nil {
			t.Fatal("writeFileAt wrote through a symlink")
		}
		body, err := os.ReadFile(outside)
		if err != nil || string(body) != "outside\n" {
			t.Errorf("target = %q (%v), want it untouched", body, err)
		}
		if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("temporary file left behind: %v", err)
		}
	})

	t.Run("a name that is not one of this package's own", func(t *testing.T) {
		dir := runDir(t)
		root := openRootAt(t, dir)
		for _, name := range []string{"", ".", "..", "../escape.json", "untracked/../../escape.json", "/absolute.json"} {
			if err := writeFileAt(root, name, []byte("body\n")); err == nil {
				t.Errorf("writeFileAt accepted %q", name)
			}
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Errorf("directory holds %v (%v), want nothing", entries, err)
		}
	})

	t.Run("through a root whose directory was replaced", func(t *testing.T) {
		dir, outside := runDir(t), runDir(t)
		root := openRootAt(t, dir)
		moved := dir + ".moved"
		if err := os.Rename(dir, moved); err != nil {
			t.Fatalf("rename: %v", err)
		}
		if err := os.Symlink(outside, dir); err != nil {
			t.Fatalf("plant symlink: %v", err)
		}

		if err := writeFileAt(root, "doc.json", []byte("body\n")); err != nil {
			t.Fatalf("writeFileAt: %v", err)
		}
		if _, err := os.Stat(filepath.Join(moved, "doc.json")); err != nil {
			t.Errorf("document did not land in the directory the root holds: %v", err)
		}
		entries, err := os.ReadDir(outside)
		if err != nil || len(entries) != 0 {
			t.Errorf("outside holds %v (%v), want nothing", entries, err)
		}
	})
}

// 15b. The capture directory itself is held by descriptor, so replacing it
// mid-run sends nothing anywhere else.

func TestEvidenceCannotBeDivertedByReplacingTheCaptureDirectory(t *testing.T) {
	const secret = "diverted-evidence-marker"

	// divert plants a symlink to outside at the capture directory's name, after
	// disposing of the original the way the argument says, and reports what
	// Finalize made of it.
	divert := func(t *testing.T, outside string, dispose func(t *testing.T, gitDir string)) error {
		t.Helper()
		repo, run := gitRepo(t), runDir(t)
		c := start(t, repo, run)

		// One tracked change and one untracked file, both carrying the marker, so
		// that a diverted patch or a diverted body would be visible outside.
		write(t, repo, "b.txt", "b0\n"+secret+"\n")
		write(t, repo, "new.txt", secret+"\n")

		gitDir := gitDirOf(run)
		dispose(t, gitDir)
		if err := os.Symlink(outside, gitDir); err != nil {
			t.Fatalf("plant symlink: %v", err)
		}
		_, err := c.Finalize(context.Background())
		return err
	}

	// assertUntouched proves the directory the symlink pointed at received
	// nothing at all: no artifact, no body, no mode change.
	assertUntouched := func(t *testing.T, outside string, before os.FileInfo) {
		t.Helper()
		entries, err := os.ReadDir(outside)
		if err != nil {
			t.Fatalf("read outside: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("outside holds %d entries, want none", len(entries))
		}
		after, err := os.Lstat(outside)
		if err != nil {
			t.Fatalf("stat outside: %v", err)
		}
		if after.Mode() != before.Mode() {
			t.Errorf("outside mode = %v, was %v", after.Mode(), before.Mode())
		}
		assertAbsent(t, outside, secret)
	}

	t.Run("the original directory renamed away", func(t *testing.T) {
		outside := runDir(t)
		before, err := os.Lstat(outside)
		if err != nil {
			t.Fatalf("stat outside: %v", err)
		}
		var moved string
		finalizeErr := divert(t, outside, func(t *testing.T, gitDir string) {
			t.Helper()
			moved = gitDir + ".moved"
			if err := os.Rename(gitDir, moved); err != nil {
				t.Fatalf("rename the capture directory: %v", err)
			}
		})

		// The descriptor still holds the original directory, so the evidence is
		// either written where it was always going or not written at all.
		if finalizeErr == nil {
			for _, name := range []string{baselineFile, patchFile, trackedStatFile, untrackedFile} {
				if _, err := os.Stat(filepath.Join(moved, name)); err != nil {
					t.Errorf("%s did not land in the original directory: %v", name, err)
				}
			}
		}
		assertUntouched(t, outside, before)
	})

	t.Run("the original directory removed", func(t *testing.T) {
		outside := runDir(t)
		before, err := os.Lstat(outside)
		if err != nil {
			t.Fatalf("stat outside: %v", err)
		}
		finalizeErr := divert(t, outside, func(t *testing.T, gitDir string) {
			t.Helper()
			if err := os.RemoveAll(gitDir); err != nil {
				t.Fatalf("remove the capture directory: %v", err)
			}
		})
		if finalizeErr == nil {
			t.Fatal("Finalize reported evidence it had nowhere to write")
		}
		assertUntouched(t, outside, before)
	})
}

// 15c. A capture that has been closed has nowhere left to write, and says so.

func TestFinalizeAfterCloseFailsRatherThanWritingAnywhere(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	c := start(t, repo, run)
	write(t, repo, "new.txt", "body\n")

	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if res, err := c.Finalize(context.Background()); err == nil {
		t.Fatalf("Finalize reported %+v from a closed capture", res)
	}
	if _, err := os.Stat(filepath.Join(gitDirOf(run), untrackedFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a closed capture wrote its evidence anyway: %v", err)
	}
}

func TestStartLeavesNothingBehindWhenItFails(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	runGit(t, repo, "update-ref", refName("run-1"), "HEAD")
	before, err := gitOut(t, repo, "rev-parse", refName("run-1"))
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}

	if c, err := Start(context.Background(), repo, "run-1", run, Options{}); err == nil {
		c.Close(context.Background())
		t.Fatal("Start pinned a baseline over an existing ref")
	}
	if _, err := os.Stat(gitDirOf(run)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("evidence directory survived a failed Start: %v", err)
	}
	after, err := gitOut(t, repo, "rev-parse", refName("run-1"))
	if err != nil || after != before {
		t.Errorf("existing ref changed: %q -> %q (%v)", before, after, err)
	}
}

// 16. The capture measures the repository, not a corner of it.

func TestStartRefusesADirectoryBelowTheRepositoryRoot(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatalf("create subdirectory: %v", err)
	}

	c, err := Start(context.Background(), sub, "run-1", run, Options{})
	if err == nil {
		c.Close(context.Background())
		t.Fatal("Start accepted a subdirectory as the repository root")
	}
	if _, err := os.Stat(gitDirOf(run)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("evidence directory created for a rejected root: %v", err)
	}
}

// 17. A question that was never answered is not an answer of no baseline.

func TestCancellationIsNotAMissingBaseline(t *testing.T) {
	t.Run("during Start", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		c, err := Start(ctx, repo, "run-1", run, Options{})
		if err == nil {
			c.Close(context.Background())
			t.Fatal("Start reported a capture from a cancelled context")
		}
		if _, err := os.Stat(filepath.Join(gitDirOf(run), baselineFile)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a baseline document was written anyway: %v", err)
		}
	})

	t.Run("during Finalize", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		ctx, cancel := context.WithCancel(context.Background())
		c, err := Start(ctx, repo, "run-1", run, Options{})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		cancel()

		if res, err := c.Finalize(ctx); err == nil {
			t.Fatalf("Finalize reported %+v from a cancelled context", res)
		}
		if _, err := os.Stat(filepath.Join(gitDirOf(run), trackedStatFile)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("an unreachable baseline was claimed: %v", err)
		}
		// The ref is not the run's to leave behind, cancelled or not.
		if out, err := gitOut(t, repo, "rev-parse", "--verify", refName("run-1")); err == nil {
			t.Errorf("temp ref survived a cancelled finalize: %s", out)
		}
	})
}

// 18. Every document says what a difference does and does not mean.

func TestDocumentsCarryTheirAttribution(t *testing.T) {
	t.Run("when the evidence is available", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		c := start(t, repo, run)
		write(t, repo, "c.txt", "c0\nchanged\n")
		write(t, repo, "new.txt", "new\n")
		finalize(t, c)

		stat := readJSON[trackedStatDoc](t, filepath.Join(gitDirOf(run), trackedStatFile))
		if stat.Attribution != Attribution {
			t.Errorf("tracked stat attribution = %q, want %q", stat.Attribution, Attribution)
		}
		doc := readJSON[untrackedDoc](t, filepath.Join(gitDirOf(run), untrackedFile))
		if doc.Attribution != Attribution {
			t.Errorf("untracked attribution = %q, want %q", doc.Attribution, Attribution)
		}
	})

	t.Run("when it is not", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		c := startWith(t, repo, run, Options{MaxPatchBytes: 64})
		write(t, repo, "big.txt", strings.Repeat("line of tracked change\n", 200))
		runGit(t, repo, "add", "big.txt")
		finalize(t, c)

		stat := readJSON[trackedStatDoc](t, filepath.Join(gitDirOf(run), trackedStatFile))
		if stat.Status != statusUnavailable || stat.Attribution != Attribution {
			t.Errorf("stat = %+v, want an unavailable document that still says what it means", stat)
		}
	})
}

// 19. A patch that is not valid UTF-8 is refused whole rather than coerced.
// Git hands back the bytes the files hold, and a Latin-1 file makes a patch no
// JSON encoder can carry: the encoder would replace every invalid byte with
// U+FFFD and the patch on disk would then differ from the one the repository
// produced while still reading as evidence of it.

func TestFinalizeRefusesAPatchThatIsNotUTF8(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	// A sanitizer that refuses what it cannot read, which is what the bundle's
	// own redactor does: the patch must be judged before it ever gets here.
	c := startWith(t, repo, run, Options{Sanitize: func(s string) (string, error) {
		if !utf8.ValidString(s) {
			return "", errors.New("sanitizer: not valid UTF-8")
		}
		return s, nil
	}})

	writeBytes(t, repo, "latin1.txt", []byte("caf\xe9 cr\xe8me\n"))
	runGit(t, repo, "add", "latin1.txt")
	write(t, repo, "notes.txt", "left behind\n")

	res := finalize(t, c)

	if res.Status != statusUnavailable || res.Reason != reasonPatchNotUTF8 {
		t.Fatalf("status = %q reason = %q, want %q/%q", res.Status, res.Reason, statusUnavailable, reasonPatchNotUTF8)
	}
	if _, err := os.Stat(filepath.Join(gitDirOf(run), patchFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a patch that is not UTF-8 was written: %v", err)
	}
	stat := readJSON[trackedStatDoc](t, filepath.Join(gitDirOf(run), trackedStatFile))
	if stat.Status != statusUnavailable || stat.Reason != reasonPatchNotUTF8 || stat.Attribution != Attribution {
		t.Errorf("stat = %+v, want an unavailable document naming the reason", stat)
	}
	// No mangled copy of the patch reached the evidence by any other route.
	assertAbsent(t, run, "�")
	assertAbsent(t, run, "diff --git")

	// The untracked capture is a separate question and is still answered.
	doc := readJSON[untrackedDoc](t, filepath.Join(gitDirOf(run), untrackedFile))
	if res.UntrackedFiles != 1 || len(doc.Files) != 1 || doc.Files[0].Path != "notes.txt" || !doc.Files[0].Stored {
		t.Errorf("untracked = %+v, want the file the run left still captured", doc.Files)
	}
}

// A binary patch is ASCII whatever the file holds, because --binary encodes the
// blob rather than quoting it, so binary tracked changes stay supported.

func TestFinalizeStillCapturesBinaryTrackedChanges(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)
	c := start(t, repo, run)

	writeBytes(t, repo, "blob.bin", []byte{0x00, 0x01, 0xff, 0xfe, 0x89, 'P', 'N', 'G'})
	runGit(t, repo, "add", "blob.bin")

	res := finalize(t, c)
	if res.Status != statusAvailable || res.BinaryTracked != 1 {
		t.Fatalf("result = %+v, want available evidence naming one binary file", res)
	}
	patch, err := os.ReadFile(filepath.Join(gitDirOf(run), patchFile))
	if err != nil {
		t.Fatalf("read patch: %v", err)
	}
	if !utf8.Valid(patch) {
		t.Errorf("a binary patch is not valid UTF-8:\n%q", patch)
	}
	if !bytes.Contains(patch, []byte("GIT binary patch")) {
		t.Errorf("patch does not carry the binary blob:\n%s", patch)
	}
}

// 20. What an untracked hash can be reversed into is what was stored, never the
// secret that was redacted out of it.

func TestUntrackedHashesTheSanitizedBytesRatherThanTheSecret(t *testing.T) {
	// Low entropy on purpose: a raw SHA256 of a short, guessable secret is the
	// secret, because anyone holding the hash can enumerate it.
	const secret = "hunter2"
	const raw = "password: " + secret + "\n"
	const sanitized = "password: [redacted]\n"

	repo, run := gitRepo(t), runDir(t)
	c := startWith(t, repo, run, Options{Sanitize: func(s string) (string, error) {
		return strings.ReplaceAll(s, secret, "[redacted]"), nil
	}})

	write(t, repo, "creds.txt", raw)
	blob := []byte{0x00, 'b', 'i', 'n', 0xff}
	writeBytes(t, repo, "blob.bin", blob)

	finalize(t, c)
	got := untrackedByPath(t, run)

	f := got["creds.txt"]
	if !f.Stored || f.HashBasis != hashBasisSanitized {
		t.Fatalf("creds.txt = %+v, want a stored file hashed over what was stored", f)
	}
	if f.SHA256 != sum(sanitized) {
		t.Errorf("creds.txt sha256 = %q, want the sanitized body's %q", f.SHA256, sum(sanitized))
	}
	// The hash of the plaintext is what a reader could brute-force, so it must
	// appear nowhere in the evidence at all.
	assertAbsent(t, run, sum(raw))
	assertAbsent(t, run, secret)

	// A binary body is never stored, so its raw hash identifies a file that is
	// not in the bundle rather than standing in for text that was redacted.
	b := got["blob.bin"]
	if b.SHA256 != sumBytes(blob) || b.HashBasis != hashBasisRawBinary || b.Stored || b.Reason != reasonBinary {
		t.Errorf("blob.bin = %+v, want the raw hash of an unstored binary", b)
	}
}

func TestUntrackedOmitsTheHashOfTextItNeverSanitized(t *testing.T) {
	const secret = "hunter2"

	t.Run("text over the per-file limit is described without a hash", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		c := startWith(t, repo, run, Options{MaxTextFileBytes: 8})

		body := "password: " + secret + " and more\n"
		write(t, repo, "big.txt", body)

		finalize(t, c)
		f := untrackedByPath(t, run)["big.txt"]
		if f.Stored || f.Reason != reasonFileTooLarge {
			t.Fatalf("big.txt = %+v, want it described but unstored", f)
		}
		if f.SHA256 != "" || f.HashBasis != "" {
			t.Errorf("big.txt = %+v, want no hash of text that was never sanitized", f)
		}
		assertAbsent(t, run, sum(body))
	})

	t.Run("the aggregate limit keeps the sanitized hash", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		c := startWith(t, repo, run, Options{MaxStoredTextBytes: 12, Sanitize: func(s string) (string, error) {
			return strings.ReplaceAll(s, secret, "[redacted]"), nil
		}})

		write(t, repo, "a-first.txt", "0123456789\n")
		write(t, repo, "b-second.txt", "pw: "+secret+"\n")

		finalize(t, c)
		f := untrackedByPath(t, run)["b-second.txt"]
		if f.Stored || f.Reason != reasonStorageLimit {
			t.Fatalf("b-second.txt = %+v, want it described but unstored", f)
		}
		if f.SHA256 != sum("pw: [redacted]\n") || f.HashBasis != hashBasisSanitized {
			t.Errorf("b-second.txt = %+v, want the hash of the sanitized text", f)
		}
		assertAbsent(t, run, sum("pw: "+secret+"\n"))
	})

	t.Run("what has no body has no hash", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		c := start(t, repo, run)

		if err := os.Symlink("/etc/passwd", filepath.Join(repo, "link")); err != nil {
			t.Fatalf("plant symlink: %v", err)
		}
		finalize(t, c)
		f := untrackedByPath(t, run)["link"]
		if f.Kind != kindSymlink || f.SHA256 != "" || f.HashBasis != "" {
			t.Errorf("link = %+v, want a described symlink with no hash", f)
		}
	})
}

// untrackedByPath reads the untracked document and keys it by the path each
// entry names.
func untrackedByPath(t *testing.T, run string) map[string]untrackedEntry {
	t.Helper()
	doc := readJSON[untrackedDoc](t, filepath.Join(gitDirOf(run), untrackedFile))
	got := make(map[string]untrackedEntry, len(doc.Files))
	for _, f := range doc.Files {
		got[f.Path] = f
	}
	return got
}

func sum(s string) string { return sumBytes([]byte(s)) }

func sumBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// 21. Whether the evidence was ever collected is readable from disk alone.

func TestResultDocumentRecordsWhetherCollectionHappened(t *testing.T) {
	resultPath := func(run string) string { return filepath.Join(gitDirOf(run), resultFile) }

	t.Run("pending before the run has been measured", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		start(t, repo, run)

		doc := readJSON[resultDoc](t, resultPath(run))
		if doc.Status != statusPending || doc.Attribution != Attribution {
			t.Errorf("result = %+v, want a pending document that says what it will mean", doc)
		}
	})

	t.Run("available once the run has been measured", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		c := start(t, repo, run)
		write(t, repo, "c.txt", "c0\nchanged\n")
		write(t, repo, "new.txt", "new\n")
		res := finalize(t, c)

		doc := readJSON[resultDoc](t, resultPath(run))
		if doc.Status != statusAvailable || doc.Reason != "" || doc.Attribution != Attribution {
			t.Fatalf("result = %+v, want the available evidence this run collected", doc)
		}
		if doc.Baseline != res.Baseline || doc.Baseline == "" {
			t.Errorf("result baseline = %q, want %q", doc.Baseline, res.Baseline)
		}
		if doc.TrackedFiles != 1 || doc.Added != 1 || doc.UntrackedFiles != 1 || doc.StoredTextFiles != 1 {
			t.Errorf("result = %+v, want the counts Finalize reported (%+v)", doc, res)
		}
	})

	t.Run("unavailable evidence is not a failed collection", func(t *testing.T) {
		repo, run := gitRepo(t), runDir(t)
		c := startWith(t, repo, run, Options{MaxPatchBytes: 64})
		write(t, repo, "big.txt", strings.Repeat("line of tracked change\n", 200))
		runGit(t, repo, "add", "big.txt")
		finalize(t, c)

		doc := readJSON[resultDoc](t, resultPath(run))
		if doc.Status != statusUnavailable || doc.Reason != reasonPatchTooLarge {
			t.Errorf("result = %+v, want unavailable evidence naming why", doc)
		}
	})

	t.Run("a collection that failed says so and keeps the error to itself", func(t *testing.T) {
		const boom = "sanitizer refused the body"
		repo, run := gitRepo(t), runDir(t)
		c := startWith(t, repo, run, Options{Sanitize: func(s string) (string, error) {
			if strings.Contains(s, "explode") {
				return "", errors.New(boom)
			}
			return s, nil
		}})

		// The patch is written first, so the failure lands on a capture that has
		// already left a partial artifact behind.
		write(t, repo, "c.txt", "c0\nchanged\n")
		write(t, repo, "notes.txt", "explode\n")

		if _, err := c.Finalize(context.Background()); err == nil {
			t.Fatal("Finalize succeeded, want the sanitizer's refusal reported")
		}
		if _, err := os.Stat(filepath.Join(gitDirOf(run), patchFile)); err != nil {
			t.Fatalf("the partial artifact is missing, so this is not the case under test: %v", err)
		}

		doc := readJSON[resultDoc](t, resultPath(run))
		if doc.Status != statusUnavailable || doc.Reason != reasonCollectionFailed {
			t.Errorf("result = %+v, want a collection that failed to say so", doc)
		}
		// The error names repository paths and whatever the sanitizer said,
		// none of which anything has sanitized.
		assertAbsent(t, run, boom)
	})

	t.Run("a result something else replaced is not overwritten", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			plant func(t *testing.T, path, elsewhere string)
		}{
			{"a symlink out of the bundle", func(t *testing.T, path, elsewhere string) {
				if err := os.Symlink(elsewhere, path); err != nil {
					t.Fatalf("plant symlink: %v", err)
				}
			}},
			{"a file the capture never wrote", func(t *testing.T, path, elsewhere string) {
				if err := os.WriteFile(path, []byte("planted\n"), 0o600); err != nil {
					t.Fatalf("plant file: %v", err)
				}
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				repo, run := gitRepo(t), runDir(t)
				elsewhere := filepath.Join(runDir(t), "elsewhere.json")
				if err := os.WriteFile(elsewhere, []byte("not the bundle's\n"), 0o600); err != nil {
					t.Fatalf("write elsewhere: %v", err)
				}
				c := start(t, repo, run)

				if err := os.Remove(resultPath(run)); err != nil {
					t.Fatalf("remove result: %v", err)
				}
				tc.plant(t, resultPath(run), elsewhere)

				if _, err := c.Finalize(context.Background()); err == nil {
					t.Fatal("Finalize succeeded, want it to refuse a result it did not write")
				}
				if body := readFile(t, elsewhere); body != "not the bundle's\n" {
					t.Errorf("%s = %q, want it untouched", elsewhere, body)
				}
			})
		}
	})
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
