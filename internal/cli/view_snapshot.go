package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
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
	maxViewSnapshots = 10
)

type viewAction struct {
	action.Action
	SamePathObserved []string `json:"samePathObserved,omitempty"`
}

type viewActionPage struct {
	Items      []viewAction `json:"items"`
	NextCursor *int64       `json:"nextCursor,omitempty"`
	// EndCursor is the offset after the last item, present even at the end
	// of the stream: a page that follows a run as it grows resumes there.
	EndCursor int64 `json:"endCursor"`
}

type viewEventPage struct {
	Items      []json.RawMessage `json:"items"`
	NextCursor *int64            `json:"nextCursor,omitempty"`
	EndCursor  int64             `json:"endCursor"`
}

type viewSnapshot struct {
	id                string
	documents         map[string][]byte
	actions           *os.File
	actionSize        int64
	events            *os.File
	eventSize         int64
	unparsed          *os.File
	patch             *os.File
	patchSize         int64
	patchSections     map[string]viewPatchSection
	changes           []viewChange
	changePaths       map[string]struct{}
	cwd               string
	repoRoot          string
	changeStatus      string
	changeReason      string
	changeAttribution string
	changeBaseline    string
}

var viewSnapshotFiles = []string{
	manifestFile,
	promptFile,
	actionsFile,
	providerEventsFile,
	processDir + "/" + resultFile,
	gitDir + "/" + resultFile,
	gitDir + "/" + trackedStatFile,
	gitDir + "/" + untrackedChangesFile,
	gitDir + "/" + trackedPatchFile,
	verifyDir + "/" + verifyResults,
	verifyPosthocDir + "/" + verifyResults,
	verifyPosthocDir + "/" + verifyPosthocMeta,
	providerUsageFile,
}

