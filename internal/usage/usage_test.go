package usage

import (
	"math"
	"testing"
)

func TestValidateAcceptsBoundedProviderReportedUsage(t *testing.T) {
	input, cached, output := int64(1200), int64(9000), int64(340)
	cost := 0.0421
	report := Report{
		Schema: 1, Attribution: AttributionProviderReported,
		Provider: "claude", Scope: ScopeRun,
		InputTokens: &input, CachedInputTokens: &cached,
		OutputTokens: &output, CostUSD: &cost,
	}
	if err := report.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateRejectsUnboundedOrAmbiguousUsage(t *testing.T) {
	negative := int64(-1)
	tooMany := MaxTokens + 1
	nan := math.NaN()
	tests := []struct {
		name   string
		report Report
	}{
		{"wrong schema", Report{Schema: 2, Attribution: AttributionProviderReported, Provider: "claude", Scope: ScopeRun}},
		{"wrong attribution", Report{Schema: 1, Attribution: "observed", Provider: "claude", Scope: ScopeRun}},
		{"missing provider", Report{Schema: 1, Attribution: AttributionProviderReported, Scope: ScopeRun}},
		{"unknown scope vocabulary", Report{Schema: 1, Attribution: AttributionProviderReported, Provider: "claude", Scope: "turn"}},
		{"no measurement", Report{Schema: 1, Attribution: AttributionProviderReported, Provider: "claude", Scope: ScopeRun}},
		{"negative tokens", Report{Schema: 1, Attribution: AttributionProviderReported, Provider: "claude", Scope: ScopeRun, InputTokens: &negative}},
		{"too many tokens", Report{Schema: 1, Attribution: AttributionProviderReported, Provider: "claude", Scope: ScopeRun, OutputTokens: &tooMany}},
		{"non-finite cost", Report{Schema: 1, Attribution: AttributionProviderReported, Provider: "claude", Scope: ScopeRun, CostUSD: &nan}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.report.Validate(); err == nil {
				t.Fatal("Validate succeeded, want rejection")
			}
		})
	}
}
