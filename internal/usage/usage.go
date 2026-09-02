// Package usage defines the small, provider-reported resource summary kept
// separately from normalized actions and observed evidence.
package usage

import (
	"fmt"
	"math"
)

const (
	AttributionProviderReported = "provider_reported"
	// SourceStream is usage the provider reported in its event stream as the
	// run ended; SourceTranscript is usage read from the provider's own
	// transcript file at session end, whose format is the provider's and
	// undocumented. Both are the provider's word; the report says which.
	SourceStream     = "stream"
	SourceTranscript = "transcript"
	MaxModelBytes    = 256
	ScopeRun         = "run"
	ScopeSession     = "session"
	ScopeUnknown     = "unknown"

	MaxTokens  int64   = 1_000_000_000_000_000
	MaxCostUSD float64 = 1_000_000_000
)

// Report is the canonical provider-reported usage artifact. Pointer fields
// preserve the difference between a reported zero and a value not reported.
type Report struct {
	Schema                   int      `json:"schema"`
	Attribution              string   `json:"attribution"`
	Provider                 string   `json:"provider"`
	Scope                    string   `json:"scope"`
	InputTokens              *int64   `json:"inputTokens,omitempty"`
	CachedInputTokens        *int64   `json:"cachedInputTokens,omitempty"`
	CacheCreationInputTokens *int64   `json:"cacheCreationInputTokens,omitempty"`
	OutputTokens             *int64   `json:"outputTokens,omitempty"`
	CostUSD                  *float64 `json:"costUSD,omitempty"`
	Source                   string   `json:"source,omitempty"`
	Model                    string   `json:"model,omitempty"`
}

// Validate rejects values outside the deliberately small schema and arithmetic
// bounds before they can be persisted or rendered.
func (r Report) Validate() error {
	if r.Schema != 1 {
		return fmt.Errorf("usage: unsupported schema %d", r.Schema)
	}
	if r.Attribution != AttributionProviderReported {
		return fmt.Errorf("usage: attribution is %q", r.Attribution)
	}
	if r.Provider != "claude" && r.Provider != "codex" {
		return fmt.Errorf("usage: unsupported provider %q", r.Provider)
	}
	if r.Scope != ScopeRun && r.Scope != ScopeSession && r.Scope != ScopeUnknown {
		return fmt.Errorf("usage: unsupported scope %q", r.Scope)
	}
	if r.Source != "" && r.Source != SourceStream && r.Source != SourceTranscript {
		return fmt.Errorf("usage: unsupported source %q", r.Source)
	}
	if len(r.Model) > MaxModelBytes {
		return fmt.Errorf("usage: model name of %d bytes is longer than %d", len(r.Model), MaxModelBytes)
	}
	values := []*int64{r.InputTokens, r.CachedInputTokens, r.CacheCreationInputTokens, r.OutputTokens}
	hasValue := r.CostUSD != nil
	for _, value := range values {
		if value == nil {
			continue
		}
		hasValue = true
		if *value < 0 || *value > MaxTokens {
			return fmt.Errorf("usage: token count %d outside recorded range", *value)
		}
	}
	if r.CostUSD != nil && (math.IsNaN(*r.CostUSD) || math.IsInf(*r.CostUSD, 0) || *r.CostUSD < 0 || *r.CostUSD > MaxCostUSD) {
		return fmt.Errorf("usage: cost outside recorded range")
	}
	if !hasValue {
		return fmt.Errorf("usage: no measurement reported")
	}
	return nil
}
