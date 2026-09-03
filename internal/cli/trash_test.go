package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/lock"
	"github.com/seongwoo-choi/agentrec/internal/storage"
)

func viewMutate(t *testing.T, handler http.Handler, method, target, token string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, nil)
	request.Host = "127.0.0.1:7788"
	if token != "" {
		request.Header.Set("X-Agentrec-Token", token)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func viewToken(t *testing.T, handler http.Handler) string {
	t.Helper()
	var out struct{ Token string }
	viewJSONRequest(t, handler, "/api/token", &out)
	if len(out.Token) != 32 {
		t.Fatalf("token = %q, want 32 hex characters", out.Token)
	}
	return out.Token
}

func viewRunIDs(t *testing.T, handler http.Handler) []string {
	t.Helper()
	var list struct {
		Runs []struct{ ID string } `json:"runs"`
	}
	viewJSONRequest(t, handler, "/api/runs", &list)
	var ids []string
	for _, run := range list.Runs {
		ids = append(ids, run.ID)
	}
	return ids
}

// Deleting from the viewer moves the run to the trash and restoring brings it
// back; only a request the page itself made — with the token only its script
// can read, from this origin — is honoured.
func TestViewDeletesIntoTheTrashAndRestores(t *testing.T) {
	root := home(t)
	at := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	writeRun(t, root, "run-a", "claude", at, "completed")
	writeRun(t, root, "run-b", "codex", at.Add(time.Hour), "completed")
	handler := newViewHandler(root, "latest", false)
	t.Cleanup(func() { handler.Close() })
	token := viewToken(t, handler)

	for name, headers := range map[string]map[string]string{
		"no token":     nil,
		"cross-site":   {"Sec-Fetch-Site": "cross-site"},
		"other origin": {"Origin": "https://evil.example"},
	} {
		tok := token
		if name == "no token" {
			tok = ""
		}
		if res := viewMutate(t, handler, http.MethodDelete, "/api/runs/run-a", tok, headers); res.Code != http.StatusForbidden {
			t.Errorf("DELETE with %s = %d, want 403", name, res.Code)
		}
	}
	if got := viewRunIDs(t, handler); strings.Join(got, " ") != "run-b run-a" {
		t.Fatalf("runs after refused deletes = %v", got)
	}

	if res := viewMutate(t, handler, http.MethodDelete, "/api/runs/run-a", token, map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://127.0.0.1:7788"}); res.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d %s, want 204", res.Code, res.Body.String())
	}
	if got := viewRunIDs(t, handler); strings.Join(got, " ") != "run-b" {
		t.Errorf("runs after delete = %v, want only run-b", got)
	}
	if _, err := os.Stat(filepath.Join(trashRootFor(root), "run-a", "manifest.json")); err != nil {
		t.Errorf("trashed run is not in the trash: %v", err)
	}
	if res := viewMutate(t, handler, http.MethodDelete, "/api/runs/run-a", token, nil); res.Code != http.StatusNotFound {
		t.Errorf("DELETE of a trashed run = %d, want 404", res.Code)
	}
	if res := viewMutate(t, handler, http.MethodPost, "/api/runs/run-a/restore", token, nil); res.Code != http.StatusNoContent {
		t.Fatalf("restore = %d %s, want 204", res.Code, res.Body.String())
	}
	if got := viewRunIDs(t, handler); strings.Join(got, " ") != "run-b run-a" {
		t.Errorf("runs after restore = %v", got)
	}
	if res := viewMutate(t, handler, http.MethodPost, "/api/runs/run-a/restore", token, nil); res.Code != http.StatusNotFound {
		t.Errorf("restore of a run that is not in the trash = %d, want 404", res.Code)
	}
	if res := viewMutate(t, handler, http.MethodDelete, "/api/runs/..%2f..", token, nil); res.Code == http.StatusNoContent {
		t.Errorf("DELETE with a traversal id was accepted")
	}
}

// A session that is still being recorded cannot be deleted: its recorder
// would keep writing into the trash.
func TestViewRefusesToDeleteAnOpenSession(t *testing.T) {
	root := home(t)
	sessionSocketHome(t)
	const sessionID = "session-open-del"
	b, err := storage.Create(root, "run-open", storage.Manifest{Provider: "claude", CWD: "/tmp", StartedAt: time.Now(), Mode: storage.ModeSession, SessionID: sessionID})
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
	handler := newViewHandler(root, "latest", false)
	t.Cleanup(func() { handler.Close() })
	if res := viewMutate(t, handler, http.MethodDelete, "/api/runs/run-open", viewToken(t, handler), nil); res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "still open") {
		t.Errorf("DELETE of an open session = %d %s, want 409 saying it is open", res.Code, res.Body.String())
	}
}

