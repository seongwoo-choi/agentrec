package cli

import (
	"fmt"
	"io"
)

// Build metadata. A source build leaves the development fallback in place; a
// release build overwrites all three with
// `go build -ldflags "-X github.com/seongwoo-choi/agentrec/internal/cli.version=..."`,
// which requires each variable to stay a plain string initialized to a constant.
var (
	version = "dev"
	commit  = unknownValue
	built   = unknownValue
)

const versionUsage = "usage: agentrec version\n"

// runVersion prints the build metadata on stdout as three fixed lines. The
// output is a contract other tooling reads, so nothing else is written.
func runVersion(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "unexpected argument: %q\n", args[0])
		fmt.Fprint(stderr, versionUsage)
		return 2
	}

	fmt.Fprintf(stdout, "agentrec %s\ncommit %s\nbuilt %s\n", version, commit, built)
	return 0
}
