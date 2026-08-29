package cli

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/seongwoo-choi/agentrec/internal/action"
	"github.com/seongwoo-choi/agentrec/internal/storage"
)

const (
	viewPageSize     = 250
	viewPageBytes    = 1 << 20
	maxViewSnapshots = 4
)

type viewActionPage struct {
	Items      []action.Action `json:"items"`
	NextCursor *int64          `json:"nextCursor,omitempty"`
}

type viewEventPage struct {
	Items      []json.RawMessage `json:"items"`
	NextCursor *int64            `json:"nextCursor,omitempty"`
}

type viewSnapshot struct {
	id         string
	runRoot    *os.Root
	actions    *os.File
	actionTemp string
	actionSize int64
	events     *os.File
	eventTemp  string
	eventSize  int64
}

var viewSnapshotFiles = []string{
	manifestFile,
	promptFile,
	actionsFile,
	providerEventsFile,
	unparsedFile,
	processDir + "/" + resultFile,
	gitDir + "/" + resultFile,
	verifyDir + "/" + verifyResults,
	providerUsageFile,
}

type viewFileIdentity struct {
	present bool
	info    os.FileInfo
}

func (s *viewSnapshot) Close() error {
	var errs []error
	if s.actions != nil {
		errs = append(errs, s.actions.Close())
	}
	if s.actionTemp != "" {
		errs = append(errs, os.Remove(s.actionTemp))
	}
	if s.events != nil {
		errs = append(errs, s.events.Close())
	}
	if s.eventTemp != "" {
		errs = append(errs, os.Remove(s.eventTemp))
	}
	if s.runRoot != nil {
		errs = append(errs, s.runRoot.Close())
	}
	return errors.Join(errs...)
}

type viewSnapshotStore struct {
	root string
	sem  chan struct{}
	mu   sync.RWMutex
	byID map[string]*viewSnapshot
	ids  []string
}

func newViewSnapshotStore(root string) *viewSnapshotStore {
	return &viewSnapshotStore{root: root, sem: make(chan struct{}, 2), byID: make(map[string]*viewSnapshot)}
}

func (s *viewSnapshotStore) withSlot(r *http.Request, fn func() error) error {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
		return fn()
	case <-r.Context().Done():
		return r.Context().Err()
	}
}

func (s *viewSnapshotStore) create(runID string) (viewRunResponse, error) {
	runRoot, err := openRunRoot(s.root, runID)
	if err != nil {
		return viewRunResponse{}, err
	}
	snapshot := &viewSnapshot{runRoot: runRoot}
	fail := func(err error) (viewRunResponse, error) {
		_ = snapshot.Close()
		return viewRunResponse{}, err
	}
	before, err := viewRunFingerprint(runRoot)
	if err != nil {
		return fail(err)
	}

	manifest, err := readManifestFromRoot(runRoot)
	if err != nil {
		return fail(err)
	}
	if err := validateUnparsedStreamFromRoot(runRoot, manifest.UnparsedLines); err != nil {
		return fail(err)
	}
	prompt, err := readViewPrompt(runRoot)
	if err != nil {
		return fail(err)
	}
	evidence, err := readViewEvidenceFromRoot(runRoot, manifest)
	if err != nil {
		return fail(err)
	}
	actionSource, actionSize, err := openViewStream(runRoot, actionsFile, maxActionStreamBytes, false)
	if err != nil {
		return fail(err)
	}
	snapshot.actions, snapshot.actionTemp, err = copyViewStream(actionSource, actionSize)
	closeErr := actionSource.Close()
	if err != nil {
		return fail(err)
	}
	if closeErr != nil {
		return fail(closeErr)
	}
	snapshot.actionSize = actionSize
	actionCount, err := countViewActions(snapshot.actions, snapshot.actionSize)
	if err != nil {
		return fail(err)
	}
	eventSource, eventSize, err := openViewStream(runRoot, providerEventsFile, maxEventStreamBytes, true)
	if err != nil {
		return fail(err)
	}
	if eventSource != nil {
		snapshot.events, snapshot.eventTemp, err = copyViewStream(eventSource, eventSize)
		closeErr = eventSource.Close()
		if err != nil {
			return fail(err)
		}
		if closeErr != nil {
			return fail(closeErr)
		}
		snapshot.eventSize = eventSize
	}
	eventCount := 0
	if snapshot.events != nil {
		eventCount, err = countViewEvents(snapshot.events, snapshot.eventSize)
		if err != nil {
			return fail(err)
		}
	}
	after, err := viewRunFingerprint(runRoot)
	if err != nil {
		return fail(err)
	}
	if !sameViewFingerprint(before, after) {
		return fail(errors.New("cli: run changed while the viewer snapshot was being created; retry"))
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return fail(fmt.Errorf("cli: create viewer snapshot: %w", err))
	}
	snapshot.id = hex.EncodeToString(tokenBytes)
	s.add(snapshot)

	return viewRunResponse{
		SchemaVersion: 1,
		SnapshotID:    snapshot.id,
		ActionCount:   actionCount,
		EventCount:    eventCount,
		Run: viewRunInfo{
			ID: runID, Provider: manifest.Provider, ProviderVersion: manifest.ProviderVersion,
			Project: projectName(manifest.CWD), CWD: manifest.CWD, Prompt: prompt,
			StartedAt: manifest.StartedAt, EndedAt: manifest.EndedAt, ExitReason: manifest.ExitReason,
			WarningCount: manifest.WarningCount, UnparsedLines: manifest.UnparsedLines,
			VersionUnverified: manifest.VersionUnverified,
		},
		ProviderEvents: viewProviderEvents{Attribution: "provider_reported", Present: snapshot.events != nil},
		Evidence:       evidence,
	}, nil
}

