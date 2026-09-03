package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/seongwoo-choi/agentrec/internal/evidence"
)

const (
	trackedStatFile        = "tracked-stat.json"
	untrackedChangesFile   = "untracked.json"
	trackedPatchFile       = "tracked.patch"
	maxViewChangeDocBytes  = 64 << 20
	maxViewPatchBytes      = 64 << 20
	maxViewChanges         = 100_000
	maxViewPatchHeaderSize = 64 << 10
)

func validViewChangePath(value string) bool {
	if value == "" || strings.IndexByte(value, 0) >= 0 || path.IsAbs(value) {
		return false
	}
	clean := path.Clean(value)
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

type viewChange struct {
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

type viewChangePage struct {
	Status      string       `json:"status,omitempty"`
	Reason      string       `json:"reason,omitempty"`
	Attribution string       `json:"attribution,omitempty"`
	Baseline    string       `json:"baseline,omitempty"`
	Total       int          `json:"total"`
	Items       []viewChange `json:"items"`
	NextCursor  *int64       `json:"nextCursor,omitempty"`
}

type viewPatchPage struct {
	Path        string `json:"path"`
	Patch       string `json:"patch"`
	Attribution string `json:"attribution"`
	NextCursor  *int64 `json:"nextCursor,omitempty"`
}

type viewPatchSection struct {
	start int64
	end   int64
}

type trackedChangeDocument struct {
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	Attribution string `json:"attribution"`
	Baseline    string `json:"baseline,omitempty"`
	Files       []struct {
		Path      string `json:"path"`
		Additions *int   `json:"additions,omitempty"`
		Deletions *int   `json:"deletions,omitempty"`
		Binary    bool   `json:"binary,omitempty"`
	} `json:"files"`
	Totals struct {
		Files     int `json:"files"`
		Additions int `json:"additions"`
		Deletions int `json:"deletions"`
		Binary    int `json:"binary"`
	} `json:"totals"`
}

type untrackedChangeDocument struct {
	Attribution string `json:"attribution"`
	Count       int    `json:"count"`
	Stored      int    `json:"stored"`
	Files       []struct {
		Path      string `json:"path"`
		Kind      string `json:"kind"`
		Mode      string `json:"mode"`
		Size      int64  `json:"size"`
		SHA256    string `json:"sha256,omitempty"`
		HashBasis string `json:"hashBasis,omitempty"`
		Stored    bool   `json:"stored"`
		Reason    string `json:"reason,omitempty"`
		StoredAs  string `json:"storedAs,omitempty"`
	} `json:"files"`
}

func prepareViewChanges(snapshot *viewSnapshot) error {
	return prepareViewChangesContext(context.Background(), snapshot)
}

func prepareViewChangesContext(ctx context.Context, snapshot *viewSnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	gitRaw, gitErr := capturedViewDocument(snapshot, gitDir+"/"+resultFile)
	if gitErr == nil && !utf8.Valid(gitRaw) {
		return errors.New("cli: read git/result.json: invalid UTF-8")
	}
	result, err := decodeGitResult(gitRaw, gitErr)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	trackedRaw, trackedPresent := snapshot.documents[gitDir+"/"+trackedStatFile]
	untrackedRaw, untrackedPresent := snapshot.documents[gitDir+"/"+untrackedChangesFile]
	if result == nil {
		if trackedPresent || untrackedPresent || snapshot.patch != nil {
			markViewChangesUnavailable(snapshot, "repository change artifacts are missing git/result.json", "")
			return nil
		}
		markViewChangesUnavailable(snapshot, "repository change evidence was not recorded", "")
		return nil
	}
	switch result.Status {
	case "pending", "available", "unavailable":
	default:
		return fmt.Errorf("cli: repository change evidence has unknown status %q", result.Status)
	}
	if !trackedPresent || !untrackedPresent {
		markViewChangesUnavailable(snapshot, "repository result is missing change-list evidence", result.Baseline)
		return nil
	}

	var tracked trackedChangeDocument
	if err := decodeViewChangeDocumentContext(ctx, trackedRaw, gitDir+"/"+trackedStatFile, &tracked); err != nil {
		return err
	}
	var untracked untrackedChangeDocument
	if err := decodeViewChangeDocumentContext(ctx, untrackedRaw, gitDir+"/"+untrackedChangesFile, &untracked); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if tracked.Attribution != evidence.Attribution || untracked.Attribution != evidence.Attribution {
		return errors.New("cli: repository change evidence has an invalid attribution")
	}
	if tracked.Baseline != result.Baseline {
		return errors.New("cli: repository change evidence has a different baseline than git/result.json")
	}
	if tracked.Status == "" || tracked.Status != result.Status {
		return errors.New("cli: repository change evidence has a different status than git/result.json")
	}
	if tracked.Reason != result.Reason {
		return errors.New("cli: repository change evidence has a different reason than git/result.json")
	}
	switch tracked.Status {
	case "pending":
		markViewChangesUnavailable(snapshot, "repository change evidence is pending", result.Baseline)
		return nil
	case "available", "unavailable":
	default:
		return fmt.Errorf("cli: repository change evidence has unknown status %q", tracked.Status)
	}
	if tracked.Status == "unavailable" && (result.TrackedFiles != 0 || result.Added != 0 || result.Deleted != 0 || len(tracked.Files) != 0 || tracked.Totals.Files != 0 || tracked.Totals.Additions != 0 || tracked.Totals.Deletions != 0 || tracked.Totals.Binary != 0 || snapshot.patch != nil) {
		return errors.New("cli: unavailable repository evidence contains tracked changes")
	}
	if len(tracked.Files) != result.TrackedFiles || len(untracked.Files) != result.UntrackedFiles || untracked.Count != len(untracked.Files) {
		return errors.New("cli: repository change evidence counts disagree with git/result.json")
	}
	if len(tracked.Files)+len(untracked.Files) > maxViewChanges {
		return fmt.Errorf("cli: repository change evidence holds more than %d files", maxViewChanges)
	}

	snapshot.changeStatus = tracked.Status
	snapshot.changeReason = tracked.Reason
	snapshot.changeAttribution = evidence.Attribution
	snapshot.changeBaseline = tracked.Baseline
	snapshot.changes = make([]viewChange, 0, len(tracked.Files)+len(untracked.Files))
	paths := make(map[string]struct{}, len(tracked.Files)+len(untracked.Files))
	trackedAdditions, trackedDeletions, trackedBinary := 0, 0, 0
	for _, file := range tracked.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !validViewChangePath(file.Path) {
			return errors.New("cli: tracked change has an invalid path")
		}
		if _, exists := paths[file.Path]; exists {
			return fmt.Errorf("cli: repository change evidence repeats path %q", file.Path)
		}
		paths[file.Path] = struct{}{}
		switch {
		case file.Binary && (file.Additions != nil || file.Deletions != nil):
			return errors.New("cli: binary tracked change reports text line counts")
		case file.Binary:
			trackedBinary++
		case file.Additions == nil || file.Deletions == nil || *file.Additions < 0 || *file.Deletions < 0:
			return errors.New("cli: text tracked change has invalid line counts")
		case *file.Additions > maxRepositoryCount-trackedAdditions || *file.Deletions > maxRepositoryCount-trackedDeletions:
			return errors.New("cli: tracked change line counts exceed the repository evidence limit")
		default:
			trackedAdditions += *file.Additions
			trackedDeletions += *file.Deletions
		}
		snapshot.changes = append(snapshot.changes, viewChange{
			Path: file.Path, Kind: "tracked", Tracked: true,
			Additions: file.Additions, Deletions: file.Deletions, Binary: file.Binary,
		})
	}
	if tracked.Totals.Files != len(tracked.Files) || tracked.Totals.Additions != trackedAdditions ||
		tracked.Totals.Deletions != trackedDeletions || tracked.Totals.Binary != trackedBinary ||
		tracked.Totals.Additions != result.Added || tracked.Totals.Deletions != result.Deleted ||
		tracked.Totals.Binary != result.BinaryTracked {
		return errors.New("cli: tracked change totals disagree with git/result.json")
	}
	stored := 0
	for _, file := range untracked.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !validViewChangePath(file.Path) || file.Size < 0 {
			return errors.New("cli: untracked change has invalid metadata")
		}
		if _, exists := paths[file.Path]; exists {
			return fmt.Errorf("cli: repository change evidence repeats path %q", file.Path)
		}
		paths[file.Path] = struct{}{}
		if file.Stored {
			stored++
		}
		snapshot.changes = append(snapshot.changes, viewChange{
			Path: file.Path, Kind: file.Kind, Tracked: false,
			Mode: file.Mode, Size: file.Size, Stored: file.Stored, Reason: file.Reason,
		})
	}
	if stored != untracked.Stored || stored != result.StoredTextFiles {
		return errors.New("cli: stored untracked change counts disagree with git/result.json")
	}
	if snapshot.patch == nil {
		if len(tracked.Files) != 0 {
			markViewChangesUnavailable(snapshot, "tracked changes exist without tracked.patch", result.Baseline)
		}
		return nil
	}
	if err := validateViewPatchUTF8Context(ctx, snapshot.patch, snapshot.patchSize); err != nil {
		return err
	}
	sections, err := indexViewPatchContext(ctx, snapshot.patch, snapshot.patchSize)
	if err != nil {
		return err
	}
	if len(sections) != len(tracked.Files) {
		return errors.New("cli: tracked.patch sections disagree with the tracked change list")
	}
	for _, file := range tracked.Files {
		if _, ok := sections[file.Path]; !ok {
			return fmt.Errorf("cli: tracked.patch has no section for %q", file.Path)
		}
	}
	snapshot.patchSections = sections
	return nil
}

