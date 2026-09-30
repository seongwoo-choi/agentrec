package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/storage"
)

// The stored filename deliberately has no relationship to the sanitized display path.
func storedTextFixture(t *testing.T, text string, edit func(map[string]any)) (string, string, string) {
	t.Helper()
	root := home(t)
	b, err := storage.Create(root, "stored-text", storage.Manifest{Provider: "claude", CWD: "/workspace-do-not-read", StartedAt: early})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Finalize(storage.Finalization{EndedAt: late, ExitReason: "completed"}); err != nil {
		t.Fatal(err)
	}
	git := filepath.Join(b.Dir(), "git")
	if err := os.MkdirAll(filepath.Join(git, "untracked"), 0700); err != nil {
		t.Fatal(err)
	}
	storedAs := "untracked/" + strings.Repeat("a", 64) + ".txt"
	file := map[string]any{"path": "notes/[REDACTED:1] & new.txt", "kind": "file", "size": len(text), "stored": true, "storedAs": storedAs, "hashBasis": "sanitized", "sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(text)))}
	if edit != nil {
		edit(file)
	}
	raw, err := json.Marshal(map[string]any{"attribution": "observed during run, not causal proof", "count": 1, "stored": 1, "files": []any{file}})
	if err != nil {
		t.Fatal(err)
	}
	writeViewFixture(t, filepath.Join(git, "untracked.json"), string(raw))
	writeViewFixture(t, filepath.Join(git, "result.json"), `{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","trackedFiles":0,"added":0,"deleted":0,"untrackedFiles":1,"storedTextFiles":1}`)
	writeViewFixture(t, filepath.Join(git, "tracked-stat.json"), `{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","files":[],"totals":{"files":0,"additions":0,"deletions":0,"binary":0}}`)
	bodyPath := filepath.Join(git, storedAs)
	writeViewFixture(t, bodyPath, text)
	return root, b.Dir(), bodyPath
}

func storedTextSnapshot(t *testing.T, root string) (*viewHandler, string) {
	t.Helper()
	h := newViewHandler(root, "stored-text", false)
	t.Cleanup(func() {
		if err := h.Close(); err != nil {
			t.Error(err)
		}
	})
	var detail viewRunResponse
	viewJSONRequest(t, h, "/api/runs/stored-text", &detail)
	return h, "/api/snapshots/" + detail.SnapshotID + "/stored-text?path=" + url.QueryEscape("notes/[REDACTED:1] & new.txt")
}

func storedTextRequest(h *viewHandler, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", target, nil)
	req.Host = "localhost:42817"
	h.ServeHTTP(rec, req)
	return rec
}

func TestViewStoredTextRefusesUnverifiedBytes(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		edit   func(map[string]any)
		mutate func(*testing.T, string)
		reason string
	}{
		{name: "missing", text: "secret", mutate: func(t *testing.T, p string) {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
		}, reason: "cannot open"},
		{name: "in-place-mismatch", text: "secret", mutate: func(t *testing.T, p string) { writeViewFixture(t, p, "changed") }, reason: "digest"},
		{name: "missing-digest", text: "secret", edit: func(m map[string]any) { delete(m, "sha256") }, reason: "identity"},
		{name: "legacy-basis", text: "secret", edit: func(m map[string]any) { m["hashBasis"] = "original" }, reason: "identity"},
		{name: "missing-basis", text: "secret", edit: func(m map[string]any) { delete(m, "hashBasis") }, reason: "identity"},
		{name: "uppercase-digest", text: "secret", edit: func(m map[string]any) { m["sha256"] = strings.Repeat("A", 64) }, reason: "identity"},
		{name: "invalid-storedAs", text: "secret", edit: func(m map[string]any) { m["storedAs"] = "untracked/arbitrary.txt" }, reason: "identity"},
		{name: "traversal", text: "secret", edit: func(m map[string]any) { m["storedAs"] = "../manifest.json" }, reason: "identity"},
		{name: "absolute", text: "secret", edit: func(m map[string]any) { m["storedAs"] = "/etc/passwd" }, reason: "identity"},
		{name: "wrong-prefix", text: "secret", edit: func(m map[string]any) { m["storedAs"] = "git/untracked/" + strings.Repeat("a", 64) + ".txt" }, reason: "identity"},
		{name: "invalid-utf8", text: string([]byte{0xff}), reason: "UTF-8"},
		{name: "incomplete-utf8", text: string([]byte{0xe2, 0x82}), reason: "UTF-8"},
		{name: "over-limit", text: strings.Repeat("x", (1<<20)+1), reason: "1 MiB"},
		{name: "directory", text: "secret", mutate: func(t *testing.T, p string) {
			os.Remove(p)
			if err := os.Mkdir(p, 0700); err != nil {
				t.Fatal(err)
			}
		}, reason: "cannot open"},
		{name: "FIFO", text: "secret", mutate: func(t *testing.T, p string) {
			os.Remove(p)
			if err := syscall.Mkfifo(p, 0600); err != nil {
				t.Fatal(err)
			}
		}, reason: "cannot open"},
		{name: "symlink", text: "secret", mutate: func(t *testing.T, p string) {
			os.Remove(p)
			if err := os.Symlink("/dev/zero", p); err != nil {
				t.Fatal(err)
			}
		}, reason: "cannot open"},
		{name: "ancestor-symlink", text: "secret", mutate: func(t *testing.T, p string) {
			dir := filepath.Dir(p)
			if err := os.Rename(dir, dir+"-held"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(dir+"-held", dir); err != nil {
				t.Fatal(err)
			}
		}, reason: "cannot open"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root, _, body := storedTextFixture(t, tc.text, tc.edit)
			h, target := storedTextSnapshot(t, root) // Invalid optional identity must not break run/metadata.
			if tc.mutate != nil {
				tc.mutate(t, body)
			}
			start := time.Now()
			rec := storedTextRequest(h, target)
			if time.Since(start) > time.Second {
				t.Fatal("stored reader blocked")
			}
			if rec.Code != 400 || !strings.Contains(rec.Body.String(), tc.reason) || strings.Contains(rec.Body.String(), `"text":`) {
				t.Fatalf("unverified body: status=%d %.200s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestViewStoredTextExactLimitAndPinnedIdentity(t *testing.T) {
	text := strings.Repeat("x", 1<<20)
	root, runDir, body := storedTextFixture(t, text, nil)
	h, target := storedTextSnapshot(t, root)
	// Moving the visible run path cannot redirect the held root to a replacement.
	if err := os.Rename(runDir, runDir+"-held"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(body), 0700); err != nil {
		t.Fatal(err)
	}
	writeViewFixture(t, body, "outside replacement")
	rec := storedTextRequest(h, target)
	var got struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || got.Text != text {
		t.Fatalf("held root/limit failed: status=%d length=%d", rec.Code, len(got.Text))
	}
}

func TestViewStoredTextOnlyRecordedDisplayPath(t *testing.T) {
	root, _, _ := storedTextFixture(t, "recorded", nil)
	h, target := storedTextSnapshot(t, root)
	base := strings.Split(target, "?")[0]
	for _, query := range []string{"path=../manifest.json", "path=/etc/passwd", "path=notes%2f..%2fmanifest.json", "path=untracked/" + strings.Repeat("a", 64) + ".txt", "path=missing", "path=%ff", "path=", "", strings.Split(target, "?")[1] + "&storedAs=manifest.json", strings.Split(target, "?")[1] + "&path=other"} {
		rec := storedTextRequest(h, base+"?"+query)
		if rec.Code != 400 || strings.Contains(rec.Body.String(), `"text":`) {
			t.Fatalf("query %q => %d %s", query, rec.Code, rec.Body.String())
		}
	}
}

func TestViewStoredTextCancelledRequest(t *testing.T) {
	root, _, _ := storedTextFixture(t, "recorded", nil)
	h, target := storedTextSnapshot(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("GET", target, nil).WithContext(ctx)
	req.Host = "localhost:42817"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == 200 || strings.Contains(rec.Body.String(), `"text":`) {
		t.Fatalf("cancelled read served bytes: %d %s", rec.Code, rec.Body.String())
	}
}

func TestViewStoredTextSnapshotRootsClose(t *testing.T) {
	root, _, _ := storedTextFixture(t, "recorded", nil)
	store := newViewSnapshotStore(root)
	t.Cleanup(func() { _ = store.Close() })
	first, err := store.create("stored-text")
	if err != nil {
		t.Fatal(err)
	}
	var held *os.Root
	found, err := store.withSnapshot(first.SnapshotID, func(snapshot *viewSnapshot) error {
		held = snapshot.sourceRoot
		return nil
	})
	if err != nil || !found || held == nil {
		t.Fatalf("missing held root: %v", err)
	}
	for i := 0; i < maxViewSnapshots; i++ {
		if _, err := store.create("stored-text"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := held.Stat("."); err == nil {
		t.Fatal("eviction retained an open source root")
	}
	latest, err := store.create("stored-text")
	if err != nil {
		t.Fatal(err)
	}
	found, err = store.withSnapshot(latest.SnapshotID, func(snapshot *viewSnapshot) error {
		held = snapshot.sourceRoot
		return nil
	})
	if err != nil || !found || held == nil {
		t.Fatalf("missing latest root: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := held.Stat("."); err == nil {
		t.Fatal("store close retained an open source root")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestViewStoredTextHTTP(t *testing.T) {
	for _, text := range []string{"<script>alert('literal')</script>\n한글", ""} {
		t.Run(fmt.Sprintf("bytes-%d", len(text)), func(t *testing.T) {
			root, _, _ := storedTextFixture(t, text, nil)
			h, target := storedTextSnapshot(t, root)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", target, nil)
			req.Host = "localhost:42817"
			h.ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("stored text status=%d body=%s", rec.Code, rec.Body.String())
			}
			var got struct {
				Path        string `json:"path"`
				Text        string `json:"text"`
				Attribution string `json:"attribution"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Text != text || got.Path != "notes/[REDACTED:1] & new.txt" || got.Attribution != "observed during run, not causal proof" {
				t.Fatalf("unexpected stored text: %+v", got)
			}
		})
	}
}