// A traced run being recorded — its repository lock held — cannot be
// deleted; the moment the lock is gone, an unfinished run that nobody is
// writing may go once the grace period has passed.
func TestViewRefusesToDeleteATraceStillBeingRecorded(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	b, err := storage.Create(root, "run-trace", storage.Manifest{Provider: "claude", Argv: []string{"claude"}, CWD: repo, StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteAction(readAction(time.Now())); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	held, err := lock.Acquire(ctx, filepath.Join(filepath.Dir(root), locksDirName), repo)
	if err != nil {
		t.Fatal(err)
	}
	handler := newViewHandler(root, "latest", false)
	t.Cleanup(func() { handler.Close() })
	token := viewToken(t, handler)
	if res := viewMutate(t, handler, http.MethodDelete, "/api/runs/run-trace", token, nil); res.Code != http.StatusConflict {
		t.Errorf("DELETE of a trace being recorded = %d %s, want 409", res.Code, res.Body.String())
	}
	held.Release()
	// Nobody holds the repository now, but the manifest was written moments
	// ago: a recorder that died is told from one still preparing by time.
	if res := viewMutate(t, handler, http.MethodDelete, "/api/runs/run-trace", token, nil); res.Code != http.StatusConflict {
		t.Errorf("DELETE of a fresh unfinished run = %d, want 409 within the grace period", res.Code)
	}
	old := time.Now().Add(-2 * closeOutGrace)
	if err := os.Chtimes(filepath.Join(root, "run-trace", manifestFile), old, old); err != nil {
		t.Fatal(err)
	}
	if res := viewMutate(t, handler, http.MethodDelete, "/api/runs/run-trace", token, nil); res.Code != http.StatusNoContent {
		t.Errorf("DELETE of an abandoned run = %d %s, want 204", res.Code, res.Body.String())
	}
}

// A run that ended but whose close-out has not filed the report yet is still
// being written; a deleted run is no longer named as the one to open.
func TestViewRefusesToDeleteDuringCloseOutAndForgetsTheInitialRun(t *testing.T) {
	root := home(t)
	at := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	writeRun(t, root, "run-old", "claude", at, "completed")
	writeRun(t, root, "run-new", "claude", time.Now().Add(-time.Minute), "completed")
	handler := newViewHandler(root, "run-old", false)
	t.Cleanup(func() { handler.Close() })
	token := viewToken(t, handler)
	if res := viewMutate(t, handler, http.MethodDelete, "/api/runs/run-new", token, nil); res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "closed out") {
		t.Errorf("DELETE of a run closing out = %d %s, want 409", res.Code, res.Body.String())
	}
	if res := viewMutate(t, handler, http.MethodDelete, "/api/runs/run-old", token, nil); res.Code != http.StatusNoContent {
		t.Fatalf("DELETE of an old run = %d %s", res.Code, res.Body.String())
	}
	var list struct {
		InitialRunID string `json:"initialRunId"`
	}
	viewJSONRequest(t, handler, "/api/runs", &list)
	if list.InitialRunID != "" {
		t.Errorf("initialRunId after deleting it = %q, want none", list.InitialRunID)
	}
}

// The trash is a place, not a black hole: it can be listed, a run restored
// from it, and it can be emptied on purpose.
func TestTrashRunRefusesASymlinkedTrashRoot(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", early, "completed")
	outside := t.TempDir()
	if err := os.Symlink(outside, trashRootFor(root)); err != nil {
		t.Fatal(err)
	}

	if err := trashRun(root, "run-a"); err == nil {
		t.Fatal("trashRun accepted a symlinked trash root")
	}
	if _, err := os.Stat(filepath.Join(root, "run-a", manifestFile)); err != nil {
		t.Fatalf("run moved despite refusal: %v", err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("symlink target entries = %v, %v, want untouched", entries, err)
	}
}

func TestTrashEmptyRefusesARelocatedTrashRoot(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", early, "completed")
	if err := trashRun(root, "run-a"); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(t.TempDir(), "moved-trash")
	var moveErr error

	if _, err := emptyTrash(root, func() {
		moveErr = os.Rename(trashRootFor(root), moved)
		if moveErr == nil {
			writeRun(t, trashRootFor(root), "run-a", "codex", early, "completed")
		}
	}); err == nil {
		t.Fatal("trash empty followed a trash root moved outside the data root")
	}
	if moveErr != nil {
		t.Fatalf("move trash root: %v", moveErr)
	}
	if _, err := os.Stat(filepath.Join(moved, "run-a", manifestFile)); err != nil {
		t.Fatalf("moved run deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(trashRootFor(root), "run-a", manifestFile)); err != nil {
		t.Fatalf("replacement run deleted: %v", err)
	}
}

func TestTrashEmptyRefusesASymlinkedTrashRoot(t *testing.T) {
	root := home(t)
	outside := t.TempDir()
	victim := filepath.Join(outside, "keep")
	if err := os.Mkdir(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "evidence"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, trashRootFor(root)); err != nil {
		t.Fatal(err)
	}

	if code, _, _ := run(t, "trash", "empty"); code != exitFailure {
		t.Fatalf("trash empty exit = %d, want %d for a symlinked trash root", code, exitFailure)
	}
	if got, err := os.ReadFile(filepath.Join(victim, "evidence")); err != nil || string(got) != "keep" {
		t.Fatalf("external evidence = %q, %v, want untouched", got, err)
	}
}

func TestRestoreRunRefusesASymlinkedTrashRoot(t *testing.T) {
	root := home(t)
	outside := t.TempDir()
	writeRun(t, outside, "run-a", "claude", early, "completed")
	if err := os.Symlink(outside, trashRootFor(root)); err != nil {
		t.Fatal(err)
	}

	if err := restoreRun(root, "run-a"); err == nil {
		t.Fatal("restoreRun accepted a symlinked trash root")
	}
	if _, err := os.Stat(filepath.Join(outside, "run-a", manifestFile)); err != nil {
		t.Fatalf("external run moved despite refusal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "run-a")); !os.IsNotExist(err) {
		t.Fatalf("runs root entry = %v, want absent", err)
	}
}

func TestTrashListRefusesASymlinkedTrashRoot(t *testing.T) {
	root := home(t)
	outside := t.TempDir()
	writeRun(t, outside, "run-a", "claude", early, "completed")
	if err := os.Symlink(outside, trashRootFor(root)); err != nil {
		t.Fatal(err)
	}

	if code, stdout, _ := run(t, "trash"); code != exitFailure {
		t.Fatalf("trash list exit = %d, want %d; output:\n%s", code, exitFailure, stdout)
	}
}

func TestTrashListKeepsTheOpenedRootAfterPathReplacement(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", early, "completed")
	if err := trashRun(root, "run-a"); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	writeRun(t, outside, "run-b", "codex", late, "completed")
	parked := filepath.Join(filepath.Dir(root), "trash-held")
	var replaceErr error

	runs, unreadable, err := listTrash(root, func() {
		if replaceErr = os.Rename(trashRootFor(root), parked); replaceErr == nil {
			replaceErr = os.Symlink(outside, trashRootFor(root))
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if replaceErr != nil {
		t.Fatalf("replace trash root: %v", replaceErr)
	}
	if unreadable != 0 || len(runs) != 1 || runs[0].ID != "run-a" {
		t.Fatalf("listed runs = %+v, unreadable %d, want held run-a", runs, unreadable)
	}
}

func TestTrashCommandListsRestoresAndEmpties(t *testing.T) {
	root := home(t)
	at := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	writeRun(t, root, "run-a", "claude", at, "completed")
	if code, stdout, _ := run(t, "trash"); code != 0 || !strings.Contains(stdout, "The trash is empty.") {
		t.Errorf("empty trash: exit %d %q", code, stdout)
	}
	if err := trashRun(root, "run-a"); err != nil {
		t.Fatal(err)
	}
	if code, stdout, _ := run(t, "trash"); code != 0 || !strings.Contains(stdout, "run-a  claude  tmp  completed") || !strings.Contains(stdout, "agentrec trash restore") {
		t.Errorf("trash listing: exit %d\n%s", code, stdout)
	}
	if code, stdout, _ := run(t, "list"); code != 0 || strings.Contains(stdout, "run-a") {
		t.Errorf("list still shows the trashed run: exit %d\n%s", code, stdout)
	}
	if code, stdout, _ := run(t, "trash", "restore", "run-a"); code != 0 || !strings.Contains(stdout, "restored run-a") {
		t.Errorf("restore: exit %d %q", code, stdout)
	}
	if code, _, stderr := run(t, "trash", "restore", "run-a"); code != exitFailure || !strings.Contains(stderr, "no such run in the trash") {
		t.Errorf("restore twice: exit %d %q", code, stderr)
	}
	if err := trashRun(root, "run-a"); err != nil {
		t.Fatal(err)
	}
	if code, stdout, _ := run(t, "trash", "empty"); code != 0 || !strings.Contains(stdout, "erased 1 run(s)") {
		t.Errorf("empty: exit %d %q", code, stdout)
	}
	if entries, _ := os.ReadDir(trashRootFor(root)); len(entries) != 0 {
		t.Errorf("trash still holds %d entries after empty", len(entries))
	}
	for _, args := range [][]string{{"trash", "restore"}, {"trash", "empty", "now"}, {"trash", "bogus"}} {
		if code, _, _ := run(t, args...); code != exitUsage {
			t.Errorf("%v exit = %d, want %d", args, code, exitUsage)
		}
	}
}

// The guard on both mutations: a token of the right shape but the wrong
// value, a same-site or foreign http origin, a traversal id, and two viewers
// never sharing a token.
func TestViewMutationGuardsCoverBothEndpoints(t *testing.T) {
	root := home(t)
	at := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	writeRun(t, root, "run-a", "claude", at, "completed")
	handler := newViewHandler(root, "latest", false)
	t.Cleanup(func() { handler.Close() })
	token := viewToken(t, handler)
	other := newViewHandler(root, "latest", false)
	t.Cleanup(func() { other.Close() })
	if viewToken(t, other) == token {
		t.Errorf("two viewers were given the same token")
	}
	wrong := strings.Repeat("0", len(token))
	if wrong == token {
		wrong = strings.Repeat("f", len(token))
	}
	for _, tc := range []struct {
		name    string
		token   string
		headers map[string]string
	}{
		{"wrong token", wrong, nil},
		{"same-site", token, map[string]string{"Sec-Fetch-Site": "same-site"}},
		{"http foreign origin", token, map[string]string{"Origin": "http://evil.example"}},
		{"other port", token, map[string]string{"Origin": "http://127.0.0.1:1"}},
	} {
		if res := viewMutate(t, handler, http.MethodDelete, "/api/runs/run-a", tc.token, tc.headers); res.Code != http.StatusForbidden {
			t.Errorf("DELETE with %s = %d, want 403", tc.name, res.Code)
		}
		if res := viewMutate(t, handler, http.MethodPost, "/api/runs/run-a/restore", tc.token, tc.headers); res.Code != http.StatusForbidden {
			t.Errorf("restore with %s = %d, want 403", tc.name, res.Code)
		}
	}
	if err := trashRun(root, "run-a"); err != nil {
		t.Fatal(err)
	}
	if res := viewMutate(t, handler, http.MethodPost, "/api/runs/run-a/restore", "", nil); res.Code != http.StatusForbidden {
		t.Errorf("restore without a token = %d, want 403", res.Code)
	}
	for _, id := range []string{"../run-a", "run-a/..", ".", "a/b"} {
		if err := restoreRun(root, id); err == nil {
			t.Errorf("restoreRun(%q) accepted a bad id", id)
		}
		if err := trashRun(root, id); err == nil {
			t.Errorf("trashRun(%q) accepted a bad id", id)
		}
	}
	// What is already there is never overwritten, in either direction.
	writeRun(t, root, "run-a", "claude", at, "completed")
	if err := restoreRun(root, "run-a"); !errors.Is(err, errRunExists) {
		t.Errorf("restore over an existing run = %v, want errRunExists", err)
	}
	if err := trashRun(root, "run-a"); !errors.Is(err, errRunExists) {
		t.Errorf("trash over a trashed run = %v, want errRunExists", err)
	}
	if err := os.WriteFile(filepath.Join(root, "not-a-run"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := trashRun(root, "not-a-run"); err == nil || !strings.Contains(err.Error(), "not a run") {
		t.Errorf("trashRun(file) = %v, want a refusal", err)
	}
}
