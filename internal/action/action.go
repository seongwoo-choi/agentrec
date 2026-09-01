// Package action defines the provider-neutral normalized action record that
// agentrec persists, and the streaming writer that serializes it as JSONL.
package action

import (
	"encoding/json"
	"time"
)

// Assurance records how strongly an action's contents are attested: whether it
// was merely reported by the provider or independently observed.
type Assurance string

// Assurance levels identify the source that recorded normalized actions.
const AssuranceProviderReported Assurance = "provider_reported"

// Action types observed across providers. Type stays a plain string so
// providers may emit types not listed here.
const (
	TypeAgentMessage  = "agent.message"
	TypeUserPrompt    = "user.prompt"
	TypeFileRead      = "file.read"
	TypeFileWrite     = "file.write"
	TypeFileEdit      = "file.edit"
	TypeShellExec     = "shell.exec"
	TypeSearch        = "search"
	TypeWebFetch      = "web.fetch"
	TypeMCPCall       = "mcp.call"
	TypeSubagentSpawn = "subagent.spawn"
	TypeToolCall      = "tool.call"
	TypeProviderError = "provider.error"
)

// Action is one normalized step in a recorded run. Input and Result hold
// provider-specific payloads as raw JSON values.
type Action struct {
	ID         string          `json:"id"`
	ParentID   string          `json:"parentId,omitempty"`
	Type       string          `json:"type"`
	Provider   string          `json:"provider,omitempty"`
	Assurance  Assurance       `json:"assurance"`
	StartedAt  time.Time       `json:"startedAt,omitempty,omitzero"`
	FinishedAt time.Time       `json:"finishedAt,omitempty,omitzero"`
	Status     string          `json:"status,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	// RepositoryPaths are recorder-derived repository-relative projections of
	// explicit file action inputs. They report a path observation, not causality.
	RepositoryPaths         []string `json:"repositoryPaths,omitempty"`
	RepositoryPathsRecorded bool     `json:"repositoryPathsRecorded,omitempty"`
}
