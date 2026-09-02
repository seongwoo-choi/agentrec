package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/seongwoo-choi/agentrec/internal/action"
)

// A run that is still being recorded has evidence that is not there yet:
// the repository is measured when the session ends. Until then the page can
// ask what the working tree looks like now — a look, not a measurement, and
// labelled as one — and it can search every run for a word.

const (
	liveGitTimeout   = 5 * time.Second
	liveOutputLimit  = 1 << 20
	searchBudget     = 4 * time.Second
	searchMaxHits    = 200
	searchMinQuery   = 2
	searchSnippetMax = 160
)

var errRunNotRunning = errors.New("cli: the run is not running")

type liveFile struct {
	Path string `json:"path"`
	// Status is git's two-column code as it is: the index column, then the
	// working tree column ("M " staged, " M" not, "??" untracked).
	Status string `json:"status"`
	From   string `json:"from,omitempty"`
}

type liveChanges struct {
	MeasuredAt time.Time  `json:"measuredAt"`
	CWD        string     `json:"cwd"`
	Files      []liveFile `json:"files"`
	Note       string     `json:"note"`
	// Truncated says git had more to say than the output cap allowed.
	Truncated bool `json:"truncated,omitempty"`
}

const liveNote = "the working tree as it is now, observed during the run: not the measurement filed when the run ends, and not proof the agent caused it"

// readLiveChanges looks at the working tree of a run that is still going.
func readLiveChanges(root, runID string) (liveChanges, error) {
	if err := checkRunID(runID); err != nil {
		return liveChanges{}, err
	}
	m, err := readManifest(filepath.Join(root, runID))
	if err != nil {
		return liveChanges{}, err
	}
	// Running is what the trash refuses to move: a session whose recorder
	// holds its lock, a trace whose repository lock is held, an unfinished
	// run touched moments ago.
	if !errors.Is(runOpen(root, filepath.Join(root, runID), m), errRunOpen) {
		return liveChanges{}, errRunNotRunning
	}
	if !filepath.IsAbs(m.CWD) {
		return liveChanges{}, errors.New("cli: the run's working directory is not absolute")
	}
	// Asked without reading a single file's content: diff-files compares
	// stat data, diff-index --cached compares the index to HEAD, ls-files
	// --others lists what the index lacks, so nothing the repository
	// configures — a clean filter, a submodule, a hook — runs as the operator
	// on the page's behalf. The working tree is pinned to the run's
	// directory, whatever the repository's own configuration says.
	git := func(args ...string) ([]byte, bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), liveGitTimeout)
		defer cancel()
		full := append([]string{"-c", "core.fsmonitor=false", "-c", "color.ui=never", "-C", m.CWD, "--work-tree=" + m.CWD}, args...)
		cmd := exec.CommandContext(ctx, "git", full...)
		cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		cmd.WaitDelay = 2 * time.Second
		var out bytes.Buffer
		limited := &limitedWriter{w: &out, limit: liveOutputLimit}
		cmd.Stdout, cmd.Stderr = limited, io.Discard
		err := cmd.Run()
		return out.Bytes(), limited.dropped, err
	}
	fields := func(raw []byte, dropped bool) [][]byte {
		entries := bytes.Split(raw, []byte{0})
		if len(entries) > 0 && len(entries[len(entries)-1]) == 0 {
			entries = entries[:len(entries)-1]
		}
		if dropped && len(entries) > 0 {
			// The last entry may be cut mid-path: not a file to report.
			entries = entries[:len(entries)-1]
		}
		return entries
	}

	live := liveChanges{MeasuredAt: time.Now(), CWD: m.CWD, Files: []liveFile{}, Note: liveNote}
	type change struct {
		index, tree byte
		from        string
	}
	changes := map[string]*change{}
	at := func(path string) *change {
		c, ok := changes[path]
		if !ok {
			c = &change{index: ' ', tree: ' '}
			changes[path] = c
		}
		return c
	}
	// Index against HEAD: what is staged. A repository with no commit yet
	// has nothing to compare against, and its index is all additions.
	staged, dropped, err := git("diff-index", "--cached", "--name-status", "-z", "-M", "HEAD")
	if err != nil {
		staged, dropped, err = git("ls-files", "-z", "--cached")
		if err != nil {
			return liveChanges{}, fmt.Errorf("cli: git in %s: %w", m.CWD, err)
		}
		for _, p := range fields(staged, dropped) {
			at(string(p)).index = 'A'
		}
	} else {
		entries := fields(staged, dropped)
		for i := 0; i < len(entries); i++ {
			status := string(entries[i])
			if status == "" || i+1 >= len(entries) {
				break
			}
			i++
			path := string(entries[i])
			if (status[0] == 'R' || status[0] == 'C') && i+1 < len(entries) {
				i++
				c := at(string(entries[i]))
				c.index, c.from = status[0], path
				continue
			}
			at(path).index = status[0]
		}
	}
	live.Truncated = live.Truncated || dropped
	// Working tree against the index, by stat data alone: diff-files does
	// not open a file to decide, so nothing the repository configures runs.
	tree, dropped, err := git("diff-files", "--name-status", "-z")
	if err != nil {
		return liveChanges{}, fmt.Errorf("cli: git in %s: %w", m.CWD, err)
	}
	live.Truncated = live.Truncated || dropped
	entries := fields(tree, dropped)
	for i := 0; i+1 < len(entries); i += 2 {
		status := string(entries[i])
		if status == "" {
			continue
		}
		at(string(entries[i+1])).tree = status[0]
	}
	untracked, dropped, err := git("ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return liveChanges{}, fmt.Errorf("cli: git in %s: %w", m.CWD, err)
	}
	live.Truncated = live.Truncated || dropped
	for _, p := range fields(untracked, dropped) {
		c := at(string(p))
		c.index, c.tree = '?', '?'
	}
	for path, c := range changes {
		if c.index == ' ' && c.tree == ' ' {
			continue
		}
		live.Files = append(live.Files, liveFile{Path: path, Status: string([]byte{c.index, c.tree}), From: c.from})
	}
	sort.Slice(live.Files, func(i, j int) bool { return live.Files[i].Path < live.Files[j].Path })
	return live, nil
}