func validateViewPatchUTF8(file io.ReaderAt, size int64) error {
	return validateViewPatchUTF8Context(context.Background(), file, size)
}

func validateViewPatchUTF8Context(ctx context.Context, file io.ReaderAt, size int64) error {
	reader := io.NewSectionReader(file, 0, size)
	buffer := make([]byte, 64<<10)
	pending := make([]byte, 0, utf8.UTFMax)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := reader.Read(buffer)
		data := buffer[:n]
		if len(pending) != 0 {
			joined := make([]byte, 0, len(pending)+len(data))
			joined = append(joined, pending...)
			joined = append(joined, data...)
			data = joined
			pending = pending[:0]
		}
		for offset := 0; offset < len(data); {
			if data[offset] < utf8.RuneSelf {
				offset++
				continue
			}
			if !utf8.FullRune(data[offset:]) {
				pending = append(pending, data[offset:]...)
				break
			}
			r, width := utf8.DecodeRune(data[offset:])
			if r == utf8.RuneError && width == 1 {
				return errors.New("cli: tracked.patch is not valid UTF-8")
			}
			offset += width
		}
		switch {
		case errors.Is(err, io.EOF):
			if len(pending) != 0 {
				return errors.New("cli: tracked.patch is not valid UTF-8")
			}
			return nil
		case err != nil:
			return fmt.Errorf("cli: validate tracked.patch UTF-8: %w", err)
		}
	}
}

