package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadProviderUsageTreatsAnAbsentArtifactAsNoUsage(t *testing.T) {
	got, err := readProviderUsage(t.TempDir(), "claude")
	if err != nil {
		t.Fatalf("readProviderUsage absent artifact: %v", err)
	}
	if got != nil {
		t.Fatalf("readProviderUsage absent artifact = %+v, want nil", got)
	}
}

func TestReadProviderUsageValidatesTheArtifactBeforeReturningIt(t *testing.T) {
	valid := `{"schema":1,"attribution":"provider_reported","provider":"claude","scope":"run","inputTokens":12,"costUSD":0.25}`
	tests := []struct {
		name     string
		provider string
		install  func(*testing.T, string)
		wantErr  bool
	}{
		{name: "valid", provider: "claude", install: writeUsageBytes(valid)},
		{name: "malformed JSON", provider: "claude", install: writeUsageBytes(`{"schema":`), wantErr: true},
		{name: "unsupported schema", provider: "claude", install: writeUsageBytes(strings.Replace(valid, `"schema":1`, `"schema":2`, 1)), wantErr: true},
		{name: "manifest provider mismatch", provider: "codex", install: writeUsageBytes(valid), wantErr: true},
		{name: "symlink", provider: "claude", install: func(t *testing.T, dir string) {
			outside := filepath.Join(t.TempDir(), providerUsageFile)
			if err := os.WriteFile(outside, []byte(valid), 0o600); err != nil {
				t.Fatalf("write outside usage: %v", err)
			}
			if err := os.Symlink(outside, filepath.Join(dir, providerUsageFile)); err != nil {
				t.Fatalf("symlink usage: %v", err)
			}
		}, wantErr: true},
		{name: "non-regular", provider: "claude", install: func(t *testing.T, dir string) {
			if err := os.Mkdir(filepath.Join(dir, providerUsageFile), 0o700); err != nil {
				t.Fatalf("mkdir usage: %v", err)
			}
		}, wantErr: true},
		{name: "oversize", provider: "claude", install: writeUsageBytes(strings.Repeat(" ", maxDocumentBytes+1)), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.install(t, dir)

			got, err := readProviderUsage(dir, tt.provider)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("readProviderUsage() = %+v, nil; want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("readProviderUsage(): %v", err)
			}
			if got == nil || got.Provider != "claude" || got.Scope != "run" || got.InputTokens == nil || *got.InputTokens != 12 {
				t.Fatalf("readProviderUsage() = %+v, want validated Claude run usage", got)
			}
		})
	}
}

func writeUsageBytes(raw string) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, providerUsageFile), []byte(raw), 0o600); err != nil {
			t.Fatalf("write usage: %v", err)
		}
	}
}

func TestShowRendersProviderReportedUsageWithoutDerivingMetrics(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")
	writeUsageBytes(`{"schema":1,"attribution":"provider_reported","provider":"claude","scope":"run","inputTokens":1200,"cachedInputTokens":9000,"cacheCreationInputTokens":80,"outputTokens":340,"costUSD":0.0421}`)(t, filepath.Join(root, "run-a"))

	code, stdout, stderr := run(t, "show", "run-a")
	if code != 0 {
		t.Fatalf("show exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	for _, want := range []string{
		"PROVIDER-REPORTED USAGE",
		"Attribution  provider_reported",
		"Provider     claude",
		"Scope        run",
		"Input Tokens 1200",
		"Cached Input Tokens 9000",
		"Cache Creation Input Tokens 80",
		"Output Tokens 340",
		"Cost USD     0.0421",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("show output =\n%s\nwant %q", stdout, want)
		}
	}
	for _, forbidden := range []string{"Total Tokens", "Score", "score"} {
		if strings.Contains(stdout, forbidden) {
			t.Errorf("show output =\n%s\nwant no derived %q", stdout, forbidden)
		}
	}
	if got := strings.Count(stdout, "10:00:01  READ  README.md"); got != 1 {
		t.Errorf("rendered action count = %d, want usage not to add an action", got)
	}
}

func TestShadowComparisonKeepsEachProvidersUsageSeparate(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")
	writeRun(t, root, "run-b", "codex", early, "completed")
	writeUsageBytes(`{"schema":1,"attribution":"provider_reported","provider":"claude","scope":"run","inputTokens":100,"outputTokens":30,"costUSD":0.04}`)(t, filepath.Join(root, "run-a"))
	writeUsageBytes(`{"schema":1,"attribution":"provider_reported","provider":"codex","scope":"session","inputTokens":250,"cachedInputTokens":40,"outputTokens":70}`)(t, filepath.Join(root, "run-b"))

	var out strings.Builder
	err := renderComparison(&out, root, []leg{
		{runner: "claude", runID: "run-a", order: 1},
		{runner: "codex", runID: "run-b", order: 2},
	})
	if err != nil {
		t.Fatalf("renderComparison: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"Usage Attribution provider_reported",
		"Usage Provider claude",
		"Usage Scope  run",
		"Usage Provider codex",
		"Usage Scope  session",
		"Input Tokens 100",
		"Input Tokens 250",
		"Cached Input Tokens 40",
		"Cost USD     0.04",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("comparison =\n%s\nwant %q", got, want)
		}
	}
	if strings.Count(got, "Recorded Actions 1") != 2 {
		t.Errorf("comparison =\n%s\nwant usage not to change either action count", got)
	}
	for _, forbidden := range []string{"Total Tokens", "Combined", "Equivalent", "Score", "score"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("comparison =\n%s\nwant no cross-provider or derived field %q", got, forbidden)
		}
	}
}
