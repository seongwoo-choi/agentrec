// Package provider holds what every agent CLI adapter needs to agree on: the
// shape of a prepared command, and how the recorded version is established.
package provider

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Command is a ready-to-run agent CLI invocation. Args excludes Executable.
type Command struct {
	Executable string
	Args       []string
	Version    string
}

// VersionProbe reports the raw `--version` output of the named executable.
type VersionProbe func(ctx context.Context, executable string) (string, error)

// Version is a semantic major.minor.patch triple.
type Version struct {
	Major, Minor, Patch int
}

// VersionSpec is the range of an agent CLI whose events agentrec can read.
// Max is exclusive.
type VersionSpec struct {
	Product  string
	Min, Max Version
}

// ResolveVersion reports the normalized version of the executable. A nil probe
// runs `<executable> --version`.
func ResolveVersion(ctx context.Context, executable string, probe VersionProbe, spec VersionSpec) (string, error) {
	if probe == nil {
		probe = defaultVersionProbe
	}
	raw, err := probe(ctx, executable)
	if err != nil {
		return "", fmt.Errorf("%s: could not run %s --version: %w; agentrec supports %s", spec.Product, executable, err, spec)
	}
	v, ok := extractVersion(raw, spec.Product)
	if !ok {
		return "", fmt.Errorf("%s: could not read a version from %q; agentrec supports %s", spec.Product, strings.TrimSpace(raw), spec)
	}
	if v.before(spec.Min) || !v.before(spec.Max) {
		return "", fmt.Errorf("%s: version %s is not supported; agentrec supports %s; upgrade or install a supported version", executable, v, spec)
	}
	return v.String(), nil
}

// before reports whether v precedes want.
func (v Version) before(want Version) bool {
	if v.Major != want.Major {
		return v.Major < want.Major
	}
	if v.Minor != want.Minor {
		return v.Minor < want.Minor
	}
	return v.Patch < want.Patch
}

// maxProbeDetail bounds how much of a failed probe's output an error carries:
// enough to see what the binary complained about, not enough to bury the
// message it is attached to.
const maxProbeDetail = 1024

// defaultVersionProbe runs `<executable> --version` so production callers need
// no wiring beyond a nil probe.
func defaultVersionProbe(ctx context.Context, name string) (string, error) {
	// Combined: a CLI that refuses to report its version may explain itself on
	// either stream, and that explanation is what the user needs to act on.
	out, err := exec.CommandContext(ctx, name, "--version").CombinedOutput()
	switch {
	case err == nil:
		return string(out), nil
	case ctx.Err() != nil:
		// The process was killed for our own deadline; "signal: killed" would
		// hide that from callers matching on their context error.
		return "", ctx.Err()
	}
	detail := strings.TrimSpace(string(out))
	if detail == "" {
		return "", err
	}
	if len(detail) > maxProbeDetail {
		detail = strings.ToValidUTF8(detail[:maxProbeDetail], "") + "..."
	}
	return "", fmt.Errorf("%w: %s", err, detail)
}

// versionPattern matches one major.minor.patch triple. The surrounding bounds
// keep longer digit runs — build dates, four-part build numbers — from being
// read as a version by matching only part of them.
var versionPattern = regexp.MustCompile(`(?:^|[^0-9.])([0-9]+)\.([0-9]+)\.([0-9]+)(?:[^0-9.]|$)`)

// extractVersion reports the first triple on the first line that announces the
// installed product. Lines that begin with anything else — an "Update
// available" banner above the real output, a build date — are not about the
// binary agentrec is running and are skipped.
func extractVersion(raw, product string) (Version, bool) {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !announcesVersion(line, product) {
			continue
		}
		match := versionPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		if v, ok := parseTriple(match[1:]); ok {
			return v, true
		}
	}
	return Version{}, false
}

// announcesVersion reports whether line looks like the product's own version
// line: it names the product, or it is the version itself.
func announcesVersion(line, product string) bool {
	if strings.HasPrefix(strings.ToLower(line), strings.ToLower(product)) {
		return true
	}
	line = strings.TrimPrefix(line, "v")
	return line != "" && line[0] >= '0' && line[0] <= '9'
}

// parseTriple converts matched digit runs into a Version, rejecting components
// too large to be a real version number.
func parseTriple(parts []string) (Version, bool) {
	var nums [3]int
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return Version{}, false
		}
		nums[i] = n
	}
	return Version{nums[0], nums[1], nums[2]}, true
}

// String renders a version without zero padding.
func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// String renders the range as callers should state it to a user.
func (s VersionSpec) String() string {
	return fmt.Sprintf("%s >=%s,<%s", s.Product, s.Min, s.Max)
}