func viewRunFingerprint(root *os.Root) (map[string]viewFileIdentity, error) {
	fingerprint := make(map[string]viewFileIdentity, len(viewSnapshotFiles))
	for _, name := range viewSnapshotFiles {
		info, err := lstatConfined(root, name)
		if errors.Is(err, os.ErrNotExist) {
			fingerprint[name] = viewFileIdentity{}
			continue
		}
		if err != nil {
			return nil, err
		}
		fingerprint[name] = viewFileIdentity{present: true, info: info}
	}
	return fingerprint, nil
}

func sameViewFingerprint(a, b map[string]viewFileIdentity) bool {
	for _, name := range viewSnapshotFiles {
		left, right := a[name], b[name]
		if left.present != right.present {
			return false
		}
		if !left.present {
			continue
		}
		if !os.SameFile(left.info, right.info) || left.info.Size() != right.info.Size() || !left.info.ModTime().Equal(right.info.ModTime()) || left.info.Mode() != right.info.Mode() {
			return false
		}
	}
	return true
}

func readViewEvidenceFromRoot(root *os.Root, manifest storage.Manifest) (viewEvidence, error) {
	result, err := readProcessResultFromRoot(root)
	if err != nil {
		return viewEvidence{}, err
	}
	git, err := readGitResultFromRoot(root)
	if err != nil {
		return viewEvidence{}, err
	}
	verification, err := readVerificationFromRoot(root)
	if err != nil {
		return viewEvidence{}, err
	}
	usage, err := readProviderUsageFromRoot(root, manifest.Provider)
	if err != nil {
		return viewEvidence{}, err
	}
	return viewEvidence{
		ProviderUsage: viewFields(providerUsageFields(usage)),
		Supervisor:    viewFields(supervisorFields(manifest, result)),
		Repository:    viewFields(repositoryFields(git)),
		Verification:  viewFields(verificationFields(verification)),
	}, nil
}

func copyViewStream(source *os.File, size int64) (*os.File, string, error) {
	copyFile, err := os.CreateTemp("", "agentrec-view-snapshot-*")
	if err != nil {
		return nil, "", fmt.Errorf("cli: create viewer stream snapshot: %w", err)
	}
	path := copyFile.Name()
	ok := false
	defer func() {
		if ok {
			return
		}
		_ = copyFile.Close()
		_ = os.Remove(path)
	}()
	if _, err := io.CopyN(copyFile, io.NewSectionReader(source, 0, size), size); err != nil {
		return nil, "", fmt.Errorf("cli: copy viewer stream snapshot: %w", err)
	}
	if _, err := copyFile.Seek(0, io.SeekStart); err != nil {
		return nil, "", fmt.Errorf("cli: rewind viewer stream snapshot: %w", err)
	}
	if err := os.Remove(path); err == nil {
		path = ""
	}
	ok = true
	return copyFile, path, nil
}

func openViewStream(root *os.Root, name string, limit int, optional bool) (*os.File, int64, error) {
	file, err := openRegularFromRoot(root, name)
	if optional && errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, fmt.Errorf("cli: inspect %s: %w", name, err)
	}
	if info.Size() > int64(limit) {
		file.Close()
		return nil, 0, fmt.Errorf("cli: %s is larger than %d bytes", name, limit)
	}
	return file, info.Size(), nil
}

func countViewActions(file *os.File, size int64) (int, error) {
	scanner := bufio.NewScanner(io.NewSectionReader(file, 0, size))
	scanner.Buffer(nil, maxActionBytes)
	count := 0
	for line := 1; scanner.Scan(); line++ {
		if len(strings.TrimSpace(scanner.Text())) == 0 {
			continue
		}
		if count == maxActions {
			return 0, fmt.Errorf("cli: %s holds more than %d actions", actionsFile, maxActions)
		}
		var item action.Action
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return 0, fmt.Errorf("cli: %s line %d is not a recorded action: %w", actionsFile, line, err)
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("cli: read %s: %w", actionsFile, err)
	}
	return count, nil
}

