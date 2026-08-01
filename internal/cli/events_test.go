package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEventsSummarizesProviderReportedEventTypes(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")
	writeEventStream(t, root, "run-a", "{\"type\":\"result\",\"payload\":{\"secret\":\"nested-provider-payload\"}}\n{\"type\":\"assistant\"}\n{\"type\":\"result\"}\n")

	code, stdout, stderr := run(t, "events", "run-a")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	want := "PROVIDER-REPORTED EVENTS\nRun          run-a\nAttribution  provider_reported\nArtifact     present\nEvents       3\n\nTYPES\n\"assistant\"  1\n\"result\"  2\n"
	if stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout, want)
	}
	if strings.Contains(stdout, "nested-provider-payload") || strings.Contains(stdout, "payload") {
		t.Errorf("stdout = %q, want no arbitrary nested provider payload", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestEventsJSONEmitsStableSanitizedWrapper(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")
	writeEventStream(t, root, "run-a", "{\"z\":1,\"type\":\"assistant\",\"text\":\"line\\nnext\\u001b[31m\\u202e\"}\n")

	code, stdout, stderr := run(t, "events", "run-a", "--json")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	want := "{\"schemaVersion\":1,\"runId\":\"run-a\",\"attribution\":\"provider_reported\",\"artifactPresent\":true,\"events\":[{\"z\":1,\"type\":\"assistant\",\"text\":\"line\\nnext\\u001b[31m\\u202e\"}]}\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if strings.ContainsAny(stdout, "\n\x1b\u202e") && stdout[:len(stdout)-1] != strings.ReplaceAll(stdout[:len(stdout)-1], "\n", "") {
		t.Errorf("stdout contains an embedded terminal control: %q", stdout)
	}
	var doc struct {
		SchemaVersion   int              `json:"schemaVersion"`
		RunID           string           `json:"runId"`
		Attribution     string           `json:"attribution"`
		ArtifactPresent bool             `json:"artifactPresent"`
		Events          []map[string]any `json:"events"`
	}
	decodeJSONOutput(t, stdout, &doc)
	if doc.SchemaVersion != 1 || doc.RunID != "run-a" || doc.Attribution != "provider_reported" || !doc.ArtifactPresent || len(doc.Events) != 1 {
		t.Errorf("decoded wrapper = %#v", doc)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestEventsReportsAnAbsentArtifactWithoutGuessing(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")
	if err := os.Remove(filepath.Join(root, "run-a", providerEventsFile)); err != nil {
		t.Fatalf("remove event stream: %v", err)
	}

	code, human, stderr := run(t, "events", "run-a")
	if code != 0 || stderr != "" {
		t.Fatalf("human exit = %d, stderr = %q", code, stderr)
	}
	wantHuman := "PROVIDER-REPORTED EVENTS\nRun          run-a\nAttribution  provider_reported\nArtifact     absent\nEvents       0\n"
	if human != wantHuman {
		t.Errorf("human stdout = %q, want %q", human, wantHuman)
	}

	code, machine, stderr := run(t, "events", "run-a", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("JSON exit = %d, stderr = %q", code, stderr)
	}
	wantJSON := "{\"schemaVersion\":1,\"runId\":\"run-a\",\"attribution\":\"provider_reported\",\"artifactPresent\":false,\"events\":[]}\n"
	if machine != wantJSON {
		t.Errorf("JSON stdout = %q, want %q", machine, wantJSON)
	}
}

func TestEventsRejectsUnsafeEventArtifacts(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		root := home(t)
		writeRun(t, root, "run-a", "claude", late, "completed")
		path := filepath.Join(root, "run-a", providerEventsFile)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "outside.jsonl")
		if err := os.WriteFile(target, []byte("{\"type\":\"outside\"}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		assertEventsReadFailure(t, "run-a", "regular file")
	})

	t.Run("non-regular", func(t *testing.T) {
		root := home(t)
		writeRun(t, root, "run-a", "claude", late, "completed")
		path := filepath.Join(root, "run-a", providerEventsFile)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		assertEventsReadFailure(t, "run-a", "regular file")
	})

	t.Run("oversize artifact", func(t *testing.T) {
		root := home(t)
		writeRun(t, root, "run-a", "claude", late, "completed")
		path := filepath.Join(root, "run-a", providerEventsFile)
		if err := os.Truncate(path, int64(maxEventStreamBytes)+1); err != nil {
			t.Fatal(err)
		}
		assertEventsReadFailure(t, "run-a", "larger than")
	})

	t.Run("too many events", func(t *testing.T) {
		root := home(t)
		writeRun(t, root, "run-a", "claude", late, "completed")
		line := []byte("{}\n")
		contents := make([]byte, 0, len(line)*(maxEvents+1))
		for range maxEvents + 1 {
			contents = append(contents, line...)
		}
		writeEventStream(t, root, "run-a", string(contents))
		assertEventsReadFailure(t, "run-a", "more than")
	})

	t.Run("oversize line", func(t *testing.T) {
		root := home(t)
		writeRun(t, root, "run-a", "claude", late, "completed")
		writeEventStream(t, root, "run-a", "{\"type\":\""+strings.Repeat("x", maxEventBytes)+"\"}\n")
		assertEventsReadFailure(t, "run-a", "line longer than")
	})
}

func TestEventsRejectsAnythingExceptOneJSONObjectPerLine(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantText string
	}{
		{"malformed JSON", "{", "not a JSON object"},
		{"array", "[]", "not a JSON object"},
		{"null", "null", "not a JSON object"},
		{"number", "1", "not a JSON object"},
		{"multiple values", "{\"a\":1} {\"b\":2}", "more than one JSON value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := home(t)
			writeRun(t, root, "run-a", "claude", late, "completed")
			writeEventStream(t, root, "run-a", tt.line+"\n")
			assertEventsReadFailure(t, "run-a", tt.wantText)
		})
	}
}

