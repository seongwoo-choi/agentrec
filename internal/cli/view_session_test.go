package cli

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seongwoo-choi/agentrec/internal/storage"
)

func TestViewSessionGroupRejectsAmbiguity(t *testing.T) {
	const valid = `{"provider":"claude","mode":"session","sessionId":"s","repoRoot":"/a/project","canonicalCwd":"/a/project"}`
	for name, raw := range map[string]string{
		"duplicate":   strings.Replace(valid, `"sessionId":"s"`, `"sessionId":"s","sessionId":"s"`, 1),
		"case":        strings.Replace(valid, `"sessionId"`, `"SessionId"`, 1),
		"blank":       strings.Replace(valid, `"s"`, `" "`, 1),
		"control":     strings.Replace(valid, `"s"`, `"s\u0000"`, 1),
		"surrogate":   strings.Replace(valid, `"s"`, `"\ud800"`, 1),
		"replacement": strings.Replace(valid, `"s"`, `"\ufffd"`, 1),
		"utf8":        strings.Replace(valid, `"s"`, "\"\xff\"", 1),
		"oversized":   strings.Replace(valid, `"s"`, `"`+strings.Repeat("s", 4097)+`"`, 1),
		"relative":    strings.ReplaceAll(valid, "/a/project", "a/project"),
		"dot":         strings.ReplaceAll(valid, "/a/project", "/a/./project"),
		"outside":     strings.Replace(valid, `"canonicalCwd":"/a/project"`, `"canonicalCwd":"/a/project-other"`, 1),
		"missing":     strings.Replace(valid, `,"canonicalCwd":"/a/project"`, "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if got := viewSessionGroup([]byte(raw)); got != "" {
				t.Fatalf("ambiguous identity linked: %s", got)
			}
			if _, err := decodeManifest([]byte(raw)); err != nil {
				t.Fatalf("ordinary decoding changed: %v", err)
			}
		})
	}
	if g := viewSessionGroup([]byte(valid)); len(g) != 64 {
		t.Fatalf("valid identity unavailable: %q", g)
	}
}

func TestViewSessionGroupHTTP(t *testing.T) {
	root := home(t)
	originals := map[string][]byte{}
	for _, id := range []string{"one", "two", "other-provider", "other-project", "other-session", "trace", "missing"} {
		m := map[string]any{"provider": "claude", "mode": "session", "sessionId": "session", "cwd": "/a/project", "canonicalCwd": "/a/project/sub", "repoRoot": "/a/project", "startedAt": early}
		switch id {
		case "other-provider":
			m["provider"] = "codex"
		case "other-project":
			m["repoRoot"] = "/b/project"
			m["canonicalCwd"] = "/b/project"
		case "other-session":
			m["sessionId"] = "different"
		case "trace":
			m["mode"] = "trace"
		case "missing":
			delete(m, "sessionId")
		}
		if _, err := storage.Create(root, id, storage.Manifest{Provider: "claude", CWD: "/a/project", StartedAt: early}); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(m)
		path := filepath.Join(root, id, manifestFile)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		originals[path] = raw
	}
	h := newViewHandler(root, "one", false)
	t.Cleanup(func() { _ = h.Close() })
	get := func(path string) map[string]any {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.Host = "127.0.0.1:42817"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		var v map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	groups := map[string]any{}
	for _, row := range get("/api/runs")["runs"].([]any) {
		m := row.(map[string]any)
		groups[m["id"].(string)] = m["sessionGroup"]
	}
	if g, ok := groups["one"].(string); !ok || g == "" {
		t.Fatalf("missing opaque sessionGroup: %v", groups)
	}
	if groups["one"] != groups["two"] {
		t.Fatal("same identity separated")
	}
	for _, id := range []string{"other-provider", "other-project", "other-session", "trace", "missing"} {
		if groups[id] == groups["one"] {
			t.Fatalf("grouped %s", id)
		}
	}
	for id, g := range groups {
		if got := get("/api/runs/" + id)["run"].(map[string]any)["sessionGroup"]; got != g {
			t.Fatalf("list/detail mismatch %s: %v != %v", id, got, g)
		}
	}
	for path, raw := range originals {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatalf("manifest changed: %s", path)
		}
	}
}