func countViewEvents(file *os.File, size int64) (int, error) {
	scanner := bufio.NewScanner(io.NewSectionReader(file, 0, size))
	scanner.Buffer(nil, maxEventBytes)
	count, tokens := 0, 0
	for line := 1; scanner.Scan(); line++ {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		if count == maxEvents {
			return 0, fmt.Errorf("cli: %s holds more than %d events", providerEventsFile, maxEvents)
		}
		n, err := validateEventObject(raw, maxEventTokens-tokens)
		if err != nil {
			return 0, fmt.Errorf("cli: %s line %d: %w", providerEventsFile, line, err)
		}
		tokens += n
		count++
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("cli: read %s: %w", providerEventsFile, err)
	}
	return count, nil
}

func (s *viewSnapshotStore) add(snapshot *viewSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[snapshot.id] = snapshot
	s.ids = append(s.ids, snapshot.id)
	if len(s.ids) <= maxViewSnapshots {
		return
	}
	oldID := s.ids[0]
	s.ids = s.ids[1:]
	old := s.byID[oldID]
	delete(s.byID, oldID)
	_ = old.Close()
}

func (s *viewSnapshotStore) withSnapshot(id string, fn func(*viewSnapshot) error) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot, ok := s.byID[id]
	if !ok {
		return false, nil
	}
	return true, fn(snapshot)
}

func parseViewCursor(r *http.Request) (int64, error) {
	value := r.URL.Query().Get("cursor")
	if value == "" {
		return 0, nil
	}
	cursor, err := strconv.ParseInt(value, 10, 64)
	if err != nil || cursor < 0 {
		return 0, errors.New("invalid cursor")
	}
	return cursor, nil
}

func viewNextCursor(cursor, size int64) *int64 {
	if cursor >= size {
		return nil
	}
	next := cursor
	return &next
}

func readViewActionPage(snapshot *viewSnapshot, cursor int64) (viewActionPage, error) {
	if cursor > snapshot.actionSize {
		return viewActionPage{}, errors.New("cursor is outside the action stream")
	}
	scanner := bufio.NewScanner(io.NewSectionReader(snapshot.actions, cursor, snapshot.actionSize-cursor))
	scanner.Buffer(nil, maxActionBytes)
	page := viewActionPage{Items: make([]action.Action, 0, viewPageSize)}
	position := cursor
	pageBytes := 0
	for scanner.Scan() {
		lineStart := position
		line := scanner.Bytes()
		position += int64(len(line))
		if position < snapshot.actionSize {
			position++
		}
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		if len(page.Items) > 0 && pageBytes+len(line) > viewPageBytes {
			position = lineStart
			break
		}
		var item action.Action
		if err := json.Unmarshal(line, &item); err != nil {
			return viewActionPage{}, fmt.Errorf("cli: read %s page: %w", actionsFile, err)
		}
		page.Items = append(page.Items, item)
		pageBytes += len(line)
		if len(page.Items) == viewPageSize {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return viewActionPage{}, fmt.Errorf("cli: read %s page: %w", actionsFile, err)
	}
	page.NextCursor = viewNextCursor(position, snapshot.actionSize)
	return page, nil
}

func readViewEventPage(snapshot *viewSnapshot, cursor int64) (viewEventPage, error) {
	if snapshot.events == nil {
		return viewEventPage{Items: []json.RawMessage{}}, nil
	}
	if cursor > snapshot.eventSize {
		return viewEventPage{}, errors.New("cursor is outside the event stream")
	}
	scanner := bufio.NewScanner(io.NewSectionReader(snapshot.events, cursor, snapshot.eventSize-cursor))
	scanner.Buffer(nil, maxEventBytes)
	page := viewEventPage{Items: make([]json.RawMessage, 0, viewPageSize)}
	position := cursor
	pageBytes := 0
	for scanner.Scan() {
		lineStart := position
		line := scanner.Bytes()
		position += int64(len(line))
		if position < snapshot.eventSize {
			position++
		}
		raw := bytes.TrimSpace(line)
		if len(raw) == 0 {
			continue
		}
		if len(page.Items) > 0 && pageBytes+len(raw) > viewPageBytes {
			position = lineStart
			break
		}
		page.Items = append(page.Items, append(json.RawMessage(nil), raw...))
		pageBytes += len(raw)
		if len(page.Items) == viewPageSize {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return viewEventPage{}, fmt.Errorf("cli: read %s page: %w", providerEventsFile, err)
	}
	page.NextCursor = viewNextCursor(position, snapshot.eventSize)
	return page, nil
}

func (s *viewSnapshotStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for _, snapshot := range s.byID {
		errs = append(errs, snapshot.Close())
	}
	s.byID = make(map[string]*viewSnapshot)
	s.ids = nil
	return errors.Join(errs...)
}
