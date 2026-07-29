package redaction

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// assignName matches any shell or environment variable name, up to and
// including the `=`. Whether the name means a secret is decided at replacement
// time by isSecretName, so an assignment is judged by exactly the same rule as
// a JSON field of that name.
const assignName = `([A-Za-z_][A-Za-z0-9_-]*)=`

// minRun is the `{n,}` repetition that enforces minSecretLen inside a pattern.
var minRun = "{" + strconv.Itoa(minSecretLen) + ",}"

// assignRule is a NAME=VALUE pattern together with whether the shell would
// expand a `$` in the value it captures. Single quotes suppress expansion, so
// the same text means a reference in one rule and secret material in another,
// and only the rule knows which.
type assignRule struct {
	re      *regexp.Regexp
	expands bool
}

// assignRules capture the name and the assigned value separately, so `NAME=`
// and any quote delimiters survive redaction. An unquoted value ends at the
// first character that belongs to the surrounding command rather than to the
// secret: whitespace, a shell separator, or bracketing punctuation.
var assignRules = []assignRule{
	{regexp.MustCompile(assignName + `"([^"]` + minRun + `)"`), true},
	{regexp.MustCompile(assignName + `'([^']` + minRun + `)'`), false},
	{regexp.MustCompile(assignName + `([^\s"';&|,(){}<>\[\]]` + minRun + `)`), true},
}

// markerValue matches a value that is already a marker, from an earlier rule in
// this pass or an earlier pass over the same text. Re-redacting one would mint
// a second marker for a secret that already has one.
var markerValue = regexp.MustCompile(`^\[REDACTED:\d+\]$`)

// variableReference matches a value that is exactly `$NAME` or `${NAME}`, and
// nothing else. The anchors are the whole point: a value that merely opens with
// a reference — `${DB_PASSWORD}-suffix`, `$(cat /run/secrets/token)` — carries
// secret material of its own, and treating the `$` as proof of a reference is
// how a secret walks straight through.
var variableReference = regexp.MustCompile(`^\$(?:[A-Za-z_][A-Za-z0-9_]*|\{[A-Za-z_][A-Za-z0-9_]*\})$`)

// isSecretLiteral reports whether an assigned value is secret material rather
// than a marker already standing for it or a reference to a variable holding
// it. expands says whether the shell would expand the value: where it would,
// `$NAME` and `${NAME}` name a variable and the secret itself is not in the
// text; inside single quotes nothing is expanded, so the same characters are
// the secret.
func isSecretLiteral(value string, expands bool) bool {
	if markerValue.MatchString(value) {
		return false
	}
	return !expands || !variableReference.MatchString(value)
}

// tokenRules match a self-identifying secret whose entire text is replaced.
// They run after assignRules so a token that arrives as an assignment is
// captured once, under the same marker it would get standing alone.
var tokenRules = []*regexp.Regexp{
	// GitHub classic, OAuth, user-to-server, server-to-server and refresh tokens.
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
	// GitHub fine-grained personal access tokens.
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),
	// OpenAI-style keys.
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`),
	// AWS access key IDs, which are exactly twenty characters.
	regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
	// Google-style API keys.
	regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{20,}`),
	// Stripe secret and restricted keys, live and test.
	regexp.MustCompile(`\b[sr]k_(?:live|test)_[A-Za-z0-9]{16,}`),
	// JWTs: three base64url segments, the first carrying a JSON header.
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
	// Slack bot, user, app, refresh, workflow and legacy tokens.
	regexp.MustCompile(`\bxox[abeoprs]-[A-Za-z0-9-]{10,}`),
	// Slack webhook URLs, whose path is the credential. All three delivery
	// endpoints are matched: a workflow or trigger URL is as much a credential
	// as the incoming-webhook one it is usually confused with.
	regexp.MustCompile(`\bhttps://hooks\.slack\.com/(?:services|workflows|triggers)/[A-Za-z0-9/+_-]{20,}`),
	// GitLab personal, project, group and runner tokens.
	regexp.MustCompile(`\bgl(?:pat|rt|soat|ptt)-[A-Za-z0-9_-]{20,}`),
	// npm automation and publish tokens.
	regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`),
	// Hugging Face user access tokens.
	regexp.MustCompile(`\bhf_[A-Za-z0-9]{30,}`),
	// PyPI upload tokens, whose prefix encodes "pypi.org".
	regexp.MustCompile(`\bpypi-AgEIcHlwaS5vcmc[A-Za-z0-9_-]{50,}`),
}

// bearerRule captures only the credential so the scheme name survives. It runs
// last, after the shape rules, so a recognisable token inside an Authorization
// header is redacted under the marker it carries everywhere else.
var bearerRule = regexp.MustCompile(`(?i)\bbearer[ \t]+([A-Za-z0-9._~+/=-]` + minRun + `)`)

// basicRule is bearerRule's counterpart for HTTP Basic, whose credential is
// base64 of `user:password` and so is spelled in a narrower alphabet. Trailing
// padding is part of the credential, so the marker is keyed by exactly the text
// that field-name redaction would see for the same credential.
var basicRule = regexp.MustCompile(`(?i)\bbasic[ \t]+([A-Za-z0-9+/]` + minRun + `={0,2})`)

// pemRule matches a whole PEM private-key block, RSA, EC, OPENSSH or PKCS#8.
// It runs before every other rule so the base64 body is never carved up by a
// pattern that happens to match inside it.
var pemRule = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)

// pemTruncatedRule catches a key block whose END line never arrived — a clipped
// log tail, a capped event. What did arrive is still key material. It runs
// after pemRule, which has by then replaced every complete block, so the only
// BEGIN header left is one without an end and the match runs to the end of the
// string.
var pemTruncatedRule = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*`)

