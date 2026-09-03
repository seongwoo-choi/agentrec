package cli

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/storage"
)

const (
	viewRunIndexFile    = "run-index-v1"
	viewRunIndexLock    = ".run-index-v1.lock"
	viewRunIndexDirty   = ".run-index-v1.dirty"
	viewRunIndexHeader  = "agentrec-run-index-v1"
	viewRunIndexMaxLine = 512
)

type viewRunIndexEntry struct {
	id        string
	startedAt time.Time
}

var beforeViewRunIndexRebuildPublish func()

func createIndexedRun(root, runID string, manifest storage.Manifest) (*storage.Bundle, error) {
	bundle, err := storage.Create(root, runID, manifest)
	if err != nil {
		return nil, err
	}
	// The index is a rebuildable read cache. A failed update must not turn a
	// successfully opened evidence bundle into an uncloseable partial run.
	_ = updateViewRunIndex(root, func(entries []viewRunIndexEntry) []viewRunIndexEntry {
		return upsertViewRunIndexEntry(entries, viewRunIndexEntry{id: runID, startedAt: manifest.StartedAt})
	})
	return bundle, nil
}

func upsertViewRunIndexEntry(entries []viewRunIndexEntry, add viewRunIndexEntry) []viewRunIndexEntry {
	out := make([]viewRunIndexEntry, 0, len(entries)+1)
	for _, entry := range entries {
		if entry.id != add.id {
			out = append(out, entry)
		}
	}
	out = append(out, add)
	slices.SortFunc(out, compareViewRunIndexEntry)
	return out
}

func removeViewRunIndexEntry(root, runID string) {
	_ = updateViewRunIndex(root, func(entries []viewRunIndexEntry) []viewRunIndexEntry {
		return slices.DeleteFunc(entries, func(entry viewRunIndexEntry) bool { return entry.id == runID })
	})
}

func restoreViewRunIndexEntry(root, runID string) {
	run, err := readViewRunSummary(root, runID)
	if err != nil {
		return
	}
	_ = updateViewRunIndex(root, func(entries []viewRunIndexEntry) []viewRunIndexEntry {
		return upsertViewRunIndexEntry(entries, viewRunIndexEntry{id: runID, startedAt: run.StartedAt})
	})
}

func compareViewRunIndexEntry(a, b viewRunIndexEntry) int {
	if order := a.startedAt.Compare(b.startedAt); order != 0 {
		return order
	}
	return strings.Compare(a.id, b.id)
}

func viewRunIndexFallbackTime(entry fs.DirEntry) (time.Time, error) {
	prefixLen := len(time.Unix(0, 0).UTC().Format(runIDTimeLayout))
	if len(entry.Name()) > prefixLen {
		if startedAt, err := time.Parse(runIDTimeLayout, entry.Name()[:prefixLen]); err == nil {
			return startedAt, nil
		}
	}
	info, err := entry.Info()
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

// ensureViewRunIndex performs the one-time compatibility migration for a
// missing/stale catalog. Normal page reads return before taking the lock or
// enumerating the runs directory.
func ensureViewRunIndex(root string) error {
	current, err := viewRunIndexCurrent(root)
	if err != nil {
		return err
	}
	if current {
		return nil
	}
	return withViewRunIndexLock(root, func(dataRoot *os.Root) error {
		return ensureViewRunIndexLocked(root, dataRoot)
	})
}

func viewRunIndexCurrent(root string) (bool, error) {
	if _, err := os.Lstat(filepath.Join(filepath.Dir(root), viewRunIndexDirty)); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("cli: stat run index dirty marker: %w", err)
	}
	indexFile, indexErr := openViewRunIndex(root)
	var indexInfo os.FileInfo
	if indexErr == nil {
		indexInfo, indexErr = indexFile.Stat()
		indexErr = errors.Join(indexErr, indexFile.Close())
	}
	runsInfo, runsErr := os.Stat(root)
	if errors.Is(runsErr, os.ErrNotExist) {
		return indexErr == nil, nil
	}
	if runsErr != nil {
		return false, fmt.Errorf("cli: stat runs directory: %w", runsErr)
	}
	if indexErr != nil {
		if errors.Is(indexErr, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("cli: stat run index: %w", indexErr)
	}
	return !indexInfo.ModTime().Before(runsInfo.ModTime()), nil
}

func ensureViewRunIndexLocked(root string, dataRoot *os.Root) error {
	current, err := viewRunIndexCurrent(root)
	if err != nil {
		return err
	}
	if current {
		return nil
	}
	entries, err := rebuildViewRunIndexEntries(root)
	if err != nil {
		return err
	}
	if beforeViewRunIndexRebuildPublish != nil {
		beforeViewRunIndexRebuildPublish()
	}
	if err := writeViewRunIndexWithRoot(dataRoot, entries); err != nil {
		return err
	}
	if err := dataRoot.Remove(viewRunIndexDirty); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cli: clear run index dirty marker: %w", err)
	}
	return nil
}

