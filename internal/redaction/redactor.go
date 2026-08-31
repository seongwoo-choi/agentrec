// Package redaction removes secret material from provider event JSON before it
// is persisted, replacing each secret with an opaque run-local marker.
package redaction

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
)

// RuleVersion identifies the redaction rule set that produced a marker, so a
// recorded run can be interpreted against the rules in force when it was made.
// It is bumped whenever a rule is added or changed: two bundles stamped with
// different versions were judged by different rules, and a reader comparing
// their redaction counts has to know that.
const RuleVersion = "6"

// secretSuffixes are the canonicalized field-name endings whose string values
// are treated as secret material. Matching on the ending rather than on a
// substring is what keeps `PUBLIC_KEY`, `PRIMARY_KEY` and `TOKEN_ID` out: a
// name that merely mentions a secret is not one.
var secretSuffixes = []string{
	"TOKEN", "SECRET", "PASSWORD", "APIKEY", "ACCESSKEY", "PRIVATEKEY",
	"SECRETKEY", "SIGNINGKEY", "PASSWD", "AUTHORIZATION", "CREDENTIAL",
	"CREDENTIALS", "COOKIE", "PASSPHRASE", "SESSIONKEY", "AUTHKEY",
	"ENCRYPTIONKEY",
}

// nameCanonicalizer folds the spellings one secret name arrives under —
// SCREAMING_SNAKE, camelCase, kebab-cased headers — onto a single comparable
// form, so `AWS_SECRET_ACCESS_KEY`, `apiKey` and `x-api-key` are all reached.
var nameCanonicalizer = strings.NewReplacer("_", "", "-", "")

// Scanner line limits. A single provider event can carry a whole file's
// contents on one line, well past bufio.Scanner's 64 KiB default, so the cap is
// raised to a size that still bounds memory on a hostile stream.
const (
	initialLineBuffer = 64 << 10
	maxLineBuffer     = 4 << 20
)

// maxEncodedJSONDepth bounds how many layers of JSON-encoded string a single
// walk will decode. The input decides the nesting, so without a bound a hostile
// event could drive the walk until the stack gives out. Each layer of encoding
// roughly doubles the text it wraps, so eight layers is already far past
// anything a provider produces and still well inside maxLineBuffer.
const maxEncodedJSONDepth = 8

// minSecretLen is the shortest value worth redacting. Shorter values under a
// secret-looking name are almost always placeholders or enum-like flags.
const minSecretLen = 8

// ErrNotJSONObject reports that the input was not exactly one JSON object. It
// is a statement about the shape of the input and not about this package
// failing: redaction cannot say which part of such a value was the secret, so
// it refuses the value whole. A caller holding provider output can tell this
// refusal apart from a real failure and decide where the line belongs instead —
// what it must never do is persist the value as it arrived.
var ErrNotJSONObject = errors.New("redaction: not exactly one JSON object")

// Redactor holds the marker assignments for one run, so the same secret gets
// the same marker across every event without ever revealing the secret.
type Redactor struct {
	markers map[string]string
}

// New returns a Redactor with empty run-local state.
func New() *Redactor {
	return &Redactor{markers: make(map[string]string)}
}

// RedactJSON returns v with every secret-named string value replaced by its
// run-local marker.
func (r *Redactor) RedactJSON(raw []byte) ([]byte, error) {
	// Redaction fails closed: anything that is not exactly one JSON object is
	// input this package cannot vouch for, so it is never passed through. Both
	// ways of not being one are reported under ErrNotJSONObject, so a caller can
	// tell "this is not an event" from "this package could not do its work".
	v, err := decodeOne(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotJSONObject, err)
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, fmt.Errorf("%w: want a JSON object", ErrNotJSONObject)
	}

	out, err := json.Marshal(r.redactValue(v, "", 0))
	if err != nil {
		return nil, fmt.Errorf("redaction: %w", err)
	}
	return out, nil
}

// RedactArgv returns a copy of argv with values following secret-bearing option
// names replaced by run-local markers. JSON field redaction sees argv only as
// an array, so it cannot otherwise associate a value with the preceding flag.
func (r *Redactor) RedactArgv(argv []string) ([]string, error) {
	out := slices.Clone(argv)
	for i := 0; i < len(out); i++ {
		arg := out[i]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name, value, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if name == "" || !isSecretName(name) {
			continue
		}
		if inline {
			out[i] = arg[:strings.IndexByte(arg, '=')+1] + r.marker(value)
			continue
		}
		if i+1 == len(out) || strings.HasPrefix(out[i+1], "-") {
			return nil, errors.New("redaction: secret-bearing option has no unambiguous separate value; use --secret-option=<value>")
		}
		out[i+1] = r.marker(out[i+1])
		i++
	}
	return out, nil
}

