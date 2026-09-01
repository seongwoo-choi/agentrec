package redaction

import (
	"encoding/json"
	"strings"
	"testing"
)

// H2: encoding/json's HTML escaping grew a line of markup 2.67x, so an event
// that arrived under the stream line limit came back out over it and the
// whole run was rejected as storage_error. Node's JSON.stringify does not
// escape `<`, so claude CLI output triggered this routinely.
func TestRedactJSONDoesNotGrowMarkup(t *testing.T) {
	markup := strings.Repeat("<div>a</div>", 2<<20/len("<div>a</div>"))
	in := []byte(`{"text":"` + markup + `"}`)

	out, err := New().RedactJSON(in)
	if err != nil {
		t.Fatalf("RedactJSON: %v", err)
	}
	if len(out) > len(in)+64 {
		t.Errorf("len(out) = %d, want <= len(in)+64 = %d", len(out), len(in)+64)
	}
	if !json.Valid(out) {
		t.Error("output is not valid JSON")
	}
	if !strings.Contains(string(out), "<div>a</div>") {
		t.Error("markup did not survive verbatim")
	}
}

// Non-secret text is not the redactor's to rewrite: bytes that carry no
// secret come back exactly as they went in.
func TestRedactJSONRoundTripsHTMLCharacters(t *testing.T) {
	in := `{"text":"a <b> c & d"}`
	out, err := New().RedactJSON([]byte(in))
	if err != nil {
		t.Fatalf("RedactJSON: %v", err)
	}
	if string(out) != in {
		t.Errorf("RedactJSON = %s, want %s byte-for-byte", out, in)
	}
}

// encoding/json escapes U+2028/U+2029 whether or not HTML escaping is on, so
// those cannot round-trip byte-for-byte; they must still decode to the same
// text and stay within the 2x the escape costs.
func TestRedactJSONPreservesLineSeparator(t *testing.T) {
	const text = "a b c"
	in := `{"text":"` + text + `"}`
	out, err := New().RedactJSON([]byte(in))
	if err != nil {
		t.Fatalf("RedactJSON: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if got["text"] != text {
		t.Errorf("text = %q, want %q", got["text"], text)
	}
	if len(out) > 2*len(in) {
		t.Errorf("len(out) = %d, want <= 2*len(in) = %d", len(out), 2*len(in))
	}
}
