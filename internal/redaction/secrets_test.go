package redaction

import "testing"

// Cycle D: a secret-named field can hold a whole object rather than a string.
// Walking into an object publishes its shape and keys, so it is replaced whole.
// Arrays keep their established element-wise redaction and marker correlation.
func TestRedactJSONRedactsContainersUnderSecretNames(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "object under a secret name goes whole",
			raw:  `{"api_token":{"value":"synthetic-container-token-1","expires_in":3600}}`,
			want: `{"api_token":"[REDACTED:1]"}`,
		},
		{
			name: "array under a secret name keeps element correlation",
			raw:  `{"credentials":["synthetic-container-user-1","synthetic-container-pass-1"]}`,
			want: `{"credentials":["[REDACTED:1]","[REDACTED:2]"]}`,
		},
		{
			name: "nested container under a secret name goes whole",
			raw:  `{"credentials":{"aws":{"access":"synthetic-container-key-1"}}}`,
			want: `{"credentials":"[REDACTED:1]"}`,
		},
		{
			name: "non-sensitive object stays structural",
			raw:  `{"config":{"db_password":"synthetic-container-pass-2","host":"localhost"}}`,
			want: `{"config":{"db_password":"[REDACTED:1]","host":"localhost"}}`,
		},
		{
			name: "non-sensitive array stays structural",
			raw:  `{"items":[{"api_token":"synthetic-container-token-2"},"plain text"]}`,
			want: `{"items":[{"api_token":"[REDACTED:1]"},"plain text"]}`,
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

func TestSecretContainerMarkerIsDeterministic(t *testing.T) {
	// Arrange: two secret containers holding the same pairs in different source
	// orders. Canonical marshalling keys both to one marker, and the numbering
	// must not move with Go's randomized map iteration.
	raw := []byte(`{"a_token":{"y":"synthetic-order-0001","x":"synthetic-order-0002"},"b_token":{"x":"synthetic-order-0002","y":"synthetic-order-0001"}}`)
	want := `{"a_token":"[REDACTED:1]","b_token":"[REDACTED:1]"}`

	// Act & Assert
	for i := 0; i < 50; i++ {
		got, err := New().RedactJSON(raw)
		if err != nil {
			t.Fatalf("RedactJSON: %v", err)
		}
		if string(got) != want {
			t.Fatalf("run %d = %s, want %s", i, got, want)
		}
	}
}

// Cycle E: a private key that arrives truncated — a log tail, a clipped event —
// has a BEGIN header and no END. The body that did arrive is still key
// material, so everything from the header on goes.
func TestRedactJSONRedactsTruncatedPrivateKeyBlocks(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "truncated rsa block runs to the end of the string",
			raw:  `{"content":"header line\n-----BEGIN RSA PRIVATE KEY-----\nc3ludGhldGljLXRydW5jYXRlZC1yc2E=\n"}`,
			want: `{"content":"header line\n[REDACTED:1]"}`,
		},
		{
			name: "truncated openssh block",
			raw:  `{"content":"log: -----BEGIN OPENSSH PRIVATE KEY-----\nc3ludGhldGljLXRydW5jYXRlZC1zc2g="}`,
			want: `{"content":"log: [REDACTED:1]"}`,
		},
		{
			name: "truncated unlabelled pkcs8 block",
			raw:  `{"content":"-----BEGIN PRIVATE KEY-----\nc3ludGhldGljLXRydW5jYXRlZC1wa2NzOA=="}`,
			want: `{"content":"[REDACTED:1]"}`,
		},
		{
			name: "complete block is left to the complete rule",
			raw:  `{"content":"-----BEGIN RSA PRIVATE KEY-----\nc3ludGhldGljLWNvbXBsZXRlLWtleQ==\n-----END RSA PRIVATE KEY-----\nfooter line"}`,
			want: `{"content":"[REDACTED:1]\nfooter line"}`,
		},
		{
			name: "truncated certificate is not a private key",
			raw:  `{"content":"-----BEGIN CERTIFICATE-----\nc3ludGhldGljLXRydW5jYXRlZC1jZXJ0"}`,
			want: `{"content":"-----BEGIN CERTIFICATE-----\nc3ludGhldGljLXRydW5jYXRlZC1jZXJ0"}`,
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

func TestTruncatedPrivateKeyReusesOneMarker(t *testing.T) {
	// Arrange: the same clipped key block arrives on two events.
	r := New()
	raw := []byte(`{"content":"-----BEGIN RSA PRIVATE KEY-----\nc3ludGhldGljLXJlcGVhdGVkLXRydW5j"}`)

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
	if want := `{"content":"[REDACTED:1]"}`; string(first) != want {
		t.Errorf("first = %s, want %s", first, want)
	}
	if string(second) != string(first) {
		t.Errorf("second = %s, want the same marker as %s", second, first)
	}
}

// Cycle F: HTTP Basic carries the credential in the same place Bearer does, and
// the scheme name is written every way a provider feels like writing it.
func TestRedactJSONRedactsBasicAuthCredentials(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "basic prefix survives and only the credential goes",
			raw:  `{"note":"Authorization: Basic c3ludGhldGljOmJhc2ljLWNyZWQx sent"}`,
			want: `{"note":"Authorization: Basic [REDACTED:1] sent"}`,
		},
		{
			name: "lower case scheme keeps its casing",
			raw:  `{"note":"authorization: basic c3ludGhldGljOmJhc2ljLWNyZWQy sent"}`,
			want: `{"note":"authorization: basic [REDACTED:1] sent"}`,
		},
		{
			name: "upper case scheme keeps its casing",
			raw:  `{"note":"AUTHORIZATION: BASIC c3ludGhldGljOmJhc2ljLWNyZWQz sent"}`,
			want: `{"note":"AUTHORIZATION: BASIC [REDACTED:1] sent"}`,
		},
		{
			name: "separating whitespace survives",
			raw:  `{"note":"Basic \t  c3ludGhldGljOmJhc2ljLWNyZWQ0 sent"}`,
			want: `{"note":"Basic \t  [REDACTED:1] sent"}`,
		},
		{
			name: "padded credential is redacted with its padding",
			raw:  `{"note":"Basic c3ludGhldGljOnNoYXJlZC1iYXNpYw== sent"}`,
			want: `{"note":"Basic [REDACTED:1] sent"}`,
		},
		{
			name: "credential below the minimum length stays",
			raw:  `{"note":"Basic dXNlcjo= was rejected"}`,
			want: `{"note":"Basic dXNlcjo= was rejected"}`,
		},
		{
			name: "non base64 credential stays",
			raw:  `{"note":"Basic ***elided-by-provider*** was sent"}`,
			want: `{"note":"Basic ***elided-by-provider*** was sent"}`,
		},
		{
			// Prose about basic auth is not a credential. A word is rarely a
			// well-formed base64 string, and never decodes to a `user:password`.
			name: "prose word after the basic keyword stays",
			raw:  `{"note":"Basic authentication is required for this endpoint"}`,
			want: `{"note":"Basic authentication is required for this endpoint"}`,
		},
		{
			name: "lower case basic prose stays",
			raw:  `{"note":"the basic understanding here is that it retries"}`,
			want: `{"note":"the basic understanding here is that it retries"}`,
		},
		{
			// Well-formed base64 by accident, but it decodes to bytes with no
			// separator, so it cannot be a `user:password` pair.
			name: "decodable prose word without a separator stays",
			raw:  `{"note":"Basic requirements were met before the run"}`,
			want: `{"note":"Basic requirements were met before the run"}`,
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

func TestBasicCredentialSharesTheFieldMarker(t *testing.T) {
	// Arrange: one credential arrives under a secret-named field, then again
	// inside a Basic header on a later event.
	const cred = "c3ludGhldGljOnNoYXJlZC1iYXNpYw=="
	r := New()

	// Act
	field, err := r.RedactJSON([]byte(`{"credentials":"` + cred + `"}`))
	if err != nil {
		t.Fatalf("RedactJSON field: %v", err)
	}
	header, err := r.RedactJSON([]byte(`{"note":"Authorization: Basic ` + cred + `"}`))
	if err != nil {
		t.Fatalf("RedactJSON header: %v", err)
	}

	// Assert
	if want := `{"credentials":"[REDACTED:1]"}`; string(field) != want {
		t.Errorf("field = %s, want %s", field, want)
	}
	if want := `{"note":"Authorization: Basic [REDACTED:1]"}`; string(header) != want {
		t.Errorf("header = %s, want %s", header, want)
	}
}
