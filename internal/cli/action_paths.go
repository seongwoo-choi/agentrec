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
	}
	if err := json.Unmarshal(item.Input, &input); err != nil || len(input.Changes) > maxRepositoryCount {
		return nil
	}
	paths := make([]string, 0, 2+len(input.Changes))
	paths = append(paths, input.FilePath, input.Path)
	for _, change := range input.Changes {
		paths = append(paths, change.Path)
	}
	return paths
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
