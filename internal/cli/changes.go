package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
)

const changesUsage = "agentrec changes <run-id>|latest [--json]"

var changeEvidenceFiles = []string{
	path.Join(gitDir, resultFile),
	path.Join(gitDir, trackedStatFile),
	path.Join(gitDir, untrackedChangesFile),
	path.Join(gitDir, trackedPatchFile),
}

type changesOptions struct {
	selector string
	json     bool
}

type changesJSON struct {
	SchemaVersion int               `json:"schemaVersion"`
	RunID         string            `json:"runId"`
	Status        string            `json:"status"`
	Reason        string            `json:"reason,omitempty"`
	Attribution   string            `json:"attribution"`
	Baseline      string            `json:"baseline,omitempty"`
	Total         int               `json:"total"`
	Changes       []changesJSONItem `json:"changes"`
}

type changesJSONItem struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Tracked   bool   `json:"tracked"`
	Additions *int   `json:"additions,omitempty"`
	Deletions *int   `json:"deletions,omitempty"`
	Binary    bool   `json:"binary,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Stored    bool   `json:"stored,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

func runChanges(args []string, stdout, stderr io.Writer) int {
	options, err := parseChangesOptions(args)
	if err != nil {
		fmt.Fprintln(stderr, "cli: changes:", err)
		fmt.Fprintln(stderr, changesUsage)
		return 2
	}

	root, err := runsRoot()
	if err != nil {
		fmt.Fprintln(stderr, "cli: changes:", err)
		return 1
	}
	runID := options.selector
	if runID == "latest" {
		if runID, err = newestRunID(root); err != nil {
			fmt.Fprintln(stderr, "cli: changes:", err)
			return 1
		}
	}
	if err := checkRunID(runID); err != nil {
		fmt.Fprintln(stderr, "cli: changes:", err)
		fmt.Fprintln(stderr, changesUsage)
		return 2
	}

	page, err := readRunChanges(root, runID)
	if err != nil {
		fmt.Fprintln(stderr, "cli: changes:", err)
		return 1
	}
	if page.NextCursor != nil {
		fmt.Fprintf(stderr, "cli: changes: run has more than %d changed files; inspect it with agentrec view %s\n", viewPageSize, runID)
		return 1
	}

	if options.json {
		if err := renderChangesJSON(stdout, runID, page); err != nil {
			fmt.Fprintln(stderr, "cli: changes:", err)
			return 1
		}
		return 0
	}
	if err := renderChangesTerminal(stdout, runID, page); err != nil {
		fmt.Fprintln(stderr, "cli: changes:", err)
		return 1
	}
	return 0
}

func parseChangesOptions(args []string) (changesOptions, error) {
	var options changesOptions
	for _, arg := range args {
		switch arg {
		case "--json":
			if options.json {
				return changesOptions{}, fmt.Errorf("duplicate option %q", arg)
			}
			options.json = true
		default:
			if strings.HasPrefix(arg, "-") {
				return changesOptions{}, fmt.Errorf("unsupported option %q", arg)
			}
			if options.selector != "" {
				return changesOptions{}, fmt.Errorf("expected exactly one run selector")
			}
			options.selector = arg
		}
	}
	if options.selector == "" {
		return changesOptions{}, fmt.Errorf("expected exactly one run selector")
	}
	return options, nil
}

func readRunChanges(root, runID string) (viewChangePage, error) {
	source, err := openRunRoot(root, runID)
	if err != nil {
		return viewChangePage{}, err
	}
	defer source.Close()

	ctx := context.Background()
	before, err := fingerprintChangeEvidence(ctx, source)
	if err != nil {
		return viewChangePage{}, err
	}
	snapshot := &viewSnapshot{documents: make(map[string][]byte)}
	defer snapshot.Close()
	for _, name := range changeEvidenceFiles {
		expected := before[name]
		if !expected.present {
			continue
		}
		if name == path.Join(gitDir, trackedPatchFile) {
			snapshot.patch, snapshot.patchSize, err = captureViewStreamContext(ctx, source, name, expected)
		} else {
			snapshot.documents[name], err = captureViewDocumentContext(ctx, source, name, expected)
		}
		if err != nil {
			return viewChangePage{}, err
		}
	}
	if err := prepareViewChangesContext(ctx, snapshot); err != nil {
		return viewChangePage{}, err
	}
	page, err := readViewChangePage(snapshot, 0)
	if err != nil {
		return viewChangePage{}, err
	}
	after, err := fingerprintChangeEvidence(ctx, source)
	if err != nil {
		return viewChangePage{}, err
	}
	if !sameViewFingerprint(before, after) {
		return viewChangePage{}, fmt.Errorf("run changed while reading repository evidence")
	}
	return page, nil
}

func fingerprintChangeEvidence(ctx context.Context, root *os.Root) (map[string]viewFileIdentity, error) {
	fingerprint := make(map[string]viewFileIdentity, len(changeEvidenceFiles))
	for _, name := range changeEvidenceFiles {
		identity, err := viewFileFingerprintContext(ctx, root, name)
		if err != nil {
			return nil, err
		}
		fingerprint[name] = identity
	}
	return fingerprint, nil
}

func renderChangesJSON(w io.Writer, runID string, page viewChangePage) error {
	changes := make([]changesJSONItem, 0, len(page.Items))
	for _, change := range page.Items {
		changes = append(changes, changesJSONItem{
			Path: change.Path, Kind: change.Kind, Tracked: change.Tracked,
			Additions: change.Additions, Deletions: change.Deletions, Binary: change.Binary,
			Mode: change.Mode, Size: change.Size, Stored: change.Stored, Reason: change.Reason,
		})
	}
	return json.NewEncoder(w).Encode(changesJSON{
		SchemaVersion: 1,
		RunID:         runID,
		Status:        page.Status,
		Reason:        page.Reason,
		Attribution:   page.Attribution,
		Baseline:      page.Baseline,
		Total:         page.Total,
		Changes:       changes,
	})
}

func renderChangesTerminal(w io.Writer, runID string, page viewChangePage) error {
	if _, err := fmt.Fprintf(w, "Run: %s\nStatus: %s\nAttribution: %s\n", oneLine(runID), oneLine(page.Status), oneLine(page.Attribution)); err != nil {
		return err
	}
	if page.Reason != "" {
		if _, err := fmt.Fprintf(w, "Reason: %s\n", oneLine(page.Reason)); err != nil {
			return err
		}
	}
	if page.Baseline != "" {
		if _, err := fmt.Fprintf(w, "Baseline: %s\n", oneLine(page.Baseline)); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "Changed files: %d\n", page.Total); err != nil {
		return err
	}
	for _, change := range page.Items {
		additions, deletions := "-", "-"
		if change.Additions != nil {
			additions = fmt.Sprintf("+%d", *change.Additions)
		}
		if change.Deletions != nil {
			deletions = fmt.Sprintf("-%d", *change.Deletions)
		}
		if _, err := fmt.Fprintf(w, "%s	%s	%s	%s\n", strconv.Quote(change.Kind), strconv.Quote(change.Path), additions, deletions); err != nil {
			return err
		}
	}
	return nil
}
