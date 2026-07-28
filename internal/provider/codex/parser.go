// Package codex parses Codex JSONL event streams into normalized actions.
package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/action"
)

// providerName labels every action recovered from a Codex event stream.
const providerName = "codex"

// Action lifecycle statuses reported by this parser.
const (
	statusInProgress = "in_progress"
	statusCompleted  = "completed"
	statusFailed     = "failed"
)

// Scanner line limits. A command that printed a lot puts all of its output on
// one event line, well past bufio.Scanner's 64 KiB default, so the cap is
// raised to a size that still bounds memory on a hostile stream.
const (
	initialLineBuffer = 64 << 10
	maxLineBuffer     = 4 << 20
)

// metadataEvents are stream events this parser recognizes but records nothing
// for, so they must not be mistaken for unknown shapes and warned about.
var metadataEvents = map[string]bool{
	"thread.started": true,
	"turn.started":   true,
	"turn.completed": true,
}

// ParseResult holds the actions recovered from a Codex event stream along with
// the number of events that could not be interpreted.
type ParseResult struct {
	Actions      []action.Action
	WarningCount int
}

// event is the subset of a Codex JSONL line the parser needs. Codex nests the
// item discriminator under "item.type"; the top-level "type" names the event.
type event struct {
	Type string `json:"type"`
	// Timestamp is absent on some Codex transports, so it stays optional.
	Timestamp string `json:"timestamp"`
	Item      item   `json:"item"`
}

// item is a Codex work item: the discriminator plus the fields each kind needs.
type item struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Tool string `json:"tool"`
	// Status is the provider's own view of the item's outcome.
	Status string `json:"status"`
	// Command execution fields. ExitCode is a pointer because 0 is the success
	// value and must be told apart from an absent code.
	Command          string `json:"command"`
	AggregatedOutput string `json:"aggregated_output"`
	ExitCode         *int   `json:"exit_code"`
	// Agent message and structured provider operation fields.
	Text      string          `json:"text"`
	Message   string          `json:"message"`
	Changes   []fileChange    `json:"changes"`
	Server    string          `json:"server"`
	Arguments json.RawMessage `json:"arguments"`
	Result    json.RawMessage `json:"result"`
	Error     json.RawMessage `json:"error"`
}

// fileChange is the bounded subset of a Codex file_change entry that is kept
// with the action. The enclosing JSONL line is capped by maxLineBuffer.
type fileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type fileChangeInput struct {
	Path    string       `json:"path,omitempty"`
	Changes []fileChange `json:"changes"`
}

