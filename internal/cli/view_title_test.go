package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestViewRunListTitlesComeOnlyFromSafeCompletePrompts(t *testing.T) {
	root := home(t)
	tests := []struct {
		id     string
		prompt []byte
		asDir  bool
		want   string
	}{
		{id: "title", prompt: []byte("\n\t\n  Build the title-first run list  \nignored"), want: "Build the title-first run list"},
		{id: "redacted", prompt: []byte("deploy TOKEN=synthetic-title-secret\n"), want: "deploy TOKEN=[REDACTED:1]"},
		{id: "redacted-double-quoted-multiline", prompt: []byte("TOKEN=\"synthetic-title-secret\nsecond-half\""), want: "TOKEN=\"[REDACTED:1]\""},
		{id: "redacted-single-quoted-multiline", prompt: []byte("TOKEN='synthetic-title-secret\nsecond-half'"), want: "TOKEN='[REDACTED:1]'"},
		{id: "unicode", prompt: []byte(strings.Repeat("界", 121)), want: strings.Repeat("界", 120)},
		{id: "exact-limit", prompt: []byte(strings.Repeat("a", 64<<10)), want: strings.Repeat("a", 120)},
		{id: "oversize", prompt: []byte(strings.Repeat("b", (64<<10)+1))},
		{id: "invalid-utf8", prompt: []byte{0xff, 0xfe}},
		{id: "unreadable", asDir: true},
		{id: "missing"},
	}
	for _, test := range tests {
		writeRun(t, root, test.id, "claude", early, "completed")
		path := filepath.Join(root, test.id, promptFile)
		switch {
		case test.asDir:
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatalf("mkdir prompt for %s: %v", test.id, err)
			}
		case test.prompt != nil:
			if err := os.WriteFile(path, test.prompt, 0o600); err != nil {
				t.Fatalf("write prompt for %s: %v", test.id, err)
			}
		}
	}

	handler := newViewHandler(root, "", false)
	t.Cleanup(func() { _ = handler.Close() })
	request := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	request.Host = "127.0.0.1"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Runs []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var rawBody struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &rawBody); err != nil {
		t.Fatal(err)
	}
	wants := make(map[string]string, len(tests))
	for _, test := range tests {
		wants[test.id] = test.want
	}
	if len(body.Runs) != len(wants) {
		t.Fatalf("runs = %d, want %d; body=%s", len(body.Runs), len(wants), response.Body.String())
	}
	for _, run := range body.Runs {
		if run.Title != wants[run.ID] {
			t.Errorf("run %s title = %q, want %q", run.ID, run.Title, wants[run.ID])
		}
	}
	for _, run := range rawBody.Runs {
		id, _ := run["id"].(string)
		_, hasTitle := run["title"]
		if hasTitle != (wants[id] != "") {
			t.Errorf("run %s title field present = %v, want %v", id, hasTitle, wants[id] != "")
		}
	}
}