var viewSnapshotFileLimits = map[string]int64{
	manifestFile:                               maxDocumentBytes,
	promptFile:                                 maxDocumentBytes,
	actionsFile:                                maxActionStreamBytes,
	providerEventsFile:                         maxEventStreamBytes,
	unparsedFile:                               maxActionStreamBytes,
	processDir + "/" + resultFile:              maxDocumentBytes,
	gitDir + "/" + resultFile:                  maxDocumentBytes,
	gitDir + "/" + trackedStatFile:             maxViewChangeDocBytes,
	gitDir + "/" + untrackedChangesFile:        maxViewChangeDocBytes,
	gitDir + "/" + trackedPatchFile:            maxViewPatchBytes,
	verifyDir + "/" + verifyResults:            maxDocumentBytes,
	verifyPosthocDir + "/" + verifyResults:     maxDocumentBytes,
	verifyPosthocDir + "/" + verifyPosthocMeta: maxDocumentBytes,
	providerUsageFile:                          maxDocumentBytes,
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
	if s.patch != nil {
		errs = append(errs, s.patch.Close())
		s.patch = nil
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
	cache     *viewStreamCache
}

func newViewSnapshotStore(root string) *viewSnapshotStore {
	return &viewSnapshotStore{root: root, sem: make(chan struct{}, 2), byID: make(map[string]*viewSnapshot), cache: newViewStreamCache(filepath.Join(filepath.Dir(root), viewCacheDirName))}
}

// A run being followed is snapshotted every few seconds, and each snapshot
// used to copy its whole action and event streams. The cache keeps one
// growing copy per run and stream in a directory of this process's own
// beside the runs: a later snapshot appends only what the source gained
// since the copy was made, and every snapshot reads the copy through a
// section bounded by the size it captured, so appends never move under a
// reader. A source that is another file, that shrank, or whose last bytes
// no longer match the copy is copied afresh. Bounded by entries and bytes,
// oldest out first; gone with the store, and a viewer that died leaves a
// directory the next one removes.
const (
	viewCacheDirName = "viewer-cache"
	viewCacheTail    = 64 << 10
)

var (
	viewCacheEntries = 16
	viewCacheBytes   = int64(1 << 30)
)

type viewStreamCache struct {
	base  string
	dir   string
	mu    sync.Mutex
	ready bool
	items map[string]*cachedStream
	order []string
}

type cachedStream struct {
	path string
	size int64
	src  os.FileInfo
}

func newViewStreamCache(base string) *viewStreamCache {
	return &viewStreamCache{base: base, items: map[string]*cachedStream{}}
}

// prepare makes this store's own cache directory, named after the process
// that owns it, and removes the directories of viewers no longer alive.
func (c *viewStreamCache) prepare() error {
	if c.ready {
		return nil
	}
	if err := os.MkdirAll(c.base, 0o700); err != nil {
		return fmt.Errorf("cli: create viewer cache: %w", err)
	}
	entries, err := os.ReadDir(c.base)
	if err != nil {
		return fmt.Errorf("cli: read viewer cache: %w", err)
	}
	for _, entry := range entries {
		owner, _, _ := strings.Cut(entry.Name(), "-")
		pid, err := strconv.Atoi(owner)
		if err != nil || processAlive(pid) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(c.base, entry.Name())); err != nil {
			return fmt.Errorf("cli: remove a stale viewer cache: %w", err)
		}
	}
	dir, err := os.MkdirTemp(c.base, strconv.Itoa(os.Getpid())+"-")
	if err != nil {
		return fmt.Errorf("cli: create viewer cache: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("cli: restrict viewer cache: %w", err)
	}
	c.dir = dir
	c.ready = true
	return nil
}

// capture returns a read-only handle on the cached copy of an append-only
// stream, brought up to size from the source, and the size to read it to.
func (c *viewStreamCache) capture(ctx context.Context, runID, name string, source *os.File, info os.FileInfo, size int64) (*os.File, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.prepare(); err != nil {
		return nil, err
	}
	key := runID + "/" + strings.ReplaceAll(name, "/", "_")
	entry, ok := c.items[key]
	if ok && (!os.SameFile(entry.src, info) || size < entry.size || !c.tailMatches(entry, source)) {
		// Not the file that was copied, shorter than it, or rewritten
		// under the same name: start over.
		os.Remove(entry.path)
		delete(c.items, key)
		ok = false
	}
	if !ok {
		entry = &cachedStream{path: filepath.Join(c.dir, strings.ReplaceAll(key, "/", "--")), size: 0}
		if err := os.RemoveAll(entry.path); err != nil {
			return nil, fmt.Errorf("cli: reset viewer cache entry: %w", err)
		}
	}
	if !ok || size > entry.size {
		w, err := os.OpenFile(entry.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return nil, fmt.Errorf("cli: open viewer cache entry: %w", err)
		}
		n, err := io.Copy(w, viewContextReader{ctx: ctx, reader: io.NewSectionReader(source, entry.size, size-entry.size)})
		if closeErr := w.Close(); err == nil {
			err = closeErr
		}
		// A source that shrank between the look and the copy gives fewer
		// bytes than were asked for; recording the size as reached would
		// leave a hole every later append reads across.
		if err == nil && n != size-entry.size {
			err = fmt.Errorf("the stream holds %d of the %d bytes seen", entry.size+n, size)
		}
		if err != nil {
			os.Remove(entry.path)
			delete(c.items, key)
			return nil, fmt.Errorf("cli: extend viewer cache entry: %w", err)
		}
		entry.size = size
	}
	entry.src = info
	if !ok {
		c.items[key] = entry
	}
	c.touch(key)
	// Opened before anything is evicted: the reader must hold the copy it
	// was promised even when this very entry is what the cap gives up.
	copied, err := os.Open(entry.path)
	if err != nil {
		return nil, err
	}
	c.evict()
	return copied, nil
}

// tailMatches reports whether the last bytes of the copy are still the
// source's: a stream rewritten in place under the same inode is caught
// here, since an append-only stream never changes what it already holds.
func (c *viewStreamCache) tailMatches(entry *cachedStream, source *os.File) bool {
	n := min(entry.size, viewCacheTail)
	if n == 0 {
		return true
	}
	copied, err := os.Open(entry.path)
	if err != nil {
		return false
	}
	defer copied.Close()
	ours := make([]byte, n)
	theirs := make([]byte, n)
	if _, err := copied.ReadAt(ours, entry.size-n); err != nil {
		return false
	}
	if _, err := source.ReadAt(theirs, entry.size-n); err != nil {
		return false
	}
	return bytes.Equal(ours, theirs)
}

func (c *viewStreamCache) touch(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, key)
}

