// Package provider holds what every agent CLI adapter needs to agree on: the
// shape of a prepared command, and how the recorded version is established.
package provider

import (
	"context"
	"errors"
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
	// VersionUnverified records that Version is outside the range this adapter's
	// parser was written against, and the caller asked for the run to be recorded
	// anyway. It travels with the command so the recorder stamps the bundle with
	// it: a run read by a parser that does not claim to understand the stream it
	// read must say so, whatever it then reports.
	VersionUnverified bool
}

// Options are the caller's decisions about how strictly a command is prepared.
// The zero value is the strict default: a version outside the supported range
// is refused rather than recorded.
type Options struct {
	// AllowUnsupportedVersion records a run against a provider version outside
	// the supported range instead of refusing it. It is the operator's explicit
	// override for a provider that has moved on faster than this parser: the
	// events may not be the shape the parser expects, so what such a run reports
	// about itself is worth less, and the bundle is stamped to say so.
	AllowUnsupportedVersion bool
}

// ErrUnsupportedVersion reports a version outside the range an adapter's parser
// was written against. It is the one version failure a caller may choose to
// proceed past — the executable answered, and it answered with a version — so
// it is distinguishable from a probe that could not be run or an output no
// version could be read from, neither of which leaves anything to record.
var ErrUnsupportedVersion = errors.New("provider: version is outside the supported range")

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
//
// A version that was read but falls outside spec is returned alongside an error
// wrapping ErrUnsupportedVersion: the refusal is the default, and a caller that
// has been told to record such a run anyway still needs the version to record.
// Every other failure returns no version, because there was none to read.
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
		return v.String(), fmt.Errorf("%w: %s: version %s is not supported; agentrec supports %s; upgrade, install a supported version, or record it anyway with --allow-unsupported-version", ErrUnsupportedVersion, executable, v, spec)
	}
	return v.String(), nil
}

// ResolveVersionFor applies opts to ResolveVersion, reporting the version to
// record and whether it was recorded unverified. It is the one place the
// override is interpreted, so both adapters treat it identically.
func ResolveVersionFor(ctx context.Context, executable string, probe VersionProbe, spec VersionSpec, opts Options) (version string, unverified bool, err error) {
	version, err = ResolveVersion(ctx, executable, probe, spec)
	switch {
	case err == nil:
		return version, false, nil
	case opts.AllowUnsupportedVersion && errors.Is(err, ErrUnsupportedVersion):
		return version, true, nil
	}
	return "", false, err
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