// decodeOne decodes exactly one JSON value from raw, rejecting anything that
// follows it. Numbers are kept as text so a value survives the round trip
// unchanged.
func decodeOne(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("redaction: %w", err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("redaction: unexpected trailing JSON value")
		}
		return nil, fmt.Errorf("redaction: trailing input: %w", err)
	}
	return v, nil
}

// redactValue returns a copy of v with secrets replaced. name is the field the
// value was found under; array elements inherit the name of the field holding
// the array, since that is what describes their contents. depth counts the
// layers of JSON-encoded string already unwrapped to reach v.
func (r *Redactor) redactValue(v any, name string, depth int) any {
	switch val := v.(type) {
	case map[string]any:
		if isSecretName(name) {
			return r.canonicalMarker(val)
		}
		out := make(map[string]any, len(val))
		// Keys are visited in sorted order so marker numbering depends only on
		// the document, never on Go's randomized map iteration.
		for _, key := range slices.Sorted(maps.Keys(val)) {
			out[key] = r.redactValue(val[key], key, depth)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, child := range val {
			out[i] = r.redactValue(child, name, depth)
		}
		return out
	case string:
		if isSecretName(name) {
			if marker, ok := r.markers[val]; ok {
				return marker
			}
			if len(val) < minSecretLen {
				if safe, ok := r.replaceKnownSecrets(val); ok {
					return safe
				}
				return r.redactText(val, depth)
			}
			// A value that is already a marker came from an earlier pass over
			// this run's output. Marking it again would spend a second marker on
			// text that is not the secret, losing the correlation the first one
			// records.
			if markerValue.MatchString(val) {
				return val
			}
			return r.marker(val)
		}
		return r.redactText(val, depth)
	default:
		// A number, a boolean or a null under a secret name is still the secret:
		// an expiry-free API key can arrive as a bare integer, and printing it
		// because it was not spelled with quotes publishes it.
		if isSecretName(name) {
			return r.canonicalMarker(val)
		}
		return val
	}
}

// RedactJSONL redacts a JSON Lines stream, reading one event per line from src
// and writing its sanitized form to dst. All lines share one Redactor state, so
// a secret repeated across events keeps a single marker.
func (r *Redactor) RedactJSONL(src io.Reader, dst io.Writer) error {
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 0, initialLineBuffer), maxLineBuffer)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		out, err := r.RedactJSON(line)
		if err != nil {
			return err
		}
		if _, err := dst.Write(append(out, '\n')); err != nil {
			return fmt.Errorf("redaction: write: %w", err)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("redaction: read: %w", err)
	}
	return nil
}

// marker returns the run-local marker for secret, assigning a new one the first
// time that exact value is seen.
func (r *Redactor) marker(secret string) string {
	if m, ok := r.markers[secret]; ok {
		return m
	}
	m := fmt.Sprintf("[REDACTED:%d]", len(r.markers)+1)
	r.markers[secret] = m
	return m
}

func (r *Redactor) replaceKnownSecrets(text string) (string, bool) {
	secrets := slices.Collect(maps.Keys(r.markers))
	slices.SortFunc(secrets, func(a, b string) int {
		if len(a) > len(b) {
			return -1
		}
		if len(a) < len(b) {
			return 1
		}
		return strings.Compare(a, b)
	})
	pairs := make([]string, 0, len(secrets)*2)
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			pairs = append(pairs, secret, r.markers[secret])
		}
	}
	if len(pairs) == 0 {
		return text, false
	}
	return strings.NewReplacer(pairs...).Replace(text), true
}

// canonicalMarker returns the marker for a value that is replaced whole: an
// object found under a secret-bearing name, or a scalar that is not a string.
// Walking into an object would publish its shape and its keys, neither of which
// the rules below can judge. The value is marshalled first so the marker is
// keyed by its canonical text: encoding/json sorts object keys and writes one
// spelling per number, so the same value keys the same marker however it
// arrived, and a secret that appears once as a number and once as the string of
// that number stays correlated. A value that came out of decodeOne always
// marshals again.
func (r *Redactor) canonicalMarker(v any) string {
	out, _ := json.Marshal(v)
	return r.marker(string(out))
}

// isSecretName reports whether a field or variable name ends with a
// secret-bearing suffix. Matching on the ending keeps `token_id` and other
// names that merely mention a secret out of the way.
func isSecretName(name string) bool {
	canonical := nameCanonicalizer.Replace(strings.ToUpper(name))
	for _, suffix := range secretSuffixes {
		if strings.HasSuffix(canonical, suffix) {
			return true
		}
	}
	return false
}