type limitedWriter struct {
	w       io.Writer
	limit   int
	n       int
	dropped bool
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n >= l.limit {
		l.dropped = l.dropped || len(p) > 0
		return len(p), nil
	}
	keep := p
	if l.n+len(keep) > l.limit {
		keep = keep[:l.limit-l.n]
		l.dropped = true
	}
	l.n += len(keep)
	_, err := l.w.Write(keep)
	return len(p), err
}

type searchHit struct {
	RunID     string    `json:"runId"`
	Project   string    `json:"project"`
	Provider  string    `json:"provider"`
	StartedAt time.Time `json:"startedAt"`
	Kind      string    `json:"kind"`
	ActionID  string    `json:"actionId,omitempty"`
	Type      string    `json:"type,omitempty"`
	Offset    int64     `json:"offset"`
	Snippet   string    `json:"snippet"`
}

type searchResult struct {
	Query     string      `json:"query"`
	Hits      []searchHit `json:"hits"`
	Truncated bool        `json:"truncated"`
	Scanned   int         `json:"scanned"`
}

// searchSnippetKeys is what an action is searched and quoted by: the same
// allowlisted input keys the reports show, so nothing a report would not
// show is surfaced by a search.
var searchSnippetKeys = []string{"prompt", "text", "command", "file_path", "path", "query", "pattern", "url", "tool", "name", "message"}

