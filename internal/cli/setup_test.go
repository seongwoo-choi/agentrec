package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupHome gives setup a private home directory holding the provider
// configuration directories named, and returns it.
func setupHome(t *testing.T, dirs ...string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	restore := sessionExecutable
	t.Cleanup(func() { sessionExecutable = restore })
	sessionExecutable = func() (string, error) { return "/usr/local/bin/agentrec", nil }
	// Whether the test runner's stdin is a terminal is no business of setup's.
	restoreInteractive := setupInteractive
	t.Cleanup(func() { setupInteractive = restoreInteractive })
	setupInteractive = func() bool { return false }
	return home
}

// answerSetup makes setup believe it is on a terminal and gives it these
// lines as its answers.
func answerSetup(t *testing.T, answers string) {
	t.Helper()
	restoreIn, restoreInteractive := setupStdin, setupInteractive
	t.Cleanup(func() { setupStdin, setupInteractive = restoreIn, restoreInteractive })
	setupStdin = strings.NewReader(answers)
	setupInteractive = func() bool { return true }
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s is not JSON: %v\n%s", path, err, raw)
	}
	return doc
}

func hookCommands(t *testing.T, doc map[string]any, event string) []string {
	t.Helper()
	hooks, _ := doc["hooks"].(map[string]any)
	groups, _ := hooks[event].([]any)
	var commands []string
	for _, g := range groups {
		group, _ := g.(map[string]any)
		for _, h := range group["hooks"].([]any) {
			hook := h.(map[string]any)
			command, _ := hook["command"].(string)
			commands = append(commands, command)
		}
	}
	return commands
}

// A fresh machine gets a settings file holding nothing but the recorder's
// hooks, one group per event, and a hooks file for Codex with its own events.
func TestSetupInstallsIntoEveryDetectedProvider(t *testing.T) {
	home := setupHome(t, ".claude", ".codex")

	code, stdout, stderr := run(t, "setup")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	claude := readJSON(t, filepath.Join(home, ".claude", "settings.json"))
	for _, event := range hookEvents["claude"] {
		if got := hookCommands(t, claude, event); len(got) != 1 || got[0] != "/usr/local/bin/agentrec hook claude" {
			t.Errorf("claude %s commands = %v, want the one agentrec hook", event, got)
		}
	}
	codex := readJSON(t, filepath.Join(home, ".codex", "hooks.json"))
	for _, event := range hookEvents["codex"] {
		if got := hookCommands(t, codex, event); len(got) != 1 || got[0] != "/usr/local/bin/agentrec hook codex" {
			t.Errorf("codex %s commands = %v, want the one agentrec hook", event, got)
		}
	}
	if _, registered := codex["hooks"].(map[string]any)[hookPostToolUseFailure]; registered {
		t.Errorf("Codex was given PostToolUseFailure, which it never sends")
	}
	for _, want := range []string{"Claude Code: ~/.claude/settings.json", "Codex: ~/.codex/hooks.json", "installed", "/hooks inside Codex", "next session"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "backup:") {
		t.Errorf("a backup was written for files that did not exist:\n%s", stdout)
	}
}

