package redaction

import (
	"encoding/json"
	"strings"
	"testing"
)

// Cycle A: a provider that encodes a JSON array into a string carries the same
// secrets an encoded object does, so the array has to be walked the same way.
func TestRedactJSONRedactsEncodedArrays(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "encoded array of objects",
			raw:  `{"payload":"[{\"password\":\"synthetic-encoded-pass-1\"},{\"client_secret\":\"synthetic-encoded-secret-2\"}]"}`,
			want: `{"payload":"[{\"password\":\"[REDACTED:1]\"},{\"client_secret\":\"[REDACTED:2]\"}]"}`,
		},
		{
			name: "encoded array of secret strings",
			raw:  `{"payload":"[\"ghp_abcdefghijklmnopqrst0009\",\"plain text\"]"}`,
			want: `{"payload":"[\"[REDACTED:1]\",\"plain text\"]"}`,
		},
		{
			name: "encoded array nested inside an encoded object",
			raw:  `{"payload":"{\"items\":[{\"api_token\":\"synthetic-encoded-token-3\"}]}"}`,
			want: `{"payload":"{\"items\":[{\"api_token\":\"[REDACTED:1]\"}]}"}`,
		},
		{
			name: "surrounding whitespace is trimmed away",
			raw:  `{"payload":"  [{\"password\":\"synthetic-encoded-pass-4\"}]  "}`,
			want: `{"payload":"[{\"password\":\"[REDACTED:1]\"}]"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := New().RedactJSON([]byte(tt.raw))
			if err != nil {
				t.Fatalf("RedactJSON: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("RedactJSON = %s, want %s", got, tt.want)
			}
		})
	}
}

// Cycle B: a string that announces itself as JSON and then does not parse
// cannot be read by any rule here, so passing it through is a guess. The whole
// string is replaced instead.
func TestRedactJSONFailsClosedOnUnparsableEncodedJSON(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		leak string
	}{
		{
			name: "truncated object",
			raw:  `{"payload":"{\"db_password\": \"synthetic-broken-pass-1"}`,
			leak: "synthetic-broken-pass-1",
		},
		{
			name: "truncated array",
			raw:  `{"payload":"[{\"client_secret\":\"synthetic-broken-secret-2\"}"}`,
			leak: "synthetic-broken-secret-2",
		},
		{
			name: "object followed by trailing text",
			raw:  `{"payload":"{\"api_token\":\"synthetic-broken-token-3\"} and more"}`,
			leak: "synthetic-broken-token-3",
		},
		{
			name: "array followed by a second value",
			raw:  `{"payload":"[1,2] [\"synthetic-broken-token-4\"]"}`,
			leak: "synthetic-broken-token-4",
		},
		{
			name: "single quoted pseudo json",
			raw:  `{"payload":"{'db_password': 'synthetic-broken-pass-5'}"}`,
			leak: "synthetic-broken-pass-5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := New().RedactJSON([]byte(tt.raw))
			if err != nil {
				t.Fatalf("RedactJSON: %v", err)
			}
			if want := `{"payload":"[REDACTED:1]"}`; string(got) != want {
				t.Errorf("RedactJSON = %s, want %s", got, want)
			}
			if strings.Contains(string(got), tt.leak) {
				t.Errorf("RedactJSON = %s, leaked %q", got, tt.leak)
			}
		})
	}
}

func TestUnparsableEncodedJSONReusesOneMarker(t *testing.T) {
	// Arrange: the same unreadable payload arrives on two events.
	r := New()
	raw := []byte(`{"payload":"{\"db_password\": broken"}`)

	// Act
	first, err := r.RedactJSON(raw)
	if err != nil {
		t.Fatalf("RedactJSON first: %v", err)
	}
	second, err := r.RedactJSON(raw)
	if err != nil {
		t.Fatalf("RedactJSON second: %v", err)
	}

	// Assert
	if string(first) != string(second) {
		t.Errorf("first = %s, second = %s, want the same marker", first, second)
	}
}

// encodeInto returns payload wrapped as a JSON-encoded string under key, one
// more layer of the encoding providers apply when they forward a body verbatim.
func encodeInto(t *testing.T, key, payload string) string {
	t.Helper()
	quoted, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return `{"` + key + `":` + string(quoted) + `}`
}

// Cycle C: encoded JSON nests without any bound in the input, so the walk needs
// one of its own. Past the bound the remaining string is replaced whole, which
// costs readability but never leaks what is left inside it.
func TestRedactJSONBoundsEncodedJSONRecursion(t *testing.T) {
	// Arrange: two layers past the bound around one secret. Each layer of
	// encoding roughly doubles the text it wraps, so the depth is expressed
	// against the bound rather than as a large literal, which would grow the
	// line past maxLineBuffer long before the redactor ever saw it.
	const secret = "synthetic-deep-secret-0001"
	raw := `{"password":"` + secret + `"}`
	for i := 0; i < maxEncodedJSONDepth+2; i++ {
		raw = encodeInto(t, "inner", raw)
	}
	if len(raw) > maxLineBuffer {
		t.Fatalf("test input is %d bytes, past the %d byte line cap", len(raw), maxLineBuffer)
	}

	// Act
	got, err := New().RedactJSON([]byte(raw))
	if err != nil {
		t.Fatalf("RedactJSON: %v", err)
	}

	// Assert: the tail past the bound went whole, so neither the secret nor the
	// field name that held it reached the output.
	if strings.Contains(string(got), secret) {
		t.Errorf("RedactJSON leaked %q", secret)
	}
	if !strings.Contains(string(got), "[REDACTED:1]") {
		t.Errorf("RedactJSON = %s, want the tail replaced by a marker", got)
	}
	if strings.Contains(string(got), "password") {
		t.Errorf("RedactJSON = %s, want no structure walked past the bound", got)
	}
}

func TestRedactJSONRedactsUpToTheEncodedDepthBound(t *testing.T) {
	// Arrange: a secret sitting exactly at the deepest layer still walked.
	const secret = "synthetic-bound-secret-0002"
	raw := `{"password":"` + secret + `"}`
	for i := 0; i < maxEncodedJSONDepth; i++ {
		raw = encodeInto(t, "inner", raw)
	}

	// Act
	got, err := New().RedactJSON([]byte(raw))
	if err != nil {
		t.Fatalf("RedactJSON: %v", err)
	}

	// Assert: still walked structurally, so the innermost field name survives.
	if strings.Contains(string(got), secret) {
		t.Errorf("RedactJSON leaked %q", secret)
	}
	if !strings.Contains(string(got), `password`) {
		t.Errorf("RedactJSON = %s, want the structure kept at the bound", got)
	}
}

// Cycle D: opening with a bracket is not enough to call a string JSON. A log
// line prefixed with its level, or prose someone wrapped in braces, would be
// replaced whole by the fail-closed path — erasing readable output that never
// held a secret. Only a string whose first byte past the bracket could begin a
// JSON member is judged as encoded JSON at all; the rest goes to the pattern
// rules, which read it as the text it is.
func TestRedactJSONLeavesJSONLikeProseAlone(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "log level prefix is not an encoded array",
			raw:  `{"message":"[INFO] server started"}`,
			want: `{"message":"[INFO] server started"}`,
		},
		{
			name: "prose in braces is not an encoded object",
			raw:  `{"message":"{not JSON but ordinary prose}"}`,
			want: `{"message":"{not JSON but ordinary prose}"}`,
		},
		{
			name: "valid encoded object is still walked",
			raw:  `{"payload":"{\"password\":\"synthetic-jsonlike-pass-1\"}"}`,
			want: `{"payload":"{\"password\":\"[REDACTED:1]\"}"}`,
		},
		{
			name: "valid encoded array is still walked",
			raw:  `{"payload":"[{\"password\":\"synthetic-jsonlike-pass-2\"}]"}`,
			want: `{"payload":"[{\"password\":\"[REDACTED:1]\"}]"}`,
		},
		{
			name: "malformed secret-bearing object still goes whole",
			raw:  `{"payload":"{\"db_password\": \"synthetic-jsonlike-pass-3"}`,
			want: `{"payload":"[REDACTED:1]"}`,
		},
		{
			name: "malformed secret-bearing array still goes whole",
			raw:  `{"payload":"[{\"client_secret\":\"synthetic-jsonlike-secret-4\"}"}`,
			want: `{"payload":"[REDACTED:1]"}`,
		},
		{
			name: "bare open brace still goes whole",
			raw:  `{"payload":"{"}`,
			want: `{"payload":"[REDACTED:1]"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := New().RedactJSON([]byte(tt.raw))
			if err != nil {
				t.Fatalf("RedactJSON: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("RedactJSON = %s, want %s", got, tt.want)
			}
		})
	}
}