func markViewChangesUnavailable(snapshot *viewSnapshot, reason, baseline string) {
	snapshot.changeStatus = "unavailable"
	snapshot.changeReason = reason
	snapshot.changeAttribution = evidence.Attribution
	snapshot.changeBaseline = baseline
	snapshot.changes = nil
	snapshot.patchSections = nil
}

func summarizeViewChanges(snapshot *viewSnapshot) viewChangeSummary {
	summary := viewChangeSummary{
		Status: snapshot.changeStatus, Reason: snapshot.changeReason,
		Attribution: snapshot.changeAttribution, Baseline: snapshot.changeBaseline,
		Total: len(snapshot.changes),
	}
	for _, change := range snapshot.changes {
		if change.Tracked {
			summary.Tracked++
			if change.Binary {
				summary.Binary++
			} else {
				summary.Additions += *change.Additions
				summary.Deletions += *change.Deletions
			}
		} else {
			summary.Untracked++
		}
	}
	return summary
}

func decodeViewChangeDocument(data []byte, name string, target any) error {
	return decodeViewChangeDocumentContext(context.Background(), data, name, target)
}

func validateViewJSONElementBoundsContext(ctx context.Context, data []byte) error {
	inString := false
	escaped := false
	stringBytes := 0
	objectStarts := make([]int, 0, 2)
	for i, b := range data {
		if i%viewContextChunkSize == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if inString {
			if escaped {
				escaped = false
			} else if b == '\\' {
				escaped = true
			} else if b == '"' {
				inString = false
				continue
			}
			stringBytes++
			if stringBytes > viewContextChunkSize {
				return fmt.Errorf("JSON string exceeds %d bytes", viewContextChunkSize)
			}
			continue
		}
		if len(objectStarts) > 1 && i-objectStarts[len(objectStarts)-1]+1 > viewContextChunkSize {
			return errors.New("nested JSON object exceeds viewer bound")
		}
		switch b {
		case '"':
			inString = true
			stringBytes = 0
		case '{':
			objectStarts = append(objectStarts, i)
		case '}':
			if len(objectStarts) == 0 {
				continue
			}
			start := objectStarts[len(objectStarts)-1]
			if len(objectStarts) > 1 && i-start+1 > viewContextChunkSize {
				return fmt.Errorf("nested JSON object exceeds %d bytes", viewContextChunkSize)
			}
			objectStarts = objectStarts[:len(objectStarts)-1]
		}
	}
	return ctx.Err()
}