func (c *viewStreamCache) evict() {
	var total int64
	for _, e := range c.items {
		total += e.size
	}
	// The newest entry is never given up: it is what the snapshot just
	// asked for, and dropping it would make a cache smaller than one
	// stream copy the whole thing again on every single snapshot.
	for len(c.order) > 1 && (len(c.order) > viewCacheEntries || total > viewCacheBytes) {
		key := c.order[0]
		c.order = c.order[1:]
		if e, ok := c.items[key]; ok {
			total -= e.size
			os.Remove(e.path)
			delete(c.items, key)
		}
	}
}

func (c *viewStreamCache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = map[string]*cachedStream{}
	c.order = nil
	if !c.ready {
		return nil
	}
	c.ready = false
	return os.RemoveAll(c.dir)
}

type viewContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r viewContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func captureViewRun(source *os.Root, expected map[string]viewFileIdentity, snapshot *viewSnapshot) error {
	return captureViewRunContext(context.Background(), source, expected, snapshot)
}

func captureViewRunContext(ctx context.Context, source *os.Root, expected map[string]viewFileIdentity, snapshot *viewSnapshot) error {
	return captureViewRunCached(ctx, source, expected, snapshot, nil, "")
}

// captureViewRunCached captures a run, taking its append-only streams
// through the cache when one is given.
func captureViewRunCached(ctx context.Context, source *os.Root, expected map[string]viewFileIdentity, snapshot *viewSnapshot, cache *viewStreamCache, runID string) error {
	snapshot.documents = make(map[string][]byte)
	for _, name := range viewSnapshotFiles {
		if err := ctx.Err(); err != nil {
			return err
		}
		identity := expected[name]
		if !identity.present {
			continue
		}
		switch name {
		case actionsFile, providerEventsFile, unparsedFile, gitDir + "/" + trackedPatchFile:
			var file *os.File
			var size int64
			var err error
			if cache != nil && viewAppendOnly[name] {
				file, size, err = captureViewStreamCached(ctx, source, name, identity, cache, runID)
			} else {
				file, size, err = captureViewStreamContext(ctx, source, name, identity)
			}
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
			case gitDir + "/" + trackedPatchFile:
				snapshot.patch, snapshot.patchSize = file, size
			}
		default:
			raw, err := captureViewDocumentContext(ctx, source, name, identity)
			if err != nil {
				return err
			}
			snapshot.documents[name] = raw
		}
	}
	return nil
}

func captureViewDocument(source *os.Root, name string, expected viewFileIdentity) ([]byte, error) {
	return captureViewDocumentContext(context.Background(), source, name, expected)
}