// Setup owns only its hook groups: every other key, hook and value comes back
// byte for byte and in order, a second run changes nothing, --verify replaces
// the earlier installation instead of joining it, and --uninstall leaves the
// file as it was before setup ever ran.
func TestSetupPreservesEverythingItDoesNotOwn(t *testing.T) {
	home := setupHome(t, ".claude")
	path := filepath.Join(home, ".claude", "settings.json")
	original := `{
  "zeta": {"nested": [1, 2.50, "x"], "keep": true},
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Bash",
        "hooks": [{"type": "command", "command": "/usr/bin/other-logger", "timeout": 12}]
      }
    ],
    "Notification": [
      {"hooks": [{"type": "prompt", "prompt": "Was this good?"}]}
    ]
  },
  "alpha": "last on purpose",
  "number": 12345678901234567890
}
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if code, _, stderr := run(t, "setup", "--claude"); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(after)
	// Order and bytes of everything else.
	for _, want := range []string{
		`"zeta": {"nested": [1, 2.50, "x"], "keep": true}`,
		`"alpha": "last on purpose"`,
		`"number": 12345678901234567890`,
		`"command": "/usr/bin/other-logger", "timeout": 12`,
		`{"hooks": [{"type": "prompt", "prompt": "Was this good?"}]}`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("settings lost %q:\n%s", want, text)
		}
	}
	if zi, hi, ai, ni := strings.Index(text, `"zeta"`), strings.Index(text, `"hooks"`), strings.Index(text, `"alpha"`), strings.Index(text, `"number"`); !(zi < hi && hi < ai && ai < ni) {
		t.Errorf("top-level key order changed:\n%s", text)
	}
	doc := readJSON(t, path)
	if got := hookCommands(t, doc, hookPostToolUse); len(got) != 2 || got[0] != "/usr/bin/other-logger" || got[1] != "/usr/local/bin/agentrec hook claude" {
		t.Errorf("PostToolUse commands = %v, want the existing logger first and agentrec appended", got)
	}
	if got := hookCommands(t, doc, "Notification"); len(got) != 1 {
		t.Errorf("Notification was touched: %v", got)
	}
	backups, _ := filepath.Glob(path + ".bak-agentrec-*")
	if len(backups) != 1 {
		t.Fatalf("backups = %v, want exactly one", backups)
	}
	if kept, _ := os.ReadFile(backups[0]); string(kept) != original {
		t.Errorf("backup is not the original file")
	}

	// Idempotent.
	code, stdout, _ := run(t, "setup", "--claude")
	if code != 0 || !strings.Contains(stdout, "unchanged") || strings.Contains(stdout, "installed") {
		t.Errorf("second run exit %d, stdout:\n%s\nwant every event unchanged", code, stdout)
	}
	if again, _ := os.ReadFile(path); string(again) != text {
		t.Errorf("second run rewrote the file")
	}
	if backups, _ = filepath.Glob(path + ".bak-agentrec-*"); len(backups) != 1 {
		t.Errorf("second run wrote another backup: %v", backups)
	}

	// --verify replaces, never duplicates.
	code, stdout, _ = run(t, "setup", "--claude", verifyFlag)
	if code != 0 || !strings.Contains(stdout, "updated") {
		t.Errorf("verify run exit %d, stdout:\n%s\nwant events updated", code, stdout)
	}
	doc = readJSON(t, path)
	if got := hookCommands(t, doc, hookPostToolUse); len(got) != 2 || got[1] != "/usr/local/bin/agentrec hook claude --verify" {
		t.Errorf("PostToolUse commands after --verify = %v, want the agentrec group replaced", got)
	}

	// --uninstall removes only what setup added.
	code, stdout, _ = run(t, "setup", "--claude", "--uninstall")
	if code != 0 || !strings.Contains(stdout, "removed") {
		t.Errorf("uninstall exit %d, stdout:\n%s", code, stdout)
	}
	doc = readJSON(t, path)
	if got := hookCommands(t, doc, hookPostToolUse); len(got) != 1 || got[0] != "/usr/bin/other-logger" {
		t.Errorf("PostToolUse after uninstall = %v, want only the existing logger", got)
	}
	for _, event := range []string{hookSessionStart, hookUserPromptSubmit, hookPostToolUseFailure, hookStop, hookSessionEnd} {
		if _, left := doc["hooks"].(map[string]any)[event]; left {
			t.Errorf("%s was left behind as an empty event", event)
		}
	}
	if got := hookCommands(t, doc, "Notification"); len(got) != 1 {
		t.Errorf("Notification was touched by uninstall: %v", got)
	}
	if code, stdout, _ := run(t, "setup", "--claude", "--uninstall"); code != 0 || !strings.Contains(stdout, "absent") {
		t.Errorf("uninstall on a clean file exit %d, stdout:\n%s", code, stdout)
	}
}

// A file that is not a JSON object is refused untouched: setup would otherwise
// be guessing at what the operator's configuration meant.
func TestSetupRefusesAFileItCannotReadAsAnObject(t *testing.T) {
	home := setupHome(t, ".claude")
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.WriteFile(path, []byte("[1, 2, 3]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := run(t, "setup", "--claude")
	if code != exitFailure || !strings.Contains(stderr, "not a JSON object") {
		t.Errorf("exit %d, stderr %q; want a refusal", code, stderr)
	}
	if after, _ := os.ReadFile(path); string(after) != "[1, 2, 3]\n" {
		t.Errorf("the file was modified: %q", after)
	}
}

// --project writes beside the repository rather than under the home directory,
// and an unknown flag is a usage error.
func TestSetupProjectScopeAndUsage(t *testing.T) {
	setupHome(t)
	repo := cleanRepo(t)
	code, stdout, stderr := run(t, "setup", "--claude", "--project")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	doc := readJSON(t, filepath.Join(repo, ".claude", "settings.json"))
	if got := hookCommands(t, doc, hookSessionStart); len(got) != 1 {
		t.Errorf("project settings SessionStart = %v", got)
	}
	if !strings.Contains(stdout, filepath.Join(".claude", "settings.json")) {
		t.Errorf("stdout does not name the project file:\n%s", stdout)
	}
	if code, _, _ := run(t, "setup", "--gemini"); code != exitUsage {
		t.Errorf("unknown flag exit code = %d, want %d", code, exitUsage)
	}
	if code, _, stderr := run(t, "setup"); code != exitFailure || !strings.Contains(stderr, "pass --claude or --codex") {
		t.Errorf("no provider detected: exit %d, stderr %q", code, stderr)
	}
}

func TestIsAgentrecHookCommand(t *testing.T) {
	for _, tc := range []struct {
		command  string
		provider string
		want     bool
	}{
		{"/opt/homebrew/bin/agentrec hook claude", "claude", true},
		{"/opt/homebrew/bin/agentrec hook claude --verify", "claude", true},
		{"'/opt/agent rec/agentrec' hook codex", "codex", true},
		{"/opt/homebrew/bin/agentrec hook codex", "claude", false},
		{"/usr/bin/other-logger", "claude", false},
		{"/usr/bin/notagentrec hook claude", "claude", false},
	} {
		if got := isAgentrecHookCommand(tc.command, tc.provider); got != tc.want {
			t.Errorf("isAgentrecHookCommand(%q, %s) = %v, want %v", tc.command, tc.provider, got, tc.want)
		}
	}
}

// On a terminal with no flags, setup asks which agent, whether to verify and
// whose file, and the answers do exactly what the flags would have done.
func TestSetupAsksOnATerminalWhenNoFlagsAreGiven(t *testing.T) {
	home := setupHome(t, ".claude", ".codex")
	answerSetup(t, "2\ny\n1\n")

	code, stdout, stderr := run(t, "setup")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)\n%s", code, stderr, stdout)
	}
	for _, want := range []string{"1) Claude Code  (found)", "2) Codex  (found)", "Choice [3]: ", "Verify [n]: ", "Running: agentrec setup --codex --verify"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	codexHooks, err := os.ReadFile(filepath.Join(home, ".codex", "hooks.json"))
	if err != nil || !strings.Contains(string(codexHooks), "--verify") {
		t.Errorf("codex hooks = %q, %v; want the verifying hook command", codexHooks, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Errorf("claude settings were written although only Codex was chosen: %v", err)
	}

	// Enter alone takes every default: the one detected agent, no verify, the user's file.
	home = setupHome(t, ".claude")
	answerSetup(t, "\n\n\n")
	if code, stdout, _ := run(t, "setup"); code != 0 || !strings.Contains(stdout, "Choice [1]: ") || !strings.Contains(stdout, "Running: agentrec setup --claude\n") {
		t.Errorf("defaults: exit %d\n%s", code, stdout)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); err != nil {
		t.Errorf("claude settings were not written on defaults: %v", err)
	}

	// End of input before an answer, or an answer that is not a choice, changes nothing.
	for _, answers := range []string{"", "9\n"} {
		home = setupHome(t, ".claude")
		answerSetup(t, answers)
		if code, _, stderr := run(t, "setup"); code != exitFailure || !strings.Contains(stderr, "setup cancelled; nothing was changed") {
			t.Errorf("answers %q: exit %d, stderr %q", answers, code, stderr)
		}
		if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
			t.Errorf("answers %q: a hooks file was written anyway: %v", answers, err)
		}
	}

	// Flags skip the questions even on a terminal.
	setupHome(t, ".claude")
	answerSetup(t, "")
	if code, stdout, _ := run(t, "setup", "--claude"); code != 0 || strings.Contains(stdout, "Choice [") {
		t.Errorf("--claude on a terminal: exit %d\n%s", code, stdout)
	}
}
