package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/seongwoo-choi/agentrec/internal/action"
	"github.com/seongwoo-choi/agentrec/internal/evidence"
	"github.com/seongwoo-choi/agentrec/internal/report"
	"github.com/seongwoo-choi/agentrec/internal/storage"
)

func TestCloseViewReportsCleanupFailure(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer

	if code := closeView(file, &stderr, 0); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "cli: close viewer:") {
		t.Fatalf("stderr = %q, want viewer cleanup error", stderr.String())
	}
}

func TestViewAPIIncludesRequestActionsEventsAndEvidence(t *testing.T) {
	root := home(t)
	b, err := storage.Create(root, "run-ui", storage.Manifest{
		Provider: "claude", ProviderVersion: "999.0.0", VersionUnverified: true,
		Argv: []string{"claude", "-p", "inspect this repository"}, CWD: "/tmp/agentrec", StartedAt: early,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WritePrompt("inspect this repository"); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteAction(action.Action{
		ID: "search-1", Type: action.TypeSearch, Provider: "claude",
		Assurance: action.AssuranceProviderReported, StartedAt: early,
		Status: "completed", Input: json.RawMessage(`{"query":"Action Timeline"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProviderEvent([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Search"}]}}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProcessResult(processResultJSON(t, early, "completed")); err != nil {
		t.Fatal(err)
	}
	if err := b.Finalize(storage.Finalization{EndedAt: late, ExitReason: "completed"}); err != nil {
		t.Fatal(err)
	}

	handler := newViewHandler(root, "run-ui", false)
	t.Cleanup(func() { _ = handler.Close() })
	request := httptest.NewRequest(http.MethodGet, "/api/runs/run-ui", nil)
	request.Host = "127.0.0.1:42817"
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	var body struct {
		SchemaVersion int    `json:"schemaVersion"`
		SnapshotID    string `json:"snapshotId"`
		ActionCount   int    `json:"actionCount"`
		EventCount    int    `json:"eventCount"`
		Run           struct {
			ID                string `json:"id"`
			Provider          string `json:"provider"`
			Prompt            string `json:"prompt"`
			ProviderVersion   string `json:"providerVersion"`
			VersionUnverified bool   `json:"versionUnverified"`
		} `json:"run"`
		ProviderEvents struct {
			Attribution string `json:"attribution"`
			Present     bool   `json:"present"`
		} `json:"providerEvents"`
		Changes struct {
			Status    string `json:"status"`
			Total     int    `json:"total"`
			Tracked   int    `json:"tracked"`
			Untracked int    `json:"untracked"`
			Additions int    `json:"additions"`
			Deletions int    `json:"deletions"`
		} `json:"changes"`
		Evidence struct {
			Supervisor []viewField `json:"supervisor"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.SchemaVersion != 1 || body.Run.ID != "run-ui" || body.Run.Provider != "claude" {
		t.Fatalf("unexpected run envelope: %+v", body.Run)
	}
	if body.Run.Prompt != "inspect this repository" {
		t.Errorf("prompt = %q", body.Run.Prompt)
	}
	if body.Run.ProviderVersion != "999.0.0" || !body.Run.VersionUnverified {
		t.Errorf("unsupported provider warning metadata = %+v", body.Run)
	}
	if body.ActionCount != 1 || body.EventCount != 1 || body.SnapshotID == "" {
		t.Fatalf("stream metadata = snapshot %q actions %d events %d", body.SnapshotID, body.ActionCount, body.EventCount)
	}
	if body.Changes.Status != "unavailable" || body.Changes.Total != 0 {
		t.Fatalf("change summary = %+v", body.Changes)
	}
	if !body.ProviderEvents.Present || body.ProviderEvents.Attribution != "provider_reported" {
		t.Fatalf("provider events = %+v", body.ProviderEvents)
	}
	var actionsPage viewActionPage
	viewJSONRequest(t, handler, "/api/snapshots/"+body.SnapshotID+"/actions?cursor=0", &actionsPage)
	if len(actionsPage.Items) != 1 || actionsPage.Items[0].Type != action.TypeSearch || actionsPage.NextCursor != nil {
		t.Fatalf("actions page = %+v", actionsPage)
	}
	var eventsPage viewEventPage
	viewJSONRequest(t, handler, "/api/snapshots/"+body.SnapshotID+"/events?cursor=0", &eventsPage)
	if len(eventsPage.Items) != 1 || eventsPage.NextCursor != nil {
		t.Fatalf("events page = %+v", eventsPage)
	}
	if len(body.Evidence.Supervisor) == 0 {
		t.Fatal("supervisor evidence is empty")
	}
}

func TestViewChangesExposeImmutableTrackedAndUntrackedEvidence(t *testing.T) {
	root := home(t)
	b, err := storage.Create(root, "run-changes", storage.Manifest{
		Provider: "claude", Argv: []string{"claude"}, CWD: "/tmp/alias", CanonicalCWD: "/tmp/agentrec", RepoRoot: "/tmp/agentrec", StartedAt: early,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProcessResult(processResultJSON(t, early, "completed")); err != nil {
		t.Fatal(err)
	}
	gitPath := filepath.Join(b.Dir(), gitDir)
	if err := os.Mkdir(gitPath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeViewFixture(t, filepath.Join(gitPath, resultFile), `{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","trackedFiles":1,"added":2,"deleted":1,"untrackedFiles":1,"storedTextFiles":1}`)
	writeViewFixture(t, filepath.Join(gitPath, "tracked-stat.json"), `{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","files":[{"path":"dir/a file.txt","additions":2,"deletions":1}],"totals":{"files":1,"additions":2,"deletions":1,"binary":0}}`)
	writeViewFixture(t, filepath.Join(gitPath, "untracked.json"), `{"attribution":"observed during run, not causal proof","count":1,"stored":1,"files":[{"path":"new.txt","kind":"file","mode":"-rw-------","size":4,"stored":true,"storedAs":"git/untracked/new.txt"}]}`)
	patch := "diff --git \"a/dir/a file.txt\" \"b/dir/a file.txt\"\n--- \"a/dir/a file.txt\"\n+++ \"b/dir/a file.txt\"\n@@ -1 +1,2 @@\n-old\n+new\n+line\n"
	patchPath := filepath.Join(gitPath, "tracked.patch")
	writeViewFixture(t, patchPath, patch)
	if err := b.WriteAction(action.Action{ID: "edit-match", Type: action.TypeFileEdit, Provider: "claude", Assurance: action.AssuranceProviderReported, Status: "completed", Input: json.RawMessage(`{"file_path":"/tmp/alias/dir/a file.txt"}`), RepositoryPaths: []string{"dir/a file.txt"}, RepositoryPathsRecorded: true}); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteAction(action.Action{ID: "shell-text-only", Type: action.TypeShellExec, Provider: "claude", Assurance: action.AssuranceProviderReported, Status: "completed", Input: json.RawMessage(`{"command":"cat 'dir/a file.txt'"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := b.Finalize(storage.Finalization{EndedAt: late, ExitReason: "completed"}); err != nil {
		t.Fatal(err)
	}

	handler := newViewHandler(root, "run-changes", false)
	t.Cleanup(func() { _ = handler.Close() })
	var run struct {
		SnapshotID string            `json:"snapshotId"`
		Changes    viewChangeSummary `json:"changes"`
	}
	viewJSONRequest(t, handler, "/api/runs/run-changes", &run)
	if run.Changes.Status != "available" || run.Changes.Total != 2 || run.Changes.Tracked != 1 || run.Changes.Untracked != 1 || run.Changes.Additions != 2 || run.Changes.Deletions != 1 {
		t.Fatalf("change summary = %+v", run.Changes)
	}
	var actions struct {
		Items []struct {
			ID               string   `json:"id"`
			SamePathObserved []string `json:"samePathObserved"`
		} `json:"items"`
	}
	viewJSONRequest(t, handler, "/api/snapshots/"+run.SnapshotID+"/actions?cursor=0", &actions)
	if len(actions.Items) != 2 || !slices.Equal(actions.Items[0].SamePathObserved, []string{"dir/a file.txt"}) || len(actions.Items[1].SamePathObserved) != 0 {
		t.Fatalf("action path correlations = %+v", actions.Items)
	}
	var changes struct {
		Attribution string `json:"attribution"`
		Baseline    string `json:"baseline"`
		Total       int    `json:"total"`
		Items       []struct {
			Path      string `json:"path"`
			Tracked   bool   `json:"tracked"`
			Additions *int   `json:"additions"`
			Deletions *int   `json:"deletions"`
			Stored    bool   `json:"stored"`
		} `json:"items"`
	}
	viewJSONRequest(t, handler, "/api/snapshots/"+run.SnapshotID+"/changes?cursor=0", &changes)
	if changes.Attribution != "observed during run, not causal proof" || changes.Baseline != "abc123" || changes.Total != 2 || len(changes.Items) != 2 {
		t.Fatalf("changes = %+v", changes)
	}
	if got := changes.Items[0]; got.Path != "dir/a file.txt" || !got.Tracked || got.Additions == nil || *got.Additions != 2 || got.Deletions == nil || *got.Deletions != 1 {
		t.Fatalf("tracked change = %+v", got)
	}
	if got := changes.Items[1]; got.Path != "new.txt" || got.Tracked || !got.Stored {
		t.Fatalf("untracked change = %+v", got)
	}

	target := "/api/snapshots/" + run.SnapshotID + "/patch?path=dir%2Fa+file.txt&cursor=0"
	var before struct {
		Path  string `json:"path"`
		Patch string `json:"patch"`
	}
	viewJSONRequest(t, handler, target, &before)
	if before.Path != "dir/a file.txt" || before.Patch != patch {
		t.Fatalf("patch = %+v", before)
	}
	writeViewFixture(t, patchPath, strings.ReplaceAll(patch, "new", "BAD"))
	var after struct {
		Path  string `json:"path"`
		Patch string `json:"patch"`
	}
	viewJSONRequest(t, handler, target, &after)
	if after != before {
		t.Fatalf("patch changed after source mutation: before=%+v after=%+v", before, after)
	}
}

func TestViewSamePathObservationsUseOnlyExplicitFileActionPaths(t *testing.T) {
	changed := map[string]struct{}{"dir/a.txt": {}, "pkg/file.go": {}, "README.md": {}}
	tests := []struct {
		name         string
		item         action.Action
		canonicalCWD string
		repoRoot     string
		want         []string
	}{
		{name: "claude absolute file path", item: action.Action{Type: action.TypeFileEdit, Input: json.RawMessage(`{"file_path":"/repo/dir/a.txt"}`)}, canonicalCWD: "/repo", repoRoot: "/repo", want: []string{"dir/a.txt"}},
		{name: "codex changes", item: action.Action{Type: action.TypeFileEdit, Input: json.RawMessage(`{"changes":[{"path":"./dir/a.txt"}]}`)}, canonicalCWD: "/repo", repoRoot: "/repo", want: []string{"dir/a.txt"}},
		{name: "subdirectory relative", item: action.Action{Type: action.TypeFileEdit, Input: json.RawMessage(`{"path":"file.go"}`)}, canonicalCWD: "/repo/pkg", repoRoot: "/repo", want: []string{"pkg/file.go"}},
		{name: "subdirectory parent", item: action.Action{Type: action.TypeFileRead, Input: json.RawMessage(`{"file_path":"../README.md"}`)}, canonicalCWD: "/repo/pkg", repoRoot: "/repo", want: []string{"README.md"}},
		{name: "persisted alias absolute subdirectory", item: action.Action{Type: action.TypeFileEdit, RepositoryPaths: []string{"pkg/file.go"}, RepositoryPathsRecorded: true}, canonicalCWD: "/repo/pkg", repoRoot: "/repo", want: []string{"pkg/file.go"}},
		{name: "persisted alias absolute parent", item: action.Action{Type: action.TypeFileRead, RepositoryPaths: []string{"README.md"}, RepositoryPathsRecorded: true}, canonicalCWD: "/repo/pkg", repoRoot: "/repo", want: []string{"README.md"}},
		{name: "recorded empty does not fall back", item: action.Action{Type: action.TypeFileRead, Input: json.RawMessage(`{"file_path":"/repo/dir/a.txt"}`), RepositoryPathsRecorded: true}, canonicalCWD: "/repo", repoRoot: "/repo"},
		{name: "command text", item: action.Action{Type: action.TypeShellExec, Input: json.RawMessage(`{"command":"cat dir/a.txt"}`)}, canonicalCWD: "/repo", repoRoot: "/repo"},
		{name: "outside repository", item: action.Action{Type: action.TypeFileRead, Input: json.RawMessage(`{"file_path":"/other/dir/a.txt"}`)}, canonicalCWD: "/repo/pkg", repoRoot: "/repo"},
		{name: "root repository", item: action.Action{Type: action.TypeFileWrite, Input: json.RawMessage(`{"file_path":"/dir/a.txt"}`)}, canonicalCWD: "/", repoRoot: "/", want: []string{"dir/a.txt"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := viewSamePathObservations(tt.item, tt.canonicalCWD, tt.repoRoot, changed); !slices.Equal(got, tt.want) {
				t.Fatalf("observations = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestViewRepositoryRootRequiresAbsoluteAncestor(t *testing.T) {
	tests := []struct {
		cwd, root string
		want      bool
	}{
		{"/repo/pkg", "", true},
		{"/repo/pkg", "/repo", true},
		{"/repo/pkg", "/repo/pkg", true},
		{"/repo/pkg", "/other", false},
		{"/repo/pkg", "repo", false},
	}
	for _, test := range tests {
		if got := validViewRepositoryRoot(test.cwd, test.root); got != test.want {
			t.Fatalf("validViewRepositoryRoot(%q, %q) = %t, want %t", test.cwd, test.root, got, test.want)
		}
	}
}

func TestCanonicalManifestCWDResolvesSymlink(t *testing.T) {
	realRoot := t.TempDir()
	realCWD := filepath.Join(realRoot, "pkg")
	if err := os.Mkdir(realCWD, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realRoot, alias); err != nil {
		t.Fatal(err)
	}
	got, err := canonicalManifestCWD(filepath.Join(alias, "pkg"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(realCWD)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("canonicalManifestCWD = %q, want %q", got, want)
	}
}

func TestCanonicalManifestPathsResolveCWDAndRepoRootSymlinks(t *testing.T) {
	realRoot := t.TempDir()
	realCWD := filepath.Join(realRoot, "pkg")
	if err := os.Mkdir(realCWD, 0o700); err != nil {
		t.Fatal(err)
	}
	aliasRoot := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Fatal(err)
	}
	gotCWD, gotRoot, err := canonicalManifestPaths(filepath.Join(aliasRoot, "pkg"), aliasRoot)
	if err != nil {
		t.Fatal(err)
	}
	wantRoot, err := filepath.EvalSymlinks(realRoot)
	if err != nil {
		t.Fatal(err)
	}
	if gotCWD != filepath.Join(wantRoot, "pkg") || gotRoot != wantRoot {
		t.Fatalf("canonicalManifestPaths = (%q, %q), want (%q, %q)", gotCWD, gotRoot, filepath.Join(wantRoot, "pkg"), wantRoot)
	}
}

func TestObserveActionRepositoryPathsResolvesAliasesAndMissingFiles(t *testing.T) {
	realRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(realRoot, "pkg"), 0o700); err != nil {
		t.Fatal(err)
	}
	aliasRoot := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(realRoot)
	if err != nil {
		t.Fatal(err)
	}
	item := action.Action{
		Type:  action.TypeFileEdit,
		Input: json.RawMessage(`{"file_path":"` + filepath.Join(aliasRoot, "pkg", "new.go") + `"}`),
	}
	canonicalCWD := filepath.Join(canonicalRoot, "pkg")
	got := observeActionRepositoryPaths(item, filepath.Join(aliasRoot, "pkg"), canonicalCWD, canonicalRoot)
	if !slices.Equal(got, []string{"pkg/new.go"}) {
		t.Fatalf("repository paths = %v, want [pkg/new.go]", got)
	}
	shell := action.Action{Type: action.TypeShellExec, Input: json.RawMessage(`{"command":"cat pkg/new.go"}`)}
	if got := observeActionRepositoryPaths(shell, aliasRoot, canonicalRoot, canonicalRoot); len(got) != 0 {
		t.Fatalf("shell repository paths = %v, want none", got)
	}
	otherRoot := t.TempDir()
	if err := os.Remove(aliasRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(otherRoot, aliasRoot); err != nil {
		t.Fatal(err)
	}
	if got := observeActionRepositoryPaths(item, filepath.Join(aliasRoot, "pkg"), canonicalCWD, canonicalRoot); !slices.Equal(got, []string{"pkg/new.go"}) {
		t.Fatalf("repository paths after alias repoint = %v, want [pkg/new.go]", got)
	}
}

func TestViewSnapshotRejectsRepositoryRootOutsideCWD(t *testing.T) {
	root := home(t)
	started := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	b, err := storage.Create(root, "run-invalid-root", storage.Manifest{
		Provider: "claude", Argv: []string{"claude"}, CWD: "/repo/pkg", RepoRoot: "/other", StartedAt: started,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WritePrompt("inspect"); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProcessResult(processResultJSON(t, started, "completed")); err != nil {
		t.Fatal(err)
	}
	if err := b.Finalize(storage.Finalization{EndedAt: started, ExitReason: "completed"}); err != nil {
		t.Fatal(err)
	}
	store := newViewSnapshotStore(root)
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.create("run-invalid-root"); err == nil {
		t.Fatal("snapshot accepted repository root outside CWD")
	}
}

func TestViewRunRemainsReadableWhenChangeListEvidenceIsIncomplete(t *testing.T) {
	root := home(t)
	b, err := storage.Create(root, "run-incomplete-changes", storage.Manifest{
		Provider: "claude", Argv: []string{"claude"}, CWD: "/tmp/agentrec", StartedAt: early,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteAction(action.Action{ID: "read-1", Type: action.TypeFileRead, Provider: "claude", Assurance: action.AssuranceProviderReported, Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	gitPath := filepath.Join(b.Dir(), gitDir)
	if err := os.Mkdir(gitPath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeViewFixture(t, filepath.Join(gitPath, resultFile), `{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","trackedFiles":0,"added":0,"deleted":0,"untrackedFiles":0,"storedTextFiles":0}`)
	if err := b.Finalize(storage.Finalization{EndedAt: late, ExitReason: "completed"}); err != nil {
		t.Fatal(err)
	}

	handler := newViewHandler(root, "run-incomplete-changes", false)
	t.Cleanup(func() { _ = handler.Close() })
	var run struct {
		SnapshotID  string `json:"snapshotId"`
		ActionCount int    `json:"actionCount"`
		Changes     struct {
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"changes"`
	}
	viewJSONRequest(t, handler, "/api/runs/run-incomplete-changes", &run)
	if run.SnapshotID == "" || run.ActionCount != 1 {
		t.Fatalf("run detail = snapshot %q actions %d", run.SnapshotID, run.ActionCount)
	}
	if run.Changes.Status != "unavailable" || run.Changes.Reason == "" {
		t.Fatalf("run change summary = %+v", run.Changes)
	}
	var changes viewChangePage
	viewJSONRequest(t, handler, "/api/snapshots/"+run.SnapshotID+"/changes?cursor=0", &changes)
	if changes.Status != "unavailable" || changes.Reason == "" || changes.Total != 0 || len(changes.Items) != 0 {
		t.Fatalf("changes = %+v", changes)
	}
}

func TestViewPatchPagesPreserveUTF8Boundaries(t *testing.T) {
	patch := strings.Repeat("x", viewPageBytes-1) + "가rest"
	path := filepath.Join(t.TempDir(), "tracked.patch")
	writeViewFixture(t, path, patch)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	snapshot := &viewSnapshot{
		patch: file,
		patchSections: map[string]viewPatchSection{
			"unicode.txt": {start: 0, end: int64(len(patch))},
		},
		changeAttribution: evidence.Attribution,
	}

	first, err := readViewPatchPage(snapshot, "unicode.txt", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(first.Patch) || first.NextCursor == nil {
		t.Fatalf("first page split UTF-8: valid=%t next=%v", utf8.ValidString(first.Patch), first.NextCursor)
	}
	second, err := readViewPatchPage(snapshot, "unicode.txt", *first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if first.Patch+second.Patch != patch {
		t.Fatal("patch pages did not preserve exact UTF-8 text")
	}
}

func TestPrepareViewChangesRejectsInvalidUTF8InsideTrackedPatch(t *testing.T) {
	patch := append([]byte("diff --git a/bad.txt b/bad.txt\n@@ -0,0 +1 @@\n+before"), 0xff)
	patch = append(patch, []byte("after\n")...)
	path := filepath.Join(t.TempDir(), "tracked.patch")
	if err := os.WriteFile(path, patch, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	snapshot := &viewSnapshot{
		documents: map[string][]byte{
			gitDir + "/" + resultFile:           []byte(`{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","trackedFiles":1,"added":1,"deleted":0,"untrackedFiles":0,"storedTextFiles":0}`),
			gitDir + "/" + trackedStatFile:      []byte(`{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","files":[{"path":"bad.txt","additions":1,"deletions":0}],"totals":{"files":1,"additions":1,"deletions":0,"binary":0}}`),
			gitDir + "/" + untrackedChangesFile: []byte(`{"attribution":"observed during run, not causal proof","count":0,"stored":0,"files":[]}`),
		},
		patch: file, patchSize: int64(len(patch)),
	}
	if err := prepareViewChanges(snapshot); err == nil {
		t.Fatal("prepareViewChanges accepted invalid UTF-8 inside tracked.patch")
	}
}

func TestPrepareViewChangesRejectsDuplicateTrackedPath(t *testing.T) {
	patch := "diff --git a/a.txt b/a.txt\n" +
		"diff --git a/hidden.txt b/hidden.txt\n"
	path := filepath.Join(t.TempDir(), "tracked.patch")
	writeViewFixture(t, path, patch)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	snapshot := &viewSnapshot{
		documents: map[string][]byte{
			gitDir + "/" + resultFile:           []byte(`{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","trackedFiles":2,"added":2,"deleted":0,"untrackedFiles":0,"storedTextFiles":0}`),
			gitDir + "/" + trackedStatFile:      []byte(`{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","files":[{"path":"a.txt","additions":1,"deletions":0},{"path":"a.txt","additions":1,"deletions":0}],"totals":{"files":2,"additions":2,"deletions":0,"binary":0}}`),
			gitDir + "/" + untrackedChangesFile: []byte(`{"attribution":"observed during run, not causal proof","count":0,"stored":0,"files":[]}`),
		},
		patch: file, patchSize: int64(len(patch)),
	}
	if err := prepareViewChanges(snapshot); err == nil {
		t.Fatal("prepareViewChanges accepted a duplicate tracked path")
	}
}

func TestPrepareViewChangesRejectsNonCanonicalUntrackedPath(t *testing.T) {
	snapshot := &viewSnapshot{documents: map[string][]byte{
		gitDir + "/" + resultFile:           []byte(`{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","trackedFiles":0,"added":0,"deleted":0,"untrackedFiles":1,"storedTextFiles":0}`),
		gitDir + "/" + trackedStatFile:      []byte(`{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","files":[],"totals":{"files":0,"additions":0,"deletions":0,"binary":0}}`),
		gitDir + "/" + untrackedChangesFile: []byte(`{"attribution":"observed during run, not causal proof","count":1,"stored":0,"files":[{"path":"../outside","kind":"file","mode":"-rw-------","size":1,"stored":false,"reason":"not stored"}]}`),
	}}
	if err := prepareViewChanges(snapshot); err == nil {
		t.Fatal("prepareViewChanges accepted a non-canonical untracked path")
	}
}

func TestPrepareViewChangesRejectsInvalidUTF8InJSONEvidence(t *testing.T) {
	untracked := append([]byte(`{"attribution":"observed during run, not causal proof","count":1,"stored":0,"files":[{"path":"bad`), 0xff)
	untracked = append(untracked, []byte(`.txt","kind":"file","mode":"-rw-------","size":1,"stored":false,"reason":"not stored"}]}`)...)
	snapshot := &viewSnapshot{documents: map[string][]byte{
		gitDir + "/" + resultFile:           []byte(`{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","trackedFiles":0,"added":0,"deleted":0,"untrackedFiles":1,"storedTextFiles":0}`),
		gitDir + "/" + trackedStatFile:      []byte(`{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","files":[],"totals":{"files":0,"additions":0,"deletions":0,"binary":0}}`),
		gitDir + "/" + untrackedChangesFile: untracked,
	}}
	if err := prepareViewChanges(snapshot); err == nil {
		t.Fatal("prepareViewChanges accepted invalid UTF-8 in JSON evidence")
	}
}

func TestPrepareViewChangesRejectsUnknownStatus(t *testing.T) {
	snapshot := &viewSnapshot{documents: map[string][]byte{
		gitDir + "/" + resultFile:           []byte(`{"status":"future","attribution":"observed during run, not causal proof","baseline":"abc123","trackedFiles":0,"added":0,"deleted":0,"untrackedFiles":0,"storedTextFiles":0}`),
		gitDir + "/" + trackedStatFile:      []byte(`{"status":"future","attribution":"observed during run, not causal proof","baseline":"abc123","files":[],"totals":{"files":0,"additions":0,"deletions":0,"binary":0}}`),
		gitDir + "/" + untrackedChangesFile: []byte(`{"attribution":"observed during run, not causal proof","count":0,"stored":0,"files":[]}`),
	}}
	if err := prepareViewChanges(snapshot); err == nil {
		t.Fatal("prepareViewChanges accepted an unknown repository status")
	}
}

func TestPrepareViewChangesRejectsUnknownStatusBeforeMissingLists(t *testing.T) {
	snapshot := &viewSnapshot{documents: map[string][]byte{
		gitDir + "/" + resultFile: []byte(`{"status":"future","attribution":"observed during run, not causal proof","baseline":"abc123"}`),
	}}
	if err := prepareViewChanges(snapshot); err == nil {
		t.Fatal("prepareViewChanges accepted an unknown status before missing list evidence")
	}
}

func TestPrepareViewChangesRejectsTrackedDataWhenUnavailable(t *testing.T) {
	snapshot := &viewSnapshot{documents: map[string][]byte{
		gitDir + "/" + resultFile:           []byte(`{"status":"unavailable","reason":"patch_too_large","attribution":"observed during run, not causal proof","baseline":"abc123","trackedFiles":1,"added":1,"deleted":0,"untrackedFiles":0,"storedTextFiles":0}`),
		gitDir + "/" + trackedStatFile:      []byte(`{"status":"unavailable","reason":"patch_too_large","attribution":"observed during run, not causal proof","baseline":"abc123","files":[{"path":"a.txt","additions":1,"deletions":0}],"totals":{"files":1,"additions":1,"deletions":0,"binary":0}}`),
		gitDir + "/" + untrackedChangesFile: []byte(`{"attribution":"observed during run, not causal proof","count":0,"stored":0,"files":[]}`),
	}}
	if err := prepareViewChanges(snapshot); err == nil {
		t.Fatal("prepareViewChanges accepted tracked data with unavailable status")
	}
}

func TestPrepareViewChangesKeepsPendingStatusUnavailable(t *testing.T) {
	snapshot := &viewSnapshot{documents: map[string][]byte{
		gitDir + "/" + resultFile:           []byte(`{"status":"pending","attribution":"observed during run, not causal proof","baseline":"abc123","trackedFiles":0,"added":0,"deleted":0,"untrackedFiles":0,"storedTextFiles":0}`),
		gitDir + "/" + trackedStatFile:      []byte(`{"status":"pending","attribution":"observed during run, not causal proof","baseline":"abc123","files":[],"totals":{"files":0,"additions":0,"deletions":0,"binary":0}}`),
		gitDir + "/" + untrackedChangesFile: []byte(`{"attribution":"observed during run, not causal proof","count":0,"stored":0,"files":[]}`),
	}}
	if err := prepareViewChanges(snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.changeStatus != "unavailable" || !strings.Contains(snapshot.changeReason, "pending") {
		t.Fatalf("pending changes = status %q reason %q", snapshot.changeStatus, snapshot.changeReason)
	}
}

func TestPrepareViewChangesRejectsPatchPrefixBytes(t *testing.T) {
	patch := "hidden prefix\ndiff --git a/a.txt b/a.txt\n"
	path := filepath.Join(t.TempDir(), "tracked.patch")
	writeViewFixture(t, path, patch)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	snapshot := &viewSnapshot{
		documents: map[string][]byte{
			gitDir + "/" + resultFile:           []byte(`{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","trackedFiles":1,"added":1,"deleted":0,"untrackedFiles":0,"storedTextFiles":0}`),
			gitDir + "/" + trackedStatFile:      []byte(`{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","files":[{"path":"a.txt","additions":1,"deletions":0}],"totals":{"files":1,"additions":1,"deletions":0,"binary":0}}`),
			gitDir + "/" + untrackedChangesFile: []byte(`{"attribution":"observed during run, not causal proof","count":0,"stored":0,"files":[]}`),
		},
		patch: file, patchSize: int64(len(patch)),
	}
	if err := prepareViewChanges(snapshot); err == nil {
		t.Fatal("prepareViewChanges accepted bytes before the first patch section")
	}
}

func TestPrepareViewChangesRejectsDifferentUnavailableReasons(t *testing.T) {
	snapshot := &viewSnapshot{documents: map[string][]byte{
		gitDir + "/" + resultFile:           []byte(`{"status":"unavailable","reason":"patch_too_large","attribution":"observed during run, not causal proof","baseline":"abc123","trackedFiles":0,"added":0,"deleted":0,"untrackedFiles":0,"storedTextFiles":0}`),
		gitDir + "/" + trackedStatFile:      []byte(`{"status":"unavailable","reason":"baseline_unreachable","attribution":"observed during run, not causal proof","baseline":"abc123","files":[],"totals":{"files":0,"additions":0,"deletions":0,"binary":0}}`),
		gitDir + "/" + untrackedChangesFile: []byte(`{"attribution":"observed during run, not causal proof","count":0,"stored":0,"files":[]}`),
	}}
	if err := prepareViewChanges(snapshot); err == nil {
		t.Fatal("prepareViewChanges accepted different unavailable reasons")
	}
}

func TestPrepareViewChangesRejectsInconsistentTrackedTotals(t *testing.T) {
	snapshot := &viewSnapshot{documents: map[string][]byte{
		gitDir + "/" + resultFile:           []byte(`{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","trackedFiles":0,"added":0,"deleted":0,"untrackedFiles":0,"storedTextFiles":0}`),
		gitDir + "/" + trackedStatFile:      []byte(`{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","files":[],"totals":{"files":1,"additions":0,"deletions":0,"binary":0}}`),
		gitDir + "/" + untrackedChangesFile: []byte(`{"attribution":"observed during run, not causal proof","count":0,"stored":0,"files":[]}`),
	}}
	if err := prepareViewChanges(snapshot); err == nil {
		t.Fatal("prepareViewChanges accepted totals that disagree with the tracked file list")
	}
}

func TestPrepareViewChangesKeepsIncompleteEvidenceUnavailable(t *testing.T) {
	snapshot := &viewSnapshot{documents: map[string][]byte{
		gitDir + "/" + resultFile: []byte(`{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","trackedFiles":0,"added":0,"deleted":0,"untrackedFiles":0,"storedTextFiles":0}`),
	}}
	if err := prepareViewChanges(snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.changeStatus != "unavailable" || snapshot.changeReason == "" || len(snapshot.changes) != 0 {
		t.Fatalf("incomplete changes = status %q reason %q items %d", snapshot.changeStatus, snapshot.changeReason, len(snapshot.changes))
	}
}

func TestPrepareViewChangesKeepsLegacyAndOrphanedEvidenceUnavailable(t *testing.T) {
	tests := []struct {
		name      string
		snapshot  *viewSnapshot
		wantCause string
	}{
		{
			name:      "legacy bundle without repository evidence",
			snapshot:  &viewSnapshot{documents: map[string][]byte{}},
			wantCause: "not recorded",
		},
		{
			name: "orphaned change artifact without result",
			snapshot: &viewSnapshot{documents: map[string][]byte{
				gitDir + "/" + trackedStatFile: []byte(`{"status":"available"}`),
			}},
			wantCause: "missing git/result.json",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := prepareViewChanges(test.snapshot); err != nil {
				t.Fatal(err)
			}
			if test.snapshot.changeStatus != "unavailable" || !strings.Contains(test.snapshot.changeReason, test.wantCause) || len(test.snapshot.changes) != 0 {
				t.Fatalf("changes = status %q reason %q items %d", test.snapshot.changeStatus, test.snapshot.changeReason, len(test.snapshot.changes))
			}
		})
	}
}

func TestPrepareViewChangesKeepsMissingTrackedPatchUnavailable(t *testing.T) {
	snapshot := &viewSnapshot{documents: map[string][]byte{
		gitDir + "/" + resultFile:           []byte(`{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","trackedFiles":1,"added":1,"deleted":0,"untrackedFiles":0,"storedTextFiles":0}`),
		gitDir + "/" + trackedStatFile:      []byte(`{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","files":[{"path":"lost.txt","additions":1,"deletions":0}],"totals":{"files":1,"additions":1,"deletions":0,"binary":0}}`),
		gitDir + "/" + untrackedChangesFile: []byte(`{"attribution":"observed during run, not causal proof","count":0,"stored":0,"files":[]}`),
	}}
	if err := prepareViewChanges(snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.changeStatus != "unavailable" || !strings.Contains(snapshot.changeReason, "tracked.patch") || len(snapshot.changes) != 0 {
		t.Fatalf("changes = status %q reason %q items %d", snapshot.changeStatus, snapshot.changeReason, len(snapshot.changes))
	}
}

func TestPrepareViewChangesRejectsUnlistedPatchSection(t *testing.T) {
	patch := "diff --git a/listed.txt b/listed.txt\n--- a/listed.txt\n+++ b/listed.txt\n@@ -0,0 +1 @@\n+listed\n" +
		"diff --git a/hidden.txt b/hidden.txt\n--- a/hidden.txt\n+++ b/hidden.txt\n@@ -0,0 +1 @@\n+hidden\n"
	path := filepath.Join(t.TempDir(), "tracked.patch")
	writeViewFixture(t, path, patch)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	snapshot := &viewSnapshot{
		documents: map[string][]byte{
			gitDir + "/" + resultFile:           []byte(`{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","trackedFiles":1,"added":1,"deleted":0,"untrackedFiles":0,"storedTextFiles":0}`),
			gitDir + "/" + trackedStatFile:      []byte(`{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","files":[{"path":"listed.txt","additions":1,"deletions":0}],"totals":{"files":1,"additions":1,"deletions":0,"binary":0}}`),
			gitDir + "/" + untrackedChangesFile: []byte(`{"attribution":"observed during run, not causal proof","count":0,"stored":0,"files":[]}`),
		},
		patch: file, patchSize: int64(len(patch)),
	}
	if err := prepareViewChanges(snapshot); err == nil {
		t.Fatal("prepareViewChanges accepted an unlisted patch section")
	}
}

func TestPrepareViewChangesRejectsLineCountOverflow(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	tracked := fmt.Sprintf(`{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","files":[{"path":"one.txt","additions":%d,"deletions":0},{"path":"two.txt","additions":%d,"deletions":0},{"path":"three.txt","additions":3,"deletions":0}],"totals":{"files":3,"additions":1,"deletions":0,"binary":0}}`, maxInt, maxInt)
	patch := "diff --git a/one.txt b/one.txt\n" +
		"diff --git a/two.txt b/two.txt\n" +
		"diff --git a/three.txt b/three.txt\n"
	path := filepath.Join(t.TempDir(), "tracked.patch")
	writeViewFixture(t, path, patch)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	snapshot := &viewSnapshot{
		documents: map[string][]byte{
			gitDir + "/" + resultFile:           []byte(`{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","trackedFiles":3,"added":1,"deleted":0,"untrackedFiles":0,"storedTextFiles":0}`),
			gitDir + "/" + trackedStatFile:      []byte(tracked),
			gitDir + "/" + untrackedChangesFile: []byte(`{"attribution":"observed during run, not causal proof","count":0,"stored":0,"files":[]}`),
		},
		patch: file, patchSize: int64(len(patch)),
	}
	if err := prepareViewChanges(snapshot); err == nil {
		t.Fatal("prepareViewChanges accepted overflowing tracked line counts")
	}
}

func TestParseViewPatchHeaderRejectsDifferentPaths(t *testing.T) {
	if _, err := parseViewPatchHeader("diff --git a/old.txt b/new.txt"); err == nil {
		t.Fatal("parseViewPatchHeader accepted different paths despite --no-renames capture")
	}
}

func TestParseViewPatchHeaderRejectsNonCanonicalPath(t *testing.T) {
	for _, line := range []string{
		"diff --git a/../outside b/../outside",
		"diff --git a//absolute b//absolute",
	} {
		if _, err := parseViewPatchHeader(line); err == nil {
			t.Fatalf("parseViewPatchHeader accepted %q", line)
		}
	}
}

func writeViewFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestViewPaginatesLargeStreams(t *testing.T) {
	root := home(t)
	b, err := storage.Create(root, "run-pages", storage.Manifest{Provider: "claude", Argv: []string{"claude"}, CWD: "/tmp/agentrec", StartedAt: early})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < viewPageSize+1; i++ {
		id := fmt.Sprintf("action-%03d", i)
		if err := b.WriteAction(action.Action{ID: id, Type: action.TypeToolCall, Provider: "claude", Assurance: action.AssuranceProviderReported, Status: "completed"}); err != nil {
			t.Fatal(err)
		}
		if err := b.WriteProviderEvent([]byte(fmt.Sprintf(`{"type":"item","index":%d}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Finalize(storage.Finalization{EndedAt: late, ExitReason: "completed"}); err != nil {
		t.Fatal(err)
	}
	handler := newViewHandler(root, "run-pages", false)
	t.Cleanup(func() { _ = handler.Close() })

	var detail struct {
		SnapshotID  string `json:"snapshotId"`
		ActionCount int    `json:"actionCount"`
		EventCount  int    `json:"eventCount"`
	}
	viewJSONRequest(t, handler, "/api/runs/run-pages", &detail)
	if detail.ActionCount != viewPageSize+1 || detail.EventCount != viewPageSize+1 {
		t.Fatalf("counts = actions %d events %d", detail.ActionCount, detail.EventCount)
	}
	var first viewActionPage
	viewJSONRequest(t, handler, "/api/snapshots/"+detail.SnapshotID+"/actions?cursor=0", &first)
	if len(first.Items) != viewPageSize || first.NextCursor == nil {
		t.Fatalf("first page = %d items, cursor %v", len(first.Items), first.NextCursor)
	}
	var second viewActionPage
	viewJSONRequest(t, handler, fmt.Sprintf("/api/snapshots/%s/actions?cursor=%d", detail.SnapshotID, *first.NextCursor), &second)
	if len(second.Items) != 1 || second.NextCursor != nil || second.Items[0].ID != fmt.Sprintf("action-%03d", viewPageSize) {
		t.Fatalf("second page = %+v", second)
	}
}

func TestViewPagesAreAlsoBoundedByBytes(t *testing.T) {
	root := home(t)
	b, err := storage.Create(root, "run-byte-pages", storage.Manifest{Provider: "claude", Argv: []string{"claude"}, CWD: "/tmp/agentrec", StartedAt: early})
	if err != nil {
		t.Fatal(err)
	}
	payload := strings.Repeat("x", 600<<10)
	for i := 0; i < 2; i++ {
		if err := b.WriteProviderEvent([]byte(fmt.Sprintf(`{"type":"item","index":%d,"payload":%q}`, i, payload))); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Finalize(storage.Finalization{EndedAt: late, ExitReason: "completed"}); err != nil {
		t.Fatal(err)
	}
	handler := newViewHandler(root, "run-byte-pages", false)
	t.Cleanup(func() { _ = handler.Close() })
	var detail struct {
		SnapshotID string `json:"snapshotId"`
	}
	viewJSONRequest(t, handler, "/api/runs/run-byte-pages", &detail)
	var first viewEventPage
	viewJSONRequest(t, handler, "/api/snapshots/"+detail.SnapshotID+"/events?cursor=0", &first)
	if len(first.Items) != 1 || first.NextCursor == nil {
		t.Fatalf("first byte-bounded page = %d items, cursor %v", len(first.Items), first.NextCursor)
	}
	var second viewEventPage
	viewJSONRequest(t, handler, fmt.Sprintf("/api/snapshots/%s/events?cursor=%d", detail.SnapshotID, *first.NextCursor), &second)
	if len(second.Items) != 1 || second.NextCursor != nil {
		t.Fatalf("second byte-bounded page = %d items, cursor %v", len(second.Items), second.NextCursor)
	}
}

type blockingCheckContext struct {
	context.Context
	reached   chan<- struct{}
	once      sync.Once
	remaining atomic.Int32
}

func (c *blockingCheckContext) Err() error {
	if err := c.Context.Err(); err != nil {
		return err
	}
	if c.remaining.Add(-1) >= 0 {
		return nil
	}
	c.once.Do(func() { c.reached <- struct{}{} })
	<-c.Context.Done()
	return c.Context.Err()
}

func TestCancelledChangeParsingReleasesBothSnapshotSlots(t *testing.T) {
	root := home(t)
	store := newViewSnapshotStore(root)
	t.Cleanup(func() { _ = store.Close() })
	raw := []byte(`{"status":"available","attribution":"repository_observed","files":[],"totals":{"files":0,"additions":0,"deletions":0,"binary":0}}`)
	reached := make(chan struct{}, 2)
	done := make(chan error, 2)
	cancels := make([]context.CancelFunc, 0, 2)
	for range 2 {
		base, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		ctx := &blockingCheckContext{Context: base, reached: reached}
		ctx.remaining.Store(3)
		request := httptest.NewRequest(http.MethodGet, "/api/runs/run", nil).WithContext(ctx)
		go func() {
			done <- store.withSlot(request, func() error {
				var target trackedChangeDocument
				return decodeViewChangeDocumentContext(request.Context(), raw, "tracked", &target)
			})
		}()
	}
	for range 2 {
		select {
		case <-reached:
		case <-time.After(2 * time.Second):
			t.Fatal("two change parsers did not occupy both slots")
		}
	}
	for _, cancel := range cancels {
		cancel()
	}
	third := httptest.NewRequest(http.MethodGet, "/api/runs/run", nil)
	thirdStarted := make(chan struct{})
	thirdDone := make(chan error, 1)
	go func() {
		thirdDone <- store.withSlot(third, func() error {
			close(thirdStarted)
			return nil
		})
	}()
	select {
	case <-thirdStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("third request did not acquire a slot after parser cancellation")
	}
	for range 2 {
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled parser error = %v, want context.Canceled", err)
		}
	}
	if err := <-thirdDone; err != nil {
		t.Fatal(err)
	}
}

func TestCancelledSnapshotsReleaseBothSlotsBeforeBlockedWorkIsReleased(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run", "claude", early, "completed")
	store := newViewSnapshotStore(root)
	t.Cleanup(func() { _ = store.Close() })
	started := make(chan struct{}, 2)
	thirdStarted := make(chan struct{})
	var calls atomic.Int32
	store.beforeFingerprint = func(ctx context.Context) {
		if calls.Add(1) <= 2 {
			started <- struct{}{}
			<-ctx.Done()
		} else {
			close(thirdStarted)
		}
	}

	cancels := make([]context.CancelFunc, 0, 2)
	done := make(chan error, 2)
	for range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		request := httptest.NewRequest(http.MethodGet, "/api/runs/run", nil).WithContext(ctx)
		go func() {
			done <- store.withSlot(request, func() error {
				_, err := store.createContext(request.Context(), "run")
				return err
			})
		}()
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("two snapshot requests did not occupy both slots")
		}
	}
	for _, cancel := range cancels {
		cancel()
	}

	thirdRequest := httptest.NewRequest(http.MethodGet, "/api/runs/run", nil)
	thirdDone := make(chan error, 1)
	go func() {
		thirdDone <- store.withSlot(thirdRequest, func() error {
			_, err := store.createContext(thirdRequest.Context(), "run")
			return err
		})
	}()
	select {
	case <-thirdStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("third snapshot did not acquire a slot after both cancellations")
	}
	for range 2 {
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled snapshot error = %v, want context.Canceled", err)
		}
	}
	if err := <-thirdDone; err != nil {
		t.Fatalf("third snapshot error = %v", err)
	}
}

type cancelAfterRead struct {
	reader io.Reader
	cancel context.CancelFunc
	read   bool
}

func (r *cancelAfterRead) Read(p []byte) (int, error) {
	if len(p) > 16 {
		p = p[:16]
	}
	n, err := r.reader.Read(p)
	if !r.read {
		r.read = true
		r.cancel()
	}
	return n, err
}

type cancelAfterContextChecks struct {
	context.Context
	remaining int
}

func (c *cancelAfterContextChecks) Err() error {
	if c.remaining == 0 {
		return context.Canceled
	}
	c.remaining--
	return nil
}

type recordingReader struct {
	reader io.Reader
	max    int
}

func (r *recordingReader) Read(p []byte) (int, error) {
	if len(p) > r.max {
		r.max = len(p)
	}
	return r.reader.Read(p)
}

func TestViewContextReaderBoundsEachRead(t *testing.T) {
	data := bytes.Repeat([]byte("a"), 2*viewContextChunkSize)
	recorder := &recordingReader{reader: bytes.NewReader(data)}
	got := make([]byte, len(data))
	if _, err := io.ReadFull(viewContextReader{ctx: context.Background(), reader: recorder}, got); err != nil {
		t.Fatal(err)
	}
	if recorder.max > viewContextChunkSize {
		t.Fatalf("largest read buffer = %d, want <= %d", recorder.max, viewContextChunkSize)
	}
}

func TestViewChangeDocumentRejectsUnterminatedOversizedObject(t *testing.T) {
	raw := []byte(`{"status":"available","attribution":"repository_observed","baseline":"HEAD","files":[{"path":"file.txt","additions":1,"deletions":1` + strings.Repeat(" ", 2*viewContextChunkSize))
	var target trackedChangeDocument
	err := decodeViewChangeDocumentContext(context.Background(), raw, "tracked.json", &target)
	if err == nil || !strings.Contains(err.Error(), "nested JSON object exceeds") {
		t.Fatalf("decode error = %v, want nested object bound", err)
	}
}

func TestViewChangeDocumentRejectsOversizedSingleField(t *testing.T) {
	raw := []byte(`{"status":"available","attribution":"repository_observed","baseline":"HEAD","files":[{"path":"` + strings.Repeat("a", viewContextChunkSize+1) + `","additions":1,"deletions":0}],"totals":{"files":1,"additions":1,"deletions":0}}`)
	var document trackedChangeDocument
	if err := decodeViewChangeDocumentContext(context.Background(), raw, "tracked-stat.json", &document); err == nil || !strings.Contains(err.Error(), "JSON string exceeds") {
		t.Fatalf("decode error = %v, want bounded-string rejection", err)
	}
}

func TestViewLinearPassesStopWhenCancelledMidWork(t *testing.T) {
	data := bytes.Repeat([]byte("a"), 2*viewContextChunkSize)
	t.Run("document hash", func(t *testing.T) {
		ctx := &cancelAfterContextChecks{Context: context.Background(), remaining: 1}
		if _, err := hashViewBytesContext(ctx, data); !errors.Is(err, context.Canceled) {
			t.Fatalf("hash error = %v, want context.Canceled", err)
		}
	})
	t.Run("UTF-8 validation", func(t *testing.T) {
		ctx := &cancelAfterContextChecks{Context: context.Background(), remaining: 1}
		if _, err := validViewUTF8Context(ctx, data); !errors.Is(err, context.Canceled) {
			t.Fatalf("UTF-8 error = %v, want context.Canceled", err)
		}
	})
	t.Run("snapshot rehash", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		reader := &cancelAfterRead{reader: bytes.NewReader(data), cancel: cancel}
		if _, err := copyViewNContext(ctx, io.Discard, reader, int64(len(data))); !errors.Is(err, context.Canceled) {
			t.Fatalf("copy error = %v, want context.Canceled", err)
		}
	})
}

func TestViewParsersStopWhenCancelledMidRead(t *testing.T) {
	t.Run("unparsed", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		reader := &cancelAfterRead{reader: strings.NewReader("one\ntwo\n"), cancel: cancel}
		if err := validateUnparsedReaderContext(ctx, reader, int64(len("one\ntwo\n")), 2); !errors.Is(err, context.Canceled) {
			t.Fatalf("unparsed error = %v, want context.Canceled", err)
		}
	})
	t.Run("change document reader", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		reader := &cancelAfterRead{reader: strings.NewReader(`{"attribution":"repository_observed","baseline":"base","status":"available","reason":"","files":[]}`), cancel: cancel}
		var target trackedChangeDocument
		if err := decodeViewChangeReaderContext(ctx, reader, "tracked", &target); !errors.Is(err, context.Canceled) {
			t.Fatalf("change document reader error = %v, want context.Canceled", err)
		}
	})
	t.Run("change document parse", func(t *testing.T) {
		var raw strings.Builder
		raw.WriteString(`{"status":"available","attribution":"repository_observed","files":[`)
		for i := range 5000 {
			if i > 0 {
				raw.WriteByte(',')
			}
			fmt.Fprintf(&raw, `{"path":"file-%d","additions":1,"deletions":1}`, i)
		}
		raw.WriteString(`],"totals":{"files":5000,"additions":5000,"deletions":5000,"binary":0}}`)
		ctx := &cancelAfterContextChecks{Context: context.Background(), remaining: 20}
		var target trackedChangeDocument
		if err := decodeViewChangeDocumentContext(ctx, []byte(raw.String()), "tracked", &target); !errors.Is(err, context.Canceled) {
			t.Fatalf("change document parse error = %v, want context.Canceled", err)
		}
	})
}

func TestViewExpensivePreparationStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := prepareViewChangesContext(ctx, &viewSnapshot{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("prepare changes error = %v, want context.Canceled", err)
	}
	file, err := os.CreateTemp(t.TempDir(), "events")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString("{}\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := countViewActionsContext(ctx, file, 3); !errors.Is(err, context.Canceled) {
		t.Fatalf("count actions error = %v, want context.Canceled", err)
	}
	if _, err := countViewEventsContext(ctx, file, 3); !errors.Is(err, context.Canceled) {
		t.Fatalf("count events error = %v, want context.Canceled", err)
	}
}

func TestViewFingerprintStopsAfterCancellation(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-cancelled-fingerprint", "claude", early, "completed")
	runRoot, err := openRunRoot(root, "run-cancelled-fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	defer runRoot.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := viewRunFingerprintContext(ctx, runRoot); !errors.Is(err, context.Canceled) {
		t.Fatalf("fingerprint error = %v, want context.Canceled", err)
	}
}

func TestViewFingerprintDetectsReplacedEvidenceFile(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-fingerprint", "claude", early, "completed")
	runRoot, err := openRunRoot(root, "run-fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	defer runRoot.Close()
	before, err := viewRunFingerprint(runRoot)
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(root, "run-fingerprint", processDir, resultFile)
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	replacement := resultPath + ".replacement"
	if err := os.WriteFile(replacement, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, resultPath); err != nil {
		t.Fatal(err)
	}
	after, err := viewRunFingerprint(runRoot)
	if err != nil {
		t.Fatal(err)
	}
	if sameViewFingerprint(before, after) {
		t.Fatal("fingerprint accepted a replaced process result")
	}
}

func TestViewFingerprintDetectsSameMetadataContentRewrite(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-content-fingerprint", "claude", early, "completed")
	runRoot, err := openRunRoot(root, "run-content-fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	defer runRoot.Close()
	resultPath := filepath.Join(root, "run-content-fingerprint", processDir, resultFile)
	info, err := os.Stat(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	before, err := viewRunFingerprint(runRoot)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := bytes.Clone(raw)
	rewritten[0] ^= 1
	if err := os.WriteFile(resultPath, rewritten, info.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(resultPath, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := viewRunFingerprint(runRoot)
	if err != nil {
		t.Fatal(err)
	}

	if sameViewFingerprint(before, after) {
		t.Fatal("fingerprint accepted an in-place content rewrite with restored metadata")
	}
}

func TestViewSnapshotCaptureRejectsABAContent(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-aba", "claude", early, "completed")
	runRoot, err := openRunRoot(root, "run-aba")
	if err != nil {
		t.Fatal(err)
	}
	defer runRoot.Close()
	resultPath := filepath.Join(root, "run-aba", processDir, resultFile)
	info, err := os.Stat(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	before, err := viewRunFingerprint(runRoot)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := bytes.Clone(original)
	rewritten[0] ^= 1
	if err := os.WriteFile(resultPath, rewritten, info.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(resultPath, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}

	snapshot := &viewSnapshot{}
	copyErr := captureViewRun(runRoot, before, snapshot)
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultPath, original, info.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(resultPath, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := viewRunFingerprint(runRoot)
	if err != nil {
		t.Fatal(err)
	}

	if !sameViewFingerprint(before, after) {
		t.Fatal("ABA test did not restore the endpoint fingerprint")
	}
	if copyErr == nil || !strings.Contains(copyErr.Error(), "changed") {
		t.Fatalf("captureViewRun error = %v, want consumed-content mismatch", copyErr)
	}
}

func TestViewContextReaderStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := viewContextReader{ctx: ctx, reader: strings.NewReader("evidence")}
	if _, err := reader.Read(make([]byte, 8)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Read error = %v, want context.Canceled", err)
	}
}

func TestViewSnapshotStoreDoesNotAddAfterCancellation(t *testing.T) {
	store := newViewSnapshotStore(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snapshot := &viewSnapshot{id: "cancelled"}
	if err := store.addContext(ctx, snapshot); !errors.Is(err, context.Canceled) {
		t.Fatalf("addContext error = %v, want context.Canceled", err)
	}
	if len(store.byID) != 0 {
		t.Fatalf("published %d snapshots after cancellation", len(store.byID))
	}
}

func TestViewSnapshotDoesNotPublishAfterRequestCancellation(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-cancelled-snapshot", "claude", early, "completed")
	handler := newViewHandler(root, "run-cancelled-snapshot", false)
	t.Cleanup(func() { _ = handler.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	handler.snapshots.afterCopy = cancel
	request := httptest.NewRequest(http.MethodGet, "/api/runs/run-cancelled-snapshot", nil).WithContext(ctx)
	request.Host = "127.0.0.1"
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	handler.snapshots.mu.RLock()
	defer handler.snapshots.mu.RUnlock()
	if len(handler.snapshots.byID) != 0 {
		t.Fatalf("cancelled request published %d snapshot(s)", len(handler.snapshots.byID))
	}
}

func TestViewSnapshotCreateRejectsMutationAfterCapture(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-create-mutation", "claude", early, "completed")
	resultPath := filepath.Join(root, "run-create-mutation", processDir, resultFile)
	info, err := os.Stat(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	store := newViewSnapshotStore(root)
	store.afterCopy = func() {
		rewritten := bytes.Clone(original)
		rewritten[0] ^= 1
		if err := os.WriteFile(resultPath, rewritten, info.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(resultPath, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
	_, err = store.create("run-create-mutation")
	if err == nil || !strings.Contains(err.Error(), "run changed") {
		t.Fatalf("create error = %v, want coherent-snapshot retry", err)
	}
	if len(store.byID) != 0 {
		t.Fatalf("registered snapshots = %d, want 0", len(store.byID))
	}
}

func TestViewSnapshotCreateCannotRegisterAfterStoreClose(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-close-race", "claude", early, "completed")
	store := newViewSnapshotStore(root)
	captured := make(chan struct{})
	resume := make(chan struct{})
	store.afterCopy = func() {
		close(captured)
		<-resume
	}

	result := make(chan error, 1)
	go func() {
		_, err := store.create("run-close-race")
		result <- err
	}()
	<-captured
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(resume)
	if err := <-result; err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("create error = %v, want closed store", err)
	}
	if len(store.byID) != 0 || len(store.ids) != 0 {
		t.Fatalf("closed store was repopulated: byID=%d ids=%d", len(store.byID), len(store.ids))
	}
}

func TestViewSnapshotIgnoresUnparsedArtifactWhenManifestCountIsZero(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-zero-unparsed", "claude", early, "completed")
	if err := os.Symlink("missing", filepath.Join(root, "run-zero-unparsed", unparsedFile)); err != nil {
		t.Fatal(err)
	}
	store := newViewSnapshotStore(root)
	t.Cleanup(func() { _ = store.Close() })

	if _, err := store.create("run-zero-unparsed"); err != nil {
		t.Fatalf("create rejected an ignored zero-count unparsed artifact: %v", err)
	}
}

func TestViewStreamCaptureFailsWhenAnonymousUnlinkFails(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-unlink-failure", "claude", early, "completed")
	runRoot, err := openRunRoot(root, "run-unlink-failure")
	if err != nil {
		t.Fatal(err)
	}
	defer runRoot.Close()
	identity, err := viewFileFingerprint(runRoot, actionsFile)
	if err != nil {
		t.Fatal(err)
	}

	file, _, err := captureViewStreamWithUnlink(runRoot, actionsFile, identity, func(string) error {
		return os.ErrPermission
	})
	if file != nil {
		file.Close()
		t.Fatal("capture returned a stream after unlink failed")
	}
	if err == nil || !strings.Contains(err.Error(), "unlink viewer stream snapshot") {
		t.Fatalf("capture error = %v, want unlink failure", err)
	}
}

func TestViewSnapshotKeepsImmutableStreamBytes(t *testing.T) {
	root := home(t)
	b, err := storage.Create(root, "run-immutable", storage.Manifest{Provider: "claude", Argv: []string{"claude"}, CWD: "/tmp/agentrec", StartedAt: early})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteAction(action.Action{ID: "action-1", Type: action.TypeSearch, Provider: "claude", Assurance: action.AssuranceProviderReported, StartedAt: early, Status: "completed", Input: json.RawMessage(`{"query":"before"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := b.Finalize(storage.Finalization{EndedAt: late, ExitReason: "completed"}); err != nil {
		t.Fatal(err)
	}
	handler := newViewHandler(root, "run-immutable", false)
	var detail struct {
		SnapshotID string `json:"snapshotId"`
	}
	viewJSONRequest(t, handler, "/api/runs/run-immutable", &detail)
	snapshot := handler.snapshots.byID[detail.SnapshotID]
	// The append-only streams are served from the viewer's own cache, a
	// 0700 directory beside the runs whose copies only ever grow; every
	// other capture is an unlinked temp file.
	cacheDir := handler.snapshots.cache.dir
	if rel, err := filepath.Rel(cacheDir, snapshot.actions.Name()); err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("action snapshot is served from %s, want the viewer cache", snapshot.actions.Name())
	}
	if info, err := os.Stat(cacheDir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("viewer cache dir: %v, mode %v", err, info.Mode())
	}
	if _, err := snapshot.actions.WriteAt([]byte("x"), 0); err == nil {
		t.Fatal("action snapshot retained write capability")
	}

	actionsPath := filepath.Join(root, "run-immutable", actionsFile)
	raw, err := os.ReadFile(actionsPath)
	if err != nil {
		t.Fatal(err)
	}
	replaced := bytes.ReplaceAll(raw, []byte("before"), []byte("after!"))
	if err := os.WriteFile(actionsPath, replaced, 0o600); err != nil {
		t.Fatal(err)
	}
	var page viewActionPage
	viewJSONRequest(t, handler, "/api/snapshots/"+detail.SnapshotID+"/actions?cursor=0", &page)
	if len(page.Items) != 1 || !bytes.Contains(page.Items[0].Input, []byte("before")) {
		t.Fatalf("snapshot page changed with source file: %+v", page.Items)
	}
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestViewHandlerServesSelfContainedReadOnlyUIWithSecurityHeaders(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-ui", "claude", early, "completed")
	handler := newViewHandler(root, "run-ui", false)
	t.Cleanup(func() { _ = handler.Close() })

	for _, path := range []string{"/", "/assets/app.css", "/assets/app.js"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Host = "localhost:42817"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", path, response.Code)
		}
		if response.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("GET %s has no Content-Security-Policy", path)
		}
	}

	for name, want := range map[string]string{
		"ui_assets/index.html": `id="timeline-warning"`,
		"ui_assets/app.js":     "timeline may be incomplete",
		"ui_assets/app.css":    ".action-dot.fail",
	} {
		body, err := viewAssets.ReadFile(name)
		if err != nil || !strings.Contains(string(body), want) {
			t.Errorf("%s does not contain %q (err=%v)", name, want, err)
		}
	}

	request := httptest.NewRequest(http.MethodPost, "/api/runs/run-ui", strings.NewReader("{}"))
	request.Host = "localhost:42817"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/runs/run-ui", nil)
	request.Host = "attacker.example"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("foreign Host status = %d, want 403", response.Code)
	}
}

func TestViewChangesUIUsesSnapshotBackedChangeAndPatchViews(t *testing.T) {
	checks := map[string][]string{
		"ui_assets/index.html": {`data-mode="changes"`, `id="change-count"`, `aria-controls="timeline"`, `role="tabpanel"`, `id="inspector-status"`, `id="inspector" class="inspector-empty">`},
		"ui_assets/app.js": {
			"loadPatchPage",
			"SANITIZED REPOSITORY PATCH",
			"/patch?path=",
			"Repository change evidence is unavailable.",
			"aria-pressed",
			"row.setAttribute('aria-controls', 'inspector')",
			"consumeHistory",
			"loadStreamPage('actions', 0, false, generation, false, controller.signal)",
			"change-evidence-warning",
			"cursor !== stream.currentCursor",
			"patchError",
			"AbortController",
			"ArrowRight",
			"tabIndex",
			"metric('Process outcome'",
			"metric('Verification verdict'",
			"metric('Repository evidence'",
			"action.samePathObserved",
			"same path observed — not causal proof",
			"loadStreamPage(state.mode",
			"stream.error = error instanceof Error",
			"Could not load",
			"event.preventDefault()",
		},
		"ui_assets/app.css": {".change-row", ".diff-patch", ".metric-detail", "repeat(6,minmax(0,1fr))", ".sr-only", "overflow-wrap:anywhere"},
	}
	for name, markers := range checks {
		raw, err := viewAssets.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range markers {
			if !strings.Contains(string(raw), marker) {
				t.Errorf("%s does not contain %q", name, marker)
			}
		}
	}
	app, err := viewAssets.ReadFile("ui_assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(app), "row.setAttribute('aria-pressed'") {
		t.Fatal("change disclosure buttons must not expose toggle semantics")
	}
}

func TestViewRunListPreservesExplicitInitialRunBeyondFirstPage(t *testing.T) {
	root := home(t)
	var oldest string
	for i := range 55 {
		runID := early.Add(time.Duration(i)*time.Second).UTC().Format(runIDTimeLayout) + fmt.Sprintf("-%08x", i)
		writeRun(t, root, runID, "claude", early.Add(time.Duration(i)*time.Second), "completed")
		if i == 0 {
			oldest = runID
		}
	}
	handler := newViewHandler(root, oldest, false)
	t.Cleanup(func() { _ = handler.Close() })
	var page struct {
		InitialRunID string           `json:"initialRunId"`
		Runs         []viewRunSummary `json:"runs"`
	}
	viewJSONRequest(t, handler, "/api/runs", &page)
	if page.InitialRunID != oldest {
		t.Fatalf("initial run = %q, want %q", page.InitialRunID, oldest)
	}
	for _, run := range page.Runs {
		if run.ID == oldest {
			t.Fatal("fixture did not place the explicit run beyond page one")
		}
	}
}

func TestViewRunIndexSupportsFreshMissingDataDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing", "runs")
	page, err := scanViewRunPage(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.runs) != 0 || page.total != 0 {
		t.Fatalf("fresh page = %d runs, total %d", len(page.runs), page.total)
	}
}

func TestViewRunIndexRejectsCountLargerThanFile(t *testing.T) {
	root := home(t)
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(root), viewRunIndexFile)
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%s %020d\n", viewRunIndexHeader, int(^uint(0)>>1))), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readAllViewRunIndex(root)
	if err == nil || !strings.Contains(err.Error(), "count exceeds file size") {
		t.Fatalf("read error = %v, want file-size count bound", err)
	}
}

func TestViewRunIndexDirtyMarkerRebuildsBeforeNextUpdate(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", early, "completed")
	if err := writeViewRunIndex(root, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(root), viewRunIndexDirty), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeRun(t, root, "run-b", "codex", early.Add(time.Second), "completed")
	err := updateViewRunIndex(root, func(entries []viewRunIndexEntry) []viewRunIndexEntry {
		return upsertViewRunIndexEntry(entries, viewRunIndexEntry{id: "run-b", startedAt: early.Add(time.Second)})
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := scanViewRunPage(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.runs) != 2 || page.runs[0].ID != "run-b" || page.runs[1].ID != "run-a" {
		t.Fatalf("recovered runs = %+v", page.runs)
	}
}

func TestViewRunIndexSerializesMigrationWithCreate(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", early, "completed")
	entered := make(chan struct{})
	release := make(chan struct{})
	beforeViewRunIndexRebuildPublish = func() {
		close(entered)
		<-release
	}
	t.Cleanup(func() { beforeViewRunIndexRebuildPublish = nil })
	migrationDone := make(chan error, 1)
	go func() { migrationDone <- ensureViewRunIndex(root) }()
	<-entered
	writeRun(t, root, "run-b", "codex", early.Add(time.Second), "completed")
	createDone := make(chan error, 1)
	go func() {
		err := updateViewRunIndex(root, func(entries []viewRunIndexEntry) []viewRunIndexEntry {
			return upsertViewRunIndexEntry(entries, viewRunIndexEntry{id: "run-b", startedAt: early.Add(time.Second)})
		})
		createDone <- err
	}()
	close(release)
	if err := <-migrationDone; err != nil {
		t.Fatal(err)
	}
	if err := <-createDone; err != nil {
		t.Fatal(err)
	}
	beforeViewRunIndexRebuildPublish = nil
	page, err := scanViewRunPage(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.runs) != 2 || page.runs[0].ID != "run-b" || page.runs[1].ID != "run-a" {
		t.Fatalf("serialized runs = %+v", page.runs)
	}
}

func TestViewRunIndexRejectsExternalSymlink(t *testing.T) {
	root := home(t)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "outside-index")
	const sentinel = "outside-sentinel"
	if err := os.WriteFile(external, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(filepath.Dir(root), viewRunIndexFile)); err != nil {
		t.Fatal(err)
	}
	if file, err := openViewRunIndex(root); err == nil {
		file.Close()
		t.Fatal("open accepted an external run-index symlink")
	}
	if _, err := scanViewRunPage(root, ""); err == nil {
		t.Fatal("scan accepted an external run-index symlink")
	}
	got, err := os.ReadFile(external)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sentinel {
		t.Fatalf("external index = %q, want untouched sentinel", got)
	}
}

func TestViewRunPageReadsOnlyOneCanonicalPage(t *testing.T) {
	root := home(t)
	entries := make([]viewRunIndexEntry, 0, 120)
	for i := range 120 {
		startedAt := early.Add(time.Duration(i) * time.Second)
		runID := startedAt.UTC().Format(runIDTimeLayout) + fmt.Sprintf("-%08x", i)
		if err := os.MkdirAll(filepath.Join(root, runID), 0o700); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, viewRunIndexEntry{id: runID, startedAt: startedAt})
	}
	if err := writeViewRunIndex(root, entries); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(filepath.Dir(root), viewRunIndexFile)
	indexRaw, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	oldest := bytes.IndexByte(indexRaw, '\n') + 1
	if oldest <= 0 || oldest >= len(indexRaw) {
		t.Fatal("run index has no oldest record")
	}
	indexRaw[oldest] = '!'
	if err := os.WriteFile(indexPath, indexRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	reads := 0
	read := func(_ *os.Root, runID string) (runSummary, error) {
		reads++
		return runSummary{ID: runID, StartedAt: early}, nil
	}
	first, err := scanViewRunPageWithRead(root, "", read)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.runs) != viewRunPageSize || reads != viewRunPageSize || first.nextCursor == "" {
		t.Fatalf("first page = %d runs, %d reads, cursor %q", len(first.runs), reads, first.nextCursor)
	}
	reads = 0
	second, err := scanViewRunPageWithRead(root, first.nextCursor, read)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.runs) != viewRunPageSize || reads != viewRunPageSize {
		t.Fatalf("second page = %d runs, %d reads", len(second.runs), reads)
	}
}

func TestViewRunListUIUsesExplicitCursorContinuation(t *testing.T) {
	root := home(t)
	handler := newViewHandler(root, "", false)
	t.Cleanup(func() { _ = handler.Close() })
	get := func(path string) []byte {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Host = "localhost:42817"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d", path, response.Code)
		}
		return response.Body.Bytes()
	}
	index := get("/")
	app := get("/assets/app.js")
	if !bytes.Contains(index, []byte(`id="run-load-more"`)) {
		t.Fatal("run list has no explicit load-more control")
	}
	for _, source := range []string{
		"/api/runs?cursor=${encodeURIComponent(cursor)}",
		"applyRunList(list, true)",
	} {
		if !strings.Contains(string(app), source) {
			t.Fatalf("app.js missing %q", source)
		}
	}
}

func TestViewRunListCacheDropsNewlyUnreadableRecentRun(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-cache-unreadable", "claude", early, "completed")
	cache := newViewRunListCache(root)
	first, err := cache.list()
	if err != nil || len(first.runs) != 1 {
		t.Fatalf("first list = %+v, %v", first, err)
	}
	manifestPath := filepath.Join(root, "run-cache-unreadable", manifestFile)
	original, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := cache.list()
	if err != nil {
		t.Fatal(err)
	}
	if len(second.runs) != 0 || second.unreadable != 1 {
		t.Fatalf("second list = %d runs, %d unreadable", len(second.runs), second.unreadable)
	}
	if err := os.WriteFile(manifestPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := cache.list()
	if err != nil {
		t.Fatal(err)
	}
	if len(third.runs) != 1 || third.unreadable != 0 {
		t.Fatalf("third list = %d runs, %d unreadable", len(third.runs), third.unreadable)
	}
}

func TestViewRunListCacheAvoidsFullRescanOnUnchangedPoll(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-cache", "claude", early, "completed")
	cache := newViewRunListCache(root)
	fullScans := 0
	cache.scan = func(cursor string) (viewRunPage, error) {
		fullScans++
		return scanViewRunPage(root, cursor)
	}
	if _, err := cache.list(); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.list(); err != nil {
		t.Fatal(err)
	}
	if fullScans != 1 {
		t.Fatalf("full scans = %d, want 1", fullScans)
	}
}

func TestViewRunListUsesBoundedCursorPages(t *testing.T) {
	root := home(t)
	for i := range 55 {
		writeRun(t, root, fmt.Sprintf("run-%02d", i), "claude", early.Add(time.Duration(i)*time.Second), "completed")
	}
	handler := newViewHandler(root, "", false)
	t.Cleanup(func() { _ = handler.Close() })
	type page struct {
		Runs       []viewRunSummary `json:"runs"`
		Total      int              `json:"total"`
		NextCursor string           `json:"nextCursor"`
	}
	var first page
	viewJSONRequest(t, handler, "/api/runs", &first)
	if len(first.Runs) != 50 || first.Total != 55 || first.NextCursor == "" {
		t.Fatalf("first page = %d runs, total %d, cursor %q", len(first.Runs), first.Total, first.NextCursor)
	}
	if first.NextCursor == first.Runs[len(first.Runs)-1].ID {
		t.Fatal("continuation cursor exposes the last run ID")
	}
	var second page
	viewJSONRequest(t, handler, "/api/runs?cursor="+url.QueryEscape(first.NextCursor), &second)
	if len(second.Runs) != 5 || second.Total != 55 || second.NextCursor != "" {
		t.Fatalf("second page = %d runs, total %d, cursor %q", len(second.Runs), second.Total, second.NextCursor)
	}
	seen := make(map[string]bool, 55)
	for _, run := range append(first.Runs, second.Runs...) {
		if seen[run.ID] {
			t.Fatalf("run %q appeared on both pages", run.ID)
		}
		seen[run.ID] = true
	}
}

func TestViewRunListIncludesVerificationIntegrityWarnings(t *testing.T) {
	root := home(t)
	writeRunWithWarnings(t, root, "run", "claude", early, "completed", 2)
	writeVerification(t, root, "run", map[string]any{
		"status":      "passed",
		"attribution": evidence.VerificationAttribution,
		"checks":      []any{},
		"warnings": []any{map[string]any{
			"code":  "verification_mutated_repository",
			"paths": []string{"changed.txt"},
		}},
	})
	handler := newViewHandler(root, "run", false)
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
			StatusClass  string `json:"statusClass"`
			WarningCount int    `json:"warningCount"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Runs) != 1 || body.Runs[0].StatusClass != "warn" || body.Runs[0].WarningCount != 3 {
		t.Fatalf("runs = %+v, want one warning-classified run with two process warnings and one verification warning", body.Runs)
	}
}

func TestViewRunDetailUsesCanonicalAggregateStatus(t *testing.T) {
	tests := []struct {
		name, exit, verification string
		warnings                 []any
		wantClass                string
		wantWarnings             int
	}{
		{name: "verification mutation", exit: "completed", verification: "passed", warnings: []any{map[string]any{"code": "verification_mutated_repository", "paths": []string{"changed.txt"}}}, wantClass: "warn", wantWarnings: 1},
		{name: "lost session", exit: reasonSessionLost, verification: "passed", wantClass: "fail"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := home(t)
			writeRun(t, root, "run", "claude", early, test.exit)
			writeVerification(t, root, "run", map[string]any{
				"status":      test.verification,
				"attribution": evidence.VerificationAttribution,
				"checks":      []any{},
				"warnings":    test.warnings,
			})
			handler := newViewHandler(root, "run", false)
			t.Cleanup(func() { _ = handler.Close() })
			request := httptest.NewRequest(http.MethodGet, "/api/runs/run", nil)
			request.Host = "127.0.0.1"
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d; body=%s", response.Code, response.Body.String())
			}
			var body struct {
				Run struct {
					StatusClass  string `json:"statusClass"`
					WarningCount int    `json:"warningCount"`
				} `json:"run"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Run.StatusClass != test.wantClass || body.Run.WarningCount != test.wantWarnings {
				t.Fatalf("run status = %q, warnings = %d; want %q, %d", body.Run.StatusClass, body.Run.WarningCount, test.wantClass, test.wantWarnings)
			}
		})
	}
}

func TestViewRunStatusIncludesVerificationIntegrityWarnings(t *testing.T) {
	gotClass, gotLabel := viewRunStatus("completed", "PASS", 1)
	if gotClass != "warn" || gotLabel != "PASS" {
		t.Fatalf("status = (%q, %q), want (warn, PASS)", gotClass, gotLabel)
	}
}

func TestViewRunListKeepsUnavailableVerificationNeutral(t *testing.T) {
	tests := []struct {
		name, exit, verification, wantClass, wantLabel string
	}{
		{"unavailable verification", "completed", "NOT RUN", "", "NOT RUN"},
		{"passed verification", "completed", "PASS", "pass", "PASS"},
		{"failed verification", "completed", "FAIL", "fail", "FAIL"},
		{"tainted verification", "completed", "TAINTED", "warn", "TAINTED"},
		{"unknown verification", "completed", "FUTURE", "", "FUTURE"},
		{"process failure", "storage_error", "PASS", "fail", "storage_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotClass, gotLabel := viewRunListStatus(test.exit, test.verification)
			if gotClass != test.wantClass || gotLabel != test.wantLabel {
				t.Fatalf("status = (%q, %q), want (%q, %q)", gotClass, gotLabel, test.wantClass, test.wantLabel)
			}
		})
	}
	body, err := viewAssets.ReadFile("ui_assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)
	if !strings.Contains(script, "run.statusClass") || !strings.Contains(script, "run.statusLabel") {
		t.Fatal("run list does not render the server-classified status")
	}
	if strings.Contains(script, "function runListStatus(run)") {
		t.Fatal("run list still has a second client-side status classifier")
	}
}

func TestViewRunListIsNewestFirstAndNamesInitialRun(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-old", "claude", early, "completed")
	writeRun(t, root, "run-new", "codex", late, "completed")
	handler := newViewHandler(root, "run-old", false)
	t.Cleanup(func() { _ = handler.Close() })
	request := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	request.Host = "127.0.0.1"
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	var body struct {
		InitialRunID string `json:"initialRunId"`
		Runs         []struct {
			ID          string `json:"id"`
			StatusClass string `json:"statusClass"`
			StatusLabel string `json:"statusLabel"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.InitialRunID != "run-old" {
		t.Errorf("initial run = %q", body.InitialRunID)
	}
	if len(body.Runs) != 2 || body.Runs[0].ID != "run-new" || body.Runs[1].ID != "run-old" {
		t.Fatalf("runs = %+v", body.Runs)
	}
	for _, run := range body.Runs {
		if run.StatusClass != "" || run.StatusLabel != "NOT RUN" {
			t.Errorf("run %s status = (%q, %q), want neutral NOT RUN", run.ID, run.StatusClass, run.StatusLabel)
		}
	}
}

func TestViewUsesOneHeldRunDirectoryIdentity(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run", "claude", early, "completed")
	writeVerification(t, root, "run", passedVerification())
	writeRun(t, root, "replacement", "codex", late, "failed")
	writeVerification(t, root, "replacement", map[string]any{
		"status":      "failed",
		"attribution": "supervisor_observed",
		"checks":      []any{},
	})

	held, err := openRunRoot(root, "run")
	if err != nil {
		t.Fatalf("open original run: %v", err)
	}
	defer held.Close()
	if err := os.Rename(filepath.Join(root, "run"), filepath.Join(root, "held-original")); err != nil {
		t.Fatalf("rename original run: %v", err)
	}
	if err := os.Rename(filepath.Join(root, "replacement"), filepath.Join(root, "run")); err != nil {
		t.Fatalf("install replacement run: %v", err)
	}

	rep, err := readRunFromRoot(held)
	if err != nil {
		t.Fatalf("read held report: %v", err)
	}
	manifest, err := readManifestFromRoot(held)
	if err != nil {
		t.Fatalf("read held manifest: %v", err)
	}
	if manifest.Provider != "claude" || fieldValue(rep.Verification, "Status") != "PASS" {
		t.Fatalf("held response mixed identities: provider=%q verification=%q", manifest.Provider, fieldValue(rep.Verification, "Status"))
	}
}

func fieldValue(fields []report.Field, name string) string {
	for _, field := range fields {
		if field.Name == name {
			return field.Value
		}
	}
	return ""
}

func TestParseViewArgsAcceptsOnlyLoopbackListeners(t *testing.T) {
	runID, listen, open, _, ok := parseViewArgs([]string{"run-ui", "--listen", "127.0.0.1:43177", "--no-open"})
	if !ok || runID != "run-ui" || listen != "127.0.0.1:43177" || open {
		t.Fatalf("parseViewArgs returned run=%q listen=%q open=%v ok=%v", runID, listen, open, ok)
	}
	for _, address := range []string{"0.0.0.0:43177", "192.0.2.1:43177", ":43177", "not-an-address"} {
		if _, _, _, _, ok := parseViewArgs([]string{"--listen", address}); ok {
			t.Errorf("accepted non-loopback listener %q", address)
		}
	}
}

func viewJSONRequest(t *testing.T, handler http.Handler, target string, out any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Host = "127.0.0.1"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d; body=%s", target, response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), out); err != nil {
		t.Fatalf("decode GET %s: %v", target, err)
	}
}

func TestViewRejectsTraversalRunID(t *testing.T) {
	root := home(t)
	handler := newViewHandler(root, "latest", false)
	t.Cleanup(func() { _ = handler.Close() })
	request := httptest.NewRequest(http.MethodGet, "/api/runs/%2e%2e%2fsecret", nil)
	request.Host = "127.0.0.1"
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		body, _ := io.ReadAll(response.Result().Body)
		t.Fatalf("status = %d, want 400; body=%s", response.Code, body)
	}
}

func TestViewAssetsDoNotDependOnExternalResources(t *testing.T) {
	for _, name := range []string{"ui_assets/index.html", "ui_assets/app.css", "ui_assets/app.js"} {
		raw, err := os.ReadFile(filepath.Join(name))
		if err != nil {
			// RED until the embedded UI exists.
			t.Fatal(err)
		}
		text := string(raw)
		if strings.Contains(text, "https://") || strings.Contains(text, "http://") {
			t.Errorf("%s depends on an external resource", name)
		}
	}
}
