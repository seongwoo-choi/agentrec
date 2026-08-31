package cli

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
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
	"unicode/utf8"

	"github.com/seongwoo-choi/agentrec/internal/action"
	"github.com/seongwoo-choi/agentrec/internal/evidence"
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
	documents  map[string][]byte
	actions    *os.File
	actionSize int64
	events     *os.File
	eventSize  int64
	unparsed   *os.File
}

var viewSnapshotFiles = []string{
	manifestFile,
	promptFile,
	actionsFile,
	providerEventsFile,
	processDir + "/" + resultFile,
	gitDir + "/" + resultFile,
	verifyDir + "/" + verifyResults,
	providerUsageFile,
}

var viewSnapshotFileLimits = map[string]int64{
	manifestFile:                    maxDocumentBytes,
	promptFile:                      maxDocumentBytes,
	actionsFile:                     maxActionStreamBytes,
	providerEventsFile:              maxEventStreamBytes,
	unparsedFile:                    maxActionStreamBytes,
	processDir + "/" + resultFile:   maxDocumentBytes,
	gitDir + "/" + resultFile:       maxDocumentBytes,
	verifyDir + "/" + verifyResults: maxDocumentBytes,
	providerUsageFile:               maxDocumentBytes,
}

type viewFileIdentity struct {
	present bool
	info    os.FileInfo
	digest  [sha256.Size]byte
}

func (s *viewSnapshot) Close() error {
	var errs []error
	if s.actions != nil {
		errs = append(errs, s.actions.Close())
		s.actions = nil
	}

	if s.events != nil {
		errs = append(errs, s.events.Close())
		s.events = nil
	}

	if s.unparsed != nil {
		errs = append(errs, s.unparsed.Close())
		s.unparsed = nil
	}

	return errors.Join(errs...)
}

type viewSnapshotStore struct {
	root      string
	sem       chan struct{}
	mu        sync.RWMutex
	byID      map[string]*viewSnapshot
	ids       []string
	closed    bool
	afterCopy func()
}

func newViewSnapshotStore(root string) *viewSnapshotStore {
	return &viewSnapshotStore{root: root, sem: make(chan struct{}, 2), byID: make(map[string]*viewSnapshot)}
}

func captureViewRun(source *os.Root, expected map[string]viewFileIdentity, snapshot *viewSnapshot) error {
	snapshot.documents = make(map[string][]byte)
	for _, name := range viewSnapshotFiles {
		identity := expected[name]
		if !identity.present {
			continue
		}
		switch name {
		case actionsFile, providerEventsFile, unparsedFile:
			file, size, err := captureViewStream(source, name, identity)
			if err != nil {
				return err
			}
			switch name {
			case actionsFile:
				snapshot.actions, snapshot.actionSize = file, size
			case providerEventsFile:
				snapshot.events, snapshot.eventSize = file, size
			case unparsedFile:
				snapshot.unparsed = file
			}
		default:
			raw, err := captureViewDocument(source, name, identity)
			if err != nil {
				return err
			}
			snapshot.documents[name] = raw
		}
	}
	return nil
}