func decodeViewChangeDocumentContext(ctx context.Context, data []byte, name string, target any) error {
	valid, err := validViewUTF8Context(ctx, data)
	if err != nil {
		return err
	}
	if !valid {
		return fmt.Errorf("cli: read %s: invalid UTF-8", name)
	}
	if err := validateViewJSONElementBoundsContext(ctx, data); err != nil {
		return fmt.Errorf("cli: read %s: %w", name, err)
	}
	var decodeErr error
	switch out := target.(type) {
	case *trackedChangeDocument:
		decodeErr = decodeTrackedChangeDocumentContext(ctx, bytes.NewReader(data), out)
	case *untrackedChangeDocument:
		decodeErr = decodeUntrackedChangeDocumentContext(ctx, bytes.NewReader(data), out)
	default:
		return decodeViewChangeReaderContext(ctx, bytes.NewReader(data), name, target)
	}
	if decodeErr != nil {
		return fmt.Errorf("cli: read %s: %w", name, decodeErr)
	}
	return nil
}

func newViewChangeDecoder(ctx context.Context, reader io.Reader) *json.Decoder {
	decoder := json.NewDecoder(&viewContextReader{ctx: ctx, reader: reader})
	decoder.DisallowUnknownFields()
	return decoder
}

func expectViewJSONDelimiter(decoder *json.Decoder, want json.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != want {
		return fmt.Errorf("expected %q", want)
	}
	return nil
}

func finishViewJSONDocument(decoder *json.Decoder) error {
	if err := expectViewJSONDelimiter(decoder, '}'); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing data")
	}
	return nil
}

func decodeTrackedChangeDocumentContext(ctx context.Context, reader io.Reader, out *trackedChangeDocument) error {
	decoder := newViewChangeDecoder(ctx, reader)
	if err := expectViewJSONDelimiter(decoder, '{'); err != nil {
		return err
	}
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return err
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok {
			return errors.New("object key is not a string")
		}
		switch name {
		case "status":
			err = decoder.Decode(&out.Status)
		case "reason":
			err = decoder.Decode(&out.Reason)
		case "attribution":
			err = decoder.Decode(&out.Attribution)
		case "baseline":
			err = decoder.Decode(&out.Baseline)
		case "totals":
			err = decoder.Decode(&out.Totals)
		case "files":
			out.Files = nil
			if err = expectViewJSONDelimiter(decoder, '['); err == nil {
				for decoder.More() {
					if err = ctx.Err(); err != nil {
						break
					}
					var file struct {
						Path      string `json:"path"`
						Additions *int   `json:"additions,omitempty"`
						Deletions *int   `json:"deletions,omitempty"`
						Binary    bool   `json:"binary,omitempty"`
					}
					if err = decoder.Decode(&file); err != nil {
						break
					}
					out.Files = append(out.Files, file)
					if len(out.Files) > maxViewChanges {
						err = fmt.Errorf("change document holds more than %d files", maxViewChanges)
						break
					}
				}
				if err == nil {
					err = expectViewJSONDelimiter(decoder, ']')
				}
			}
		default:
			return fmt.Errorf("unknown field %q", name)
		}
		if err != nil {
			return err
		}
	}
	return finishViewJSONDocument(decoder)
}

