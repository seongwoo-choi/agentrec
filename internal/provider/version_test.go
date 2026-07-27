package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// testSpec mirrors a real provider range closely enough to exercise extraction
// without tying these tests to either adapter's numbers.
var testSpec = VersionSpec{
	Product: "claude",
	Min:     Version{2, 1, 0},
	Max:     Version{3, 0, 0},
}

// stubProbe reports fixed output so no provider CLI is ever launched.
func stubProbe(output string) VersionProbe {
	return func(context.Context, string) (string, error) { return output, nil }
}

func TestResolveVersionReadsTheProductVersion(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"product name suffix", "2.1.220 (Claude Code)\n", "2.1.220"},
		{"product name prefix", "claude 2.1.220", "2.1.220"},
		{"bare version", "2.1.0", "2.1.0"},
		{"leading v", "v2.1.0", "2.1.0"},
		{"prerelease suffix", "2.2.0-beta.3 (Claude Code)", "2.2.0"},
		{"zero-padded components", "2.01.007 (Claude Code)", "2.1.7"},
		{"runtime version later on the line", "claude 2.1.5 (rust 1.90.0)", "2.1.5"},
		{"update banner first", "Update available: 3.0.1\nclaude 2.1.5", "2.1.5"},
		{"update banner and indented product line", "  Update available: 3.0.1\n  claude 2.1.5\n", "2.1.5"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveVersion(context.Background(), "claude", stubProbe(tc.raw), testSpec)
			if err != nil {
				t.Fatalf("ResolveVersion(%q) error: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("ResolveVersion(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestResolveVersionRejectsVersionsOutsideTheSupportedRange(t *testing.T) {
	// Min is inclusive and Max exclusive: a major bump may reshape the event
	// stream, so agentrec refuses rather than record events it cannot prove it
	// understood.
	for _, tc := range []struct{ raw, version string }{
		{"claude 2.0.44", "2.0.44"},
		{"claude 1.9.9", "1.9.9"},
		{"claude 0.0.1", "0.0.1"},
		{"claude 3.0.0", "3.0.0"},
		{"claude 3.1.0-beta.1", "3.1.0"},
		{"claude 11.0.0", "11.0.0"},
	} {
		_, err := ResolveVersion(context.Background(), "claude", stubProbe(tc.raw), testSpec)
		if err == nil {
			t.Fatalf("ResolveVersion(%q) = nil error, want out-of-range error", tc.raw)
		}
		assertActionable(t, err, tc.version)
	}
}

func TestResolveVersionAcceptsBothEndsOfTheSupportedRange(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"claude 2.1.0", "2.1.0"},
		{"claude 2.99.99", "2.99.99"},
	} {
		got, err := ResolveVersion(context.Background(), "claude", stubProbe(tc.raw), testSpec)
		if err != nil {
			t.Fatalf("ResolveVersion(%q) error: %v", tc.raw, err)
		}
		if got != tc.want {
			t.Errorf("ResolveVersion(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestResolveVersionRejectsUnreadableVersionsActionably(t *testing.T) {
	for _, raw := range []string{
		"Claude Code",
		"claude 2.1",
		"claude v.x.y",
		"claude 99999999999999999999.1.0", // overflows an int
		"",
	} {
		_, err := ResolveVersion(context.Background(), "claude", stubProbe(raw), testSpec)
		if err == nil {
			t.Fatalf("ResolveVersion(%q) = nil error, want unreadable-version error", raw)
		}
		assertActionable(t, err, "")
	}
}

func TestResolveVersionReportsProbeFailuresActionably(t *testing.T) {
	notInstalled := errors.New(`exec: "claude": executable file not found in $PATH`)
	probe := func(context.Context, string) (string, error) { return "", notInstalled }

	_, err := ResolveVersion(context.Background(), "claude", probe, testSpec)
	if err == nil {
		t.Fatal("ResolveVersion() = nil error, want probe failure")
	}
	if !errors.Is(err, notInstalled) {
		t.Errorf("ResolveVersion() error %v does not wrap the probe failure", err)
	}
	assertActionable(t, err, "")
}

// assertActionable checks that an error tells the user which binary was
// rejected, what it reported, and the whole range that would have worked.
func assertActionable(t *testing.T, err error, version string) {
	t.Helper()
	want := []string{"claude", "2.1.0", "3.0.0"}
	if version != "" {
		want = append(want, version)
	}
	for _, s := range want {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q does not mention %q", err, s)
		}
	}
}

func TestResolveVersionIgnoresNonVersionLinesAndDigitRuns(t *testing.T) {
	// A build date or a four-part build number is not a version, and a banner
	// line is about some other release than the one that is installed.
	for _, raw := range []string{
		"Update available: 3.0.1",
		"built 2026.07.28 by ci",
		"claude 2026.07.28.1",
		"claude 2.1.220.4",
		"Claude Code",
		"",
	} {
		if got, err := ResolveVersion(context.Background(), "claude", stubProbe(raw), testSpec); err == nil {
			t.Errorf("ResolveVersion(%q) = %q, nil error; want unreadable-version error", raw, got)
		}
	}
}