// searchRuns looks for q, case-insensitively, in every run's prompt, project
// and actions, newest run first, within a time budget and a hit limit.
func searchRuns(root, q string, limit int) (searchResult, error) {
	result := searchResult{Query: q, Hits: []searchHit{}}
	needle := strings.ToLower(strings.TrimSpace(q))
	if len(needle) < searchMinQuery {
		return result, fmt.Errorf("cli: a search needs at least %d characters", searchMinQuery)
	}
	if limit <= 0 || limit > searchMaxHits {
		limit = searchMaxHits
	}
	runs, _, err := listRuns(root, "")
	if err != nil {
		return result, err
	}
	deadline := time.Now().Add(searchBudget)
	// add keeps a hit while there is room, and says when there is none.
	add := func(hit searchHit) bool {
		if len(result.Hits) >= limit {
			result.Truncated = true
			return false
		}
		result.Hits = append(result.Hits, hit)
		return true
	}
	for _, run := range runs {
		if result.Truncated {
			break
		}
		if time.Now().After(deadline) {
			result.Truncated = true
			break
		}
		result.Scanned++
		// Through the same confined root every other reader uses: a link
		// planted in the store is not followed anywhere.
		runRoot, err := openRunRoot(root, run.ID)
		if err != nil {
			continue
		}
		base := searchHit{RunID: run.ID, Project: run.Project, Provider: run.Provider, StartedAt: run.StartedAt}
		if m, err := readManifestFromRoot(runRoot); err == nil && (foldIndex(m.CWD, needle) >= 0 || foldIndex(run.Project, needle) >= 0) {
			hit := base
			hit.Kind, hit.Snippet = "run", snippetAround(m.CWD, needle)
			if !add(hit) {
				runRoot.Close()
				break
			}
		}
		if prompt, err := readViewPrompt(runRoot); err == nil && prompt != "" && foldIndex(prompt, needle) >= 0 {
			hit := base
			hit.Kind, hit.Snippet = "prompt", snippetAround(prompt, needle)
			if !add(hit) {
				runRoot.Close()
				break
			}
		}
		err = searchActions(runRoot, needle, base, add)
		runRoot.Close()
		if err != nil {
			continue
		}
	}
	return result, nil
}

func searchActions(runRoot *os.Root, needle string, base searchHit, add func(searchHit) bool) error {
	f, err := openRegularFromRoot(runRoot, actionsFile)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, maxActionBytes)
	var offset int64
	for sc.Scan() {
		line := sc.Bytes()
		lineStart := offset
		offset += int64(len(line)) + 1
		// A quick look at the raw line first — unless the needle holds a
		// character JSON writes escaped, in which case only the decoded text
		// can be trusted.
		if !strings.ContainsAny(needle, "\"\\\x00\x01\x02\x03\x04\x05\x06\x07\x08\t\n\x0b\x0c\r\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f") && !bytes.Contains(bytes.ToLower(line), []byte(needle)) {
			continue
		}
		var a action.Action
		if json.Unmarshal(line, &a) != nil {
			continue
		}
		text := searchableText(a)
		if foldIndex(text, needle) < 0 {
			continue
		}
		hit := base
		hit.Kind, hit.ActionID, hit.Type, hit.Offset, hit.Snippet = "action", a.ID, a.Type, lineStart, snippetAround(text, needle)
		if !add(hit) {
			return nil
		}
	}
	return sc.Err()
}

// searchableText is what of an action a search may match: its type and
// the allowlisted input strings.
func searchableText(a action.Action) string {
	parts := []string{a.Type}
	var fields map[string]json.RawMessage
	if json.Unmarshal(a.Input, &fields) == nil {
		for _, key := range searchSnippetKeys {
			var value string
			if raw, ok := fields[key]; ok && json.Unmarshal(raw, &value) == nil && value != "" {
				parts = append(parts, value)
			}
		}
	}
	return strings.Join(parts, " ")
}

// foldIndex is the byte offset in text of the first case-insensitive match
// of needle, found on a lowered copy whose bytes are mapped back rune by
// rune, since lowering can change a rune's length.
func foldIndex(text, needle string) int {
	var lowered strings.Builder
	offsets := make([]int, 0, len(text)+1)
	for i, r := range text {
		low := string(unicode.ToLower(r))
		// One entry per byte of the lowered form, since that is what the
		// index found on it counts in.
		for j := 0; j < len(low); j++ {
			offsets = append(offsets, i)
		}
		lowered.WriteString(low)
	}
	offsets = append(offsets, len(text))
	i := strings.Index(lowered.String(), needle)
	if i < 0 || i >= len(offsets) {
		return -1
	}
	return offsets[i]
}

// snippetAround quotes the text around the first match, on one line.
func snippetAround(text, needle string) string {
	flat := singleLineText(text)
	i := foldIndex(flat, needle)
	if i < 0 {
		i = 0
	}
	start := i - searchSnippetMax/3
	if start < 0 {
		start = 0
	}
	end := start + searchSnippetMax
	if end > len(flat) {
		end = len(flat)
	}
	for start > 0 && start < len(flat) && !isRuneStart(flat[start]) {
		start--
	}
	for end < len(flat) && !isRuneStart(flat[end]) {
		end++
	}
	out := flat[start:end]
	if start > 0 {
		out = "…" + out
	}
	if end < len(flat) {
		out += "…"
	}
	return out
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

func singleLineText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