func decodeUntrackedChangeDocumentContext(ctx context.Context, reader io.Reader, out *untrackedChangeDocument) error {
	decoder := newViewChangeDecoder(ctx, reader)
	if err := expectViewJSONDelimiter(decoder, '{'); err != nil {
		return err
	}
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return err
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok {
			return errors.New("object key is not a string")
		}
		switch name {
		case "attribution":
			err = decoder.Decode(&out.Attribution)
		case "count":
			err = decoder.Decode(&out.Count)
		case "stored":
			err = decoder.Decode(&out.Stored)
		case "files":
			out.Files = nil
			if err = expectViewJSONDelimiter(decoder, '['); err == nil {
				for decoder.More() {
					if err = ctx.Err(); err != nil {
						break
					}
					var file struct {
						Path      string `json:"path"`
						Kind      string `json:"kind"`
						Mode      string `json:"mode"`
						Size      int64  `json:"size"`
						SHA256    string `json:"sha256,omitempty"`
						HashBasis string `json:"hashBasis,omitempty"`
						Stored    bool   `json:"stored"`
						Reason    string `json:"reason,omitempty"`
						StoredAs  string `json:"storedAs,omitempty"`
					}
					if err = decoder.Decode(&file); err != nil {
						break
					}
					out.Files = append(out.Files, file)
					if len(out.Files) > maxViewChanges {
						err = fmt.Errorf("change document holds more than %d files", maxViewChanges)
						break
					}
				}
				if err == nil {
					err = expectViewJSONDelimiter(decoder, ']')
				}
			}
		default:
			return fmt.Errorf("unknown field %q", name)
		}
		if err != nil {
			return err
		}
	}
	return finishViewJSONDocument(decoder)
}

