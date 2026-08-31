package evidence

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSnapshotDetectsSameSizeRewriteWithRestoredMtime(t *testing.T) {
	repo := gitRepo(t)
	path := filepath.Join(repo, "b.txt")
	write(t, repo, "b.txt", "dirty-a\n")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	p := &PinnedVerification{repoRoot: repo}
	before, err := p.snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	write(t, repo, "b.txt", "dirty-b\n")
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := p.snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	changed := changedPaths(before, after)
	if len(changed) != 1 || changed[0] != "b.txt" {
		t.Fatalf("changed paths = %v, want [b.txt]", changed)
	}
}

func TestSnapshotDetectsGitIndexExtendedFlagChanges(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		t.Run(flag, func(t *testing.T) {
			repo := gitRepo(t)
			p := &PinnedVerification{repoRoot: repo}
			before, err := p.snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}

			runGit(t, repo, "update-index", flag, "b.txt")
			after, err := p.snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}

			if before.index == after.index {
				t.Fatalf("index fingerprint did not change after %s", flag)
			}
			if changed := changedPaths(before, after); len(changed) != 0 {
				t.Fatalf("content fingerprints changed after index-only flag update: %v", changed)
			}
		})
	}
}

func TestSnapshotDetectsSplitIndexTopologyChanges(t *testing.T) {
	repo := gitRepo(t)
	p := &PinnedVerification{repoRoot: repo}
	before, err := p.snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	runGit(t, repo, "update-index", "--split-index")
	after, err := p.snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if before.index == after.index {
		t.Fatal("index fingerprint did not change after enabling split index")
	}
	if changed := changedPaths(before, after); len(changed) != 0 {
		t.Fatalf("content fingerprints changed after split-index update: %v", changed)
	}
}

func TestFingerprintPathsRejectsInvalidWorkerCount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := fingerprintPaths(ctx, nil, []string{"file"}, 0, func(*os.Root, string) string { return "unused" })
	if err == nil || !strings.Contains(err.Error(), "worker count") {
		t.Fatalf("error = %v, want invalid worker count", err)
	}
}

func TestFingerprintPathsRunsWithBoundedParallelism(t *testing.T) {
	paths := make([]string, snapshotFingerprintWorkers*4)
	for i := range paths {
		paths[i] = fmt.Sprintf("file-%d", i)
	}
	var active, maximum, arrivals atomic.Int32
	entered := make(chan struct{}, snapshotFingerprintWorkers)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	fingerprintFn := func(_ *os.Root, path string) string {
		current := active.Add(1)
		for {
			seen := maximum.Load()
			if current <= seen || maximum.CompareAndSwap(seen, current) {
				break
			}
		}
		if arrivals.Add(1) <= snapshotFingerprintWorkers {
			entered <- struct{}{}
			<-release
		}
		active.Add(-1)
		return "fingerprint:" + path
	}
	type result struct {
		files map[string]string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		files, err := fingerprintPaths(context.Background(), nil, paths, snapshotFingerprintWorkers, fingerprintFn)
		done <- result{files: files, err: err}
	}()

	for range snapshotFingerprintWorkers {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("workers did not overlap")
		}
	}
	if maximum.Load() != snapshotFingerprintWorkers {
		t.Fatalf("maximum concurrency = %d, want %d", maximum.Load(), snapshotFingerprintWorkers)
	}
	close(release)
	released = true
	got := <-done
	if got.err != nil {
		t.Fatalf("fingerprintPaths: %v", got.err)
	}
	for _, path := range paths {
		if got.files[path] != "fingerprint:"+path {
			t.Errorf("fingerprint %q = %q", path, got.files[path])
		}
	}
}
