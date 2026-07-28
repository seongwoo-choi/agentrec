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

// Assurance levels identify each evidence source without implying an ordering.
const (
	AssuranceProviderReported     Assurance = "provider_reported"
	AssuranceSupervisorObserved   Assurance = "supervisor_observed"
	AssuranceRepositoryObserved   Assurance = "repository_observed"
	AssuranceVerificationObserved Assurance = "verification_observed"
)

// Action types observed across providers. Type stays a plain string so
// providers may emit types not listed here.
const (
	TypeAgentMessage  = "agent.message"
	TypeFileRead      = "file.read"
	TypeFileWrite     = "file.write"
	TypeFileEdit      = "file.edit"
	TypeShellExec     = "shell.exec"
	TypeSearch        = "search"
	TypeWebFetch      = "web.fetch"
	TypeMCPCall       = "mcp.call"
	TypeSubagentSpawn = "subagent.spawn"
	TypeToolCall      = "tool.call"
	TypeRunResult     = "run.result"
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
}