func captureViewDocument(source *os.Root, name string, expected viewFileIdentity) ([]byte, error) {
	file, err := openRegularFromRoot(source, name)
	if err != nil {
		return nil, fmt.Errorf("cli: run changed while capturing %s for viewer snapshot: %w", name, err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("cli: inspect %s while capturing viewer snapshot: %w", name, err)
	}
	if !sameViewFileMetadata(expected.info, info) {
		file.Close()
		return nil, fmt.Errorf("cli: run changed while capturing %s for viewer snapshot", name)
	}
	raw := make([]byte, info.Size())
	_, readErr := io.ReadFull(file, raw)
	after, statErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("cli: capture %s for viewer snapshot: %w", name, readErr)
	}
	if statErr != nil {
		return nil, fmt.Errorf("cli: inspect %s after viewer snapshot capture: %w", name, statErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("cli: close %s after viewer snapshot capture: %w", name, closeErr)
	}
	if !sameViewFileMetadata(expected.info, after) || sha256.Sum256(raw) != expected.digest {
		return nil, fmt.Errorf("cli: run changed while capturing %s for viewer snapshot", name)
	}
	return raw, nil
}

func captureViewStream(source *os.Root, name string, expected viewFileIdentity) (*os.File, int64, error) {
	return captureViewStreamWithUnlink(source, name, expected, os.Remove)
}

// The snapshot resists source-artifact mutation and ordinary concurrent writes.
// An actively malicious process running as the same OS user is outside this
// boundary; isolating that actor requires a separate UID or an OS sandbox.
func captureViewStreamWithUnlink(source *os.Root, name string, expected viewFileIdentity, unlink func(string) error) (*os.File, int64, error) {
	file, err := openRegularFromRoot(source, name)
	if err != nil {
		return nil, 0, fmt.Errorf("cli: run changed while capturing %s for viewer snapshot: %w", name, err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, fmt.Errorf("cli: inspect %s while capturing viewer snapshot: %w", name, err)
	}
	if !sameViewFileMetadata(expected.info, info) {
		file.Close()
		return nil, 0, fmt.Errorf("cli: run changed while capturing %s for viewer snapshot", name)
	}
	copyFile, err := os.CreateTemp("", "agentrec-view-snapshot-*")
	if err != nil {
		file.Close()
		return nil, 0, fmt.Errorf("cli: create viewer stream snapshot: %w", err)
	}
	temp := copyFile.Name()
	cleanup := func(err error, readFile *os.File) (*os.File, int64, error) {
		var readCloseErr error
		if readFile != nil {
			readCloseErr = readFile.Close()
		}
		return nil, 0, errors.Join(err, file.Close(), copyFile.Close(), readCloseErr, os.Remove(temp))
	}
	hash := sha256.New()
	_, copyErr := io.CopyN(io.MultiWriter(copyFile, hash), file, info.Size())
	after, statErr := file.Stat()
	if copyErr != nil {
		return cleanup(fmt.Errorf("cli: capture %s for viewer snapshot: %w", name, copyErr), nil)
	}
	if statErr != nil {
		return cleanup(fmt.Errorf("cli: inspect %s after viewer snapshot capture: %w", name, statErr), nil)
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	if !sameViewFileMetadata(expected.info, after) || digest != expected.digest {
		return cleanup(fmt.Errorf("cli: run changed while capturing %s for viewer snapshot", name), nil)
	}
	readFile, err := os.Open(temp)
	if err != nil {
		return cleanup(fmt.Errorf("cli: reopen viewer stream snapshot read-only: %w", err), nil)
	}
	readInfo, err := readFile.Stat()
	if err != nil {
		return cleanup(fmt.Errorf("cli: inspect read-only viewer stream snapshot: %w", err), readFile)
	}
	writeInfo, err := copyFile.Stat()
	if err != nil {
		return cleanup(fmt.Errorf("cli: inspect writable viewer stream snapshot: %w", err), readFile)
	}
	if !os.SameFile(readInfo, writeInfo) || readInfo.Size() != info.Size() {
		return cleanup(errors.New("cli: viewer stream snapshot changed before it was pinned"), readFile)
	}
	readHash := sha256.New()
	if _, err := io.CopyN(readHash, readFile, info.Size()); err != nil {
		return cleanup(fmt.Errorf("cli: verify viewer stream snapshot: %w", err), readFile)
	}
	var readDigest [sha256.Size]byte
	copy(readDigest[:], readHash.Sum(nil))
	if readDigest != expected.digest {
		return cleanup(errors.New("cli: viewer stream snapshot changed before it was pinned"), readFile)
	}
	if err := unlink(temp); err != nil {
		return cleanup(fmt.Errorf("cli: unlink viewer stream snapshot: %w", err), readFile)
	}
	if err := file.Close(); err != nil {
		return cleanup(fmt.Errorf("cli: close %s after viewer snapshot capture: %w", name, err), readFile)
	}
	if err := copyFile.Close(); err != nil {
		readFile.Close()
		return nil, 0, fmt.Errorf("cli: close writable viewer stream snapshot: %w", err)
	}
	return readFile, info.Size(), nil
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
	sourceRoot, err := openRunRoot(s.root, runID)
	if err != nil {
		return viewRunResponse{}, err
	}
	snapshot := &viewSnapshot{}
	fail := func(err error) (viewRunResponse, error) {
		err = errors.Join(err, snapshot.Close())
		if sourceRoot != nil {
			err = errors.Join(err, sourceRoot.Close())
		}
		return viewRunResponse{}, err
	}
	before, err := viewRunFingerprint(sourceRoot)
	if err != nil {
		return fail(err)
	}
	if err := captureViewRun(sourceRoot, before, snapshot); err != nil {
		return fail(err)
	}
	if s.afterCopy != nil {
		s.afterCopy()
	}

	manifestRaw, ok := snapshot.documents[manifestFile]
	if !ok {
		return fail(fmt.Errorf("cli: open %s: %w", manifestFile, os.ErrNotExist))
	}
	manifest, err := decodeManifest(manifestRaw)
	if err != nil {
		return fail(err)
	}
	if err := validateUnparsedCount(manifest.UnparsedLines); err != nil {
		return fail(err)
	}
	if manifest.UnparsedLines > 0 {
		unparsedBefore, err := viewFileFingerprint(sourceRoot, unparsedFile)
		if err != nil {
			return fail(err)
		}
		if unparsedBefore.present {
			snapshot.unparsed, _, err = captureViewStream(sourceRoot, unparsedFile, unparsedBefore)
			if err != nil {
				return fail(err)
			}
		}
		unparsedAfter, err := viewFileFingerprint(sourceRoot, unparsedFile)
		if err != nil {
			return fail(err)
		}
		if !sameViewFileIdentity(unparsedBefore, unparsedAfter) {
			return fail(errors.New("cli: run changed while the viewer snapshot was being created; retry"))
		}
	}
	if err := validateCapturedUnparsed(snapshot, manifest.UnparsedLines); err != nil {
		return fail(err)
	}
	prompt, err := decodeViewPrompt(snapshot.documents[promptFile], snapshot.documents[promptFile] != nil)
	if err != nil {
		return fail(err)
	}
	evidence, err := readCapturedViewEvidence(snapshot, manifest)
	if err != nil {
		return fail(err)
	}
	if snapshot.actions == nil {
		return fail(fmt.Errorf("cli: open %s: %w", actionsFile, os.ErrNotExist))
	}
	actionCount, err := countViewActions(snapshot.actions, snapshot.actionSize)
	if err != nil {
		return fail(err)
	}
	eventCount := 0
	if snapshot.events != nil {
		eventCount, err = countViewEvents(snapshot.events, snapshot.eventSize)
		if err != nil {
			return fail(err)
		}
	}
	after, err := viewRunFingerprint(sourceRoot)
	if err != nil {
		return fail(err)
	}
	if !sameViewFingerprint(before, after) {
		return fail(errors.New("cli: run changed while the viewer snapshot was being created; retry"))
	}
	snapshot.documents = nil
	if err := sourceRoot.Close(); err != nil {
		return fail(fmt.Errorf("cli: close source run after viewer snapshot: %w", err))
	}
	sourceRoot = nil
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return fail(fmt.Errorf("cli: create viewer snapshot: %w", err))
	}
	snapshot.id = hex.EncodeToString(tokenBytes)
	if err := s.add(snapshot); err != nil {
		return fail(err)
	}

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
		identity, err := viewFileFingerprint(root, name)
		if err != nil {
			return nil, err
		}
		fingerprint[name] = identity
	}
	return fingerprint, nil
}

func viewFileFingerprint(root *os.Root, name string) (viewFileIdentity, error) {
	file, err := openRegularFromRoot(root, name)
	if errors.Is(err, os.ErrNotExist) {
		return viewFileIdentity{}, nil
	}
	if err != nil {
		return viewFileIdentity{}, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return viewFileIdentity{}, fmt.Errorf("cli: inspect %s for viewer snapshot: %w", name, err)
	}
	limit := viewSnapshotFileLimits[name]
	if info.Size() > limit {
		file.Close()
		return viewFileIdentity{}, fmt.Errorf("cli: %s is larger than %d bytes", name, limit)
	}
	hash := sha256.New()
	if _, err := io.CopyN(hash, file, info.Size()); err != nil {
		file.Close()
		return viewFileIdentity{}, fmt.Errorf("cli: fingerprint %s for viewer snapshot: %w", name, err)
	}
	if err := file.Close(); err != nil {
		return viewFileIdentity{}, fmt.Errorf("cli: close %s after viewer fingerprint: %w", name, err)
	}
	identity := viewFileIdentity{present: true, info: info}
	copy(identity.digest[:], hash.Sum(nil))
	return identity, nil
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
		if !sameViewFileMetadata(left.info, right.info) || left.digest != right.digest {
			return false
		}
	}
	return true
}

