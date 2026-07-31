package action

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestActionJSONRoundTrip(t *testing.T) {
	started := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	original := Action{
		ID:         "a1",
		ParentID:   "a0",
		Type:       TypeShellExec,
		Provider:   "claude-code",
		Assurance:  AssuranceProviderReported,
		StartedAt:  started,
		FinishedAt: started.Add(time.Second),
		Status:     "ok",
		Input:      json.RawMessage(`{"cmd":"ls"}`),
		Result:     json.RawMessage(`{"exitCode":0}`),
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var decoded Action
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if decoded.ID != original.ID || decoded.ParentID != original.ParentID {
		t.Errorf("ids = %q/%q, want %q/%q", decoded.ID, decoded.ParentID, original.ID, original.ParentID)
	}
	if decoded.Type != original.Type || decoded.Provider != original.Provider {
		t.Errorf("type/provider = %q/%q, want %q/%q", decoded.Type, decoded.Provider, original.Type, original.Provider)
	}
	if decoded.Assurance != original.Assurance {
		t.Errorf("assurance = %q, want %q", decoded.Assurance, original.Assurance)
	}
	if !decoded.StartedAt.Equal(original.StartedAt) || !decoded.FinishedAt.Equal(original.FinishedAt) {
		t.Errorf("times = %v/%v, want %v/%v", decoded.StartedAt, decoded.FinishedAt, original.StartedAt, original.FinishedAt)
	}
	if decoded.Status != original.Status {
		t.Errorf("status = %q, want %q", decoded.Status, original.Status)
	}

	var input, result map[string]any
	if err := json.Unmarshal(decoded.Input, &input); err != nil {
		t.Fatalf("input is not a JSON object: %v", err)
	}
	if err := json.Unmarshal(decoded.Result, &result); err != nil {
		t.Fatalf("result is not a JSON object: %v", err)
	}
	if input["cmd"] != "ls" {
		t.Errorf("input cmd = %v, want ls", input["cmd"])
	}
	if result["exitCode"] != float64(0) {
		t.Errorf("result exitCode = %v, want 0", result["exitCode"])
	}
}

func TestWriterStreamsOneJSONLineForEachAction(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	first := Action{
		ID:        "a1",
		Type:      TypeFileRead,
		Assurance: AssuranceProviderReported,
		Input:     json.RawMessage(`{"path":"main.go"}`),
	}
	if err := w.Write(first); err != nil {
		t.Fatalf("Write(first) error = %v", err)
	}
	if got := strings.Count(buf.String(), "\n"); got != 1 {
		t.Fatalf("after first Write, newline count = %d, want 1 (writer must stream, not buffer)", got)
	}

	second := Action{
		ID:        "a2",
		ParentID:  "a1",
		Type:      TypeToolCall,
		Assurance: AssuranceProviderReported,
		Result:    json.RawMessage(`{"passed":true}`),
	}
	if err := w.Write(second); err != nil {
		t.Fatalf("Write(second) error = %v", err)
	}

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("line count = %d, want 2; output = %q", len(lines), buf.String())
	}

	var decodedFirst, decodedSecond Action
	if err := json.Unmarshal([]byte(lines[0]), &decodedFirst); err != nil {
		t.Fatalf("line 1 is not valid JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &decodedSecond); err != nil {
		t.Fatalf("line 2 is not valid JSON: %v", err)
	}
	if decodedFirst.ID != "a1" || decodedSecond.ID != "a2" {
		t.Errorf("ids = %q, %q; want a1, a2", decodedFirst.ID, decodedSecond.ID)
	}
	if decodedSecond.ParentID != "a1" {
		t.Errorf("parentId = %q, want a1", decodedSecond.ParentID)
	}

	if !strings.Contains(lines[0], `"input":{"path":"main.go"}`) {
		t.Errorf("line 1 = %s; want input embedded as a JSON object, not a quoted string", lines[0])
	}
	if !strings.Contains(lines[1], `"result":{"passed":true}`) {
		t.Errorf("line 2 = %s; want result embedded as a JSON object, not a quoted string", lines[1])
	}
}

func TestWriterOmitsZeroTimestamps(t *testing.T) {
	var buf bytes.Buffer
	a := Action{ID: "a1", Type: TypeAgentMessage, Assurance: AssuranceProviderReported}

	if err := NewWriter(&buf).Write(a); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	line := buf.String()
	if strings.Contains(line, "startedAt") || strings.Contains(line, "finishedAt") {
		t.Errorf("line = %s; want zero timestamps omitted, not persisted as year-1 values", line)
	}
}

func TestWriterRejectsMissingRequiredFields(t *testing.T) {
	valid := Action{ID: "a1", Type: TypeAgentMessage, Assurance: AssuranceProviderReported}

	tests := []struct {
		name        string
		mutate      func(*Action)
		wantMessage string
	}{
		{
			name:        "missing id",
			mutate:      func(a *Action) { a.ID = "" },
			wantMessage: "action: missing id",
		},
		{
			name:        "missing type",
			mutate:      func(a *Action) { a.Type = "" },
			wantMessage: "action a1: missing type",
		},
		{
			name:        "missing assurance",
			mutate:      func(a *Action) { a.Assurance = "" },
			wantMessage: "action a1: missing assurance",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invalid := valid
			tt.mutate(&invalid)

			var buf bytes.Buffer
			err := NewWriter(&buf).Write(invalid)

			if err == nil {
				t.Fatalf("Write(%+v) error = nil, want an error", invalid)
			}
			if err.Error() != tt.wantMessage {
				t.Errorf("error = %q, want %q", err, tt.wantMessage)
			}
			if buf.Len() != 0 {
				t.Errorf("output = %q, want nothing written for a rejected action", buf.String())
			}
		})
	}
}

func TestWriterPropagatesInvalidRawMessage(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	good := Action{ID: "a1", Type: TypeAgentMessage, Assurance: AssuranceProviderReported}
	if err := w.Write(good); err != nil {
		t.Fatalf("Write(good) error = %v", err)
	}
	writtenBefore := buf.Len()

	bad := Action{
		ID:        "a2",
		Type:      TypeToolCall,
		Assurance: AssuranceProviderReported,
		Input:     json.RawMessage(`{"unterminated":`),
	}

	err := w.Write(bad)
	if err == nil {
		t.Fatalf("Write(bad) error = nil, want the encoding error propagated")
	}
	if !strings.Contains(err.Error(), "a2") {
		t.Errorf("error = %q, want it to identify the offending action a2", err)
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Errorf("error = %q, want the underlying json error preserved for the caller", err)
	}
	if buf.Len() != writtenBefore {
		t.Errorf("output grew to %q, want no partial line for a failed action", buf.String())
	}
}
