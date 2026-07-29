package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
)

const (
	shadowGroupFile     = "group.json"
	shadowWorkspaceName = "workspaces"
	maxShadowGroupBytes = 64 << 10
	shadowFileMode      = 0o600
)

// shadowGroup is the durable, deliberately small index of an ordinary pair of
// run bundles. Prompts, environment, and provider output remain only in those
// bundles; this document names no secret-bearing input.
type shadowGroup struct {
	Schema   int         `json:"schema"`
	Baseline string      `json:"baseline"`
	Legs     []shadowLeg `json:"legs"`
	Outcome  string      `json:"outcome"`
}

type shadowLeg struct {
	Runner string `json:"runner"`
	RunID  string `json:"runId"`
	Order  int    `json:"order"`
}

// shadowGroupDirectory is a group directory held open for the entire shadow
// run. path is only for Git's worktree command; root is authoritative for every
// agentrec filesystem operation in the directory.
type shadowGroupDirectory struct {
	path string
	root *os.Root
}

// pathUnchanged proves the provider-facing name still denotes the directory the
// held root owns. It is checked before any cleanup that would otherwise follow a
// replacement path, and again before publishing the durable artifact.
func (d *shadowGroupDirectory) pathUnchanged() error {
	held, err := d.root.Open(".")
	if err != nil {
		return fmt.Errorf("cli: inspect held shadow group: %w", err)
	}
	heldInfo, statErr := held.Stat()
	closeErr := held.Close()
	named, nameErr := os.Lstat(d.path)
	if statErr != nil || closeErr != nil || nameErr != nil || !named.IsDir() || !os.SameFile(heldInfo, named) {
		return fmt.Errorf("cli: shadow group directory changed during shadow run; refusing replacement")
	}
	return nil
}

// shadowGroupDir makes a fresh group and keeps it open. The held root continues
// to name this directory if an untrusted provider renames it, rather than a
// replacement subsequently installed at the pathname handed to Git.
func shadowGroupDir(dataRoot, groupID string) (*shadowGroupDirectory, error) {
	data, err := os.OpenRoot(dataRoot)
	if err != nil {
		return nil, fmt.Errorf("cli: open the shadow data root: %w", err)
	}
	defer data.Close()
	if err := data.Mkdir(shadowDirName, shadowDirMode); err != nil && !os.IsExist(err) {
		return nil, fmt.Errorf("cli: create the shadow group root: %w", err)
	}
	shadow, err := data.OpenRoot(shadowDirName)
	if err != nil {
		return nil, fmt.Errorf("cli: open the shadow group root: %w", err)
	}
	defer shadow.Close()
	if err := shadow.Chmod(".", shadowDirMode); err != nil {
		return nil, fmt.Errorf("cli: restrict the shadow group root: %w", err)
	}
	if err := shadow.Mkdir(groupID, shadowDirMode); err != nil {
		return nil, fmt.Errorf("cli: create the shadow group %s: %w", strconv.Quote(groupID), err)
	}
	group, err := shadow.OpenRoot(groupID)
	if err != nil {
		return nil, fmt.Errorf("cli: open the shadow group %s: %w", strconv.Quote(groupID), err)
	}
	if err := group.Chmod(".", shadowDirMode); err != nil {
		group.Close()
		return nil, fmt.Errorf("cli: restrict the shadow group %s: %w", strconv.Quote(groupID), err)
	}
	return &shadowGroupDirectory{path: filepath.Join(dataRoot, shadowDirName, groupID), root: group}, nil
}

// writeShadowGroup installs one group document through the group's held root.
// Linking the synced temporary file is deliberately no-overwrite: an existing
// file, including a symlink, is refused rather than replaced or written through.
func writeShadowGroup(root *os.Root, group shadowGroup) error {
	if err := validateShadowGroup(group); err != nil {
		return err
	}
	raw, err := json.Marshal(group)
	if err != nil {
		return fmt.Errorf("cli: encode shadow group: %w", err)
	}
	raw = append(raw, '\n')
	const tmpName = ".group.json.tmp"
	tmp, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, shadowFileMode)
	if err != nil {
		return fmt.Errorf("cli: create shadow group file: %w", err)
	}
	defer root.Remove(tmpName)
	if err := tmp.Chmod(shadowFileMode); err != nil {
		tmp.Close()
		return fmt.Errorf("cli: restrict shadow group file: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("cli: write shadow group file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("cli: sync shadow group file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cli: close shadow group file: %w", err)
	}
	if err := root.Link(tmpName, shadowGroupFile); err != nil {
		return fmt.Errorf("cli: install shadow group file: %w", err)
	}
	if err := root.Remove(tmpName); err != nil {
		return fmt.Errorf("cli: remove the temporary shadow group file: %w", err)
	}
	// Persist the directory entry too: a synced file that a crash can still lose
	// its name for is not a durable group artifact.
	dir, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("cli: open shadow group directory: %w", err)
	}
	err = dir.Sync()
	if closeErr := dir.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("cli: sync shadow group directory: %w", err)
	}
	return nil
}

