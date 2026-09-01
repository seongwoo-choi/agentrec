package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// `agentrec setup` installs the hooks that record interactive sessions, into
// the provider's own configuration file, so that recording is one command
// rather than a fragment to paste. It changes nothing but the hook groups it
// owns: every other key, hook and value in the file is written back byte for
// byte, in the order it was found, and the previous file is kept beside it.

const setupUsage = "usage: agentrec setup [--claude] [--codex] [--verify] [--project] [--uninstall]\n"

type setupOptions struct {
	claude    bool
	codex     bool
	verify    bool
	project   bool
	uninstall bool
}

func parseSetupOptions(args []string) (setupOptions, bool) {
	var opts setupOptions
	for _, arg := range args {
		switch arg {
		case "--claude":
			opts.claude = true
		case "--codex":
			opts.codex = true
		case verifyFlag:
			opts.verify = true
		case "--project":
			opts.project = true
		case "--uninstall":
			opts.uninstall = true
		default:
			return setupOptions{}, false
		}
	}
	return opts, true
}

// hooksFile is where a provider reads its hooks from: Claude Code from its
// settings file, Codex from a hooks file of its own. --project chooses the
// repository-scoped file in the working directory instead of the user's.
func hooksFile(provider string, project bool) (string, error) {
	base := ""
	if project {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cli: locate working directory: %w", err)
		}
		base = cwd
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cli: locate home directory: %w", err)
		}
		base = home
	}
	switch provider {
	case "claude":
		return filepath.Join(base, ".claude", "settings.json"), nil
	case "codex":
		return filepath.Join(base, ".codex", "hooks.json"), nil
	}
	return "", fmt.Errorf("cli: no hooks file is known for provider %s", strconv.Quote(provider))
}

func runSetup(args []string, stdout, stderr io.Writer) int {
	opts, ok := parseSetupOptions(args)
	if !ok {
		fmt.Fprint(stderr, setupUsage)
		return exitUsage
	}
	if len(args) == 0 && setupInteractive() {
		detected, err := detectedProviders(false)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitFailure
		}
		if opts, ok = promptSetupOptions(bufio.NewReader(setupStdin), stdout, detected); !ok {
			fmt.Fprintln(stderr, "cli: setup cancelled; nothing was changed")
			return exitFailure
		}
	}
	providers := []string{}
	if opts.claude {
		providers = append(providers, "claude")
	}
	if opts.codex {
		providers = append(providers, "codex")
	}
	if len(providers) == 0 {
		detected, err := detectedProviders(opts.project)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitFailure
		}
		if len(detected) == 0 {
			fmt.Fprintln(stderr, "cli: neither a Claude Code nor a Codex configuration directory was found; pass --claude or --codex to create one")
			return exitFailure
		}
		providers = detected
	}
	exe, err := sessionExecutable()
	if err != nil {
		fmt.Fprintf(stderr, "cli: locate agentrec: %v\n", err)
		return exitFailure
	}

	failed := false
	for _, provider := range providers {
		path, err := hooksFile(provider, opts.project)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitFailure
		}
		fmt.Fprintf(stdout, "%s: %s\n", providerTitle(provider), displayPath(path))
		outcome, err := installHooks(path, hookFragment(provider, exe, opts.verify), provider, opts.uninstall)
		if err != nil {
			fmt.Fprintf(stderr, "cli: %v\n", err)
			failed = true
			continue
		}
		for _, line := range outcome.lines {
			fmt.Fprintf(stdout, "  %-20s %s\n", line.event, line.state)
		}
		if outcome.backup != "" {
			fmt.Fprintf(stdout, "  backup: %s\n", displayPath(outcome.backup))
		}
		if provider == "codex" && !opts.uninstall && outcome.changed {
			fmt.Fprintln(stdout, "  Run /hooks inside Codex once to trust the new hook: Codex skips a hook it has not been told to trust.")
		}
	}
	if failed {
		return exitFailure
	}
	if !opts.uninstall {
		root, err := runsRoot()
		if err != nil {
			root = "the agentrec runs directory"
		}
		fmt.Fprintf(stdout, "Sessions already open are not recorded: recording starts with the next session, and each one is filed under %s.\n", root)
	}
	return 0
}

