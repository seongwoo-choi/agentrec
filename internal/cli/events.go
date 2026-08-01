package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
)

const providerEventsFile = "provider-events.sanitized.jsonl"

const (
	maxEventBytes       = maxActionBytes
	maxEventStreamBytes = maxActionStreamBytes
	maxEvents           = maxActions
	maxEventDepth       = 64
	maxEventTokens      = 1_000_000
)

type providerEvent struct {
	raw      json.RawMessage
	typeName string
}

type eventOutput struct {
	SchemaVersion   int               `json:"schemaVersion"`
	RunID           string            `json:"runId"`
	Attribution     string            `json:"attribution"`
	ArtifactPresent bool              `json:"artifactPresent"`
	Events          []json.RawMessage `json:"events"`
}

func runEvents(args []string, stdout, stderr io.Writer) int {
	jsonMode := len(args) == 2 && args[1] == "--json"
	if len(args) != 1 && !jsonMode {
		fmt.Fprint(stderr, "usage: agentrec events <run-id>|latest [--json]\n")
		return 2
	}
	if args[0] != latestRun {
		if err := validateRunID(args[0]); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	root, err := runsRoot()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	runID := args[0]
	if runID == latestRun {
		runID, err = newestRunID(root)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	runRoot, err := openRunRoot(root, runID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer runRoot.Close()
	events, present, err := readProviderEvents(runRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if jsonMode {
		raw := make([]json.RawMessage, len(events))
		for i := range events {
			raw[i] = events[i].raw
		}
		if err := json.NewEncoder(stdout).Encode(eventOutput{1, runID, "provider_reported", present, raw}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if err := renderEventSummary(stdout, runID, present, events); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func openRunRoot(root, runID string) (*os.Root, error) {
	runs, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("cli: open runs root: %w", err)
	}
	defer runs.Close()
	before, err := runs.Lstat(runID)
	if err != nil {
		return nil, fmt.Errorf("cli: inspect run %s: %w", runID, err)
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("cli: run %s is not a real directory", runID)
	}
	runRoot, err := runs.OpenRoot(runID)
	if err != nil {
		return nil, fmt.Errorf("cli: open run %s: %w", runID, err)
	}
	after, err := runRoot.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		runRoot.Close()
		if err != nil {
			return nil, fmt.Errorf("cli: inspect opened run %s: %w", runID, err)
		}
		return nil, fmt.Errorf("cli: run %s changed while it was opened", runID)
	}
	return runRoot, nil
}

func readProviderEvents(root *os.Root) ([]providerEvent, bool, error) {
	before, err := root.Lstat(providerEventsFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("cli: inspect %s: %w", providerEventsFile, err)
	}
	if !before.Mode().IsRegular() {
		return nil, true, fmt.Errorf("cli: %s is not a regular file", providerEventsFile)
	}
	f, err := root.Open(providerEventsFile)
	if err != nil {
		return nil, true, fmt.Errorf("cli: open %s: %w", providerEventsFile, err)
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		if err != nil {
			return nil, true, fmt.Errorf("cli: inspect opened %s: %w", providerEventsFile, err)
		}
		return nil, true, fmt.Errorf("cli: %s changed while it was opened", providerEventsFile)
	}
	if after.Size() > maxEventStreamBytes {
		return nil, true, fmt.Errorf("cli: %s is larger than %d bytes", providerEventsFile, maxEventStreamBytes)
	}

	var events []providerEvent
	totalTokens := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(nil, maxEventBytes)
	scanned := 0
	for line := 1; scanner.Scan(); line++ {
		raw := bytes.TrimSpace(scanner.Bytes())
		scanned += len(scanner.Bytes()) + 1
		if scanned > maxEventStreamBytes {
			return nil, true, fmt.Errorf("cli: %s is larger than %d bytes", providerEventsFile, maxEventStreamBytes)
		}
		if len(raw) == 0 {
			continue
		}
		if len(events) == maxEvents {
			return nil, true, fmt.Errorf("cli: %s holds more than %d events", providerEventsFile, maxEvents)
		}
		tokens, err := validateEventObject(raw, maxEventTokens-totalTokens)
		if err != nil {
			return nil, true, fmt.Errorf("cli: %s line %d: %w", providerEventsFile, line, err)
		}
		totalTokens += tokens
		var envelope struct {
			Type json.RawMessage `json:"type"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, true, fmt.Errorf("cli: %s line %d: %w", providerEventsFile, line, err)
		}
		var typeName string
		if len(envelope.Type) > 0 {
			_ = json.Unmarshal(envelope.Type, &typeName)
		}
		events = append(events, providerEvent{raw: append(json.RawMessage(nil), raw...), typeName: typeName})
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, true, fmt.Errorf("cli: %s holds a line longer than %d bytes", providerEventsFile, maxEventBytes)
		}
		return nil, true, fmt.Errorf("cli: read %s: %w", providerEventsFile, err)
	}
	return events, true, nil
}

func validateEventObject(raw []byte, remainingTokens int) (int, error) {
	if remainingTokens <= 0 {
		return 0, fmt.Errorf("event stream holds more than %d JSON tokens", maxEventTokens)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	depth, tokens := 0, 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			if depth != 0 || tokens == 0 {
				return 0, errors.New("event is not a JSON object")
			}
			return tokens, nil
		}
		if err != nil {
			return 0, fmt.Errorf("event is not a JSON object: %w", err)
		}
		tokens++
		if tokens > remainingTokens {
			return 0, fmt.Errorf("event stream holds more than %d JSON tokens", maxEventTokens)
		}
		delim, isDelim := token.(json.Delim)
		if tokens == 1 && (!isDelim || delim != '{') {
			return 0, errors.New("event is not a JSON object")
		}
		if isDelim {
			switch delim {
			case '{', '[':
				depth++
				if depth > maxEventDepth {
					return 0, fmt.Errorf("event nesting exceeds %d", maxEventDepth)
				}
			case '}', ']':
				depth--
				if depth == 0 {
					if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
						return 0, errors.New("event holds more than one JSON value")
					}
					return tokens, nil
				}
			}
		}
	}
}

func renderEventSummary(w io.Writer, runID string, present bool, events []providerEvent) error {
	counts := make(map[string]int)
	for _, event := range events {
		typeName := event.typeName
		if typeName == "" {
			typeName = "(untyped)"
		}
		counts[typeName]++
	}
	types := make([]string, 0, len(counts))
	for typeName := range counts {
		types = append(types, typeName)
	}
	slices.Sort(types)
	artifact := "absent"
	if present {
		artifact = "present"
	}
	if _, err := fmt.Fprintf(w, "PROVIDER-REPORTED EVENTS\nRun          %s\nAttribution  provider_reported\nArtifact     %s\nEvents       %d\n", runID, artifact, len(events)); err != nil {
		return err
	}
	if len(types) == 0 {
		return nil
	}
	if _, err := fmt.Fprint(w, "\nTYPES\n"); err != nil {
		return err
	}
	for _, typeName := range types {
		if _, err := fmt.Fprintf(w, "%s  %d\n", strconv.QuoteToASCII(typeName), counts[typeName]); err != nil {
			return err
		}
	}
	return nil
}
