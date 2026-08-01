// Package claude parses Claude Code stream-json events into normalized actions.
package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/action"
)

// providerName labels every action recovered from a Claude event stream.
const providerName = "claude"

// mcpToolPrefix marks tool names served by an MCP server.
const mcpToolPrefix = "mcp__"

// Scanner buffer sizes: single events routinely carry whole file contents, so
// the default 64 KiB line limit is far too small.
const (
	initialLineBuffer = 64 << 10
	maxLineBytes      = 4 << 20
)

// Hook markers on "system" events: only a PostToolUse response times the tool
// itself, so only that subtype/event pair enriches an action.
const (
	hookResponseSubtype = "hook_response"
	postToolUseHook     = "PostToolUse"
)

// Action lifecycle statuses reported by this parser.
const (
	statusInProgress = "in_progress"
	statusCompleted  = "completed"
	statusFailed     = "failed"
)

// ParseResult holds the actions recovered from a Claude event stream along with
// the number of events that could not be interpreted.
type ParseResult struct {
	Actions      []action.Action
	WarningCount int
}

// event is the subset of a Claude stream-json line the parser needs.
type event struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	// ParentToolUseID is set on events emitted inside a subagent, naming the
	// tool_use that spawned it.
	ParentToolUseID string `json:"parent_tool_use_id"`
	Message         struct {
		Content []contentBlock `json:"content"`
	} `json:"message"`
	ToolUseResult json.RawMessage `json:"tool_use_result"`
	// Subtype, HookEvent and Output describe hook activity on "system" events.
	Subtype   string `json:"subtype"`
	HookEvent string `json:"hook_event"`
	Output    string `json:"output"`
}

// hookOutput is the subset of a hook_response "output" payload the parser
// needs: the tool_use it reports on and how long that tool ran.
type hookOutput struct {
	ToolUseID  string `json:"tool_use_id"`
	DurationMs int64  `json:"duration_ms"`
}

// contentBlock covers both assistant tool_use blocks and user tool_result blocks.
type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	Text      string          `json:"text"`
	IsError   bool            `json:"is_error"`
}

// resultPayload is the normalized result body: the tool_result content plus the
// provider's richer tool_use_result, both kept as raw JSON, and the tool
// duration a PostToolUse hook reported.
type resultPayload struct {
	Content       json.RawMessage `json:"content,omitempty"`
	ToolUseResult json.RawMessage `json:"toolUseResult,omitempty"`
	DurationMs    int64           `json:"durationMs,omitempty"`
}