// detectedProviders lists the providers that have a configuration directory
// where their hooks file would go. A provider that was never set up on this
// machine is not given a hooks file it would be surprised by.
func detectedProviders(project bool) ([]string, error) {
	var detected []string
	for _, provider := range []string{"claude", "codex"} {
		path, err := hooksFile(provider, project)
		if err != nil {
			return nil, err
		}
		if info, err := os.Stat(filepath.Dir(path)); err == nil && info.IsDir() {
			detected = append(detected, provider)
		}
	}
	return detected, nil
}

// setupStdin and setupInteractive are replaced in tests, which answer the
// prompts from a reader and decide whether there is a terminal to ask on.
var (
	setupStdin       io.Reader = os.Stdin
	setupInteractive           = stdinIsTerminal
)

// stdinIsTerminal asks the kernel for terminal attributes rather than checking
// for a character device: /dev/null is a character device too, and a scripted
// `agentrec setup </dev/null` must not be asked questions.
func stdinIsTerminal() bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdin.Fd(), ioctlReadTermios, uintptr(unsafe.Pointer(&termios)))
	return errno == 0
}

// promptSetupOptions asks, on a terminal with no flags given, what the flags
// would have said: which agent to record, whether to run the pinned checks,
// and whose file to write. Every question has a default that matches what the
// flags do on their own, so Enter alone is never a surprise, and the flags
// the answers amount to are printed so the next run can skip the questions.
func promptSetupOptions(in *bufio.Reader, out io.Writer, detected []string) (setupOptions, bool) {
	var opts setupOptions
	fmt.Fprintln(out, "agentrec setup: record interactive sessions")
	fmt.Fprintln(out)

	agentDefault := "3"
	switch {
	case slices.Equal(detected, []string{"claude"}):
		agentDefault = "1"
	case slices.Equal(detected, []string{"codex"}):
		agentDefault = "2"
	}
	fmt.Fprintf(out, "Which agent should be recorded?\n  1) Claude Code%s\n  2) Codex%s\n  3) both\n",
		detectedMark("claude", detected), detectedMark("codex", detected))
	answer, ok := ask(in, out, "Choice", agentDefault)
	if !ok {
		return opts, false
	}
	switch answer {
	case "1":
		opts.claude = true
	case "2":
		opts.codex = true
	case "3":
		opts.claude, opts.codex = true, true
	default:
		fmt.Fprintf(out, "%q is not one of 1, 2 or 3.\n", answer)
		return opts, false
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Run the checks pinned in .agentrec.yaml after each session? (--verify)")
	fmt.Fprintln(out, "  Only repositories whose .agentrec.yaml is committed and unchanged are checked.")
	answer, ok = ask(in, out, "Verify", "n")
	if !ok {
		return opts, false
	}
	opts.verify = answer == "y" || answer == "Y" || answer == "yes"

	fmt.Fprintln(out)
	fmt.Fprintf(out, "Where should the hooks go?\n  1) this user (%s, %s)\n  2) this project only (./.claude, ./.codex)\n",
		displayPath(claudeUserHooksHint()), displayPath(codexUserHooksHint()))
	answer, ok = ask(in, out, "Choice", "1")
	if !ok {
		return opts, false
	}
	switch answer {
	case "1":
	case "2":
		opts.project = true
	default:
		fmt.Fprintf(out, "%q is not one of 1 or 2.\n", answer)
		return opts, false
	}

	fmt.Fprintf(out, "\nRunning: agentrec setup %s\n\n", strings.Join(opts.flags(), " "))
	return opts, true
}

func detectedMark(provider string, detected []string) string {
	if slices.Contains(detected, provider) {
		return "  (found)"
	}
	return ""
}

// ask prints a question with its default and reads one line. An empty line is
// the default; end of input before any answer cancels.
func ask(in *bufio.Reader, out io.Writer, label, fallback string) (string, bool) {
	fmt.Fprintf(out, "%s [%s]: ", label, fallback)
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(out)
		return "", false
	}
	if line = strings.TrimSpace(line); line == "" {
		return fallback, true
	}
	return line, true
}

// flags spells the options back as the command line that would set them.
func (o setupOptions) flags() []string {
	var flags []string
	if o.claude {
		flags = append(flags, "--claude")
	}
	if o.codex {
		flags = append(flags, "--codex")
	}
	if o.verify {
		flags = append(flags, verifyFlag)
	}
	if o.project {
		flags = append(flags, "--project")
	}
	if o.uninstall {
		flags = append(flags, "--uninstall")
	}
	return flags
}

