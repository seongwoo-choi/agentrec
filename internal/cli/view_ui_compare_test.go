package cli

import (
	"strings"
	"testing"
)

// TestViewUICompareRunsAndVerifyLater pins the compare-two-runs sheet and the
// verify-later UI to the served assets: the page must still offer both, and
// every new English key must have a word in each of the three language tables.
func TestViewUICompareRunsAndVerifyLater(t *testing.T) {
	checks := map[string][]string{
		"ui_assets/index.html": {`id="diff-panel"`, `id="diff-list"`, `id="diff-result"`, `data-i18n="Compare runs"`},
		"ui_assets/app.js": {
			"'Compare with…'",
			"openDiff(",
			"#compare=",
			"pageAll(`${base}/changes`, DIFF_CHANGE_PAGES)",
			"Some changed files were not read; this split is incomplete.",
			"This run was not verified when it ended.",
			"'{size} in the trash'",
			"the repository HEAD has not moved since.",
			"whether the repository HEAD moved since is not known.",
			"'Only in {id}'",
			"'In both'",
			"posthocVerification",
			"/verify`",
			"'Verify now'",
			"'Verified later'",
			"headMovedSince === true",
			"the repository HEAD has moved since",
			"if (title === 'Verification') renderPosthoc(block);",
		},
		"ui_assets/app.css": {".diff-table", ".diff-files", ".posthoc-caveat", ".verify-actions"},
	}
	for name, markers := range checks {
		raw, err := viewAssets.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range markers {
			if !strings.Contains(string(raw), marker) {
				t.Errorf("%s does not contain %q", name, marker)
			}
		}
	}
	app, err := viewAssets.ReadFile("ui_assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"'Compare with…'", "'Compare runs'", "'Verify now'", "'Verified later'", "'Cannot verify: {error}'", "'Only in {id}'"} {
		// A table entry is the key followed by a colon; call sites never are.
		if got := strings.Count(string(app), key+":"); got != 3 {
			t.Errorf("%s has %d language-table entries, want 3 (ko, ja, zh-CN)", key, got)
		}
	}
}
