package redaction

import (
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRedactJSONReplacesTopLevelAPIToken(t *testing.T) {
	// Arrange
	r := New()
	raw := []byte(`{"api_token":"sk-synthetic-token-aaaa"}`)

	// Act
	got, err := r.RedactJSON(raw)

	// Assert
	if err != nil {
		t.Fatalf("RedactJSON: %v", err)
	}
	want := `{"api_token":"[REDACTED:1]"}`
	if string(got) != want {
		t.Errorf("RedactJSON = %s, want %s", got, want)
	}
}

func TestRedactJSONWalksNestedValuesAndHonorsNameAndLength(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "nested object",
			raw:  `{"env":{"outer":{"DB_PASSWORD":"synthetic-password-1"}}}`,
			want: `{"env":{"outer":{"DB_PASSWORD":"[REDACTED:1]"}}}`,
		},
		{
			name: "array elements keep order",
			raw:  `{"rotated_token":["synthetic-token-a","synthetic-token-b"]}`,
			want: `{"rotated_token":["[REDACTED:1]","[REDACTED:2]"]}`,
		},
		{
			name: "plural suffix is not a secret name",
			raw:  `{"tokens":["synthetic-token-a"]}`,
			want: `{"tokens":["synthetic-token-a"]}`,
		},
		{
			name: "array of objects",
			raw:  `{"items":[{"client_secret":"synthetic-secret-1"},{"note":"synthetic-secret-1"}]}`,
			want: `{"items":[{"client_secret":"[REDACTED:1]"},{"note":"synthetic-secret-1"}]}`,
		},
		{
			name: "case insensitive suffix",
			raw:  `{"Service_Api_Key":"synthetic-api-key-1"}`,
			want: `{"Service_Api_Key":"[REDACTED:1]"}`,
		},
		{
			name: "suffix must end the name",
			raw:  `{"token_id":"synthetic-token-id-1","secretless":"synthetic-value-1"}`,
			want: `{"secretless":"synthetic-value-1","token_id":"synthetic-token-id-1"}`,
		},
		{
			name: "short value stays",
			raw:  `{"api_token":"short7x"}`,
			want: `{"api_token":"short7x"}`,
		},
		{
			name: "value at exactly the minimum length is redacted",
			raw:  `{"api_token":"short8xy"}`,
			want: `{"api_token":"[REDACTED:1]"}`,
		},
		{
			name: "scalar values under a secret name become markers",
			raw:  `{"api_token":null,"rotation_secret":1234567890123}`,
			want: `{"api_token":"[REDACTED:1]","rotation_secret":"[REDACTED:2]"}`,
		},
		{
			name: "boolean under a secret name becomes a marker",
			raw:  `{"api_token":true}`,
			want: `{"api_token":"[REDACTED:1]"}`,
		},
		{
			name: "numeric array leaf under a secret name becomes a marker",
			raw:  `{"credentials":[1234567890,"synthetic-array-scalar-1"]}`,
			want: `{"credentials":["[REDACTED:1]","[REDACTED:2]"]}`,
		},
		{
			name: "scalars under an ordinary name are kept",
			raw:  `{"bytes_written":214,"exit_code":0,"ok":true,"detail":null}`,
			want: `{"bytes_written":214,"detail":null,"exit_code":0,"ok":true}`,
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

func TestSecretScalarIsKeyedByItsCanonicalJSONText(t *testing.T) {
	// Arrange: one credential arrives as a JSON number, then as the string that
	// spells the same number. Keying on canonical text correlates the two.
	r := New()

	// Act
	number, err := r.RedactJSON([]byte(`{"api_token":1234567890123}`))
	if err != nil {
		t.Fatalf("RedactJSON number: %v", err)
	}
	text, err := r.RedactJSON([]byte(`{"session_token":"1234567890123"}`))
	if err != nil {
		t.Fatalf("RedactJSON text: %v", err)
	}

	// Assert
	if want := `{"api_token":"[REDACTED:1]"}`; string(number) != want {
		t.Errorf("number = %s, want %s", number, want)
	}
	if want := `{"session_token":"[REDACTED:1]"}`; string(text) != want {
		t.Errorf("text = %s, want %s", text, want)
	}
}

func TestRedactJSONReusesMarkerForRepeatedSecret(t *testing.T) {
	// Arrange
	r := New()

	// Act
	first, err := r.RedactJSON([]byte(`{"api_token":"synthetic-token-shared","db_password":"synthetic-password-1"}`))
	if err != nil {
		t.Fatalf("RedactJSON first: %v", err)
	}
	second, err := r.RedactJSON([]byte(`{"session_token":"synthetic-token-shared"}`))
	if err != nil {
		t.Fatalf("RedactJSON second: %v", err)
	}

	// Assert
	wantFirst := `{"api_token":"[REDACTED:1]","db_password":"[REDACTED:2]"}`
	if string(first) != wantFirst {
		t.Errorf("first = %s, want %s", first, wantFirst)
	}
	wantSecond := `{"session_token":"[REDACTED:1]"}`
	if string(second) != wantSecond {
		t.Errorf("second = %s, want %s", second, wantSecond)
	}
}

// A marker is not secret material. Minting a second marker for one hides which
// secret the first marker stood for, so redacting already-redacted output has
// to leave it exactly as it is.
func TestRedactJSONLeavesAlreadyMarkedFieldValues(t *testing.T) {
	// Arrange: output from an earlier pass over the same run.
	r := New()
	first, err := r.RedactJSON([]byte(`{"api_token":"synthetic-already-marked-1","db_password":"synthetic-already-marked-2"}`))
	if err != nil {
		t.Fatalf("RedactJSON first: %v", err)
	}

	// Act
	second, err := r.RedactJSON(first)
	if err != nil {
		t.Fatalf("RedactJSON second: %v", err)
	}
	// A marker this run never issued is still a marker, not a secret.
	unseen, err := r.RedactJSON([]byte(`{"session_token":"[REDACTED:9]"}`))
	if err != nil {
		t.Fatalf("RedactJSON unseen: %v", err)
	}

	// Assert
	if string(second) != string(first) {
		t.Errorf("second = %s, want the first pass unchanged: %s", second, first)
	}
	if want := `{"session_token":"[REDACTED:9]"}`; string(unseen) != want {
		t.Errorf("unseen = %s, want %s", unseen, want)
	}
}

func TestRedactJSONAssignsMarkersInSortedKeyOrder(t *testing.T) {
	// Arrange: three sibling secrets whose sorted key order differs from the
	// order they appear in the document.
	raw := []byte(`{"z_token":"synthetic-token-z","a_token":"synthetic-token-a","m_token":"synthetic-token-m"}`)
	want := `{"a_token":"[REDACTED:1]","m_token":"[REDACTED:2]","z_token":"[REDACTED:3]"}`

	// Act & Assert: repeated runs must agree, since Go randomizes map ordering.
	for i := 0; i < 50; i++ {
		got, err := New().RedactJSON(raw)
		if err != nil {
			t.Fatalf("RedactJSON: %v", err)
		}
		if string(got) != want {
			t.Fatalf("run %d: RedactJSON = %s, want %s", i, got, want)
		}
	}
}

func TestRedactJSONNeverEmitsTheSecret(t *testing.T) {
	// Arrange
	const secret = "synthetic-token-must-not-leak"

	// Act
	got, err := New().RedactJSON([]byte(`{"api_token":"` + secret + `"}`))
	if err != nil {
		t.Fatalf("RedactJSON: %v", err)
	}

	// Assert
	if strings.Contains(string(got), secret) {
		t.Errorf("RedactJSON = %s, must not contain the secret", got)
	}
}

func TestRedactJSONRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "empty input", raw: ``},
		{name: "blank input", raw: `   `},
		{name: "truncated object", raw: `{"api_token":"synthetic-token-1"`},
		{name: "unquoted key", raw: `{api_token:"synthetic-token-1"}`},
		{name: "trailing value", raw: `{"a":1} {"b":2}`},
		{name: "trailing garbage", raw: `{"a":1} not-json`},
		{name: "trailing comma-separated value", raw: `{"a":1},{"b":2}`},
		{name: "not an object", raw: `"synthetic-token-1"`},
		{name: "top-level array", raw: `[{"api_token":"synthetic-token-1"}]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := New().RedactJSON([]byte(tt.raw))
			if err == nil {
				t.Fatalf("RedactJSON = %s, want error", got)
			}
			if got != nil {
				t.Errorf("RedactJSON returned %s alongside error, want nil", got)
			}
		})
	}
}

// tinyReader hands out at most 3 bytes per Read so line splitting is exercised
// at boundaries a provider stream could produce anywhere.
type tinyReader struct {
	data []byte
}

func (t *tinyReader) Read(p []byte) (int, error) {
	if len(t.data) == 0 {
		return 0, io.EOF
	}
	n := min(len(p), min(3, len(t.data)))
	copy(p, t.data[:n])
	t.data = t.data[n:]
	return n, nil
}

// fixturePath resolves a redaction fixture relative to this package's source
// directory so tests do not depend on the process working directory.
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test source directory")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "testdata", "redaction", name)
}

// errReader fails after handing out prefix, standing in for a truncated or
// broken provider stream.
type errReader struct {
	prefix []byte
	err    error
}

func (e *errReader) Read(p []byte) (int, error) {
	if len(e.prefix) == 0 {
		return 0, e.err
	}
	n := copy(p, e.prefix)
	e.prefix = e.prefix[n:]
	return n, nil
}

// errWriter fails on the nth write, counting from one.
type errWriter struct {
	failOn int
	writes int
	err    error
}

func (e *errWriter) Write(p []byte) (int, error) {
	e.writes++
	if e.writes >= e.failOn {
		return 0, e.err
	}
	return len(p), nil
}

func TestRedactJSONLPropagatesReadError(t *testing.T) {
	// Arrange
	wantErr := errors.New("stream broke")
	src := &errReader{prefix: []byte(`{"api_token":"synthetic-token-1"}` + "\n"), err: wantErr}

	// Act
	err := New().RedactJSONL(src, &strings.Builder{})

	// Assert
	if !errors.Is(err, wantErr) {
		t.Fatalf("RedactJSONL error = %v, want %v", err, wantErr)
	}
}

func TestRedactJSONLPropagatesWriteError(t *testing.T) {
	// Arrange
	wantErr := errors.New("disk full")
	src := strings.NewReader(`{"a":1}` + "\n" + `{"b":2}` + "\n")

	// Act
	err := New().RedactJSONL(src, &errWriter{failOn: 2, err: wantErr})

	// Assert
	if !errors.Is(err, wantErr) {
		t.Fatalf("RedactJSONL error = %v, want %v", err, wantErr)
	}
}

func TestRedactJSONLRejectsMalformedLine(t *testing.T) {
	// Arrange
	src := strings.NewReader(`{"a":1}` + "\n" + `{"broken` + "\n")
	var out strings.Builder

	// Act
	err := New().RedactJSONL(src, &out)

	// Assert
	if err == nil {
		t.Fatal("RedactJSONL error = nil, want error")
	}
	if strings.Count(out.String(), "\n") != 1 {
		t.Errorf("output = %q, want only the line before the failure", out.String())
	}
}

func TestRedactJSONLSkipsBlankLines(t *testing.T) {
	// Arrange
	src := strings.NewReader("\n" + `{"api_token":"synthetic-token-1"}` + "\n   \n\n")
	var out strings.Builder

	// Act
	if err := New().RedactJSONL(src, &out); err != nil {
		t.Fatalf("RedactJSONL: %v", err)
	}

	// Assert
	want := `{"api_token":"[REDACTED:1]"}` + "\n"
	if out.String() != want {
		t.Errorf("RedactJSONL wrote %q, want %q", out.String(), want)
	}
}