func sameViewFileMetadata(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && a.Mode() == b.Mode()
}

func sameViewFileIdentity(a, b viewFileIdentity) bool {
	return a.present == b.present && (!a.present || sameViewFileMetadata(a.info, b.info) && a.digest == b.digest)
}

func capturedViewDocument(snapshot *viewSnapshot, name string) ([]byte, error) {
	raw, ok := snapshot.documents[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return raw, nil
}

func decodeViewPrompt(raw []byte, present bool) (string, error) {
	if !present {
		return "", nil
	}
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("cli: %s is not valid UTF-8", promptFile)
	}
	return strings.TrimSuffix(string(raw), "\n"), nil
}

func validateCapturedUnparsed(snapshot *viewSnapshot, want int) error {
	validationErr := validateUnparsedCount(want)
	if validationErr == nil && want > 0 {
		switch {
		case snapshot.unparsed == nil:
			validationErr = os.ErrNotExist
		default:
			if _, err := snapshot.unparsed.Seek(0, io.SeekStart); err != nil {
				validationErr = fmt.Errorf("cli: rewind %s: %w", unparsedFile, err)
			} else {
				validationErr = validateUnparsedFile(snapshot.unparsed, want)
			}
		}
	}
	if snapshot.unparsed != nil {
		validationErr = errors.Join(validationErr, snapshot.unparsed.Close())
		snapshot.unparsed = nil
	}
	return validationErr
}