func validViewUTF8Context(ctx context.Context, data []byte) (bool, error) {
	for start := 0; start < len(data); {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		end := min(start+viewContextChunkSize, len(data))
		if end < len(data) {
			for end > start && !utf8.RuneStart(data[end]) {
				end--
			}
			if end == start {
				return false, nil
			}
		}
		if !utf8.Valid(data[start:end]) {
			return false, nil
		}
		start = end
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return true, nil
}

func decodeViewChangeReaderContext(ctx context.Context, reader io.Reader, name string, target any) error {
	decoder := json.NewDecoder(&viewContextReader{ctx: ctx, reader: reader})
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("cli: read %s: %w", name, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("cli: read %s: trailing data", name)
	}
	return nil
}

func readViewChangePage(snapshot *viewSnapshot, cursor int64) (viewChangePage, error) {
	if cursor > int64(len(snapshot.changes)) {
		return viewChangePage{}, errors.New("cursor is outside the change list")
	}
	end := cursor + viewPageSize
	if end > int64(len(snapshot.changes)) {
		end = int64(len(snapshot.changes))
	}
	items := append([]viewChange(nil), snapshot.changes[cursor:end]...)
	return viewChangePage{
		Status: snapshot.changeStatus, Reason: snapshot.changeReason,
		Attribution: snapshot.changeAttribution, Baseline: snapshot.changeBaseline,
		Total: len(snapshot.changes), Items: items, NextCursor: viewNextCursor(end, int64(len(snapshot.changes))),
	}, nil
}

func readViewPatchPage(snapshot *viewSnapshot, path string, cursor int64) (viewPatchPage, error) {
	section, ok := snapshot.patchSections[path]
	if !ok {
		return viewPatchPage{}, errors.New("patch is unavailable for this path")
	}
	size := section.end - section.start
	if cursor > size {
		return viewPatchPage{}, errors.New("cursor is outside the patch section")
	}
	length := int64(viewPageBytes)
	if remaining := size - cursor; remaining < length {
		length = remaining
	}
	raw := make([]byte, length)
	if _, err := snapshot.patch.ReadAt(raw, section.start+cursor); err != nil && !errors.Is(err, io.EOF) {
		return viewPatchPage{}, fmt.Errorf("cli: read tracked patch page: %w", err)
	}
	for len(raw) > 0 && !utf8.Valid(raw) {
		raw = raw[:len(raw)-1]
	}
	if len(raw) == 0 && length != 0 {
		return viewPatchPage{}, errors.New("cli: tracked patch cursor is not at a UTF-8 boundary")
	}
	next := cursor + int64(len(raw))
	return viewPatchPage{
		Path: path, Patch: string(raw), Attribution: snapshot.changeAttribution,
		NextCursor: viewNextCursor(next, size),
	}, nil
}

func indexViewPatch(file io.ReaderAt, size int64) (map[string]viewPatchSection, error) {
	return indexViewPatchContext(context.Background(), file, size)
}

func indexViewPatchContext(ctx context.Context, file io.ReaderAt, size int64) (map[string]viewPatchSection, error) {
	reader := bufio.NewReaderSize(io.NewSectionReader(file, 0, size), maxViewPatchHeaderSize)
	sections := make(map[string]viewPatchSection)
	var currentPath string
	var currentStart int64
	var offset int64
	atLineStart := true
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fragment, err := reader.ReadSlice('\n')
		lineStart := offset
		offset += int64(len(fragment))
		if atLineStart && bytes.HasPrefix(fragment, []byte("diff --git ")) {
			if errors.Is(err, bufio.ErrBufferFull) {
				return nil, errors.New("cli: tracked.patch has an overlong diff header")
			}
			path, parseErr := parseViewPatchHeader(strings.TrimSuffix(string(fragment), "\n"))
			if parseErr != nil {
				return nil, parseErr
			}
			if currentPath == "" && lineStart != 0 {
				return nil, errors.New("cli: tracked.patch has bytes before the first diff header")
			}
			if currentPath != "" {
				sections[currentPath] = viewPatchSection{start: currentStart, end: lineStart}
			}
			if _, duplicate := sections[path]; duplicate || path == currentPath {
				return nil, fmt.Errorf("cli: tracked.patch repeats path %q", path)
			}
			currentPath, currentStart = path, lineStart
		}
		switch {
		case err == nil:
			atLineStart = true
		case errors.Is(err, bufio.ErrBufferFull):
			atLineStart = false
		case errors.Is(err, io.EOF):
			if currentPath == "" && offset != 0 {
				return nil, errors.New("cli: tracked.patch has no diff header")
			}
			if currentPath != "" {
				sections[currentPath] = viewPatchSection{start: currentStart, end: offset}
			}
			return sections, nil
		default:
			return nil, fmt.Errorf("cli: index tracked.patch: %w", err)
		}
	}
}

func parseViewPatchHeader(line string) (string, error) {
	rest := strings.TrimPrefix(line, "diff --git ")
	left, rest, err := parseViewPatchToken(rest)
	if err != nil {
		return "", err
	}
	right, rest, err := parseViewPatchToken(strings.TrimLeft(rest, " "))
	if err != nil || strings.TrimSpace(rest) != "" || !strings.HasPrefix(left, "a/") || !strings.HasPrefix(right, "b/") {
		return "", errors.New("cli: tracked.patch has an invalid diff header")
	}
	leftPath := strings.TrimPrefix(left, "a/")
	rightPath := strings.TrimPrefix(right, "b/")
	if !validViewChangePath(leftPath) || !validViewChangePath(rightPath) {
		return "", errors.New("cli: tracked.patch has a non-canonical path")
	}
	if leftPath != rightPath {
		return "", errors.New("cli: tracked.patch has different paths in a no-renames diff header")
	}
	return rightPath, nil
}

func parseViewPatchToken(value string) (string, string, error) {
	if value == "" {
		return "", "", errors.New("cli: tracked.patch has an invalid diff header")
	}
	if value[0] != '"' {
		if split := strings.IndexByte(value, ' '); split >= 0 {
			return value[:split], value[split:], nil
		}
		return value, "", nil
	}
	escaped := false
	for i := 1; i < len(value); i++ {
		switch {
		case escaped:
			escaped = false
		case value[i] == '\\':
			escaped = true
		case value[i] == '"':
			token, err := strconv.Unquote(value[:i+1])
			if err != nil {
				return "", "", errors.New("cli: tracked.patch has an invalid quoted path")
			}
			return token, value[i+1:], nil
		}
	}
	return "", "", errors.New("cli: tracked.patch has an unterminated quoted path")
}
