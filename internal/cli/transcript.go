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
	"sort"
	"strings"
	"syscall"
	"time"

	usageartifact "github.com/seongwoo-choi/agentrec/internal/usage"
)

// A session's hooks carry no usage, but the transcript they point at does:
// the provider's own record of every turn. It is read once, at session end,
// and only what it says about resources and identity is kept — token counts,
// the model, the provider's version — never a word of its content. The
// format is the provider's own and undocumented; the artifact says so.
//
// A transcript outlives a session: a resumed session appends to the same
// file. Only lines written since this run started are counted, so the usage
// filed is this session's, not the file's.

// errTranscriptNoUsage says the transcript was read and held no usage: a
// session that ended before any answer, or a format this reader does not
// know. Nothing is filed, and nothing is wrong.
var errTranscriptNoUsage = errors.New("transcript holds no usage")

const (
	transcriptReadLimit  = 256 << 20
	transcriptLineLimit  = 8 << 20
	transcriptFieldLimit = 128
)

// optSum tells a reported zero from a field the provider never wrote.
type optSum struct {
	value int64
	seen  bool
}

func (o *optSum) add(v *int64) {
	if v != nil && *v >= 0 {
		o.value += *v
		o.seen = true
	}
}

func (o *optSum) reset() { *o = optSum{} }

func (o optSum) ptr() *int64 {
	if !o.seen {
		return nil
	}
	v := o.value
	return &v
}

type claudeTranscriptLine struct {
	Type              string    `json:"type"`
	Version           string    `json:"version"`
	RequestID         string    `json:"requestId"`
	Timestamp         time.Time `json:"timestamp"`
	IsAPIErrorMessage bool      `json:"isApiErrorMessage"`
	Message           struct {
		Model string `json:"model"`
		Usage *struct {
			Input         *int64 `json:"input_tokens"`
			CacheCreation *int64 `json:"cache_creation_input_tokens"`
			CacheRead     *int64 `json:"cache_read_input_tokens"`
			Output        *int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type codexTokenUsage struct {
	Input      *int64 `json:"input_tokens"`
	Cached     *int64 `json:"cached_input_tokens"`
	CacheWrite *int64 `json:"cache_write_input_tokens"`
	Output     *int64 `json:"output_tokens"`
}

type codexRolloutLine struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Payload   struct {
		Type       string `json:"type"`
		Model      string `json:"model"`
		CLIVersion string `json:"cli_version"`
		Info       *struct {
			Last *codexTokenUsage `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

// openTranscript opens the file without ever blocking on it — a FIFO at the
// path would otherwise hold the recorder forever — and accepts only a
// regular file of a size it will read.
func openTranscript(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("transcript path is not absolute")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("transcript is not a regular file")
	}
	if info.Size() > transcriptReadLimit {
		f.Close()
		return nil, fmt.Errorf("transcript of %d bytes is larger than %d", info.Size(), transcriptReadLimit)
	}
	return f, nil
}

// readTranscriptUsage sums what the provider's transcript says the session
// used since the run started, and names the model(s) and the provider's
// version that wrote the last of it.
func readTranscriptUsage(provider, path string, since time.Time) (usageartifact.Report, string, error) {
	report := usageartifact.Report{Schema: 1, Attribution: usageartifact.AttributionProviderReported, Provider: provider, Scope: usageartifact.ScopeSession, Source: usageartifact.SourceTranscript}
	if provider != "claude" && provider != "codex" {
		return report, "", fmt.Errorf("no transcript reader for provider %q", provider)
	}
	f, err := openTranscript(path)
	if err != nil {
		return report, "", err
	}
	defer f.Close()

	var input, cacheCreation, cacheRead, output optSum
	measured := false
	models := map[string]bool{}
	version := ""
	seen := map[string]bool{}
	sc := bufio.NewScanner(io.LimitReader(f, transcriptReadLimit))
	sc.Buffer(make([]byte, 0, 64<<10), transcriptLineLimit)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		switch provider {
		case "claude":
			var l claudeTranscriptLine
			if json.Unmarshal(line, &l) != nil || (!l.Timestamp.IsZero() && l.Timestamp.Before(since)) {
				continue
			}
			if l.Version != "" {
				version = bounded(l.Version, transcriptFieldLimit)
			}
			// Claude Code files its own placeholders — an API error, an
			// interrupted turn — as assistant lines with a "<synthetic>"
			// model and no usage worth the name. They are not the model's.
			if l.Type != "assistant" || l.IsAPIErrorMessage || strings.HasPrefix(l.Message.Model, "<") {
				continue
			}
			if l.Message.Model != "" {
				models[bounded(l.Message.Model, transcriptFieldLimit)] = true
			}
			// One API response is written as one line per content block,
			// each carrying the response's usage: counted once per request.
			if l.Message.Usage == nil || (l.RequestID != "" && seen[l.RequestID]) {
				continue
			}
			seen[l.RequestID] = true
			measured = true
			input.add(l.Message.Usage.Input)
			cacheCreation.add(l.Message.Usage.CacheCreation)
			cacheRead.add(l.Message.Usage.CacheRead)
			output.add(l.Message.Usage.Output)
		case "codex":
			var l codexRolloutLine
			if json.Unmarshal(line, &l) != nil || (!l.Timestamp.IsZero() && l.Timestamp.Before(since)) {
				continue
			}
			if l.Type == "session_meta" && l.Payload.CLIVersion != "" {
				version = bounded(l.Payload.CLIVersion, transcriptFieldLimit)
			}
			if l.Type == "turn_context" && l.Payload.Model != "" {
				models[bounded(l.Payload.Model, transcriptFieldLimit)] = true
			}
			// token_count carries the last response's usage beside a total
			// for the whole file; the responses since the run started are
			// what this session used.
			if l.Type == "event_msg" && l.Payload.Type == "token_count" && l.Payload.Info != nil && l.Payload.Info.Last != nil {
				measured = true
				last := l.Payload.Info.Last
				input.add(last.Input)
				cacheCreation.add(last.CacheWrite)
				cacheRead.add(last.Cached)
				output.add(last.Output)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return report, "", fmt.Errorf("read transcript: %w", err)
	}
	if !measured {
		return report, version, errTranscriptNoUsage
	}
	report.InputTokens, report.CachedInputTokens, report.CacheCreationInputTokens, report.OutputTokens = input.ptr(), cacheRead.ptr(), cacheCreation.ptr(), output.ptr()
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)
	report.Model = bounded(strings.Join(names, ", "), usageartifact.MaxModelBytes-4)
	return report, version, nil
}