func rebuildViewRunIndexEntries(root string) ([]viewRunIndexEntry, error) {
	runsRoot, err := os.OpenRoot(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("cli: open runs directory: %w", err)
	}
	defer runsRoot.Close()
	dir, err := runsRoot.Open(".")
	if err != nil {
		return nil, fmt.Errorf("cli: open held runs directory: %w", err)
	}
	entries, err := dir.ReadDir(-1)
	closeErr := dir.Close()
	if err != nil {
		return nil, fmt.Errorf("cli: read runs directory: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("cli: close runs directory: %w", closeErr)
	}
	out := make([]viewRunIndexEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || validateRunID(entry.Name()) != nil {
			continue
		}
		run, err := readViewRunSummaryFromRoot(runsRoot, entry.Name())
		if err == nil {
			out = append(out, viewRunIndexEntry{id: run.ID, startedAt: run.StartedAt})
			continue
		}
		startedAt, parseErr := viewRunIndexFallbackTime(entry)
		if parseErr != nil {
			continue
		}
		out = append(out, viewRunIndexEntry{id: entry.Name(), startedAt: startedAt})
	}
	slices.SortFunc(out, compareViewRunIndexEntry)
	return out, nil
}

func updateViewRunIndex(root string, mutate func([]viewRunIndexEntry) []viewRunIndexEntry) error {
	return withViewRunIndexLock(root, func(dataRoot *os.Root) error {
		if err := ensureViewRunIndexLocked(root, dataRoot); err != nil {
			return err
		}
		dirty, err := dataRoot.OpenFile(viewRunIndexDirty, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("cli: create run index dirty marker: %w", err)
		}
		if err := dirty.Sync(); err != nil {
			dirty.Close()
			return fmt.Errorf("cli: sync run index dirty marker: %w", err)
		}
		if err := dirty.Close(); err != nil {
			return fmt.Errorf("cli: close run index dirty marker: %w", err)
		}
		entries, err := readAllViewRunIndex(root)
		if err != nil {
			return err
		}
		if err := writeViewRunIndexWithRoot(dataRoot, mutate(entries)); err != nil {
			return err
		}
		if err := dataRoot.Remove(viewRunIndexDirty); err != nil {
			return fmt.Errorf("cli: clear run index dirty marker: %w", err)
		}
		return nil
	})
}

func withViewRunIndexLock(root string, fn func(*os.Root) error) error {
	dataDir := filepath.Dir(root)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("cli: create data directory for run index: %w", err)
	}
	dataRoot, err := os.OpenRoot(dataDir)
	if err != nil {
		return fmt.Errorf("cli: open data directory for run index: %w", err)
	}
	defer dataRoot.Close()
	if info, err := os.Lstat(filepath.Join(dataDir, viewRunIndexLock)); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("cli: run index lock is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cli: stat run index lock: %w", err)
	}
	lock, err := dataRoot.OpenFile(viewRunIndexLock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("cli: open run index lock: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("cli: lock run index: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn(dataRoot)
}

func writeViewRunIndex(root string, entries []viewRunIndexEntry) error {
	return withViewRunIndexLock(root, func(dataRoot *os.Root) error {
		return writeViewRunIndexWithRoot(dataRoot, entries)
	})
}

func writeViewRunIndexWithRoot(dataRoot *os.Root, entries []viewRunIndexEntry) error {
	slices.SortFunc(entries, compareViewRunIndexEntry)
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("cli: name run index temporary: %w", err)
	}
	temp := ".run-index-" + fmt.Sprintf("%x", nonce[:])
	file, err := dataRoot.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("cli: create run index temporary: %w", err)
	}
	cleanup := true
	defer func() {
		file.Close()
		if cleanup {
			dataRoot.Remove(temp)
		}
	}()
	writer := bufio.NewWriter(file)
	if _, err := fmt.Fprintf(writer, "%s %020d\n", viewRunIndexHeader, len(entries)); err != nil {
		return err
	}
	for _, entry := range entries {
		line := entry.startedAt.UTC().Format(runIDTimeLayout) + "\t" + base64.RawURLEncoding.EncodeToString([]byte(entry.id)) + "\n"
		if len(line) >= viewRunIndexMaxLine {
			return fmt.Errorf("cli: run id is too long for viewer index")
		}
		if _, err := writer.WriteString(line); err != nil {
			return fmt.Errorf("cli: write run index: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("cli: flush run index: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("cli: sync run index: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("cli: close run index: %w", err)
	}
	if err := dataRoot.Rename(temp, viewRunIndexFile); err != nil {
		return fmt.Errorf("cli: install run index: %w", err)
	}
	cleanup = false
	return nil
}

func openViewRunIndex(root string) (*os.File, error) {
	dataRoot, err := os.OpenRoot(filepath.Dir(root))
	if err != nil {
		return nil, err
	}
	info, statErr := os.Lstat(filepath.Join(filepath.Dir(root), viewRunIndexFile))
	if statErr != nil {
		dataRoot.Close()
		return nil, statErr
	}
	if !info.Mode().IsRegular() {
		dataRoot.Close()
		return nil, errors.New("cli: run index is not a regular file")
	}
	file, openErr := dataRoot.Open(viewRunIndexFile)
	closeErr := dataRoot.Close()
	if openErr != nil {
		return nil, errors.Join(openErr, closeErr)
	}
	if closeErr != nil {
		file.Close()
		return nil, closeErr
	}
	return file, nil
}

func readAllViewRunIndex(root string) ([]viewRunIndexEntry, error) {
	file, err := openViewRunIndex(root)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, viewRunIndexMaxLine), viewRunIndexMaxLine)
	if !scanner.Scan() {
		return nil, errors.New("cli: run index has no header")
	}
	count, err := parseViewRunIndexHeader(scanner.Text())
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("cli: stat run index: %w", err)
	}
	remaining := info.Size() - int64(len(scanner.Text())+1)
	maxCount := remaining / int64(len(runIDTimeLayout)+4)
	if int64(count) > maxCount {
		return nil, errors.New("cli: run index count exceeds file size")
	}
	entries := make([]viewRunIndexEntry, 0, count)
	for scanner.Scan() {
		entry, err := parseViewRunIndexLine(scanner.Text())
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("cli: read run index: %w", err)
	}
	if len(entries) != count {
		return nil, fmt.Errorf("cli: run index count is %d, want %d", len(entries), count)
	}
	return entries, nil
}

