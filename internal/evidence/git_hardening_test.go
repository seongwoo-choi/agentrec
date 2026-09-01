package evidence

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The repository's own attributes and the operator's global configuration can
// both change what git diff says. The evidence is meant to be the change itself,
// the same bytes wherever it is captured, so neither gets a say.
func TestFinalizeIgnoresDiffRewritingByRepositoryAndOperator(t *testing.T) {
	repo, run := gitRepo(t), runDir(t)

	// The operator's machine: prefixes off, mnemonic prefixes on and colour
	// forced even into a pipe. The capture runs with this process's environment,
	// so this is what it sees.
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[diff]\n\tnoprefix = true\n\tmnemonicPrefix = true\n[color]\n\tui = always\n"), 0o600); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	// The repository: a textconv filter under which both sides of every .txt
	// read the same, and an fsmonitor hook that leaves a mark when run. The mark
	// lives outside the worktree so it is not itself captured as untracked.
	marker := filepath.Join(t.TempDir(), "hook-ran")
	runGit(t, repo, "config", "diff.hide.textconv", "echo CLEAN #")
	runGit(t, repo, "config", "core.fsmonitor", "sh -c 'touch "+marker+"; exit 1'")
	write(t, repo, filepath.Join(".git", "info", "attributes"), "*.txt diff=hide\n")
	write(t, repo, "b.txt", "b1\n")

	c := start(t, repo, run)
	res := finalize(t, c)

	if res.Status != statusAvailable || res.TrackedFiles != 1 || res.Added != 1 || res.Deleted != 1 {
		t.Fatalf("result = %+v, want 1 file +1/-1 available", res)
	}
	patch, err := os.ReadFile(filepath.Join(gitDirOf(run), patchFile))
	if err != nil {
		t.Fatalf("read patch: %v", err)
	}
	for _, want := range []string{"--- a/b.txt", "+++ b/b.txt", "-b0", "+b1"} {
		if !bytes.Contains(patch, []byte(want)) {
			t.Errorf("patch does not contain %q:\n%s", want, patch)
		}
	}
	if bytes.Contains(patch, []byte("CLEAN")) {
		t.Errorf("the textconv filter got into the patch:\n%s", patch)
	}
	if bytes.ContainsRune(patch, 0x1b) {
		t.Errorf("colour got into the patch:\n%q", patch)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the repository's fsmonitor hook ran during capture (marker stat: %v)", err)
	}
}

// A process Git leaves behind holding its pipes — a filter, a hook — must not
// hold the capture open with it: Git's own exit ends the wait, and what Git
// wrote before exiting is kept.
func TestGitAtReturnsWhenAGrandchildHoldsThePipes(t *testing.T) {
	if testing.Short() {
		// The test can only end when the wait delay does, so it costs that delay
		// on every run.
		t.Skip("waits out the pipe delay")
	}
	for _, bin := range []string{"/bin/sh", "/bin/sleep"} {
		if _, err := os.Stat(bin); err != nil {
			t.Skipf("needs %s", bin)
		}
	}
	// PATH holds only the stand-in while it runs, so the grandchild is named by
	// its full path.
	stub := t.TempDir()
	script := "#!/bin/sh\nprintf 'answer\\n'\n/bin/sleep 30 &\nexit 0\n"
	if err := os.WriteFile(filepath.Join(stub, "git"), []byte(script), 0o700); err != nil {
		t.Fatalf("write git stand-in: %v", err)
	}
	t.Setenv("PATH", stub)

	started := time.Now()
	out, err := gitAt(context.Background(), "", maxSmallBytes, "anything")
	if took := time.Since(started); took > 7*time.Second {
		t.Fatalf("gitAt took %v, want Git's exit plus the wait delay", took)
	}
	if err != nil || string(out) != "answer\n" {
		t.Fatalf("gitAt = %q, %v; want the answer Git wrote and no error", out, err)
	}
}
