package usage

import (
	"strings"
	"testing"
)

func TestValidateSourceAndModel(t *testing.T) {
	one := int64(1)
	base := Report{Schema: 1, Attribution: AttributionProviderReported, Provider: "claude", Scope: ScopeSession, InputTokens: &one}
	for _, source := range []string{"", SourceStream, SourceTranscript} {
		r := base
		r.Source = source
		if err := r.Validate(); err != nil {
			t.Errorf("source %q rejected: %v", source, err)
		}
	}
	r := base
	r.Source = "hearsay"
	if err := r.Validate(); err == nil {
		t.Error("an unknown source was accepted")
	}
	r = base
	r.Model = strings.Repeat("m", MaxModelBytes)
	if err := r.Validate(); err != nil {
		t.Errorf("a model name at the limit was rejected: %v", err)
	}
	r.Model += "m"
	if err := r.Validate(); err == nil {
		t.Error("a model name past the limit was accepted")
	}
}
