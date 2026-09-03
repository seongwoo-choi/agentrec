package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHumanBytesAndParseAge(t *testing.T) {
	// 1023.6 KB must roll over rather than print the nonsense "1024 KB".
	for n, want := range map[int64]string{0: "0 B", 512: "512 B", 48 << 10: "48 KB", 1536: "1.5 KB", 312 << 20: "312 MB", 1288490188: "1.2 GB", (1 << 20) - 1: "1.0 MB", (1 << 30) - 1: "1.0 GB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
	for s, want := range map[string]time.Duration{"12h": 12 * time.Hour, "30d": 30 * 24 * time.Hour, "2w": 14 * 24 * time.Hour} {
		if got, err := parseAge(s); err != nil || got != want {
			t.Errorf("parseAge(%q) = %v, %v; want %v", s, got, err, want)
		}
	}
	for _, s := range []string{"", "d", "0d", "30", "30x", "-1d", "1.5d", "30 d"} {
		if _, err := parseAge(s); err == nil {
			t.Errorf("parseAge(%q) accepted", s)
		}
	}
}

func TestTrashSweepMovesOldRunsAndKeepsRecentAndOpenOnes(t *testing.T) {
	root := home(t)
	now := time.Now()
	writeRun(t, root, "old-done", "claude", now.Add(-40*24*time.Hour), "completed")
	writeRun(t, root, "recent-done", "claude", now.Add(-24*time.Hour), "completed")
	writeRun(t, root, "old-open", "codex", now.Add(-40*24*time.Hour), "")

	var stdout, stderr bytes.Buffer
	if code := runTrash([]string{"sweep", "30d", "--dry-run"}, &stdout, &stderr); code != 0 {
		t.Fatalf("dry run exited %d: %s", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "would move old-done") || !strings.Contains(out, "would move 1 run(s)") || !strings.Contains(out, "kept 1 still held") {
		t.Fatalf("dry run said:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(root, "old-done")); err != nil {
		t.Fatalf("a dry run moved the run: %v", err)
	}

	stdout.Reset()
	if code := runTrash([]string{"sweep", "30d"}, &stdout, &stderr); code != 0 {
		t.Fatalf("sweep exited %d: %s", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "moved old-done") || !strings.Contains(out, "moved 1 run(s)") || !strings.Contains(out, "kept 1 still held") {
		t.Fatalf("sweep said:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(trashRootFor(root), "old-done")); err != nil {
		t.Fatalf("old run not in the trash: %v", err)
	}
	for _, kept := range []string{"recent-done", "old-open"} {
		if _, err := os.Stat(filepath.Join(root, kept)); err != nil {
			t.Fatalf("%s should have stayed: %v", kept, err)
		}
	}
	// A run the sweep cannot date is left alone and said out loud: reporting
	// only what moved would read as "everything else was recent".
	undated := filepath.Join(root, "run-undated")
	if err := os.MkdirAll(undated, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(undated, manifestFile), []byte(`{"schemaVersion":1,"provider":"claude","argv":["claude"],"cwd":"/tmp"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := runTrash([]string{"sweep", "30d"}, &stdout, &stderr); code != 0 {
		t.Fatalf("sweep exited %d: %s", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "left 1 that could not be dated") {
		t.Fatalf("the sweep hid what it could not judge:\n%s", out)
	}
	if _, err := os.Stat(undated); err != nil {
		t.Fatalf("an undated run was swept: %v", err)
	}

	for _, bad := range [][]string{{"sweep"}, {"sweep", "30x"}, {"sweep", "0d"}, {"sweep", "30d", "--now"}} {
		stderr.Reset()
		if code := runTrash(bad, &stdout, &stderr); code == 0 {
			t.Errorf("trash %v exited 0", bad)
		}
	}
}

func TestStatusAndRunListReportDiskUsage(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", time.Now().Add(-2*time.Hour), "completed")
	want := storeBytes(root)
	if want <= 0 {
		t.Fatalf("storeBytes(root) = %d", want)
	}
	if got := storeBytes(filepath.Join(root, "missing")); got != 0 {
		t.Fatalf("storeBytes(missing) = %d", got)
	}
	// Regular files are summed as they are; a symlink is neither followed
	// nor counted.
	sized := t.TempDir()
	if err := os.WriteFile(filepath.Join(sized, "a"), make([]byte, 1000), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sized, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sized, "sub", "b"), make([]byte, 24), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "run-a"), filepath.Join(sized, "link")); err != nil {
		t.Fatal(err)
	}
	if got := storeBytes(sized); got != 1024 {
		t.Fatalf("storeBytes(sized) = %d, want the two files only", got)
	}

	var stdout, stderr bytes.Buffer
	if code := runStatus(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("status exited %d: %s", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "1 recorded under "+root+", "+humanBytes(want)+" on disk") || strings.Contains(out, "trash ") {
		t.Fatalf("status said:\n%s", out)
	}
	if err := trashRun(root, "run-a"); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := runStatus(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("status exited %d: %s", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "trash     1 run(s), "+humanBytes(want)+" (agentrec trash empty)") {
		t.Fatalf("status said:\n%s", out)
	}

	handler := newViewHandler(root, "", false)
	defer handler.Close()
	var list struct {
		StoreBytes int64 `json:"storeBytes"`
		TrashBytes int64 `json:"trashBytes"`
	}
	viewJSONRequest(t, handler, "/api/runs", &list)
	if list.StoreBytes != 0 || list.TrashBytes != want {
		t.Fatalf("run list reported store %d trash %d, want 0 and %d", list.StoreBytes, list.TrashBytes, want)
	}
}

func TestViewSnapshotsGrowTheStreamCacheInsteadOfCopying(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", time.Now().Add(-time.Hour), "completed")
	actions := filepath.Join(root, "run-a", actionsFile)
	handler := newViewHandler(root, "", false)
	defer handler.Close()
	store := handler.snapshots

	size := func(path string) int64 {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return info.Size()
	}
	first, err := store.create("run-a")
	if err != nil {
		t.Fatal(err)
	}
	cached := filepath.Join(store.cache.dir, "run-a--"+actionsFile)
	if !strings.HasPrefix(filepath.Base(store.cache.dir), strconv.Itoa(os.Getpid())+"-") || filepath.Dir(store.cache.dir) != filepath.Join(filepath.Dir(root), viewCacheDirName) {
		t.Fatalf("cache dir %s is not this process's own under the viewer cache", store.cache.dir)
	}
	if got, want := size(cached), size(actions); got != want {
		t.Fatalf("cache holds %d bytes after the first snapshot, source has %d", got, want)
	}
	line := `{"schema_version":1,"id":"a-2","type":"file.read","at":"2026-01-01T00:00:00Z","source":"provider_reported","input":{"path":"later.txt"}}` + "\n"
	if err := os.WriteFile(actions, append(mustReadFile(t, actions), line...), 0o600); err != nil {
		t.Fatal(err)
	}
	before := size(cached)
	if _, err := store.create("run-a"); err != nil {
		t.Fatal(err)
	}
	if got, want := size(cached), before+int64(len(line)); got != want {
		t.Fatalf("cache holds %d bytes after growth, want %d", got, want)
	}
	if _, err := store.create("run-a"); err != nil {
		t.Fatal(err)
	}
	if n := len(store.cache.items); n != 2 {
		t.Fatalf("cache has %d entries, want 2 (one per append-only stream)", n)
	}
	if got, want := mustReadFile(t, cached), mustReadFile(t, actions); !bytes.Equal(got, want) {
		t.Fatalf("cache holds\n%s\nsource holds\n%s", got, want)
	}

	// The first snapshot still serves the prefix it captured.
	var page viewActionPage
	viewJSONRequest(t, handler, "/api/snapshots/"+first.SnapshotID+"/actions", &page)
	if page.EndCursor != before {
		t.Fatalf("first snapshot ends at %d, want %d", page.EndCursor, before)
	}

	// A rewritten (shorter) source is copied afresh.
	if err := os.WriteFile(actions, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.create("run-a"); err != nil {
		t.Fatal(err)
	}
	if got := size(cached); got != int64(len(line)) {
		t.Fatalf("cache holds %d bytes after a rewrite, want %d", got, len(line))
	}
	// Rewritten in place, same inode and size, different bytes: caught by the
	// tail comparison and copied afresh.
	changed := bytes.ReplaceAll([]byte(line), []byte("later.txt"), []byte("other.txt"))
	if err := os.WriteFile(actions, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.create("run-a"); err != nil {
		t.Fatal(err)
	}
	if got := mustReadFile(t, cached); !bytes.Equal(got, changed) {
		t.Fatalf("cache still holds the stale prefix:\n%s", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(cached)); !os.IsNotExist(err) {
		t.Fatalf("cache dir after Close: %v", err)
	}
}

func TestViewStreamCacheEvictsOldestAndStaysPerProcess(t *testing.T) {
	root := home(t)
	for _, id := range []string{"run-a", "run-b", "run-c"} {
		writeRun(t, root, id, "claude", time.Now().Add(-time.Hour), "completed")
	}
	old := viewCacheEntries
	viewCacheEntries = 3 // one run's two streams, plus one
	t.Cleanup(func() { viewCacheEntries = old })

	store := newViewSnapshotStore(root)
	defer store.Close()
	for _, id := range []string{"run-a", "run-b"} {
		if _, err := store.create(id); err != nil {
			t.Fatal(err)
		}
	}
	dir := store.cache.dir
	if names := cacheNames(t, dir); len(names) != 3 || names[0] != "run-a--"+providerEventsFile {
		t.Fatalf("after two runs the cache holds %v, want run-a's oldest stream gone", names)
	}
	if _, err := store.create("run-a"); err != nil {
		t.Fatal(err)
	}
	if names := cacheNames(t, dir); strings.Join(names, " ") != "run-a--"+actionsFile+" run-a--"+providerEventsFile+" run-b--"+providerEventsFile {
		t.Fatalf("after run-a again the cache holds %v, want run-b's older stream gone", names)
	}

	// The byte cap evicts too, not just the entry count: a cache allowed
	// fewer bytes than one stream keeps only what it just copied.
	oldBytes := viewCacheBytes
	viewCacheBytes = 1
	t.Cleanup(func() { viewCacheBytes = oldBytes })
	if _, err := store.create("run-c"); err != nil {
		t.Fatal(err)
	}
	if names := cacheNames(t, dir); len(names) != 1 || names[0] != "run-c--"+providerEventsFile {
		t.Fatalf("under a 1-byte cap the cache holds %v, want only the stream it just copied", names)
	}
	viewCacheBytes = oldBytes

	// A directory left by a viewer that is gone is removed by the next one;
	// a live viewer's is not touched.
	stale := filepath.Join(filepath.Dir(root), viewCacheDirName, "999999-gone")
	live := filepath.Join(filepath.Dir(root), viewCacheDirName, strconv.Itoa(os.Getppid())+"-busy")
	for _, d := range []string{stale, live} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	other := newViewStreamCache(filepath.Join(filepath.Dir(root), viewCacheDirName))
	if err := other.prepare(); err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if other.dir == dir {
		t.Fatal("two stores in one process share a cache directory")
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale cache dir: %v", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("live cache dir removed: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("this process's cache dir removed: %v", err)
	}
}

func TestViewStreamCacheRefusesAShortCopy(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", time.Now().Add(-time.Hour), "completed")
	store := newViewSnapshotStore(root)
	defer store.Close()

	actions, err := os.Open(filepath.Join(root, "run-a", actionsFile))
	if err != nil {
		t.Fatal(err)
	}
	defer actions.Close()
	info, err := actions.Stat()
	if err != nil {
		t.Fatal(err)
	}
	// The size seen at the look is larger than what the source can give:
	// the copy must fail rather than record a length it never reached.
	copied, err := store.cache.capture(context.Background(), "run-a", actionsFile, actions, info, info.Size()+100)
	if err == nil {
		copied.Close()
		t.Fatal("a short copy was accepted")
	}
	if !strings.Contains(err.Error(), "of the") {
		t.Fatalf("error = %v, want the copy to say how much it holds", err)
	}
	if _, ok := store.cache.items["run-a/"+actionsFile]; ok {
		t.Fatal("the short copy stayed in the cache")
	}
}

// cacheNames lists the cache directory, oldest entry first by the store's
// own order when it can be told, else by name.
func cacheNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}