type mcpCallInput struct {
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

type mcpCallResult struct {
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// commandInput is the normalized input body for a command execution.
type commandInput struct {
	Command string `json:"command"`
}

// commandResult is the normalized result body for a command execution, using
// stable camelCase keys independent of the provider's own spelling.
type commandResult struct {
	AggregatedOutput string `json:"aggregatedOutput"`
	ExitCode         *int   `json:"exitCode"`
}

// messageResult is the normalized result body for an agent message.
type messageResult struct {
	Text string `json:"text"`
}

type providerErrorInput struct {
	Message string `json:"message"`
}

// Parse reads Codex JSONL events and returns normalized actions in the order
// their items first appear.
func Parse(r io.Reader) (ParseResult, error) {
	var res ParseResult
	index := make(map[string]int)

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, initialLineBuffer), maxLineBuffer)
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
		switch {
		case (ev.Type == "item.started" || ev.Type == "item.completed") && ev.Item.Type == "collab_tool_call" && ev.Item.Tool == "wait":
			// Known collaboration bookkeeping that carries no action of its own.
		case (ev.Type == "item.started" || ev.Type == "item.updated" || ev.Type == "item.completed") && ev.Item.Type == "todo_list":
			// Provider planning state is stream metadata, not an executed action.
		case ev.Type == "item.started" && ev.Item.Type == "mcp_tool_call":
			// The completed item carries both the call input and provider result.
		case ev.Type == "item.completed" && ev.Item.Type == "mcp_tool_call":
			if !canClaimID(index, ev.Item.ID) {
				res.WarningCount++
				continue
			}
			input, err := json.Marshal(mcpCallInput{
				Server: ev.Item.Server, Tool: ev.Item.Tool, Arguments: ev.Item.Arguments,
			})
			if err != nil {
				continue
			}
			result, err := json.Marshal(mcpCallResult{Result: ev.Item.Result, Error: ev.Item.Error})
			if err != nil {
				continue
			}
			index[ev.Item.ID] = len(res.Actions)
			res.Actions = append(res.Actions, action.Action{
				ID:         ev.Item.ID,
				Type:       action.TypeMCPCall,
				Provider:   providerName,
				Assurance:  action.AssuranceProviderReported,
				FinishedAt: parseTime(ev.Timestamp),
				Status:     commandStatus(ev.Item),
				Input:      input,
				Result:     result,
			})
		case ev.Type == "item.started" && ev.Item.Type == "command_execution":
			if !canClaimID(index, ev.Item.ID) {
				res.WarningCount++
				continue
			}
			index[ev.Item.ID] = len(res.Actions)
			input, err := json.Marshal(commandInput{Command: ev.Item.Command})
			if err != nil {
				continue
			}
			res.Actions = append(res.Actions, action.Action{
				ID:        ev.Item.ID,
				Type:      action.TypeShellExec,
				Provider:  providerName,
				Assurance: action.AssuranceProviderReported,
				StartedAt: parseTime(ev.Timestamp),
				Status:    statusInProgress,
				Input:     input,
			})
		case ev.Type == "item.completed" && ev.Item.Type == "command_execution":
			i, ok := index[ev.Item.ID]
			if !ok {
				// A completion whose start never arrived: there is no input to
				// attach it to, so it is counted rather than half-invented.
				res.WarningCount++
				continue
			}
			if res.Actions[i].Status != statusInProgress {
				// The action is already closed out; a second completion would
				// overwrite a settled result, so the first one wins.
				res.WarningCount++
				continue
			}
			res.Actions[i] = completeCommand(res.Actions[i], ev)
		case ev.Type == "item.completed" && ev.Item.Type == "agent_message":
			// Codex reports a message only once, already finished, so it never
			// passes through an in-progress state.
			if !canClaimID(index, ev.Item.ID) {
				res.WarningCount++
				continue
			}
			index[ev.Item.ID] = len(res.Actions)
			result, err := json.Marshal(messageResult{Text: ev.Item.Text})
			if err != nil {
				continue
			}
			res.Actions = append(res.Actions, action.Action{
				ID:         ev.Item.ID,
				Type:       action.TypeAgentMessage,
				Provider:   providerName,
				Assurance:  action.AssuranceProviderReported,
				FinishedAt: parseTime(ev.Timestamp),
				Status:     statusCompleted,
				Result:     result,
			})
		case ev.Type == "item.completed" && ev.Item.Type == "error":
			if !canClaimID(index, ev.Item.ID) {
				res.WarningCount++
				continue
			}
			index[ev.Item.ID] = len(res.Actions)
			input, err := json.Marshal(providerErrorInput{Message: ev.Item.Message})
			if err != nil {
				continue
			}
			res.Actions = append(res.Actions, action.Action{
				ID:         ev.Item.ID,
				Type:       action.TypeProviderError,
				Provider:   providerName,
				Assurance:  action.AssuranceProviderReported,
				FinishedAt: parseTime(ev.Timestamp),
				Status:     statusFailed,
				Input:      input,
			})
		case ev.Type == "item.started" && ev.Item.Type == "file_change":
			// The completed item carries the one normalized file-edit action.
		case ev.Type == "item.completed" && ev.Item.Type == "file_change":
			if !canClaimID(index, ev.Item.ID) {
				res.WarningCount++
				continue
			}
			input := fileChangeInput{Changes: ev.Item.Changes}
			if len(input.Changes) == 1 {
				input.Path = input.Changes[0].Path
			}
			payload, err := json.Marshal(input)
			if err != nil {
				continue
			}
			index[ev.Item.ID] = len(res.Actions)
			res.Actions = append(res.Actions, action.Action{
				ID:         ev.Item.ID,
				Type:       action.TypeFileEdit,
				Provider:   providerName,
				Assurance:  action.AssuranceProviderReported,
				FinishedAt: parseTime(ev.Timestamp),
				Status:     commandStatus(ev.Item),
				Input:      payload,
			})
		case metadataEvents[ev.Type]:
			// Known stream bookkeeping that carries no action of its own.
		default:
			// An event or item kind this parser does not model is counted and
			// skipped so that new Codex shapes do not break a usable stream.
			res.WarningCount++
		}
	}
	if err := sc.Err(); err != nil {
		return res, fmt.Errorf("read codex event stream: %w", err)
	}
	return res, nil
}

// canClaimID reports whether id can identify a new action: an empty ID cannot
// be correlated to anything, and reusing one already taken would either
// duplicate an action or orphan the original, so in both cases the first item
// holding the ID wins.
func canClaimID(index map[string]int, id string) bool {
	if id == "" {
		return false
	}
	_, taken := index[id]
	return !taken
}

// completeCommand returns a copy of act closed out by the item.completed event
// that reports on it.
func completeCommand(act action.Action, ev event) action.Action {
	act.FinishedAt = parseTime(ev.Timestamp)
	act.Status = commandStatus(ev.Item)
	if payload, err := json.Marshal(commandResult{
		AggregatedOutput: ev.Item.AggregatedOutput,
		ExitCode:         ev.Item.ExitCode,
	}); err == nil {
		act.Result = payload
	}
	return act
}

// providerFailureStatuses are the item statuses by which Codex declares a
// command failed outright, which it can do without ever producing an exit code.
var providerFailureStatuses = map[string]bool{
	"failed":      true,
	"error":       true,
	"cancelled":   true,
	"canceled":    true,
	"timeout":     true,
	"timed_out":   true,
	"interrupted": true,
}

// commandStatus decides the outcome of a finished command. Codex reports the
// item as completed whenever the process ran, so a nonzero exit code is what
// distinguishes a failure from a success; a command that never got that far is
// failed by the provider's own status instead. A status Codex hasn't declared
// a failure for is trusted only alongside a zero exit code proving the process
// actually finished cleanly; without that proof it's treated as failed rather
// than assumed complete.
func commandStatus(it item) string {
	if it.ExitCode != nil && *it.ExitCode != 0 {
		return statusFailed
	}
	if providerFailureStatuses[it.Status] {
		return statusFailed
	}
	if it.Status == "completed" || it.ExitCode != nil {
		return statusCompleted
	}
	return statusFailed
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
