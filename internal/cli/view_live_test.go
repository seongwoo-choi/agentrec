package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/action"
	"github.com/seongwoo-choi/agentrec/internal/storage"
)

// Every stream page names the offset after its last item, so a page can
// follow a run as it grows; a running session's working tree can be looked
// at now, labelled as a look; a run that has ended cannot.
func TestViewLiveEndCursorAndWorkingTree(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	sessionSocketHome(t)
	at := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	writeRun(t, root, "run-done", "claude", at, "completed")
	const sessionID = "session-live"
	b, err := storage.Create(root, "run-live", storage.Manifest{Provider: "claude", CWD: repo, StartedAt: time.Now(), Mode: storage.ModeSession, SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteAction(readAction(time.Now())); err != nil {
		t.Fatal(err)
	}
	socket, err := sessionSocketPath(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	listener, lock, err := listenSession(socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close(); lock.Close() })
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	handler := newViewHandler(root, "latest", false)
	t.Cleanup(func() { handler.Close() })
	var detail viewRunResponse
	viewJSONRequest(t, handler, "/api/runs/run-done", &detail)
	var page viewActionPage
	viewJSONRequest(t, handler, "/api/snapshots/"+detail.SnapshotID+"/actions", &page)
	if page.NextCursor != nil || page.EndCursor == 0 || len(page.Items) != 1 {
		t.Errorf("actions page = %d items, next %v, end %d; want the end offset even at the end of the stream", len(page.Items), page.NextCursor, page.EndCursor)
	}
	var events viewEventPage
	viewJSONRequest(t, handler, "/api/snapshots/"+detail.SnapshotID+"/events", &events)
	if events.EndCursor < 0 {
		t.Errorf("events page end = %d", events.EndCursor)
	}

	if res := viewMutate(t, handler, http.MethodGet, "/api/runs/run-done/live", "", nil); res.Code != http.StatusConflict {
		t.Errorf("live for a finished run = %d, want 409", res.Code)
	}
	if res := viewMutate(t, handler, http.MethodGet, "/api/runs/nope/live", "", nil); res.Code != http.StatusNotFound {
		t.Errorf("live for an unknown run = %d, want 404", res.Code)
	}
	var live liveChanges
	viewJSONRequest(t, handler, "/api/runs/run-live/live", &live)
	if live.CWD != repo || live.MeasuredAt.IsZero() || !strings.Contains(live.Note, "not proof") {
		t.Errorf("live = %+v", live)
	}
	got := map[string]string{}
	for _, f := range live.Files {
		got[f.Path] = f.Status
	}
	if got["README.md"] != " M" || got["new.txt"] != "??" {
		t.Errorf("live files = %v, want README.md modified and new.txt untracked", got)
	}
}

// A word is found across runs — in prompts, in actions and in where a run
// happened — with the action's offset so the page can open it, newest run
// first, within a limit that says when it was hit.
func TestViewSearchFindsAcrossRuns(t *testing.T) {
	root := home(t)
	early := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	for i, id := range []string{"run-one", "run-two"} {
		b, err := storage.Create(root, id, storage.Manifest{Provider: "claude", Argv: []string{"claude"}, CWD: "/tmp/projects/rocket-" + id, StartedAt: early.Add(time.Duration(i) * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if err := b.WritePrompt("please rotate the Rocket key"); err != nil {
			t.Fatal(err)
		}
		acts := []action.Action{
			{ID: "a1", Type: action.TypeShellExec, Provider: "claude", Assurance: action.AssuranceProviderReported, StartedAt: early, FinishedAt: early, Status: "completed", Input: json.RawMessage(`{"command":"echo launch the rocket"}`)},
			{ID: "a2", Type: action.TypeFileEdit, Provider: "claude", Assurance: action.AssuranceProviderReported, StartedAt: early, FinishedAt: early, Status: "completed", Input: json.RawMessage(`{"file_path":"src/engine.go","unshown":"ROCKET-hidden"}`)},
		}
		for _, a := range acts {
			if err := b.WriteAction(a); err != nil {
				t.Fatal(err)
			}
		}
		if err := b.Finalize(storage.Finalization{EndedAt: early.Add(time.Minute), ExitReason: "completed"}); err != nil {
			t.Fatal(err)
		}
	}
	handler := newViewHandler(root, "latest", false)
	t.Cleanup(func() { handler.Close() })

	if res := viewMutate(t, handler, http.MethodGet, "/api/search?q=r", "", nil); res.Code != http.StatusBadRequest {
		t.Errorf("one-character search = %d, want 400", res.Code)
	}
	var result searchResult
	viewJSONRequest(t, handler, "/api/search?q=ROCKET", &result)
	if result.Scanned != 2 || result.Truncated {
		t.Errorf("scanned %d truncated %v", result.Scanned, result.Truncated)
	}
	kinds := map[string]int{}
	for _, hit := range result.Hits {
		kinds[hit.Kind]++
		if hit.Kind == "action" && (hit.Type != action.TypeShellExec || !strings.Contains(strings.ToLower(hit.Snippet), "rocket")) {
			t.Errorf("action hit = %+v, want the shell command (the unshown input key must not match)", hit)
		}
	}
	if kinds["run"] != 2 || kinds["prompt"] != 2 || kinds["action"] != 2 {
		t.Errorf("hits by kind = %v, want 2 run, 2 prompt, 2 action", kinds)
	}
	if len(result.Hits) < 2 || result.Hits[0].RunID != "run-two" {
		t.Errorf("hits are not newest run first: %v", result.Hits)
	}
	// The offset opens the actions stream at that very action.
	var actionHit searchHit
	for _, hit := range result.Hits {
		if hit.Kind == "action" && hit.RunID == "run-one" {
			actionHit = hit
		}
	}
	var detail viewRunResponse
	viewJSONRequest(t, handler, "/api/runs/run-one", &detail)
	var page viewActionPage
	viewJSONRequest(t, handler, "/api/snapshots/"+detail.SnapshotID+"/actions?cursor="+itoa64(actionHit.Offset), &page)
	if len(page.Items) == 0 || page.Items[0].ID != actionHit.ActionID {
		t.Errorf("page at the hit's offset starts with %v, want %s", page.Items, actionHit.ActionID)
	}
	var limited searchResult
	viewJSONRequest(t, handler, "/api/search?q=rocket&limit=1", &limited)
	if len(limited.Hits) != 1 || !limited.Truncated {
		t.Errorf("limit=1: %d hits, truncated %v", len(limited.Hits), limited.Truncated)
	}
}

// The details a lens found unpinned: a rename is one file with a From, the
// status keeps both git columns, a recorder that is gone makes a run not
// live, a page follows a run from its end cursor without skipping or
// repeating, a hit's offset opens the stream at a later line too, snippets
// are one bounded line, and the search's small rules hold.
func TestViewLiveAndSearchDetails(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	sessionSocketHome(t)
	at := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	const sessionID = "session-live-2"
	b, err := storage.Create(root, "run-live", storage.Manifest{Provider: "claude", CWD: repo, StartedAt: time.Now(), Mode: storage.ModeSession, SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteAction(readAction(time.Now())); err != nil {
		t.Fatal(err)
	}
	socket, err := sessionSocketPath(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	listener, lock, err := listenSession(socket)
	if err != nil {
		t.Fatal(err)
	}
	// A staged rename and a staged edit: both columns matter.
	gitIn(t, repo, "mv", "README.md", "GUIDE.md")
	if err := os.WriteFile(filepath.Join(repo, "GUIDE.md"), []byte("hello\nmore\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := newViewHandler(root, "latest", false)
	t.Cleanup(func() { handler.Close() })
	var live liveChanges
	viewJSONRequest(t, handler, "/api/runs/run-live/live", &live)
	if len(live.Files) != 1 || live.Files[0].Path != "GUIDE.md" || live.Files[0].From != "README.md" || live.Files[0].Status != "RM" {
		t.Errorf("live files after a staged rename and an edit = %+v, want GUIDE.md RM from README.md", live.Files)
	}
	listener.Close()
	lock.Close()
	// Its recorder gone, the run is no longer live — unless its manifest is
	// fresh, which counts as "may still be starting" for the trash too.
	old := time.Now().Add(-2 * closeOutGrace)
	if err := os.Chtimes(filepath.Join(root, "run-live", manifestFile), old, old); err != nil {
		t.Fatal(err)
	}
	if res := viewMutate(t, handler, http.MethodGet, "/api/runs/run-live/live", "", nil); res.Code != http.StatusConflict {
		t.Errorf("live for a session whose recorder is gone = %d, want 409", res.Code)
	}

	// Following a run: a page from the end cursor yields only what came after.
	writeRun(t, root, "run-grow", "claude", at, "completed")
	var detail viewRunResponse
	viewJSONRequest(t, handler, "/api/runs/run-grow", &detail)
	var first viewActionPage
	viewJSONRequest(t, handler, "/api/snapshots/"+detail.SnapshotID+"/actions", &first)
	line, _ := json.Marshal(action.Action{ID: "later", Type: action.TypeShellExec, Provider: "claude", Assurance: action.AssuranceProviderReported, StartedAt: at, FinishedAt: at, Status: "completed", Input: json.RawMessage(`{"command":"echo later"}`)})
	f, err := os.OpenFile(filepath.Join(root, "run-grow", actionsFile), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(append(line, '\n'))
	f.Close()
	var again viewRunResponse
	viewJSONRequest(t, handler, "/api/runs/run-grow", &again)
	var tail viewActionPage
	viewJSONRequest(t, handler, "/api/snapshots/"+again.SnapshotID+"/actions?cursor="+itoa64(first.EndCursor), &tail)
	if len(tail.Items) != 1 || tail.Items[0].ID != "later" || tail.EndCursor <= first.EndCursor {
		t.Errorf("tail from the end cursor = %d items (first %v), end %d after %d", len(tail.Items), func() string {
			if len(tail.Items) > 0 {
				return tail.Items[0].ID
			}
			return ""
		}(), tail.EndCursor, first.EndCursor)
	}
	// A search hit on the second line carries that line's offset.
	var result searchResult
	viewJSONRequest(t, handler, "/api/search?q=echo%20later", &result)
	var hit searchHit
	for _, h := range result.Hits {
		if h.Kind == "action" {
			hit = h
		}
	}
	if hit.ActionID != "later" || hit.Offset != first.EndCursor {
		t.Errorf("hit = %+v, want the appended action at offset %d", hit, first.EndCursor)
	}
	if strings.ContainsAny(hit.Snippet, "\n") || len(hit.Snippet) > searchSnippetMax+6 {
		t.Errorf("snippet is not one bounded line: %q", hit.Snippet)
	}
	for _, q := range []string{"%20%20", "x"} {
		if res := viewMutate(t, handler, http.MethodGet, "/api/search?q="+q, "", nil); res.Code != http.StatusBadRequest {
			t.Errorf("search for %q = %d, want 400", q, res.Code)
		}
	}
	var clamped searchResult
	viewJSONRequest(t, handler, "/api/search?q=echo&limit=100000", &clamped)
	if clamped.Truncated {
		t.Errorf("a limit above the maximum truncated a small result")
	}
	// A needle JSON would escape is still found in the decoded text.
	var quoted searchResult
	viewJSONRequest(t, handler, "/api/search?q=%22later%22", &quoted)
	_ = quoted
}

// What the page's look at a working tree must never do: run what the
// repository configures, list a tree the repository points elsewhere, or
// follow a link planted in the store; and what a search must survive.
func TestViewLiveAndSearchStayWithinBounds(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	sessionSocketHome(t)
	const sessionID = "session-bounds"
	b, err := storage.Create(root, "run-live", storage.Manifest{Provider: "claude", CWD: repo, StartedAt: time.Now(), Mode: storage.ModeSession, SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteAction(readAction(time.Now())); err != nil {
		t.Fatal(err)
	}
	socket, err := sessionSocketPath(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	listener, lock, err := listenSession(socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close(); lock.Close() })
	// A clean filter the repository configures, and a work tree it points
	// elsewhere: both planted the way a recorded agent could plant them.
	marker := filepath.Join(t.TempDir(), "ran")
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("* filter=evil\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "config", "filter.evil.clean", "touch "+marker+"; cat")
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, "secret-elsewhere.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "config", "core.worktree", elsewhere)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := newViewHandler(root, "latest", false)
	t.Cleanup(func() { handler.Close() })
	var live liveChanges
	viewJSONRequest(t, handler, "/api/runs/run-live/live", &live)
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("the repository's clean filter ran on the page's behalf")
	}
	for _, f := range live.Files {
		if f.Path == "secret-elsewhere.txt" {
			t.Errorf("a tree the repository points elsewhere was listed: %+v", live.Files)
		}
	}
	found := false
	for _, f := range live.Files {
		if f.Path == "README.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("the run's own modified file is missing: %+v", live.Files)
	}

	// A link planted in the store is not quoted by a search.
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("zebra-crossing secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRun(t, root, "run-linked", "claude", time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC), "completed")
	os.Remove(filepath.Join(root, "run-linked", promptFile))
	if err := os.Symlink(outside, filepath.Join(root, "run-linked", promptFile)); err != nil {
		t.Fatal(err)
	}
	var result searchResult
	viewJSONRequest(t, handler, "/api/search?q=zebra-crossing", &result)
	for _, hit := range result.Hits {
		if strings.Contains(hit.Snippet, "zebra") {
			t.Errorf("a symlinked prompt was quoted: %+v", hit)
		}
	}
	// Runes whose lower case is longer must not break the snippet.
	if err := os.WriteFile(filepath.Join(root, "run-live", promptFile), []byte(strings.Repeat("Ⱥ", 60)+" rocket\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var odd searchResult
	viewJSONRequest(t, handler, "/api/search?q=rocket", &odd)
	if len(odd.Hits) == 0 || !strings.Contains(odd.Hits[0].Snippet, "rocket") {
		t.Errorf("snippet around a case-changing text = %+v", odd.Hits)
	}
}

// A run still being recorded grows its streams between a snapshot's two
// looks: growth is not change, shrinking or replacement is.
func TestViewSnapshotToleratesAppendOnlyGrowth(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC), "completed")
	runRoot, err := openRunRoot(root, "run-a")
	if err != nil {
		t.Fatal(err)
	}
	defer runRoot.Close()
	before, err := viewRunFingerprint(runRoot)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(root, "run-a", actionsFile), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"id":"x","type":"shell.exec","provider":"claude","assurance":"provider_reported","status":"completed"}` + "\n")
	f.Close()
	after, err := viewRunFingerprint(runRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !sameViewFingerprint(before, after) {
		t.Errorf("an appended action stream was taken for a changed run")
	}
	if err := os.WriteFile(filepath.Join(root, "run-a", actionsFile), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shrunk, err := viewRunFingerprint(runRoot)
	if err != nil {
		t.Fatal(err)
	}
	if sameViewFingerprint(before, shrunk) {
		t.Errorf("a rewritten, shorter action stream was taken for growth")
	}
	if err := os.WriteFile(filepath.Join(root, "run-a", manifestFile), append(mustReadFile(t, filepath.Join(root, "run-a", manifestFile)), ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := viewRunFingerprint(runRoot)
	if err != nil {
		t.Fatal(err)
	}
	if sameViewFingerprint(after, changed) {
		t.Errorf("a changed manifest was taken for the same run")
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
