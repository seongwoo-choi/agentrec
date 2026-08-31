package storage

import (
	"strings"
	"testing"
)

func TestValidateProviderEventAcceptsExactNestingLimit(t *testing.T) {
	raw := []byte(strings.Repeat(`{"value":`, MaxProviderEventDepth) + `0` + strings.Repeat(`}`, MaxProviderEventDepth))

	if _, err := ValidateProviderEvent(raw, MaxProviderEventTokens); err != nil {
		t.Fatalf("ValidateProviderEvent at exact nesting limit: %v", err)
	}
}

func TestValidateProviderEventAcceptsExactTokenBudget(t *testing.T) {
	values := strings.TrimSuffix(strings.Repeat("0,", MaxProviderEventTokens-5), ",")
	raw := []byte(`{"values":[` + values + `]}`)

	tokens, err := ValidateProviderEvent(raw, MaxProviderEventTokens)
	if err != nil {
		t.Fatalf("ValidateProviderEvent at exact token limit: %v", err)
	}
	if tokens != MaxProviderEventTokens {
		t.Fatalf("tokens = %d, want %d", tokens, MaxProviderEventTokens)
	}
	if _, err := ValidateProviderEvent(raw, MaxProviderEventTokens-1); err == nil {
		t.Fatal("ValidateProviderEvent accepted one token beyond remaining budget")
	}
}
