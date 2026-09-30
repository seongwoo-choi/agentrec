package cli

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