// plausiblyEncodedJSON reports whether trimmed could be an encoded JSON object
// or array. Opening with a bracket is not enough: `[INFO] server started` and
// prose someone wrapped in braces would both be replaced whole by the
// fail-closed path below, erasing readable output that never held a secret. The
// first byte past the bracket settles it, because a JSON object can only
// continue with a key or its close, and an array only with a value or its own.
// A bracket with nothing after it stays plausible, so a truncated body still
// fails closed rather than being read as prose.
func plausiblyEncodedJSON(trimmed string) bool {
	if trimmed == "" {
		return false
	}
	rest := strings.TrimLeft(trimmed[1:], " \t\r\n")
	if rest == "" {
		return trimmed[0] == '{' || trimmed[0] == '['
	}
	switch trimmed[0] {
	case '{':
		// A single quote is not JSON, but it is how a Python or Ruby dict of the
		// same body arrives, and that body carries the same secrets. Reading it
		// as prose would hand it to rules that only know `NAME=`, so it is judged
		// here and fails closed on the parse below.
		return rest[0] == '"' || rest[0] == '\'' || rest[0] == '}'
	case '[':
		if strings.IndexByte(`"{[]`, rest[0]) >= 0 {
			return true
		}
		// Primitive arrays share their first byte with common log prefixes. Treat
		// them as encoded JSON only when a decoder can consume the first complete
		// value; this rejects `[12/50]` and timestamp prefixes while still sending
		// `[1,2] trailing` through the fail-closed trailing-input check.
		dec := json.NewDecoder(strings.NewReader(trimmed))
		dec.UseNumber()
		return dec.Decode(new(any)) == nil
	}
	return false
}

// redactEncodedJSON reports whether s is an encoded JSON object or array, and
// if so returns it structurally redacted. Providers routinely carry a whole
// request or response body as an encoded string, and a plain sensitive field in
// there looks like nothing in particular to the pattern rules. depth is how
// many layers of encoding have already been unwrapped to reach s.
func (r *Redactor) redactEncodedJSON(s string, depth int) (string, bool) {
	trimmed := strings.TrimSpace(s)
	if !plausiblyEncodedJSON(trimmed) {
		return "", false
	}
	// Past the bound the walk stops rather than following input-controlled
	// nesting any further, and what is left goes whole.
	if depth >= maxEncodedJSONDepth {
		return r.marker(s), true
	}
	// A string that announces itself as JSON and then does not parse cannot be
	// read by the rules here, so it fails closed: the whole string goes, rather
	// than a guess at which part of it was the secret.
	v, err := decodeOne([]byte(trimmed))
	if err != nil {
		return r.marker(s), true
	}
	out, err := json.Marshal(r.redactValue(v, "", depth+1))
	if err != nil {
		return r.marker(s), true
	}
	return string(out), true
}

// redactText returns s with every secret-shaped run replaced by its run-local
// marker, leaving the surrounding text intact.
func (r *Redactor) redactText(s string, depth int) string {
	if nested, ok := r.redactEncodedJSON(s, depth); ok {
		return nested
	}
	s = r.redactGroup(s, pemRule, 0)
	s = r.redactGroup(s, pemTruncatedRule, 0)
	for _, rule := range assignRules {
		s = r.redactAssignment(s, rule)
	}
	for _, re := range tokenRules {
		s = r.redactGroup(s, re, 0)
	}
	s = r.redactGroupIf(s, bearerRule, 1, hasNonLetter)
	return r.redactGroupIf(s, basicRule, 1, validBasicCredential)
}

func hasNonLetter(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 'A' || value[i] > 'Z' {
			if value[i] < 'a' || value[i] > 'z' {
				return true
			}
		}
	}
	return false
}

func validBasicCredential(value string) bool {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(value)
	}
	return err == nil && strings.ContainsRune(string(decoded), ':')
}

// redactAssignment replaces the value of every NAME=VALUE match of rule whose
// name is secret-bearing, leaving the name, the `=` and any quotes in place.
func (r *Redactor) redactAssignment(s string, rule assignRule) string {
	matches := rule.re.FindAllStringSubmatchIndex(s, -1)
	if matches == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		name, start, end := s[m[2]:m[3]], m[4], m[5]
		if !isSecretName(name) || !isSecretLiteral(s[start:end], rule.expands) {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(r.marker(s[start:end]))
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}

// redactGroup replaces submatch group of every match of re in s with the marker
// for that group's text.
func (r *Redactor) redactGroup(s string, re *regexp.Regexp, group int) string {
	return r.redactGroupIf(s, re, group, func(string) bool { return true })
}

func (r *Redactor) redactGroupIf(s string, re *regexp.Regexp, group int, accept func(string) bool) string {
	matches := re.FindAllStringSubmatchIndex(s, -1)
	if matches == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		start, end := m[2*group], m[2*group+1]
		if start < 0 || !accept(s[start:end]) {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(r.marker(s[start:end]))
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}