func captureViewDocumentContext(ctx context.Context, source *os.Root, name string, expected viewFileIdentity) ([]byte, error) {
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
	_, readErr := io.ReadFull(viewContextReader{ctx: ctx, reader: file}, raw)
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

// captureViewStreamCached brings the cache's copy of an append-only stream
// up to the size seen at the first look and hands back a reader on it.
func captureViewStreamCached(ctx context.Context, source *os.Root, name string, expected viewFileIdentity, cache *viewStreamCache, runID string) (*os.File, int64, error) {
	file, err := openRegularFromRoot(source, name)
	if err != nil {
		return nil, 0, fmt.Errorf("cli: run changed while capturing %s for viewer snapshot: %w", name, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("cli: inspect %s while capturing viewer snapshot: %w", name, err)
	}
	if !viewStreamStill(name, expected.info, info) {
		return nil, 0, fmt.Errorf("cli: run changed while capturing %s for viewer snapshot", name)
	}
	size := expected.info.Size()
	copied, err := cache.capture(ctx, runID, name, file, info, size)
	if err != nil {
		return nil, 0, err
	}
	after, err := file.Stat()
	if err != nil || !viewStreamStill(name, expected.info, after) {
		copied.Close()
		return nil, 0, fmt.Errorf("cli: run changed while capturing %s for viewer snapshot", name)
	}
	return copied, size, nil
}

func captureViewStream(source *os.Root, name string, expected viewFileIdentity) (*os.File, int64, error) {
	return captureViewStreamContext(context.Background(), source, name, expected)
}

func captureViewStreamContext(ctx context.Context, source *os.Root, name string, expected viewFileIdentity) (*os.File, int64, error) {
	return captureViewStreamWithUnlinkContext(ctx, source, name, expected, os.Remove)
}

// The snapshot resists source-artifact mutation and ordinary concurrent writes.
// An actively malicious process running as the same OS user is outside this
// boundary; isolating that actor requires a separate UID or an OS sandbox.
func captureViewStreamWithUnlink(source *os.Root, name string, expected viewFileIdentity, unlink func(string) error) (*os.File, int64, error) {
	return captureViewStreamWithUnlinkContext(context.Background(), source, name, expected, unlink)
}

func captureViewStreamWithUnlinkContext(ctx context.Context, source *os.Root, name string, expected viewFileIdentity, unlink func(string) error) (*os.File, int64, error) {
	file, err := openRegularFromRoot(source, name)
	if err != nil {
		return nil, 0, fmt.Errorf("cli: run changed while capturing %s for viewer snapshot: %w", name, err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, fmt.Errorf("cli: inspect %s while capturing viewer snapshot: %w", name, err)
	}
	// An append-only stream may have grown since it was looked at: the
	// snapshot takes the prefix that was there then, which is a consistent
	// view of a run still being recorded. Anything else must be unchanged.
	if !viewStreamStill(name, expected.info, info) {
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
	size := expected.info.Size()
	_, copyErr := io.CopyN(io.MultiWriter(copyFile, hash), viewContextReader{ctx: ctx, reader: file}, size)
	after, statErr := file.Stat()
	if copyErr != nil {
		return cleanup(fmt.Errorf("cli: capture %s for viewer snapshot: %w", name, copyErr), nil)
	}
	if statErr != nil {
		return cleanup(fmt.Errorf("cli: inspect %s after viewer snapshot capture: %w", name, statErr), nil)
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	if !viewStreamStill(name, expected.info, after) || (!viewAppendOnly[name] && digest != expected.digest) {
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
	if !os.SameFile(readInfo, writeInfo) || readInfo.Size() != size {
		return cleanup(errors.New("cli: viewer stream snapshot changed before it was pinned"), readFile)
	}
	readHash := sha256.New()
	if _, err := io.CopyN(readHash, readFile, size); err != nil {
		return cleanup(fmt.Errorf("cli: verify viewer stream snapshot: %w", err), readFile)
	}
	var readDigest [sha256.Size]byte
	copy(readDigest[:], readHash.Sum(nil))
	if readDigest != digest || (!viewAppendOnly[name] && readDigest != expected.digest) {
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
	return readFile, size, nil
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
	return s.createContext(context.Background(), runID)
}

func (s *viewSnapshotStore) createContext(ctx context.Context, runID string) (viewRunResponse, error) {
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
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := captureViewRunCached(ctx, sourceRoot, before, snapshot, s.cache, runID); err != nil {
		return fail(err)
	}
	if s.afterCopy != nil {
		s.afterCopy()
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
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
	canonicalCWD := manifest.CanonicalCWD
	if canonicalCWD == "" {
		canonicalCWD = manifest.CWD
	}
	if !validViewRepositoryRoot(canonicalCWD, manifest.RepoRoot) {
		return fail(errors.New("cli: manifest repository root must be an absolute ancestor of cwd"))
	}
	snapshot.cwd = canonicalCWD
	snapshot.repoRoot = manifest.RepoRoot
	if snapshot.repoRoot == "" {
		snapshot.repoRoot = manifest.CWD
	}
	if manifest.UnparsedLines > 0 {
		unparsedBefore, err := viewFileFingerprint(sourceRoot, unparsedFile)
		if err != nil {
			return fail(err)
		}
		if unparsedBefore.present {
			snapshot.unparsed, _, err = captureViewStreamContext(ctx, sourceRoot, unparsedFile, unparsedBefore)
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
	if err := prepareViewChanges(snapshot); err != nil {
		return fail(err)
	}
	snapshot.changePaths = make(map[string]struct{}, len(snapshot.changes))
	for _, change := range snapshot.changes {
		snapshot.changePaths[change.Path] = struct{}{}
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
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return fail(fmt.Errorf("cli: create viewer snapshot: %w", err))
	}
	snapshot.id = hex.EncodeToString(tokenBytes)
	if err := s.addContext(ctx, snapshot); err != nil {
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
			StartedAt: manifest.StartedAt, EndedAt: manifest.EndedAt, ExitReason: viewExitReason(manifest),
			WarningCount: manifest.WarningCount, UnparsedLines: manifest.UnparsedLines,
			VersionUnverified: manifest.VersionUnverified,
			Mode:              manifest.Mode, SessionID: manifest.SessionID,
		},
		ProviderEvents: viewProviderEvents{Attribution: "provider_reported", Present: snapshot.events != nil},
		Changes:        summarizeViewChanges(snapshot),
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

// viewAppendOnly names the streams a recorder only ever appends to. A run
// still being recorded grows them between the two looks a snapshot takes;
// that is growth, not change, and reading them up to the size seen at
// capture is a consistent view. They are not hashed — twice per snapshot,
// every few seconds, over tens of megabytes — but their identity and
// non-shrinking size are still checked.
var viewAppendOnly = map[string]bool{actionsFile: true, providerEventsFile: true}

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
	if viewAppendOnly[name] {
		file.Close()
		return viewFileIdentity{present: true, info: info}, nil
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
		if viewAppendOnly[name] {
			if !os.SameFile(left.info, right.info) || right.info.Size() < left.info.Size() || left.info.Mode() != right.info.Mode() {
				return false
			}
			continue
		}
		if !sameViewFileMetadata(left.info, right.info) || left.digest != right.digest {
			return false
		}
	}
	return true
}

// viewStreamStill says whether a stream file is still the one that was
// looked at: unchanged, or — for an append-only stream — the same file, no
// shorter.
func viewStreamStill(name string, before, now os.FileInfo) bool {
	if viewAppendOnly[name] {
		return os.SameFile(before, now) && now.Size() >= before.Size() && before.Mode() == now.Mode()
	}
	return sameViewFileMetadata(before, now)
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
	repository, verificationSummary := appendSessionEvidence(manifest, repositoryFields(git), verificationFields(verification))
	posthoc, err := viewPosthocVerificationOf(snapshot)
	if err != nil {
		return viewEvidence{}, err
	}
	if posthoc != nil {
		posthoc.OwnRan = verification != nil
	}
	return viewEvidence{
		ProviderUsage:       viewFields(providerUsageFields(usage)),
		Supervisor:          viewFields(supervisorFields(manifest, result)),
		Repository:          viewFields(repository),
		Verification:        viewFields(verificationSummary),
		PosthocVerification: posthoc,
	}, nil
}

// viewPosthocVerificationOf reads a later verification captured with the
// snapshot, nil when the run has none.
func viewPosthocVerificationOf(snapshot *viewSnapshot) (*viewPosthocVerification, error) {
	name := verifyPosthocDir + "/" + verifyResults
	raw, err := capturedViewDocument(snapshot, name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result, err := decodeVerificationAs(raw, name, posthocAttribution)
	if err != nil {
		return nil, err
	}
	meta, err := decodePosthocMeta(capturedViewDocument(snapshot, verifyPosthocDir+"/"+verifyPosthocMeta))
	if err != nil {
		return nil, err
	}
	return newViewPosthocVerification(result, meta), nil
}

func newViewPosthocVerification(result *evidence.VerificationResult, meta *posthocMeta) *viewPosthocVerification {
	out := &viewPosthocVerification{Status: verdict(result.Status), Caveat: posthocCaveat(meta), Fields: viewFields(verificationFields(result))}
	if meta != nil {
		out.MeasuredAt = meta.MeasuredAt
		out.HeadMovedSince = meta.HeadMovedSince
	}
	return out
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
	return s.addContext(context.Background(), snapshot)
}

func (s *viewSnapshotStore) addContext(ctx context.Context, snapshot *viewSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
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

func normalizeViewActionPath(value, cwd, repoRoot string) string {
	if value == "" || strings.IndexByte(value, 0) >= 0 {
		return ""
	}
	root := path.Clean(repoRoot)
	working := path.Clean(cwd)
	if root == "." || working == "." {
		return ""
	}
	clean := path.Clean(value)
	if !strings.HasPrefix(clean, "/") {
		prefix := ""
		if root != working {
			if root == "/" {
				prefix = strings.TrimPrefix(working, "/")
			} else if strings.HasPrefix(working, root+"/") {
				prefix = strings.TrimPrefix(working, root+"/")
			} else {
				return ""
			}
		}
		clean = path.Clean(path.Join(prefix, clean))
	} else {
		if root == "/" {
			clean = strings.TrimPrefix(clean, "/")
		} else if root == "." || !strings.HasPrefix(clean, root+"/") {
			return ""
		} else {
			clean = strings.TrimPrefix(clean, root+"/")
		}
	}
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return ""
	}
	return clean
}

func validViewRepositoryRoot(cwd, repoRoot string) bool {
	if repoRoot == "" {
		return true
	}
	if !path.IsAbs(cwd) || !path.IsAbs(repoRoot) {
		return false
	}
	cleanCWD := path.Clean(cwd)
	cleanRoot := path.Clean(repoRoot)
	return cleanRoot == "/" || cleanCWD == cleanRoot || strings.HasPrefix(cleanCWD, cleanRoot+"/")
}

func viewSamePathObservations(item action.Action, cwd, repoRoot string, changed map[string]struct{}) []string {
	switch item.Type {
	case action.TypeFileRead, action.TypeFileWrite, action.TypeFileEdit:
	default:
		return nil
	}
	candidates := item.RepositoryPaths
	persisted := item.RepositoryPathsRecorded
	if !persisted {
		candidates = explicitActionPathInputs(item)
	}
	seen := make(map[string]struct{}, len(candidates))
	observed := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		relative := candidate
		if !persisted {
			relative = normalizeViewActionPath(candidate, cwd, repoRoot)
		}
		if _, ok := changed[relative]; !ok || relative == "" {
			continue
		}
		if _, duplicate := seen[relative]; duplicate {
			continue
		}
		seen[relative] = struct{}{}
		observed = append(observed, relative)
	}
	return observed
}

func readViewActionPage(snapshot *viewSnapshot, cursor int64) (viewActionPage, error) {
	if cursor > snapshot.actionSize {
		return viewActionPage{}, errors.New("cursor is outside the action stream")
	}
	scanner := bufio.NewScanner(io.NewSectionReader(snapshot.actions, cursor, snapshot.actionSize-cursor))
	scanner.Buffer(nil, maxActionBytes)
	page := viewActionPage{Items: make([]viewAction, 0, viewPageSize)}
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
		page.Items = append(page.Items, viewAction{Action: item, SamePathObserved: viewSamePathObservations(item, snapshot.cwd, snapshot.repoRoot, snapshot.changePaths)})
		pageBytes += len(line)
		if len(page.Items) == viewPageSize {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return viewActionPage{}, fmt.Errorf("cli: read %s page: %w", actionsFile, err)
	}
	page.NextCursor = viewNextCursor(position, snapshot.actionSize)
	page.EndCursor = position
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
	page.EndCursor = position
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
	if s.cache != nil {
		errs = append(errs, s.cache.Close())
	}
	return errors.Join(errs...)
}

// viewExitReason is the exit reason the viewer shows. A traced run keeps the
// manifest's own word, empty while it runs; a session bundle goes through the
// same reading as the terminal report, so an open session says running and one
// whose recorder is gone without a result says unknown, in both places.
func viewExitReason(m storage.Manifest) string {
	if m.Mode != storage.ModeSession {
		return m.ExitReason
	}
	return exitReason(m, nil)
}
