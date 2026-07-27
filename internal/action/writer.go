package action

import (
	"encoding/json"
	"fmt"
	"io"
)

// Writer streams actions as JSON Lines, one action per line. Each Write
// encodes and emits a single action, so a run is never buffered whole.
type Writer struct {
	enc *json.Encoder
}

// NewWriter returns a Writer that emits JSONL to w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{enc: json.NewEncoder(w)}
}

// Write validates a and appends it to the stream as one line.
func (w *Writer) Write(a Action) error {
	// Only fields without a meaningful zero value are required here; richer
	// validation belongs to whoever consumes the stream.
	switch {
	case a.ID == "":
		return fmt.Errorf("action: missing id")
	case a.Type == "":
		return fmt.Errorf("action %s: missing type", a.ID)
	case a.Assurance == "":
		return fmt.Errorf("action %s: missing assurance", a.ID)
	}
	if err := w.enc.Encode(a); err != nil {
		return fmt.Errorf("action %s: %w", a.ID, err)
	}
	return nil
}
