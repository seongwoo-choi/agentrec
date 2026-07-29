package redaction

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestRedactJSONRedactsEnvAssignmentsInsideStrings(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "unquoted value keeps the surrounding command",
			raw:  `{"cmd":"export API_TOKEN=synthetic-env-token-aaaa; make deploy"}`,
			want: `{"cmd":"export API_TOKEN=[REDACTED:1]; make deploy"}`,
		},
		{
			name: "double quoted value keeps its quotes",
			raw:  `{"cmd":"DB_PASSWORD=\"synthetic-env-pass-bbbb\" psql -h db"}`,
			want: `{"cmd":"DB_PASSWORD=\"[REDACTED:1]\" psql -h db"}`,
		},
		{
			name: "single quoted value keeps its quotes",
			raw:  `{"cmd":"env SERVICE_API_KEY='synthetic-env-key-cccc' run task"}`,
			want: `{"cmd":"env SERVICE_API_KEY='[REDACTED:1]' run task"}`,
		},
		{
			name: "lower case name is matched case insensitively",
			raw:  `{"cmd":"my_api_key=synthetic-env-key-dddd bootstrap"}`,
			want: `{"cmd":"my_api_key=[REDACTED:1] bootstrap"}`,
		},
		{
			name: "value below the minimum length stays",
			raw:  `{"cmd":"API_TOKEN=short7x make deploy"}`,
			want: `{"cmd":"API_TOKEN=short7x make deploy"}`,
		},
		{
			name: "value at exactly the minimum length is redacted",
			raw:  `{"cmd":"API_TOKEN=short8xy make deploy"}`,
			want: `{"cmd":"API_TOKEN=[REDACTED:1] make deploy"}`,
		},
		{
			name: "non secret variable name stays",
			raw:  `{"cmd":"PATH=/usr/local/bin/toolchain export PATH"}`,
			want: `{"cmd":"PATH=/usr/local/bin/toolchain export PATH"}`,
		},
		{
			name: "repeated assignments in one string reuse the marker",
			raw:  `{"cmd":"API_TOKEN=synthetic-env-token-aaaa; RETRY_TOKEN=synthetic-env-token-aaaa; CI_SECRET=synthetic-env-other-eeee"}`,
			want: `{"cmd":"API_TOKEN=[REDACTED:1]; RETRY_TOKEN=[REDACTED:1]; CI_SECRET=[REDACTED:2]"}`,
		},
		{
			name: "ordinary prose with a short word assignment stays",
			raw:  `{"note":"set a=b and run the build; nothing secret here"}`,
			want: `{"note":"set a=b and run the build; nothing secret here"}`,
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

func TestEnvAssignmentMarkerMatchesFieldMarker(t *testing.T) {
	// Arrange: the same secret reaches the redactor once as a whole field value
	// and once inside a command string.
	r := New()

	// Act
	first, err := r.RedactJSON([]byte(`{"api_token":"synthetic-shared-secret-1"}`))
	if err != nil {
		t.Fatalf("RedactJSON first: %v", err)
	}
	second, err := r.RedactJSON([]byte(`{"cmd":"API_TOKEN=synthetic-shared-secret-1 make deploy"}`))
	if err != nil {
		t.Fatalf("RedactJSON second: %v", err)
	}

	// Assert
	if want := `{"api_token":"[REDACTED:1]"}`; string(first) != want {
		t.Errorf("first = %s, want %s", first, want)
	}
	if want := `{"cmd":"API_TOKEN=[REDACTED:1] make deploy"}`; string(second) != want {
		t.Errorf("second = %s, want %s", second, want)
	}
}

func TestRedactJSONRedactsGitHubTokenShapes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "classic personal access token",
			raw:  `{"cmd":"gh auth login --with-token ghp_abcdefghijklmnopqrst0000 now"}`,
			want: `{"cmd":"gh auth login --with-token [REDACTED:1] now"}`,
		},
		{
			name: "oauth token",
			raw:  `{"note":"received gho_abcdefghijklmnopqrst0001 from device flow"}`,
			want: `{"note":"received [REDACTED:1] from device flow"}`,
		},
		{
			name: "user to server token",
			raw:  `{"note":"ghu_abcdefghijklmnopqrst0002 rotates hourly"}`,
			want: `{"note":"[REDACTED:1] rotates hourly"}`,
		},
		{
			name: "server to server token",
			raw:  `{"note":"ghs_abcdefghijklmnopqrst0003 rotates hourly"}`,
			want: `{"note":"[REDACTED:1] rotates hourly"}`,
		},
		{
			name: "refresh token",
			raw:  `{"note":"ghr_abcdefghijklmnopqrst0004 rotates hourly"}`,
			want: `{"note":"[REDACTED:1] rotates hourly"}`,
		},
		{
			name: "fine grained pat keeps underscores",
			raw:  `{"note":"use github_pat_synthetic_0000000000000000 for the run"}`,
			want: `{"note":"use [REDACTED:1] for the run"}`,
		},
		{
			name: "too short suffix stays",
			raw:  `{"note":"ghp_abcdefghijklmnopqrs is not long enough"}`,
			want: `{"note":"ghp_abcdefghijklmnopqrs is not long enough"}`,
		},
		{
			name: "prefix inside a longer word stays",
			raw:  `{"note":"xghp_abcdefghijklmnopqrst0005 is not a token"}`,
			want: `{"note":"xghp_abcdefghijklmnopqrst0005 is not a token"}`,
		},
		{
			name: "two distinct tokens get distinct markers",
			raw:  `{"note":"ghp_abcdefghijklmnopqrst0000 then ghp_abcdefghijklmnopqrst0006 then ghp_abcdefghijklmnopqrst0000"}`,
			want: `{"note":"[REDACTED:1] then [REDACTED:2] then [REDACTED:1]"}`,
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

func TestRedactJSONRedactsCommonAPIKeyShapes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "openai style key",
			raw:  `{"cmd":"curl -H key:sk-synthetic0000000000000000AAAA https://example.invalid"}`,
			want: `{"cmd":"curl -H key:[REDACTED:1] https://example.invalid"}`,
		},
		{
			name: "openai style key below twenty characters stays",
			raw:  `{"note":"sk-abcdefghijklmnopqrs is too short"}`,
			want: `{"note":"sk-abcdefghijklmnopqrs is too short"}`,
		},
		{
			name: "aws long term access key id",
			raw:  `{"cmd":"aws s3 ls --profile AKIASYNTHETIC0000000 --region us-east-1"}`,
			want: `{"cmd":"aws s3 ls --profile [REDACTED:1] --region us-east-1"}`,
		},
		{
			name: "aws temporary access key id",
			raw:  `{"note":"assumed role gave ASIASYNTHETIC0000001 for one hour"}`,
			want: `{"note":"assumed role gave [REDACTED:1] for one hour"}`,
		},
		{
			name: "aws key id with seventeen trailing characters stays",
			raw:  `{"note":"AKIASYNTHETIC00000012 is not an access key id"}`,
			want: `{"note":"AKIASYNTHETIC00000012 is not an access key id"}`,
		},
		{
			name: "google style key",
			raw:  `{"note":"maps call used AIzaSyC-synthetic0000000000000 today"}`,
			want: `{"note":"maps call used [REDACTED:1] today"}`,
		},
		{
			name: "stripe live secret key",
			raw:  `{"note":"charge with sk_live_synthetic0000000 in prod"}`,
			want: `{"note":"charge with [REDACTED:1] in prod"}`,
		},
		{
			name: "stripe test secret key",
			raw:  `{"note":"charge with sk_test_synthetic0000001 in sandbox"}`,
			want: `{"note":"charge with [REDACTED:1] in sandbox"}`,
		},
		{
			name: "stripe live restricted key",
			raw:  `{"note":"charge with rk_live_synthetic0000002 in prod"}`,
			want: `{"note":"charge with [REDACTED:1] in prod"}`,
		},
		{
			name: "stripe test restricted key",
			raw:  `{"note":"charge with rk_test_synthetic0000003 in sandbox"}`,
			want: `{"note":"charge with [REDACTED:1] in sandbox"}`,
		},
		{
			name: "stripe key below sixteen characters stays",
			raw:  `{"note":"sk_live_synthetic is only a label"}`,
			want: `{"note":"sk_live_synthetic is only a label"}`,
		},
		{
			name: "ordinary words are left alone",
			raw:  `{"note":"the task-management skeleton builds fine and akiaisnotakey here"}`,
			want: `{"note":"the task-management skeleton builds fine and akiaisnotakey here"}`,
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

func TestRedactJSONRedactsBearerCredentialsAndJWTs(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "bearer prefix survives and only the credential goes",
			raw:  `{"note":"Authorization: Bearer synthetic-bearer-credential-1 sent"}`,
			want: `{"note":"Authorization: Bearer [REDACTED:1] sent"}`,
		},
		{
			name: "bearer keyword is matched case insensitively and keeps its casing",
			raw:  `{"note":"authorization: bearer synthetic-bearer-credential-2 sent"}`,
			want: `{"note":"authorization: bearer [REDACTED:1] sent"}`,
		},
		{
			name: "upper case bearer keyword keeps its casing",
			raw:  `{"note":"AUTHORIZATION: BEARER synthetic-bearer-credential-3 sent"}`,
			want: `{"note":"AUTHORIZATION: BEARER [REDACTED:1] sent"}`,
		},
		{
			name: "bearer credential below the minimum length stays",
			raw:  `{"note":"Bearer abc1234 was rejected"}`,
			want: `{"note":"Bearer abc1234 was rejected"}`,
		},
		{
			// Prose about bearer auth is not a credential. Every issued token
			// carries a digit or a separator somewhere, so a run of nothing but
			// letters is a word.
			name: "prose word after the bearer keyword stays",
			raw:  `{"note":"Bearer authentication is required for this endpoint"}`,
			want: `{"note":"Bearer authentication is required for this endpoint"}`,
		},
		{
			name: "lower case bearer prose stays",
			raw:  `{"note":"the bearer credentials were rotated overnight"}`,
			want: `{"note":"the bearer credentials were rotated overnight"}`,
		},
		{
			name: "one non-letter is enough to make a credential",
			raw:  `{"note":"Bearer abcdefgh1 was accepted"}`,
			want: `{"note":"Bearer [REDACTED:1] was accepted"}`,
		},
		{
			name: "jwt shaped value is redacted whole",
			raw:  `{"note":"id_token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJzeW50aGV0aWMifQ.c3ludGhldGljLXNpZ25hdHVyZQ expired"}`,
			want: `{"note":"id_token [REDACTED:1] expired"}`,
		},
		{
			name: "two segment value is not a jwt",
			raw:  `{"note":"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJzeW50aGV0aWMifQ is not a jwt"}`,
			want: `{"note":"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJzeW50aGV0aWMifQ is not a jwt"}`,
		},
		{
			name: "dotted version string is not a jwt",
			raw:  `{"note":"upgraded the toolchain to 1.24.5 this morning"}`,
			want: `{"note":"upgraded the toolchain to 1.24.5 this morning"}`,
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

func TestBearerCredentialKeepsTheMarkerOfItsUnderlyingToken(t *testing.T) {
	tests := []struct {
		name   string
		secret string
	}{
		{name: "github token", secret: "ghp_abcdefghijklmnopqrst0007"},
		{name: "jwt", secret: "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJzeW50aGV0aWMifQ.c3ludGhldGljLXNpZ25hdHVyZQ"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			r := New()

			// Act
			bare, err := r.RedactJSON([]byte(`{"note":"` + tt.secret + `"}`))
			if err != nil {
				t.Fatalf("RedactJSON bare: %v", err)
			}
			header, err := r.RedactJSON([]byte(`{"note":"Authorization: Bearer ` + tt.secret + `"}`))
			if err != nil {
				t.Fatalf("RedactJSON header: %v", err)
			}

			// Assert
			if want := `{"note":"[REDACTED:1]"}`; string(bare) != want {
				t.Errorf("bare = %s, want %s", bare, want)
			}
			if want := `{"note":"Authorization: Bearer [REDACTED:1]"}`; string(header) != want {
				t.Errorf("header = %s, want %s", header, want)
			}
		})
	}
}

func TestRedactJSONRedactsPEMPrivateKeyBlocks(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "rsa private key block",
			raw:  `{"content":"header line\n-----BEGIN RSA PRIVATE KEY-----\nc3ludGhldGljLWtleS1ib2R5AAAA\nBBBBCCCCDDDDEEEEFFFF\n-----END RSA PRIVATE KEY-----\nfooter line"}`,
			want: `{"content":"header line\n[REDACTED:1]\nfooter line"}`,
		},
		{
			name: "ec private key block",
			raw:  `{"content":"-----BEGIN EC PRIVATE KEY-----\nc3ludGhldGljLWVjLWtleQ==\n-----END EC PRIVATE KEY-----"}`,
			want: `{"content":"[REDACTED:1]"}`,
		},
		{
			name: "openssh private key block",
			raw:  `{"content":"-----BEGIN OPENSSH PRIVATE KEY-----\nc3ludGhldGljLW9wZW5zc2gta2V5\n-----END OPENSSH PRIVATE KEY-----"}`,
			want: `{"content":"[REDACTED:1]"}`,
		},
		{
			name: "unlabelled private key block",
			raw:  `{"content":"-----BEGIN PRIVATE KEY-----\nc3ludGhldGljLXBrY3M4LWtleQ==\n-----END PRIVATE KEY-----"}`,
			want: `{"content":"[REDACTED:1]"}`,
		},
		{
			name: "public certificate block stays",
			raw:  `{"content":"-----BEGIN CERTIFICATE-----\nc3ludGhldGljLWNlcnRpZmljYXRl\n-----END CERTIFICATE-----"}`,
			want: `{"content":"-----BEGIN CERTIFICATE-----\nc3ludGhldGljLWNlcnRpZmljYXRl\n-----END CERTIFICATE-----"}`,
		},
		{
			name: "two blocks in one string are redacted separately",
			raw:  `{"content":"-----BEGIN EC PRIVATE KEY-----\nc3ludGhldGljLWtleS1vbmU=\n-----END EC PRIVATE KEY-----\nand\n-----BEGIN EC PRIVATE KEY-----\nc3ludGhldGljLWtleS10d28=\n-----END EC PRIVATE KEY-----"}`,
			want: `{"content":"[REDACTED:1]\nand\n[REDACTED:2]"}`,
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

func TestRedactJSONRedactsJSONEncodedNestedPayloads(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "plain sensitive field inside an encoded payload",
			raw:  `{"payload":"{\"db_password\":\"synthetic-nested-pass-1\",\"user\":\"appuser\"}"}`,
			want: `{"payload":"{\"db_password\":\"[REDACTED:1]\",\"user\":\"appuser\"}"}`,
		},
		{
			name: "surrounding whitespace is trimmed away",
			raw:  `{"payload":"  {\"api_token\":\"synthetic-nested-token-2\"}  "}`,
			want: `{"payload":"{\"api_token\":\"[REDACTED:1]\"}"}`,
		},
		{
			name: "two levels of encoding",
			raw:  `{"payload":"{\"inner\":\"{\\\"client_secret\\\":\\\"synthetic-nested-secret-3\\\"}\"}"}`,
			want: `{"payload":"{\"inner\":\"{\\\"client_secret\\\":\\\"[REDACTED:1]\\\"}\"}"}`,
		},
		{
			name: "patterns still apply inside an encoded payload",
			raw:  `{"payload":"{\"cmd\":\"deploy with ghp_abcdefghijklmnopqrst0008 now\"}"}`,
			want: `{"payload":"{\"cmd\":\"deploy with [REDACTED:1] now\"}"}`,
		},
		{
			name: "encoded array is left as text",
			raw:  `{"payload":"[1,2,3]"}`,
			want: `{"payload":"[1,2,3]"}`,
		},
		{
			// A payload that announces itself as JSON and then does not parse is
			// unreadable to the field rules, so the whole string goes.
			name: "malformed json becomes one marker",
			raw:  `{"payload":"{\"db_password\": broken"}`,
			want: `{"payload":"[REDACTED:1]"}`,
		},
		{
			name: "object with trailing text becomes one marker",
			raw:  `{"payload":"{\"db_password\":\"synthetic-nested-pass-4\"} trailing"}`,
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

func TestNestedJSONSecretSharesTheFieldMarker(t *testing.T) {
	// Arrange: one secret arrives as a field value, then again inside a
	// JSON-encoded payload on a later line.
	r := New()

	// Act
	first, err := r.RedactJSON([]byte(`{"db_password":"synthetic-shared-nested-1"}`))
	if err != nil {
		t.Fatalf("RedactJSON first: %v", err)
	}
	second, err := r.RedactJSON([]byte(`{"payload":"{\"db_password\":\"synthetic-shared-nested-1\"}"}`))
	if err != nil {
		t.Fatalf("RedactJSON second: %v", err)
	}

	// Assert
	if want := `{"db_password":"[REDACTED:1]"}`; string(first) != want {
		t.Errorf("first = %s, want %s", first, want)
	}
	if want := `{"payload":"{\"db_password\":\"[REDACTED:1]\"}"}`; string(second) != want {
		t.Errorf("second = %s, want %s", second, want)
	}
}

// fixtureSecrets are every synthetic secret literal planted in the redaction
// fixture. Each one appears verbatim in the fixture file, so the assertions
// below prove something even if the fixture is later rewritten.
var fixtureSecrets = []string{
	"synthetic-token-aaaaaaaa",
	"synthetic-secret-bbbbbbbb",
	"synthetic-secret-cccccccc",
	"synthetic-password-dddddddd",
	"synthetic-apikey-eeeeeeee",
	"synthetic-env-token-11111111",
	"synthetic-env-pass-22222222",
	"ghp_syntheticAAAABBBBCCCCDDDD",
	"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJzeW50aGV0aWMifQ.c3ludGhldGljLXNpZ25hdHVyZQ",
	"synthetic-bearer-credential-44444444",
	"AKIASYNTHETIC0000000",
	"sk-synthetic0000000000000000AAAA",
	"AIzaSyC-synthetic0000000000000",
	"sk_live_synthetic0000000",
	"synthetic-nested-pass-33333333",
	"c3ludGhldGljLXJzYS1rZXktYm9keQ==",
}

// markerPattern finds every run-local marker in redacted output.
var markerPattern = regexp.MustCompile(`\[REDACTED:\d+\]`)

func TestFixtureRedactsEverySyntheticSecretThroughSmallReads(t *testing.T) {
	// Arrange
	raw, err := os.ReadFile(fixturePath(t, "provider-events.jsonl"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	for _, secret := range fixtureSecrets {
		if !strings.Contains(string(raw), secret) {
			t.Fatalf("fixture does not contain %q, so this test would prove nothing", secret)
		}
	}
	var out strings.Builder

	// Act: three bytes per Read, so line splitting is exercised at every offset.
	if err := New().RedactJSONL(&tinyReader{data: raw}, &out); err != nil {
		t.Fatalf("RedactJSONL: %v", err)
	}
	got := out.String()

	// Assert: no secret survives, in plain text or as a digest of itself.
	for _, secret := range fixtureSecrets {
		if strings.Contains(got, secret) {
			t.Errorf("output leaked %q", secret)
		}
		digest := sha256.Sum256([]byte(secret))
		if strings.Contains(got, hex.EncodeToString(digest[:])) {
			t.Errorf("output contains the SHA-256 digest of %q", secret)
		}
	}

	// Assert: one marker per distinct secret, reused everywhere it appears.
	markers := make(map[string]bool)
	for _, m := range markerPattern.FindAllString(got, -1) {
		markers[m] = true
	}
	if len(markers) != len(fixtureSecrets) {
		t.Errorf("got %d distinct markers, want %d: %v", len(markers), len(fixtureSecrets), markers)
	}

	// Assert: the non-secret text around each secret is untouched.
	for _, keep := range []string{
		`/workspace/project`,
		`/bin/zsh`,
		`tok_1234567890`,
		`https://example.invalid/v1/deploy`,
		`export API_TOKEN=[REDACTED:`,
		`DB_PASSWORD=\"[REDACTED:`,
		`./scripts/migrate.sh`,
		`aws s3 cp build.tar`,
		`Bearer [REDACTED:`,
		`\"db_user\":\"appuser\"`,
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("output lost surrounding text %q", keep)
		}
	}
}

// Shapes added in rule version 2. Every one of them is matched on a distinctive
// vendor prefix, so a rule can only fire on text that announces itself as that
// vendor's credential — an expansion that widens what is caught without
// widening what is destroyed. The negative cases are the point: readable
// evidence that merely resembles a credential must survive.
func TestRedactJSONRedactsVendorTokenShapesAddedInRuleVersionTwo(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "slack bot token",
			raw:  `{"note":"posted with xoxb-000000000000-synthetic0000 to #ops"}`,
			want: `{"note":"posted with [REDACTED:1] to #ops"}`,
		},
		{
			name: "slack user token",
			raw:  `{"note":"xoxp-000000000000-synthetic0001 belongs to the app"}`,
			want: `{"note":"[REDACTED:1] belongs to the app"}`,
		},
		{
			name: "slack incoming webhook url",
			raw:  `{"cmd":"curl -X POST https://hooks.slack.com/services/T00000000/B00000000/synthetic00000000"}`,
			want: `{"cmd":"curl -X POST [REDACTED:1]"}`,
		},
		{
			name: "gitlab personal access token",
			raw:  `{"cmd":"git push https://oauth2:glpat-synthetic0000000000000@gitlab.invalid/x"}`,
			want: `{"cmd":"git push https://oauth2:[REDACTED:1]@gitlab.invalid/x"}`,
		},
		{
			name: "gitlab runner token",
			raw:  `{"note":"registered with glrt-synthetic0000000000001 last week"}`,
			want: `{"note":"registered with [REDACTED:1] last week"}`,
		},
		{
			// Deliberately not spelled as `_authToken=...`: that name is
			// secret-bearing on its own, so an assignment fixture would pass
			// without the shape rule existing at all.
			name: "npm token",
			raw:  `{"note":"published using npm_synthetic000000000000000000000000000 today"}`,
			want: `{"note":"published using [REDACTED:1] today"}`,
		},
		{
			name: "slack workflow webhook url",
			raw:  `{"cmd":"curl -X POST https://hooks.slack.com/workflows/T00000000/A00000000/synthetic00000000"}`,
			want: `{"cmd":"curl -X POST [REDACTED:1]"}`,
		},
		{
			name: "hugging face token",
			raw:  `{"note":"pulled the model with hf_synthetic00000000000000000000000000 today"}`,
			want: `{"note":"pulled the model with [REDACTED:1] today"}`,
		},
		{
			// The body has to reach the fifty characters the rule requires, which
			// is what keeps the distinctive prefix from firing on a truncated
			// mention of one.
			name: "pypi upload token",
			raw:  `{"note":"twine used pypi-AgEIcHlwaS5vcmcsynthetic00000000000000000000000000000000000000000 to upload"}`,
			want: `{"note":"twine used [REDACTED:1] to upload"}`,
		},
		{
			name: "a pypi prefix with too short a body stays",
			raw:  `{"note":"pypi-AgEIcHlwaS5vcmcshort is not a token"}`,
			want: `{"note":"pypi-AgEIcHlwaS5vcmcshort is not a token"}`,
		},
		{
			name: "a slack channel name is not a token",
			raw:  `{"note":"see #xoxb-notes for the runbook"}`,
			want: `{"note":"see #xoxb-notes for the runbook"}`,
		},
		{
			name: "an npm package name is not a token",
			raw:  `{"cmd":"npm install npm_check_updates"}`,
			want: `{"cmd":"npm install npm_check_updates"}`,
		},
		{
			name: "a gitlab prefix without a token body stays",
			raw:  `{"note":"glpat-short is not one"}`,
			want: `{"note":"glpat-short is not one"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := New().RedactJSON([]byte(tc.raw))
			if err != nil {
				t.Fatalf("RedactJSON(%s): %v", tc.raw, err)
			}
			if string(got) != tc.want {
				t.Errorf("RedactJSON(%s) =\n%s\nwant\n%s", tc.raw, got, tc.want)
			}
		})
	}
}

// Field-name suffixes added in rule version 2, and the names next to them that
// must keep working: matching on the ending is what keeps a public key, a
// primary key and a sort key readable while a passphrase is not.
func TestRedactJSONRedactsFieldNameSuffixesAddedInRuleVersionTwo(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "passphrase",
			raw:  `{"passphrase":"synthetic-passphrase-value"}`,
			want: `{"passphrase":"[REDACTED:1]"}`,
		},
		{
			name: "ssh key passphrase",
			raw:  `{"SSH_KEY_PASSPHRASE":"synthetic-passphrase-value"}`,
			want: `{"SSH_KEY_PASSPHRASE":"[REDACTED:1]"}`,
		},
		{
			name: "session key",
			raw:  `{"sessionKey":"synthetic-session-value"}`,
			want: `{"sessionKey":"[REDACTED:1]"}`,
		},
		{
			name: "auth key",
			raw:  `{"auth-key":"synthetic-auth-value"}`,
			want: `{"auth-key":"[REDACTED:1]"}`,
		},
		{
			name: "encryption key",
			raw:  `{"ENCRYPTION_KEY":"synthetic-encryption-value"}`,
			want: `{"ENCRYPTION_KEY":"[REDACTED:1]"}`,
		},
		{
			name: "a public key is not a secret name",
			raw:  `{"PUBLIC_KEY":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 user@host"}`,
			want: `{"PUBLIC_KEY":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 user@host"}`,
		},
		{
			name: "a primary key is not a secret name",
			raw:  `{"primaryKey":"orders_pkey_on_id_column"}`,
			want: `{"primaryKey":"orders_pkey_on_id_column"}`,
		},
		{
			name: "a keyboard shortcut is not a secret name",
			raw:  `{"shortcutKey":"ctrl+shift+p opens the palette"}`,
			want: `{"shortcutKey":"ctrl+shift+p opens the palette"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := New().RedactJSON([]byte(tc.raw))
			if err != nil {
				t.Fatalf("RedactJSON(%s): %v", tc.raw, err)
			}
			if string(got) != tc.want {
				t.Errorf("RedactJSON(%s) =\n%s\nwant\n%s", tc.raw, got, tc.want)
			}
		})
	}
}