// Parse reads Claude stream-json lines and returns normalized actions in the
// order their tool uses appear.
func Parse(r io.Reader) (ParseResult, error) {
	var res ParseResult
	index := make(map[string]int)
	messageSequence := 0
	// Tool durations reported by PostToolUse hooks, which may arrive either
	// side of the tool_result they describe.
	durations := make(map[string]int64)

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, initialLineBuffer), maxLineBytes)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev event
		if err := json.Unmarshal(line, &ev); err != nil {
			res.WarningCount++
			continue
		}
		switch ev.Type {
		case "assistant":
			for _, block := range ev.Message.Content {
				if block.Type == "text" && block.Text != "" {
					messageSequence++
					input, _ := json.Marshal(struct {
						Text string `json:"text"`
					}{Text: block.Text})
					res.Actions = append(res.Actions, action.Action{
						ID:        fmt.Sprintf("assistant-message-%d", messageSequence),
						ParentID:  ev.ParentToolUseID,
						Type:      action.TypeAgentMessage,
						Provider:  providerName,
						Assurance: action.AssuranceProviderReported,
						StartedAt: parseTime(ev.Timestamp),
						Status:    statusCompleted,
						Input:     input,
					})
					continue
				}
				if block.Type != "tool_use" {
					continue
				}
				// Some providers emit a tool_use without an id; it can never be
				// correlated with its result, so drop it as a warning.
				if block.ID == "" {
					res.WarningCount++
					continue
				}
				// A replayed tool_use must not append a second action nor
				// repoint the correlation: the first one wins.
				if _, dup := index[block.ID]; dup {
					res.WarningCount++
					continue
				}
				index[block.ID] = len(res.Actions)
				res.Actions = append(res.Actions, action.Action{
					ID:        block.ID,
					ParentID:  ev.ParentToolUseID,
					Type:      actionType(block.Name),
					Provider:  providerName,
					Assurance: action.AssuranceProviderReported,
					StartedAt: parseTime(ev.Timestamp),
					Status:    statusInProgress,
					Input:     block.Input,
				})
			}
		case "user":
			for _, block := range ev.Message.Content {
				if block.Type != "tool_result" {
					continue
				}
				i, ok := index[block.ToolUseID]
				if !ok {
					res.WarningCount++
					continue
				}
				// A replayed result must not overwrite an action that is
				// already closed out: the first result wins.
				if res.Actions[i].Status != statusInProgress {
					res.WarningCount++
					continue
				}
				res.Actions[i] = completeAction(res.Actions[i], block, ev, durations[block.ToolUseID])
			}
		case "system":
			// Hook activity carries no action of its own, but a PostToolUse
			// response reports how long the finished tool ran. PreToolUse
			// durations time the hook itself, not the tool, so they are left out.
			if ev.Subtype == hookResponseSubtype && ev.HookEvent == postToolUseHook {
				applyHookDuration(res.Actions, index, durations, ev.Output)
			}
		case "result", "rate_limit_event", "tool_progress":
			// Known event types that carry no action of their own.
		default:
			res.WarningCount++
		}
	}
	if err := sc.Err(); err != nil {
		return res, fmt.Errorf("read claude event stream: %w", err)
	}
	return res, nil
}

// completeAction returns a copy of act closed out by its tool_result, carrying
// the tool duration a PostToolUse hook already reported, if any.
func completeAction(act action.Action, block contentBlock, ev event, durationMs int64) action.Action {
	act.FinishedAt = parseTime(ev.Timestamp)
	act.Status = statusCompleted
	if block.IsError {
		act.Status = statusFailed
	}
	if payload, err := json.Marshal(resultPayload{
		Content:       block.Content,
		ToolUseResult: ev.ToolUseResult,
		DurationMs:    durationMs,
	}); err == nil {
		act.Result = payload
	}
	return act
}

// applyHookDuration records the tool duration a PostToolUse hook reported and
// stamps it onto the result of the action it names. The first positive duration
// wins: later hooks for the same tool_use report their own timing and must not
// overwrite it. A hook may also arrive before the tool_result, in which case the
// recorded duration is applied when that result creates the payload.
func applyHookDuration(actions []action.Action, index map[string]int, durations map[string]int64, output string) {
	var hook hookOutput
	if err := json.Unmarshal([]byte(output), &hook); err != nil || hook.DurationMs <= 0 {
		return
	}
	if _, seen := durations[hook.ToolUseID]; seen {
		return
	}
	durations[hook.ToolUseID] = hook.DurationMs

	i, ok := index[hook.ToolUseID]
	if !ok {
		return
	}
	var payload resultPayload
	if err := json.Unmarshal(actions[i].Result, &payload); err != nil || payload.DurationMs > 0 {
		return
	}
	payload.DurationMs = hook.DurationMs
	if enriched, err := json.Marshal(payload); err == nil {
		actions[i].Result = enriched
	}
}

// actionType maps a Claude tool name onto a normalized action type.
func actionType(name string) string {
	switch name {
	case "Read":
		return action.TypeFileRead
	case "Write":
		return action.TypeFileWrite
	case "Edit":
		return action.TypeFileEdit
	case "Bash":
		return action.TypeShellExec
	case "Glob", "Grep":
		return action.TypeSearch
	case "WebFetch":
		return action.TypeWebFetch
	case "Task", "Agent":
		return action.TypeSubagentSpawn
	}
	if strings.HasPrefix(name, mcpToolPrefix) {
		return action.TypeMCPCall
	}
	return action.TypeToolCall
}

// parseTime converts a provider timestamp, yielding the zero time when absent
// or unparseable.
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
