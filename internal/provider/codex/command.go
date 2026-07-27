package codex

import (
	"context"
	"fmt"

	"github.com/seongwoo-choi/agentrec/internal/provider"
)

// executable is the only binary this package ever launches. Callers pass
// arguments only; the program name is not negotiable.
const executable = "codex"

// execMode is the Codex subcommand that runs non-interactively. agentrec can
// only record that mode, and it must be the first argument.
const execMode = "exec"

// jsonFlag selects the JSONL event stream agentrec's parser consumes.
const jsonFlag = "--json"

// Command is a ready-to-run Codex invocation. Args excludes Executable.
type Command = provider.Command

// VersionProbe reports the raw `--version` output of the named executable.
type VersionProbe = provider.VersionProbe

// supported is the Codex range agentrec's parser was written against: the exec
// JSONL events of 0.144.0. A major release may reshape them, so anything from
// 1.0.0 up is refused rather than recorded on the assumption it still fits.
var supported = provider.VersionSpec{
	Product: executable,
	Min:     provider.Version{Minor: 144},
	Max:     provider.Version{Major: 1},
}

// PrepareCommand validates caller arguments and returns the full Codex
// invocation agentrec should run. A nil probe runs `codex --version`. The
// caller's slice is never mutated.
func PrepareCommand(ctx context.Context, args []string, probe VersionProbe) (Command, error) {
	if len(args) == 0 || args[0] != execMode {
		return Command{}, fmt.Errorf("codex: agentrec can only record non-interactive runs; %s must be the first argument", execMode)
	}

	version, err := provider.ResolveVersion(ctx, executable, probe, supported)
	if err != nil {
		return Command{}, err
	}

	// The injected flag goes immediately after "exec": Codex reads the prompt
	// as a positional, so anything appended later would be swallowed by it.
	prepared := make([]string, 0, len(args)+1)
	prepared = append(prepared, execMode)
	if !hasFlag(optionArgs(args[1:]), jsonFlag) {
		prepared = append(prepared, jsonFlag)
	}
	prepared = append(prepared, args[1:]...)

	return Command{Executable: executable, Args: prepared, Version: version}, nil
}

// optionArgs returns the leading arguments Codex parses as options, stopping
// at a bare "--" so trailing positionals are never read as flags.
func optionArgs(args []string) []string {
	for i, arg := range args {
		if arg == "--" {
			return args[:i]
		}
	}
	return args
}

// hasFlag reports whether any argument matches name exactly.
func hasFlag(args []string, name string) bool {
	for _, arg := range args {
		if arg == name {
			return true
		}
	}
	return false
}
