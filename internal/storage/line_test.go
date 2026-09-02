package storage

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/action"
)

// A line that would not fit the stream is refused on its own, not with the
// bundle: the next action and the next event are still written.
func TestALineTooLargeDoesNotPoisonTheBundle(t *testing.T) {
	b, err := Create(t.TempDir(), "run-line", Manifest{Provider: "claude", CWD: "/tmp", StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("x", MaxStreamLineBytes)
	big := action.Action{ID: "big", Type: action.TypeShellExec, Provider: "claude", Assurance: action.AssuranceProviderReported, StartedAt: time.Now(), FinishedAt: time.Now(), Status: "completed", Input: json.RawMessage(`{"command":"` + huge + `"}`)}
	if err := b.WriteAction(big); !errors.Is(err, ErrLineTooLarge) {
		t.Fatalf("WriteAction(huge) = %v, want ErrLineTooLarge", err)
	}
	small := big
	small.ID, small.Input = "small", json.RawMessage(`{"command":"true"}`)
	if err := b.WriteAction(small); err != nil {
		t.Errorf("WriteAction after a refused line = %v, want the bundle still writable", err)
	}
	if err := b.WriteProviderEvent([]byte(`{"type":"big","text":"` + huge + `"}`)); !errors.Is(err, ErrLineTooLarge) {
		t.Fatalf("WriteProviderEvent(huge) = %v, want ErrLineTooLarge", err)
	}
	if err := b.WriteProviderEvent([]byte(`{"type":"small"}`)); err != nil {
		t.Errorf("WriteProviderEvent after a refused line = %v, want the bundle still writable", err)
	}
}