func claudeUserHooksHint() string {
	path, err := hooksFile("claude", false)
	if err != nil {
		return "~/.claude/settings.json"
	}
	return path
}

func codexUserHooksHint() string {
	path, err := hooksFile("codex", false)
	if err != nil {
		return "~/.codex/hooks.json"
	}
	return path
}

func providerTitle(provider string) string {
	if provider == "codex" {
		return "Codex"
	}
	return "Claude Code"
}

// displayPath shows a path under the home directory the way the operator
// would type it.
func displayPath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return path
}

// hookFragment is the hooks object the operator would otherwise paste: the
// events the recorder acts on for this provider, each with one command group.
func hookFragment(provider, exe string, verify bool) hookSettings {
	command := shellWord(exe) + " hook " + provider
	if verify {
		command += " " + verifyFlag
	}
	events := hookEvents[provider]
	settings := hookSettings{Hooks: make(map[string][]hookGroup, len(events))}
	for _, event := range events {
		timeout := hookCommandTimeout
		if provider == "codex" && event == hookSessionEnd {
			timeout = codexSessionEndHookTimeout
		}
		settings.Hooks[event] = []hookGroup{{Hooks: []hookCommand{{
			Type:    "command",
			Command: command,
			Timeout: timeout,
		}}}}
	}
	return settings
}

// isAgentrecHookCommand reports whether a hook command is one this program
// installed for the provider, whichever path or option it was installed with,
// so an earlier installation is replaced rather than joined.
func isAgentrecHookCommand(command, provider string) bool {
	command = strings.TrimSuffix(strings.TrimSpace(command), " "+verifyFlag)
	if !strings.HasSuffix(command, " hook "+provider) {
		return false
	}
	exe := strings.Trim(strings.TrimSuffix(command, " hook "+provider), "'\"")
	return filepath.Base(exe) == "agentrec"
}

type setupLine struct {
	event string
	state string
}

type setupOutcome struct {
	lines   []setupLine
	changed bool
	backup  string
}

