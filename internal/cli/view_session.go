package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// viewSessionGroup is only an optional read-time relationship, never authority.
// Inspect the captured bytes: encoding/json otherwise repairs invalid Unicode
// and accepts duplicate/case-insensitive struct keys.
func viewSessionGroup(raw []byte) string {
	if len(raw) > maxDocumentBytes || !utf8.Valid(raw) {
		return ""
	}
	keys := []string{"provider", "mode", "sessionId", "repoRoot", "canonicalCwd"}
	values := map[string]string{}
	d := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := d.Token(); err != nil || tok != json.Delim('{') {
		return ""
	}
	for d.More() {
		tok, err := d.Token()
		if err != nil {
			return ""
		}
		key, ok := tok.(string)
		if !ok {
			return ""
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return ""
		}
		for _, want := range keys {
			if strings.EqualFold(key, want) {
				if key != want {
					return ""
				}
				if _, seen := values[key]; seen {
					return ""
				}
				var s string
				if json.Unmarshal(value, &s) != nil || len(s) > 4096 || strings.TrimSpace(s) == "" || strings.ContainsRune(s, utf8.RuneError) {
					return ""
				}
				for _, r := range s {
					if unicode.IsControl(r) {
						return ""
					}
				}
				values[key] = s
			}
		}
	}
	if _, err := d.Token(); err != nil || len(values) != len(keys) || values["mode"] != "session" {
		return ""
	}
	root, cwd := values["repoRoot"], values["canonicalCwd"]
	for _, p := range []string{root, cwd} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || strings.TrimSpace(p) != p {
			return ""
		}
	}
	rel, err := filepath.Rel(root, cwd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	tuple, _ := json.Marshal([]string{"agentrec-session-v1", values["provider"], values["sessionId"], root})
	sum := sha256.Sum256(tuple)
	return hex.EncodeToString(sum[:])
}
