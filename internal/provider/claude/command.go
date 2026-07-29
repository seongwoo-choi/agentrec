package claude

import (
	"context"
	"fmt"
	"strings"

	"github.com/seongwoo-choi/agentrec/internal/provider"
)

// executable is the only binary this package ever launches. Callers pass
// arguments only; the program name is not negotiable.
const executable = "claude"

// The output format agentrec's parser requires, and the flag that selects it.
const (
	outputFormatFlag = "--output-format"
	streamJSON       = "stream-json"
)

// Command is a ready-to-run Claude Code invocation. Args excludes Executable.
type Command = provider.Command

// VersionProbe reports the raw `--version` output of the named executable.
type VersionProbe = provider.VersionProbe

// Options are the caller's decisions about how strictly the command is
// prepared. The zero value refuses an unsupported version.
type Options = provider.Options

// supported is the Claude Code range agentrec's parser was written against:
// the stream-json events of 2.1.x. A major release may reshape them, so
// anything from 3.0.0 up is refused rather than recorded on the assumption it
// still fits.
var supported = provider.VersionSpec{
	Product: executable,
	Min:     provider.Version{Major: 2, Minor: 1},
	Max:     provider.Version{Major: 3},
}

// PrepareCommand validates caller arguments and returns the full Claude Code
// invocation agentrec should run. A nil probe runs `claude --version`. The
// caller's slice is never mutated.
func PrepareCommand(ctx context.Context, args []string, probe VersionProbe, opts Options) (Command, error) {
	options := optionArgs(args)
	if !hasFlag(options, "-p", "--print") {
		return Command{}, fmt.Errorf("claude: non-interactive mode requires -p or --print in the argument list")
	}

	// Injected options go first: a user prompt or a trailing "--" would
	// otherwise swallow them as positionals.
	streamJSONSet, err := checkOutputFormat(options)
	if err != nil {
		return Command{}, err
	}

	version, unverified, err := provider.ResolveVersionFor(ctx, executable, probe, supported, opts)
	if err != nil {
		return Command{}, err
	}

	var prepared []string
	if !streamJSONSet {
		prepared = append(prepared, outputFormatFlag, streamJSON)
	}
	if !hasFlag(options, "--verbose") {
		prepared = append(prepared, "--verbose")
	}
	if !hasFlag(options, "--include-hook-events") {
		prepared = append(prepared, "--include-hook-events")
	}
	prepared = append(prepared, args...)

	return Command{Executable: executable, Args: prepared, Version: version, VersionUnverified: unverified}, nil
}

// optionArgs returns the leading arguments Claude Code parses as options,
// stopping at a bare "--" so trailing positionals are never read as flags.
func optionArgs(args []string) []string {
	for i, arg := range args {
		if arg == "--" {
			return args[:i]
		}
	}
	return args
}

// checkOutputFormat reports whether the caller already asked for stream-json,
// in either "--output-format stream-json" or "--output-format=stream-json"
// form. Any other value is an error: silently overriding what the caller wrote
// would hide the fact that their requested format was ignored.
func checkOutputFormat(options []string) (bool, error) {
	found := false
	for i, arg := range options {
		var value string
		switch {
		case arg == outputFormatFlag:
			if i+1 >= len(options) {
				return false, fmt.Errorf("claude: %s has no value; agentrec requires %s %s", outputFormatFlag, outputFormatFlag, streamJSON)
			}
			value = options[i+1]
		case strings.HasPrefix(arg, outputFormatFlag+"="):
			value = strings.TrimPrefix(arg, outputFormatFlag+"=")
		default:
			continue
		}
		if value != streamJSON {
			return false, fmt.Errorf("claude: %s %q conflicts with agentrec, which can only record %s output", outputFormatFlag, value, streamJSON)
		}
		found = true
	}
	return found, nil
}

// hasFlag reports whether any argument matches one of names exactly.
func hasFlag(args []string, names ...string) bool {
	for _, arg := range args {
		for _, name := range names {
			if arg == name {
				return true
			}
		}
	}
	return false
}