func parseViewRunIndexHeader(line string) (int, error) {
	prefix := viewRunIndexHeader + " "
	if !strings.HasPrefix(line, prefix) {
		return 0, errors.New("cli: invalid run index header")
	}
	count, err := strconv.Atoi(strings.TrimPrefix(line, prefix))
	if err != nil || count < 0 {
		return 0, errors.New("cli: invalid run index count")
	}
	return count, nil
}

func parseViewRunIndexLine(line string) (viewRunIndexEntry, error) {
	key, encoded, ok := strings.Cut(line, "\t")
	if !ok {
		return viewRunIndexEntry{}, errors.New("cli: invalid run index record")
	}
	startedAt, err := time.Parse(runIDTimeLayout, key)
	if err != nil {
		return viewRunIndexEntry{}, errors.New("cli: invalid run index timestamp")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || validateRunID(string(raw)) != nil {
		return viewRunIndexEntry{}, errors.New("cli: invalid indexed run id")
	}
	return viewRunIndexEntry{id: string(raw), startedAt: startedAt}, nil
}

func viewRunIndexPage(root string, cursor int64) ([]viewRunIndexEntry, int64, int, string, error) {
	if err := ensureViewRunIndex(root); err != nil {
		return nil, 0, 0, "", err
	}
	file, err := openViewRunIndex(root)
	if err != nil {
		return nil, 0, 0, "", fmt.Errorf("cli: open run index: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, 0, "", err
	}
	headerReader := bufio.NewReaderSize(file, viewRunIndexMaxLine)
	headerBytes, err := headerReader.ReadSlice('\n')
	if err != nil || len(headerBytes) > viewRunIndexMaxLine {
		return nil, 0, 0, "", errors.New("cli: invalid run index header")
	}
	header := string(headerBytes)
	total, err := parseViewRunIndexHeader(strings.TrimSuffix(header, "\n"))
	if err != nil {
		return nil, 0, 0, "", err
	}
	headerEnd := int64(len(header))
	maxCount := (info.Size() - headerEnd) / int64(len(runIDTimeLayout)+4)
	if int64(total) > maxCount {
		return nil, 0, 0, "", errors.New("cli: run index count exceeds file size")
	}
	end := cursor
	if end == 0 {
		end = info.Size()
	}
	if end < headerEnd || end > info.Size() {
		return nil, 0, 0, "", errors.New("cursor is outside the run index")
	}
	entries := make([]viewRunIndexEntry, 0, viewRunPageSize)
	position := end
	for len(entries) < viewRunPageSize && position > headerEnd {
		start := max(headerEnd, position-int64(viewRunIndexMaxLine))
		buffer := make([]byte, position-start)
		if _, err := file.ReadAt(buffer, start); err != nil && !errors.Is(err, io.EOF) {
			return nil, 0, 0, "", fmt.Errorf("cli: read run index page: %w", err)
		}
		lineEnd := len(buffer)
		if lineEnd > 0 && buffer[lineEnd-1] == '\n' {
			lineEnd--
		}
		lineStart := strings.LastIndexByte(string(buffer[:lineEnd]), '\n') + 1
		if start > headerEnd && lineStart == 0 {
			return nil, 0, 0, "", errors.New("cli: run index record exceeds bound")
		}
		entry, err := parseViewRunIndexLine(string(buffer[lineStart:lineEnd]))
		if err != nil {
			return nil, 0, 0, "", err
		}
		entries = append(entries, entry)
		position = start + int64(lineStart)
	}
	next := int64(0)
	if position > headerEnd {
		next = position
	}
	generation := fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
	return entries, next, total, generation, nil
}