// installHooks merges the fragment into the hooks file at path, or removes it
// again. The file is read as an ordered document so that everything not owned
// by this program is written back exactly as it was found.
func installHooks(path string, fragment hookSettings, provider string, uninstall bool) (setupOutcome, error) {
	var outcome setupOutcome
	original, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return outcome, fmt.Errorf("read %s: %w", strconv.Quote(path), err)
	}
	existed := err == nil
	if uninstall && !existed {
		for _, event := range hookEvents[provider] {
			outcome.lines = append(outcome.lines, setupLine{event, "absent"})
		}
		return outcome, nil
	}

	doc, err := decodeOrderedObject(original)
	if err != nil {
		return outcome, fmt.Errorf("%s is not a JSON object: %w", strconv.Quote(path), err)
	}
	hooksRaw, hadHooks := doc.get("hooks")
	hooks := &orderedObject{values: map[string]json.RawMessage{}}
	if hadHooks {
		if hooks, err = decodeOrderedObject(hooksRaw); err != nil {
			return outcome, fmt.Errorf("%s: \"hooks\" is not a JSON object: %w", strconv.Quote(path), err)
		}
	}

	for _, event := range hookEvents[provider] {
		var groups []json.RawMessage
		if raw, ok := hooks.get(event); ok {
			if err := json.Unmarshal(raw, &groups); err != nil {
				return outcome, fmt.Errorf("%s: \"hooks\".%s is not an array: %w", strconv.Quote(path), event, err)
			}
		}
		kept := groups[:0:0]
		removed := 0
		for _, group := range groups {
			if groupIsAgentrec(group, provider) {
				removed++
				continue
			}
			kept = append(kept, group)
		}
		state := ""
		switch {
		case uninstall && removed == 0:
			state = "absent"
		case uninstall:
			state = "removed"
		default:
			ours, err := json.MarshalIndent(fragment.Hooks[event][0], "      ", "  ")
			if err != nil {
				return outcome, fmt.Errorf("encode hook group: %w", err)
			}
			switch {
			case removed == 1 && len(groups) == len(kept)+1 && sameJSON(groups[indexOfAgentrec(groups, provider)], ours):
				state = "unchanged"
			case removed > 0:
				state = "updated"
			default:
				state = "installed"
			}
			kept = append(kept, ours)
		}
		outcome.lines = append(outcome.lines, setupLine{event, state})
		if state != "unchanged" && state != "absent" {
			outcome.changed = true
		}
		if len(kept) == 0 {
			hooks.remove(event)
			continue
		}
		hooks.set(event, marshalGroups(kept))
	}
	if !outcome.changed {
		return outcome, nil
	}

	if len(hooks.keys) == 0 && hadHooks {
		doc.set("hooks", json.RawMessage("{}"))
	} else {
		doc.set("hooks", hooks.marshal("  "))
	}
	updated := doc.marshal("")

	if existed {
		backup := path + ".bak-agentrec-" + time.Now().UTC().Format("20060102T150405Z")
		if err := os.WriteFile(backup, original, 0o600); err != nil {
			return outcome, fmt.Errorf("write backup %s: %w", strconv.Quote(backup), err)
		}
		outcome.backup = backup
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return outcome, fmt.Errorf("create %s: %w", strconv.Quote(filepath.Dir(path)), err)
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp := path + ".agentrec-tmp"
	if err := os.WriteFile(tmp, updated, mode); err != nil {
		return outcome, fmt.Errorf("write %s: %w", strconv.Quote(tmp), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return outcome, fmt.Errorf("install %s: %w", strconv.Quote(path), err)
	}
	return outcome, nil
}

func groupIsAgentrec(group json.RawMessage, provider string) bool {
	var parsed struct {
		Hooks []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(group, &parsed); err != nil {
		return false
	}
	for _, hook := range parsed.Hooks {
		if isAgentrecHookCommand(hook.Command, provider) {
			return true
		}
	}
	return false
}

func indexOfAgentrec(groups []json.RawMessage, provider string) int {
	for i, group := range groups {
		if groupIsAgentrec(group, provider) {
			return i
		}
	}
	return -1
}

// sameJSON compares two JSON values by content, not formatting.
func sameJSON(a, b json.RawMessage) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	ac, err1 := json.Marshal(av)
	bc, err2 := json.Marshal(bv)
	return err1 == nil && err2 == nil && bytes.Equal(ac, bc)
}

// marshalGroups lays the groups of one event out one per line, as the
// provider's own examples do.
func marshalGroups(groups []json.RawMessage) json.RawMessage {
	var b bytes.Buffer
	b.WriteString("[\n")
	for i, group := range groups {
		b.WriteString("      ")
		b.Write(bytes.TrimSpace(group))
		if i < len(groups)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("    ]")
	return b.Bytes()
}

// orderedObject is a JSON object whose keys keep the order they were read in
// and whose values are kept as the bytes they were read as. It exists so the
// operator's configuration comes back from setup with only the hooks touched.
type orderedObject struct {
	keys   []string
	values map[string]json.RawMessage
}

func decodeOrderedObject(raw []byte) (*orderedObject, error) {
	o := &orderedObject{values: map[string]json.RawMessage{}}
	if len(bytes.TrimSpace(raw)) == 0 {
		return o, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("want an object")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("want a string key")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		if _, dup := o.values[key]; dup {
			return nil, fmt.Errorf("duplicate key %s", strconv.Quote(key))
		}
		o.keys = append(o.keys, key)
		o.values[key] = value
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data after the object")
	}
	return o, nil
}

func (o *orderedObject) get(key string) (json.RawMessage, bool) {
	v, ok := o.values[key]
	return v, ok
}

func (o *orderedObject) set(key string, value json.RawMessage) {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

func (o *orderedObject) remove(key string) {
	if _, ok := o.values[key]; !ok {
		return
	}
	delete(o.values, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// marshal writes the object with two-space indentation at the given depth
// prefix, leaving every value's own bytes as they are.
func (o *orderedObject) marshal(prefix string) json.RawMessage {
	if len(o.keys) == 0 {
		return json.RawMessage("{}")
	}
	var b bytes.Buffer
	b.WriteString("{\n")
	for i, key := range o.keys {
		b.WriteString(prefix + "  ")
		b.Write(mustMarshal(key))
		b.WriteString(": ")
		b.Write(bytes.TrimSpace(o.values[key]))
		if i < len(o.keys)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString(prefix + "}")
	if prefix == "" {
		b.WriteString("\n")
	}
	return b.Bytes()
}

func mustMarshal(v any) []byte {
	out, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return out
}