func readCapturedViewEvidence(snapshot *viewSnapshot, manifest storage.Manifest) (viewEvidence, error) {
	resultRaw, resultReadErr := capturedViewDocument(snapshot, processDir+"/"+resultFile)
	result, err := decodeProcessResult(resultRaw, resultReadErr)
	if err != nil {
		return viewEvidence{}, err
	}
	gitRaw, gitReadErr := capturedViewDocument(snapshot, gitDir+"/"+resultFile)
	git, err := decodeGitResult(gitRaw, gitReadErr)
	if err != nil {
		return viewEvidence{}, err
	}
	verificationRaw, verificationReadErr := capturedViewDocument(snapshot, verifyDir+"/"+verifyResults)
	var verification *evidence.VerificationResult
	switch {
	case errors.Is(verificationReadErr, os.ErrNotExist):
	case verificationReadErr != nil:
		return viewEvidence{}, verificationReadErr
	default:
		verification, err = decodeVerification(verificationRaw, verifyDir+"/"+verifyResults)
		if err != nil {
			return viewEvidence{}, err
		}
	}
	usageRaw, usageReadErr := capturedViewDocument(snapshot, providerUsageFile)
	usage, err := decodeProviderUsage(usageRaw, usageReadErr, manifest.Provider)
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

func (s *viewSnapshotStore) add(snapshot *viewSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("cli: viewer snapshot store is closed")
	}
	s.byID[snapshot.id] = snapshot
	s.ids = append(s.ids, snapshot.id)
	if len(s.ids) <= maxViewSnapshots {
		return nil
	}
	oldID := s.ids[0]
	s.ids = s.ids[1:]
	old := s.byID[oldID]
	delete(s.byID, oldID)
	if err := old.Close(); err != nil {
		delete(s.byID, snapshot.id)
		s.ids = s.ids[:len(s.ids)-1]
		return fmt.Errorf("cli: evict viewer snapshot %s: %w", oldID, err)
	}
	return nil
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
	s.closed = true
	var errs []error
	for _, snapshot := range s.byID {
		errs = append(errs, snapshot.Close())
	}
	s.byID = make(map[string]*viewSnapshot)
	s.ids = nil
	return errors.Join(errs...)
}
