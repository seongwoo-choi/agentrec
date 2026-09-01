package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/seongwoo-choi/agentrec/internal/action"
)

func recordsRepositoryPaths(item action.Action) bool {
	switch item.Type {
	case action.TypeFileRead, action.TypeFileWrite, action.TypeFileEdit:
		return true
	default:
		return false
	}
}

func explicitActionPathInputs(item action.Action) []string {
	if !recordsRepositoryPaths(item) {
		return nil
	}
	var input struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
		Changes  []struct {
			Path string `json:"path"`
		} `json:"changes"`
		// Command carries a whole patch when the tool is Codex's apply_patch,
		// whose file headers are the only place its paths are named.
		Command string `json:"command"`
	}
	if err := json.Unmarshal(item.Input, &input); err != nil || len(input.Changes) > maxRepositoryCount {
		return nil
	}
	patched, ok := patchFilePaths(input.Command)
	if !ok {
		return nil
	}
	paths := make([]string, 0, 2+len(input.Changes)+len(patched))
	paths = append(paths, input.FilePath, input.Path)
	for _, change := range input.Changes {
		paths = append(paths, change.Path)
	}
	return append(paths, patched...)
}

// patchFilePaths are the files an apply_patch body names in its headers. Only
// the headers are read: a patch is the model's own statement of which files it
// meant to touch, and its hunks say nothing more about that. A body naming more
// files than the count limit is refused whole, like an oversized change list.
func patchFilePaths(patch string) ([]string, bool) {
	if !strings.HasPrefix(patch, "*** Begin Patch") {
		return nil, true
	}
	var paths []string
	for _, line := range strings.Split(patch, "\n") {
		for _, header := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "} {
			if strings.HasPrefix(line, header) {
				paths = append(paths, strings.TrimSpace(strings.TrimPrefix(line, header)))
				break
			}
		}
		if len(paths) > maxRepositoryCount {
			return nil, false
		}
	}
	return paths, true
}

func pathWithin(root, candidate string) (string, bool) {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return relative, true
}

func observeActionRepositoryPaths(item action.Action, runtimeCWD, canonicalCWD, repoRoot string) []string {
	seen := make(map[string]struct{})
	var observed []string
	for _, candidate := range explicitActionPathInputs(item) {
		if candidate == "" || strings.IndexByte(candidate, 0) >= 0 {
			continue
		}
		canonical := candidate
		if !filepath.IsAbs(candidate) {
			canonical = filepath.Join(canonicalCWD, candidate)
		} else if suffix, ok := pathWithin(runtimeCWD, candidate); ok {
			canonical = filepath.Join(canonicalCWD, suffix)
		}
		relative, ok := pathWithin(repoRoot, canonical)
		if !ok {
			continue
		}
		relative = filepath.ToSlash(relative)
		if _, duplicate := seen[relative]; duplicate {
			continue
		}
		seen[relative] = struct{}{}
		observed = append(observed, relative)
	}
	return observed
}