func readShadowGroup(dataRoot, groupID string) (shadowGroup, error) {
	if err := validateRunID(groupID); err != nil {
		return shadowGroup{}, fmt.Errorf("cli: invalid shadow group id: %w", err)
	}
	root, err := openShadowGroupRoot(dataRoot, groupID)
	if err != nil {
		return shadowGroup{}, err
	}
	defer root.Close()
	info, err := root.Lstat(shadowGroupFile)
	if err != nil {
		return shadowGroup{}, fmt.Errorf("cli: inspect shadow group file %s: %w", strconv.Quote(groupID), err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxShadowGroupBytes {
		return shadowGroup{}, fmt.Errorf("cli: shadow group file %s is not a bounded regular file", strconv.Quote(groupID))
	}
	f, err := root.Open(shadowGroupFile)
	if err != nil {
		return shadowGroup{}, fmt.Errorf("cli: read shadow group file %s: %w", strconv.Quote(groupID), err)
	}
	defer f.Close()
	held, err := f.Stat()
	if err != nil || !held.Mode().IsRegular() || !os.SameFile(info, held) {
		return shadowGroup{}, fmt.Errorf("cli: shadow group file %s changed before it could be read", strconv.Quote(groupID))
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxShadowGroupBytes+1))
	if err != nil || len(raw) > maxShadowGroupBytes {
		return shadowGroup{}, fmt.Errorf("cli: read shadow group file %s: invalid size", strconv.Quote(groupID))
	}
	var group shadowGroup
	if err := json.Unmarshal(raw, &group); err != nil {
		return shadowGroup{}, fmt.Errorf("cli: decode shadow group file %s: %w", strconv.Quote(groupID), err)
	}
	if err := validateShadowGroup(group); err != nil {
		return shadowGroup{}, err
	}
	return group, nil
}

// openShadowGroupRoot checks each path component through the root that owns it,
// then proves the held root still identifies the directory checked by name.
func openShadowGroupRoot(dataRoot, groupID string) (*os.Root, error) {
	data, err := os.OpenRoot(dataRoot)
	if err != nil {
		return nil, fmt.Errorf("cli: open the shadow data root: %w", err)
	}
	defer data.Close()
	shadowInfo, err := data.Lstat(shadowDirName)
	if err != nil {
		return nil, fmt.Errorf("cli: inspect shadow group root: %w", err)
	}
	if !shadowInfo.IsDir() {
		return nil, fmt.Errorf("cli: shadow group root is not a directory")
	}
	shadow, err := data.OpenRoot(shadowDirName)
	if err != nil {
		return nil, fmt.Errorf("cli: open shadow group root: %w", err)
	}
	defer shadow.Close()
	checked, err := shadow.Lstat(groupID)
	if err != nil {
		return nil, fmt.Errorf("cli: inspect shadow group %s: %w", strconv.Quote(groupID), err)
	}
	if !checked.IsDir() {
		return nil, fmt.Errorf("cli: shadow group %s is not a directory", strconv.Quote(groupID))
	}
	group, err := shadow.OpenRoot(groupID)
	if err != nil {
		return nil, fmt.Errorf("cli: open shadow group %s: %w", strconv.Quote(groupID), err)
	}
	held, err := group.Open(".")
	if err != nil {
		group.Close()
		return nil, fmt.Errorf("cli: inspect shadow group %s: %w", strconv.Quote(groupID), err)
	}
	heldInfo, statErr := held.Stat()
	closeErr := held.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(checked, heldInfo) {
		group.Close()
		return nil, fmt.Errorf("cli: shadow group %s changed while it was being opened", strconv.Quote(groupID))
	}
	return group, nil
}

func validateShadowGroup(group shadowGroup) error {
	if group.Schema != 1 || !isLowerHex(group.Baseline, 40) || !slices.Contains([]string{"completed", "failed", "interrupted", "source_drift"}, group.Outcome) {
		return fmt.Errorf("cli: malformed shadow group")
	}
	if len(group.Legs) > len(shadowRunners) {
		return fmt.Errorf("cli: malformed shadow group legs")
	}
	seen := map[string]bool{}
	for i, l := range group.Legs {
		if !slices.Contains(shadowRunners, l.Runner) || seen[l.Runner] || l.Order != i+1 {
			return fmt.Errorf("cli: malformed shadow group legs")
		}
		if err := validateRunID(l.RunID); err != nil {
			return fmt.Errorf("cli: malformed shadow group leg: %w", err)
		}
		seen[l.Runner] = true
	}
	return nil
}

func shadowGroupFrom(pre *prepared, legs []leg, outcome string) shadowGroup {
	stored := make([]shadowLeg, 0, len(legs))
	for _, l := range legs {
		stored = append(stored, shadowLeg{Runner: l.runner, RunID: l.runID, Order: l.order})
	}
	return shadowGroup{Schema: 1, Baseline: pre.baseline, Legs: stored, Outcome: outcome}
}

func runShadowShow(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprint(stderr, "usage: agentrec shadow show <group-id>\n")
		return exitUsage
	}
	root, err := runsRoot()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	group, err := readShadowGroup(filepath.Dir(root), args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	legs := make([]leg, 0, len(group.Legs))
	for _, l := range group.Legs {
		legs = append(legs, leg{runner: l.Runner, runID: l.RunID, order: l.Order})
	}
	if err := renderComparison(stdout, root, legs); err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	return 0
}
