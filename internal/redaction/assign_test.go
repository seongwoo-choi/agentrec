package redaction

import "testing"

// Cycle A: a variable whose whole name is a secret suffix carries the same
// secret as a prefixed one, so it has to be recognised the same way.
func TestRedactJSONRedactsBareSecretVariableNames(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "bare TOKEN",
			raw:  `{"cmd":"TOKEN=synthetic-bare-token-aaaa make deploy"}`,
			want: `{"cmd":"TOKEN=[REDACTED:1] make deploy"}`,
		},
		{
			name: "bare SECRET",
			raw:  `{"cmd":"SECRET=synthetic-bare-secret-bbbb make deploy"}`,
			want: `{"cmd":"SECRET=[REDACTED:1] make deploy"}`,
		},
		{
			name: "bare PASSWORD",
			raw:  `{"cmd":"PASSWORD=synthetic-bare-pass-cccc psql -h db"}`,
			want: `{"cmd":"PASSWORD=[REDACTED:1] psql -h db"}`,
		},
		{
			name: "bare API_KEY",
			raw:  `{"cmd":"API_KEY=synthetic-bare-key-dddd run task"}`,
			want: `{"cmd":"API_KEY=[REDACTED:1] run task"}`,
		},
		{
			name: "bare lower case password stays recognised",
			raw:  `{"cmd":"password='synthetic-bare-pass-eeee' login"}`,
			want: `{"cmd":"password='[REDACTED:1]' login"}`,
		},
		{
			name: "prefixed name still matches",
			raw:  `{"cmd":"CI_JOB_TOKEN=synthetic-bare-token-ffff push"}`,
			want: `{"cmd":"CI_JOB_TOKEN=[REDACTED:1] push"}`,
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

// Cycle B: the common industry spellings of a secret name, as a JSON field and
// as an assignment, plus the near misses that must stay readable.
func TestRedactJSONRecognisesCommonSecretNames(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "aws secret access key field",
			raw:  `{"AWS_SECRET_ACCESS_KEY":"synthetic-aws-secret-0001"}`,
			want: `{"AWS_SECRET_ACCESS_KEY":"[REDACTED:1]"}`,
		},
		{
			name: "aws secret access key assignment",
			raw:  `{"cmd":"AWS_SECRET_ACCESS_KEY=synthetic-aws-secret-0001 aws s3 ls"}`,
			want: `{"cmd":"AWS_SECRET_ACCESS_KEY=[REDACTED:1] aws s3 ls"}`,
		},
		{
			name: "camel case apiKey field",
			raw:  `{"apiKey":"synthetic-api-key-0002"}`,
			want: `{"apiKey":"[REDACTED:1]"}`,
		},
		{
			name: "lower case apikey field",
			raw:  `{"apikey":"synthetic-api-key-0003"}`,
			want: `{"apikey":"[REDACTED:1]"}`,
		},
		{
			name: "hyphenated x-api-key header field",
			raw:  `{"x-api-key":"synthetic-api-key-0004"}`,
			want: `{"x-api-key":"[REDACTED:1]"}`,
		},
		{
			name: "secretKey field",
			raw:  `{"secretKey":"synthetic-secret-key-0005"}`,
			want: `{"secretKey":"[REDACTED:1]"}`,
		},
		{
			name: "signing_key field",
			raw:  `{"signing_key":"synthetic-signing-key-0006"}`,
			want: `{"signing_key":"[REDACTED:1]"}`,
		},
		{
			name: "private_key field",
			raw:  `{"private_key":"synthetic-private-key-0007"}`,
			want: `{"private_key":"[REDACTED:1]"}`,
		},
		{
			name: "passwd field",
			raw:  `{"passwd":"synthetic-passwd-0008"}`,
			want: `{"passwd":"[REDACTED:1]"}`,
		},
		{
			name: "authorization field",
			raw:  `{"authorization":"synthetic-authorization-0009"}`,
			want: `{"authorization":"[REDACTED:1]"}`,
		},
		{
			name: "credentials field",
			raw:  `{"credentials":"synthetic-credentials-0010"}`,
			want: `{"credentials":"[REDACTED:1]"}`,
		},
		{
			name: "credential field",
			raw:  `{"credential":"synthetic-credential-0011"}`,
			want: `{"credential":"[REDACTED:1]"}`,
		},
		{
			name: "cookie field",
			raw:  `{"cookie":"synthetic-cookie-0012"}`,
			want: `{"cookie":"[REDACTED:1]"}`,
		},
		{
			name: "signing_key assignment",
			raw:  `{"cmd":"signing_key=synthetic-signing-key-0013 sign release"}`,
			want: `{"cmd":"signing_key=[REDACTED:1] sign release"}`,
		},
		{
			name: "passwd assignment",
			raw:  `{"cmd":"passwd=synthetic-passwd-0014 mount share"}`,
			want: `{"cmd":"passwd=[REDACTED:1] mount share"}`,
		},
		{
			name: "token_id is an identifier, not a secret",
			raw:  `{"token_id":"synthetic-token-id-0015"}`,
			want: `{"token_id":"synthetic-token-id-0015"}`,
		},
		{
			name: "tokenId is an identifier, not a secret",
			raw:  `{"tokenId":"synthetic-token-id-0016"}`,
			want: `{"tokenId":"synthetic-token-id-0016"}`,
		},
		{
			name: "path stays readable",
			raw:  `{"path":"/usr/local/synthetic/bin"}`,
			want: `{"path":"/usr/local/synthetic/bin"}`,
		},
		{
			name: "token_id assignment stays readable",
			raw:  `{"cmd":"token_id=synthetic-token-id-0017 lookup"}`,
			want: `{"cmd":"token_id=synthetic-token-id-0017 lookup"}`,
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

// Cycle C: punctuation that follows an unquoted value belongs to the command,
// not to the secret. Swallowing it hands the same secret two markers.
func TestRedactJSONStopsUnquotedValueAtPunctuation(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "trailing comma",
			raw:  `{"cmd":"run(TOKEN=synthetic-punct-secret-1, retry)"}`,
			want: `{"cmd":"run(TOKEN=[REDACTED:1], retry)"}`,
		},
		{
			name: "closing parenthesis",
			raw:  `{"cmd":"call(SECRET=synthetic-punct-secret-1)"}`,
			want: `{"cmd":"call(SECRET=[REDACTED:1])"}`,
		},
		{
			name: "closing brace",
			raw:  `{"cmd":"eval ${API_KEY=synthetic-punct-secret-1}"}`,
			want: `{"cmd":"eval ${API_KEY=[REDACTED:1]}"}`,
		},
		{
			// encoding/json escapes the angle brackets it writes back out.
			name: "closing angle bracket",
			raw:  `{"cmd":"send <PASSWORD=synthetic-punct-secret-1>"}`,
			want: `{"cmd":"send \u003cPASSWORD=[REDACTED:1]\u003e"}`,
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

func TestPunctuatedAssignmentReusesBareSecretMarker(t *testing.T) {
	// Arrange: the same secret arrives once followed by punctuation and once
	// standing alone.
	r := New()

	// Act
	first, err := r.RedactJSON([]byte(`{"cmd":"run(TOKEN=synthetic-punct-secret-1, retry)"}`))
	if err != nil {
		t.Fatalf("RedactJSON first: %v", err)
	}
	second, err := r.RedactJSON([]byte(`{"cmd":"TOKEN=synthetic-punct-secret-1 make deploy"}`))
	if err != nil {
		t.Fatalf("RedactJSON second: %v", err)
	}

	// Assert
	if want := `{"cmd":"run(TOKEN=[REDACTED:1], retry)"}`; string(first) != want {
		t.Errorf("first = %s, want %s", first, want)
	}
	if want := `{"cmd":"TOKEN=[REDACTED:1] make deploy"}`; string(second) != want {
		t.Errorf("second = %s, want %s", second, want)
	}
}

// Cycle D: an assigned value that is already a marker, or is a reference to
// another variable, carries no secret material and must survive untouched.
func TestRedactJSONLeavesMarkersAndVariableReferences(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "double quoted marker is not redacted again",
			raw:  `{"cmd":"TOKEN=\"[REDACTED:1]\" echo done"}`,
			want: `{"cmd":"TOKEN=\"[REDACTED:1]\" echo done"}`,
		},
		{
			name: "single quoted marker is not redacted again",
			raw:  `{"cmd":"SECRET='[REDACTED:1]' echo done"}`,
			want: `{"cmd":"SECRET='[REDACTED:1]' echo done"}`,
		},
		{
			name: "unquoted variable reference stays",
			raw:  `{"cmd":"TOKEN=$GITHUB_TOKEN gh release upload"}`,
			want: `{"cmd":"TOKEN=$GITHUB_TOKEN gh release upload"}`,
		},
		{
			name: "quoted variable reference stays",
			raw:  `{"cmd":"PASSWORD=\"${DB_PASSWORD}\" psql -h db"}`,
			want: `{"cmd":"PASSWORD=\"${DB_PASSWORD}\" psql -h db"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange: marker 1 is already spent, so a second redaction of the
			// marker text would show up as [REDACTED:2].
			r := New()
			if _, err := r.RedactJSON([]byte(`{"api_token":"synthetic-already-marked-1"}`)); err != nil {
				t.Fatalf("RedactJSON arrange: %v", err)
			}

			// Act
			got, err := r.RedactJSON([]byte(tt.raw))
			if err != nil {
				t.Fatalf("RedactJSON: %v", err)
			}

			// Assert
			if string(got) != tt.want {
				t.Errorf("RedactJSON = %s, want %s", got, tt.want)
			}
		})
	}
}

// Cycle F: a value that opens with `$` is only a reference to the secret where
// the shell would expand it, and only when it is exactly a reference. Inside
// single quotes there is no expansion, so the text is the secret; elsewhere
// anything past the reference — a substitution, a suffix — is secret material
// too.
func TestRedactJSONRedactsDollarValuesThatAreNotReferences(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "single quoted secret starting with a dollar",
			raw:  `{"cmd":"SECRET='$ynthetic-dollar-secret-1' login"}`,
			want: `{"cmd":"SECRET='[REDACTED:1]' login"}`,
		},
		{
			name: "single quoted password starting with a dollar",
			raw:  `{"cmd":"PASSWORD='$2y$10$syntheticbcrypthashvalue' psql -h db"}`,
			want: `{"cmd":"PASSWORD='[REDACTED:1]' psql -h db"}`,
		},
		{
			name: "single quotes do not expand a braced reference",
			raw:  `{"cmd":"PASSWORD='${DB_PASSWORD}' psql -h db"}`,
			want: `{"cmd":"PASSWORD='[REDACTED:1]' psql -h db"}`,
		},
		{
			name: "single quotes do not expand a bare reference",
			raw:  `{"cmd":"TOKEN='$GITHUB_TOKEN_VALUE' gh release upload"}`,
			want: `{"cmd":"TOKEN='[REDACTED:1]' gh release upload"}`,
		},
		{
			name: "double quoted command substitution is not a reference",
			raw:  `{"cmd":"TOKEN=\"$(cat /run/secrets/token)\" gh release upload"}`,
			want: `{"cmd":"TOKEN=\"[REDACTED:1]\" gh release upload"}`,
		},
		{
			name: "double quoted reference with a suffix is not a reference",
			raw:  `{"cmd":"PASSWORD=\"${DB_PASSWORD}-synthetic-suffix\" psql -h db"}`,
			want: `{"cmd":"PASSWORD=\"[REDACTED:1]\" psql -h db"}`,
		},
		{
			name: "unquoted reference with a suffix is not a reference",
			raw:  `{"cmd":"TOKEN=$GITHUB_TOKEN-synthetic-suffix gh release upload"}`,
			want: `{"cmd":"TOKEN=[REDACTED:1] gh release upload"}`,
		},
		{
			name: "double quoted exact reference stays",
			raw:  `{"cmd":"TOKEN=\"$GITHUB_TOKEN\" gh release upload"}`,
			want: `{"cmd":"TOKEN=\"$GITHUB_TOKEN\" gh release upload"}`,
		},
		{
			name: "double quoted exact braced reference stays",
			raw:  `{"cmd":"PASSWORD=\"${DB_PASSWORD}\" psql -h db"}`,
			want: `{"cmd":"PASSWORD=\"${DB_PASSWORD}\" psql -h db"}`,
		},
		{
			name: "unquoted exact reference stays",
			raw:  `{"cmd":"TOKEN=$GITHUB_TOKEN gh release upload"}`,
			want: `{"cmd":"TOKEN=$GITHUB_TOKEN gh release upload"}`,
		},
		{
			name: "single quoted marker is still skipped",
			raw:  `{"cmd":"SECRET='[REDACTED:1]' echo done"}`,
			want: `{"cmd":"SECRET='[REDACTED:1]' echo done"}`,
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

// Cycle E: a PEM block assigned to a secret-named variable is redacted by the
// PEM rule, and the assignment rule must not mark the result a second time.
func TestPEMInsideSecretAssignmentKeepsOneMarker(t *testing.T) {
	const pem = `-----BEGIN RSA PRIVATE KEY-----\nc3ludGhldGljLXBlbS1ib2R5LTAwMDE=\n-----END RSA PRIVATE KEY-----`

	tests := []struct {
		name          string
		assign        string
		want          string
		wantElsewhere string
	}{
		{
			name:          "suffixed secret name",
			assign:        `{"cmd":"DEPLOY_SECRET=\"` + pem + `\" ./deploy.sh"}`,
			want:          `{"cmd":"DEPLOY_SECRET=\"[REDACTED:1]\" ./deploy.sh"}`,
			wantElsewhere: `{"note":"[REDACTED:1]"}`,
		},
		{
			name:          "private key name",
			assign:        `{"cmd":"PRIVATE_KEY=\"` + pem + `\" ./deploy.sh"}`,
			want:          `{"cmd":"PRIVATE_KEY=\"[REDACTED:1]\" ./deploy.sh"}`,
			wantElsewhere: `{"note":"[REDACTED:1]"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			r := New()

			// Act
			got, err := r.RedactJSON([]byte(tt.assign))
			if err != nil {
				t.Fatalf("RedactJSON assignment: %v", err)
			}
			elsewhere, err := r.RedactJSON([]byte(`{"note":"` + pem + `"}`))
			if err != nil {
				t.Fatalf("RedactJSON elsewhere: %v", err)
			}

			// Assert
			if string(got) != tt.want {
				t.Errorf("assignment = %s, want %s", got, tt.want)
			}
			if string(elsewhere) != tt.wantElsewhere {
				t.Errorf("elsewhere = %s, want %s", elsewhere, tt.wantElsewhere)
			}
		})
	}
}