func TestEventsEscapesControlsInHumanTypeNames(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")
	writeEventStream(t, root, "run-a", "{\"type\":\"assistant\\n\\u001b[31m\\u202e\",\"raw\":{\"body\":\"must-not-appear\"}}\n")

	code, stdout, stderr := run(t, "events", "run-a")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	for _, raw := range []string{"\x1b", "\u202e", "must-not-appear", "\n[31m"} {
		if strings.Contains(stdout, raw) {
			t.Errorf("stdout contains unsafe/raw value %q: %q", raw, stdout)
		}
	}
	for _, escaped := range []string{`"assistant\n\x1b[31m\u202e"`, "provider_reported"} {
		if !strings.Contains(stdout, escaped) {
			t.Errorf("stdout = %q, want %q", stdout, escaped)
		}
	}
}

func TestEventsRejectsRunDirectorySymlink(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")
	outside := t.TempDir()
	if err := os.Rename(filepath.Join(root, "run-a"), filepath.Join(root, "original")); err != nil {
		t.Fatal(err)
	}
	writeEventStream(t, filepath.Dir(outside), filepath.Base(outside), "{\"type\":\"outside\"}\n")
	if err := os.Symlink(outside, filepath.Join(root, "run-a")); err != nil {
		t.Fatal(err)
	}
	assertEventsReadFailure(t, "run-a", "real directory")
}

func TestEventsKeepsEscapedTypeNamesDistinct(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")
	writeEventStream(t, root, "run-a", "{\"type\":\"\\u001b\"}\n{\"type\":\"\\\\u001b\"}\n")
	code, stdout, stderr := run(t, "events", "run-a")
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	for _, want := range []string{`"\x1b"  1`, `"\\u001b"  1`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	}
}

func TestEventsRejectsExcessiveJSONNesting(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")
	line := "{\"x\":" + strings.Repeat("[", maxEventDepth) + "0" + strings.Repeat("]", maxEventDepth) + "}"
	writeEventStream(t, root, "run-a", line+"\n")
	assertEventsReadFailure(t, "run-a", "nesting exceeds")
}

func TestValidateEventObjectRejectsTokenBudgetOverflow(t *testing.T) {
	if _, err := validateEventObject([]byte(`{"x":[1,2,3]}`), 4); err == nil || !strings.Contains(err.Error(), "JSON tokens") {
		t.Fatalf("validateEventObject error = %v, want token bound", err)
	}
}

func TestEventsCommandUsageHelpAndLatestDispatch(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", early, "completed")
	writeRun(t, root, "run-b", "codex", late, "completed")
	writeEventStream(t, root, "run-a", "{\"type\":\"old\"}\n")
	writeEventStream(t, root, "run-b", "{\"type\":\"new\"}\n")

	code, stdout, stderr := run(t, "events", "latest")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "new") || strings.Contains(stdout, "old") {
		t.Errorf("latest: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, args := range [][]string{{"events"}, {"events", "run-a", "--yaml"}, {"events", "run-a", "--json", "extra"}} {
		code, stdout, stderr := run(t, args...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, "usage: agentrec events") {
			t.Errorf("%v: exit=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
	}
	code, stdout, stderr = run(t, "--help")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "agentrec events <run-id>|latest [--json]") {
		t.Errorf("help: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func assertEventsReadFailure(t *testing.T, runID, wantText string) {
	t.Helper()
	code, stdout, stderr := run(t, "events", runID)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr %q)", code, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, wantText) {
		t.Errorf("stderr = %q, want %q", stderr, wantText)
	}
}

func writeEventStream(t *testing.T, root, runID, contents string) {
	t.Helper()
	path := filepath.Join(root, runID, providerEventsFile)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write event stream: %v", err)
	}
}

func decodeJSONOutput(t *testing.T, stdout string, dst any) {
	t.Helper()
	if err := json.Unmarshal([]byte(stdout), dst); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
	}
}
