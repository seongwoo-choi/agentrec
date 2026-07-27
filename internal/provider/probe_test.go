package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCLI writes an executable shell script that stands in for a provider
// binary, so the default probe can be exercised without a real CLI installed.
func fakeCLI(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-cli")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	return path
}

func TestResolveVersionRunsTheExecutableWhenProbeIsNil(t *testing.T) {
	cli := fakeCLI(t, `[ "$1" = "--version" ] && echo "claude 2.1.5"`)

	got, err := ResolveVersion(context.Background(), cli, nil, testSpec)
	if err != nil {
		t.Fatalf("ResolveVersion error: %v", err)
	}
	if got != "2.1.5" {
		t.Errorf("ResolveVersion = %q, want %q", got, "2.1.5")
	}
}

func TestDefaultVersionProbeKeepsFailureDetail(t *testing.T) {
	// Whatever the binary complained about on either stream is the only clue
	// the user has; dropping it leaves them with a bare "exit status 1".
	for _, tc := range []struct{ name, script, want string }{
		{"stderr", `echo "dyld: library not loaded" >&2; exit 1`, "dyld: library not loaded"},
		{"stdout", `echo "not logged in"; exit 3`, "not logged in"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := defaultVersionProbe(context.Background(), fakeCLI(t, tc.script))
			if err == nil {
				t.Fatal("defaultVersionProbe() = nil error, want failure")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not carry the failure detail %q", err, tc.want)
			}
		})
	}
}

func TestDefaultVersionProbeBoundsFailureDetail(t *testing.T) {
	// A binary that dumps a stack trace must not turn one bad run into an
	// unreadable wall of text.
	_, err := defaultVersionProbe(context.Background(), fakeCLI(t, `awk 'BEGIN{for(i=0;i<400;i++)print "noise line"}' >&2; exit 1`))
	if err == nil {
		t.Fatal("defaultVersionProbe() = nil error, want failure")
	}
	if len(err.Error()) > 2*maxProbeDetail {
		t.Errorf("error is %d bytes, want detail capped near %d", len(err.Error()), maxProbeDetail)
	}
	if !strings.Contains(err.Error(), "noise line") {
		t.Errorf("error %q dropped the failure detail entirely", err)
	}
}

func TestDefaultVersionProbeReportsContextCancellation(t *testing.T) {
	// A killed probe reports "signal: killed", which says nothing about why.
	// Callers time these runs out, so they must be able to recognize their own
	// deadline with errors.Is.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := defaultVersionProbe(ctx, fakeCLI(t, `sleep 10`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("defaultVersionProbe() error = %v, want one that Is context.DeadlineExceeded", err)
	}
}
