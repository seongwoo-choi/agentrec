package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seongwoo-choi/agentrec/internal/action"
	"github.com/seongwoo-choi/agentrec/internal/report"
	"github.com/seongwoo-choi/agentrec/internal/storage"
)

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

	handler := newViewHandler(root, "run-ui")
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
	handler := newViewHandler(root, "run-pages")
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
	handler := newViewHandler(root, "run-byte-pages")
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
	handler := newViewHandler(root, "run-immutable")
	var detail struct {
		SnapshotID string `json:"snapshotId"`
	}
	viewJSONRequest(t, handler, "/api/runs/run-immutable", &detail)
	snapshot := handler.snapshots.byID[detail.SnapshotID]
	if _, err := os.Stat(snapshot.actions.Name()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("action snapshot still has a reachable pathname: %v", err)
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
	handler := newViewHandler(root, "run-ui")
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

func TestViewRunListKeepsUnavailableVerificationNeutral(t *testing.T) {
	tests := []struct {
		name, exit, verification, wantClass, wantLabel string
	}{
		{"unavailable verification", "completed", "UNAVAILABLE", "", "UNAVAILABLE"},
		{"passed verification", "completed", "PASS", "pass", "PASS"},
		{"failed verification", "completed", "FAIL", "fail", "FAIL"},
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
	handler := newViewHandler(root, "run-old")
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
		if run.StatusClass != "" || run.StatusLabel != "UNAVAILABLE" {
			t.Errorf("run %s status = (%q, %q), want neutral UNAVAILABLE", run.ID, run.StatusClass, run.StatusLabel)
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
	runID, listen, open, ok := parseViewArgs([]string{"run-ui", "--listen", "127.0.0.1:43177", "--no-open"})
	if !ok || runID != "run-ui" || listen != "127.0.0.1:43177" || open {
		t.Fatalf("parseViewArgs returned run=%q listen=%q open=%v ok=%v", runID, listen, open, ok)
	}
	for _, address := range []string{"0.0.0.0:43177", "192.0.2.1:43177", ":43177", "not-an-address"} {
		if _, _, _, ok := parseViewArgs([]string{"--listen", address}); ok {
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
	handler := newViewHandler(root, "latest")
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
