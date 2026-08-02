package evidence

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func BenchmarkVerificationSnapshot20000Files(b *testing.B) {
	for _, workers := range []int{1, snapshotFingerprintWorkers} {
		b.Run(fmt.Sprintf("workers-%d", workers), func(b *testing.B) {
			b.StopTimer()
			repo := snapshotBenchmarkRepo(b)
			p := &PinnedVerification{repoRoot: repo}
			b.StartTimer()
			for range b.N {
				if _, err := p.snapshotWithWorkers(context.Background(), workers); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func snapshotBenchmarkRepo(b *testing.B) string {
	b.Helper()
	repo := b.TempDir()
	home := b.TempDir()
	git := func(args ...string) {
		b.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+home)
		if out, err := cmd.CombinedOutput(); err != nil {
			b.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "benchmark@agentrec.local")
	git("config", "user.name", "agentrec benchmark")
	content := make([]byte, 4*1024)
	for i := range 20_000 {
		path := fmt.Sprintf("files/%03d/%05d.dat", i/200, i)
		name := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(name, content, 0o600); err != nil {
			b.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-qm", "benchmark baseline")
	return repo
}
