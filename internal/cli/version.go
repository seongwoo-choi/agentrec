package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

const versionUsage = "usage: agentrec version [--verbose]\n"

// runVersion preserves the three-line build metadata contract by default. The
// verbose form adds the executable actually invoked and every executable PATH candidate.
func runVersion(args []string, stdout, stderr io.Writer) int {
	verbose := len(args) > 0 && args[0] == "--verbose"
	if len(args) > 1 || len(args) == 1 && !verbose {
		unexpected := args[0]
		if verbose {
			unexpected = args[1]
		}
		fmt.Fprintf(stderr, "unexpected argument: %q\n", unexpected)
		fmt.Fprint(stderr, versionUsage)
		return 2
	}

	fmt.Fprintf(stdout, "agentrec %s\ncommit %s\nbuilt %s\n", version, commit, built)
	if !verbose {
		return 0
	}

	executable, executableErr := os.Executable()
	executable = absolutePath(executable)
	fmt.Fprintf(stdout, "executable %s\n", versionPath(executable, executableErr))
	candidates := versionPathCandidates(os.Getenv("PATH"))
	if len(candidates) == 0 {
		fmt.Fprintln(stdout, "path-candidate unavailable")
	}
	for _, candidate := range candidates {
		identity := "other"
		if executableErr != nil {
			identity = "unknown"
		} else if sameExecutable(executable, candidate) {
			identity = "current"
		}
		fmt.Fprintf(stdout, "path-candidate %s %s\n", strconv.Quote(candidate), identity)
	}
	return 0
}

func absolutePath(path string) string {
	absolute, err := filepath.Abs(path)
	if err == nil {
		return absolute
	}
	return path
}

func versionPath(path string, err error) string {
	if err != nil {
		return "unavailable"
	}
	return strconv.Quote(path)
}

func versionPathCandidates(pathValue string) []string {
	seen := make(map[string]struct{})
	var candidates []string
	for _, dir := range filepath.SplitList(pathValue) {
		if dir == "" {
			dir = "."
		}
		candidate := absolutePath(filepath.Join(dir, "agentrec"))
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		if _, err := exec.LookPath(candidate); err != nil {
			continue
		}
		candidates = append(candidates, candidate)
	}
	return candidates
}

func sameExecutable(left, right string) bool {
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}