// Cycle E: a bracketed prefix is how ordinary tool output announces a level, a
// timestamp or a step counter. Reading the byte past the bracket as the start of
// a JSON value calls all of those encoded arrays, and the fail-closed path then
// erases the line. Only a byte that could begin an array's first *element* —
// a string, a nested container, or the array's own close — counts.
func TestRedactJSONLeavesBracketedProseAlone(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "timestamp prefix",
			raw:  `{"message":"[2026-07-28T02:10:00Z] deploy finished"}`,
		},
		{
			name: "step counter prefix",
			raw:  `{"message":"[12/50] building package"}`,
		},
		{
			name: "fatal level prefix",
			raw:  `{"message":"[fatal] connection refused"}`,
		},
		{
			name: "trace level prefix",
			raw:  `{"message":"[trace] entering handler"}`,
		},
		{
			name: "component name prefix",
			raw:  `{"message":"[nginx] upstream timed out"}`,
		},
		{
			name: "bullet prefix",
			raw:  `{"message":"[-] step skipped"}`,
		},
		{
			name: "elapsed time prefix",
			raw:  `{"message":"[0.42s] test passed"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := New().RedactJSON([]byte(tt.raw))
			if err != nil {
				t.Fatalf("RedactJSON: %v", err)
			}
			if string(got) != tt.raw {
				t.Errorf("RedactJSON = %s, want it unchanged: %s", got, tt.raw)
			}
		})
	}
}

func TestEncodedArraySecretSharesTheFieldMarker(t *testing.T) {
	// Arrange: one secret arrives as a field value, then again inside a
	// JSON-encoded array on a later event.
	r := New()

	// Act
	first, err := r.RedactJSON([]byte(`{"db_password":"synthetic-shared-array-1"}`))
	if err != nil {
		t.Fatalf("RedactJSON first: %v", err)
	}
	second, err := r.RedactJSON([]byte(`{"payload":"[{\"db_password\":\"synthetic-shared-array-1\"}]"}`))
	if err != nil {
		t.Fatalf("RedactJSON second: %v", err)
	}

	// Assert
	if want := `{"db_password":"[REDACTED:1]"}`; string(first) != want {
		t.Errorf("first = %s, want %s", first, want)
	}
	if want := `{"payload":"[{\"db_password\":\"[REDACTED:1]\"}]"}`; string(second) != want {
		t.Errorf("second = %s, want %s", second, want)
	}
}
