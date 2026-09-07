package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/seongwoo-choi/agentrec/internal/storage"
)

func TestChangesJSONListsTrackedAndUntrackedFiles(t *testing.T) {
	root := home(t)
	b, err := storage.Create(root, "run-changes", storage.Manifest{
		Provider: "claude", Argv: []string{"claude"}, CWD: "/tmp/agentrec", RepoRoot: "/tmp/agentrec", StartedAt: early,
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
	writeViewFixture(t, filepath.Join(gitPath, trackedStatFile), `{"status":"available","attribution":"observed during run, not causal proof","baseline":"abc123","files":[{"path":"dir/a file.txt","additions":2,"deletions":1}],"totals":{"files":1,"additions":2,"deletions":1,"binary":0}}`)
	writeViewFixture(t, filepath.Join(gitPath, untrackedChangesFile), `{"attribution":"observed during run, not causal proof","count":1,"stored":1,"files":[{"path":"new.txt","kind":"file","mode":"-rw-------","size":4,"stored":true,"storedAs":"git/untracked/new.txt"}]}`)
	writeViewFixture(t, filepath.Join(gitPath, trackedPatchFile), "diff --git \"a/dir/a file.txt\" \"b/dir/a file.txt\"\n--- \"a/dir/a file.txt\"\n+++ \"b/dir/a file.txt\"\n@@ -1 +1,2 @@\n-old\n+new\n+line\n")
	if err := b.Finalize(storage.Finalization{EndedAt: late, ExitReason: "completed"}); err != nil {
		t.Fatal(err)
	}

	before := snapshot(t, root)
	code, stdout, stderr := run(t, "changes", "run-changes", "--json")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	var got struct {
		SchemaVersion int          `json:"schemaVersion"`
		RunID         string       `json:"runId"`
		Status        string       `json:"status"`
		Attribution   string       `json:"attribution"`
		Baseline      string       `json:"baseline"`
		Total         int          `json:"total"`
		Changes       []viewChange `json:"changes"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, stdout)
	}
	if got.SchemaVersion != 1 || got.RunID != "run-changes" || got.Status != "available" || got.Attribution != "observed during run, not causal proof" || got.Baseline != "abc123" || got.Total != 2 {
		t.Fatalf("changes metadata = %+v", got)
	}
	if len(got.Changes) != 2 {
		t.Fatalf("changes = %+v, want two", got.Changes)
	}
	if tracked := got.Changes[0]; tracked.Path != "dir/a file.txt" || !tracked.Tracked || tracked.Additions == nil || *tracked.Additions != 2 || tracked.Deletions == nil || *tracked.Deletions != 1 {
		t.Errorf("tracked item = %+v", tracked)
	}
	if untracked := got.Changes[1]; untracked.Path != "new.txt" || untracked.Tracked || !untracked.Stored || untracked.Size != 4 {
		t.Errorf("untracked item = %+v", untracked)
	}
	if after := snapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("changes mutated the run bundle")
	}
	code, stdout, stderr = run(t, "changes", "run-changes")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "\"tracked\"	\"dir/a file.txt\"	+2	-1") || !strings.Contains(stdout, "\"file\"	\"new.txt\"	-	-") {
		t.Fatalf("terminal output: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestChangesLatestReportsMissingEvidenceWithoutPretendingItIsEmpty(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")

	code, stdout, stderr := run(t, "changes", "--json", "latest")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	var got changesJSON
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatal(err)
	}
	if got.RunID != "run-a" || got.Status != "unavailable" || got.Reason != "repository change evidence was not recorded" || got.Changes == nil || len(got.Changes) != 0 {
		t.Fatalf("changes = %+v", got)
	}
}

func TestChangesLatestFailsWhenAnyRunIsUnreadable(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")
	broken := filepath.Join(root, "run-broken")
	if err := os.Mkdir(broken, 0o700); err != nil {
		t.Fatal(err)
	}
	writeViewFixture(t, filepath.Join(broken, manifestFile), "{")

	code, stdout, stderr := run(t, "changes", "latest", "--json")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "latest one cannot be told") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestChangesFailsClosedAboveOneViewerPage(t *testing.T) {
	root := home(t)
	b, err := storage.Create(root, "run-many", storage.Manifest{Provider: "claude", Argv: []string{"claude"}, CWD: "/tmp/agentrec", RepoRoot: "/tmp/agentrec", StartedAt: early})
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
	files := make([]map[string]any, viewPageSize+1)
	for i := range files {
		files[i] = map[string]any{"path": fmt.Sprintf("file-%03d.txt", i), "kind": "file", "mode": "-rw-------", "size": 1, "stored": false, "reason": "not stored"}
	}
	writeJSONFixture(t, filepath.Join(gitPath, resultFile), map[string]any{"status": "available", "attribution": "observed during run, not causal proof", "baseline": "abc123", "trackedFiles": 0, "added": 0, "deleted": 0, "untrackedFiles": len(files), "storedTextFiles": 0})
	writeJSONFixture(t, filepath.Join(gitPath, trackedStatFile), map[string]any{"status": "available", "attribution": "observed during run, not causal proof", "baseline": "abc123", "files": []any{}, "totals": map[string]any{"files": 0, "additions": 0, "deletions": 0, "binary": 0}})
	writeJSONFixture(t, filepath.Join(gitPath, untrackedChangesFile), map[string]any{"attribution": "observed during run, not causal proof", "count": len(files), "stored": 0, "files": files})
	if err := b.Finalize(storage.Finalization{EndedAt: late, ExitReason: "completed"}); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := run(t, "changes", "run-many", "--json")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "more than 250 changed files") || !strings.Contains(stderr, "agentrec view run-many") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestChangesRejectsInvalidAndDuplicateOptions(t *testing.T) {
	for _, args := range [][]string{{"changes"}, {"changes", "a", "b"}, {"changes", "a", "--json", "--json"}, {"changes", "a", "--raw"}, {"changes", "../a"}} {
		code, _, stderr := run(t, args...)
		if code != 2 || !strings.Contains(stderr, changesUsage) {
			t.Errorf("args=%v code=%d stderr=%q", args, code, stderr)
		}
	}
}

func TestChangesRejectsSymlinkedRepositoryEvidence(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")
	outside := t.TempDir()
	writeViewFixture(t, filepath.Join(outside, resultFile), `{"status":"outside-marker"}`)
	if err := os.Symlink(outside, filepath.Join(root, "run-a", gitDir)); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := run(t, "changes", "run-a", "--json")
	if code != 1 || stdout != "" || strings.Contains(stderr, "outside-marker") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestChangesRenderersReturnWriterErrors(t *testing.T) {
	page := viewChangePage{Status: "available", Attribution: "observed during run, not causal proof", Items: []viewChange{}}
	if err := renderChangesJSON(changesErrorWriter{}, "run-a", page); err == nil {
		t.Fatal("JSON renderer accepted writer error")
	}
	if err := renderChangesTerminal(changesErrorWriter{}, "run-a", page); err == nil {
		t.Fatal("terminal renderer accepted writer error")
	}
}

func TestChangesTerminalKeepsBaselineWhenEvidenceIsUnavailable(t *testing.T) {
	var stdout strings.Builder
	page := viewChangePage{
		Status: "unavailable", Reason: "tracked changes exist without tracked.patch",
		Attribution: "observed during run, not causal proof", Baseline: "abc123",
	}
	if err := renderChangesTerminal(&stdout, "run-a", page); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Reason: tracked changes exist without tracked.patch\n") || !strings.Contains(stdout.String(), "Baseline: abc123\n") {
		t.Fatalf("terminal output = %q", stdout.String())
	}
}

type changesErrorWriter struct{}

func (changesErrorWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func writeJSONFixture(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeViewFixture(t, path, string(raw))
}
