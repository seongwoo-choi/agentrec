package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"syscall"
	"unicode/utf8"
)

const maxViewStoredTextBytes = 1 << 20

var viewStoredAsPattern = regexp.MustCompile(`^untracked/[0-9a-f]{64}\.txt$`)
var viewStoredDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type viewStoredText struct {
	Path        string `json:"path"`
	Text        string `json:"text"`
	Attribution string `json:"attribution"`
}

func readViewStoredText(ctx context.Context, snapshot *viewSnapshot, displayPath string) (viewStoredText, error) {
	if err := ctx.Err(); err != nil {
		return viewStoredText{}, err
	}
	for _, change := range snapshot.changes {
		if change.Path != displayPath {
			continue
		}
		if change.Tracked || !change.Stored || change.Kind != "file" || snapshot.sourceRoot == nil {
			return viewStoredText{}, errors.New("stored text unavailable: no stored regular file")
		}
		if change.hashBasis != "sanitized" || !viewStoredDigestPattern.MatchString(change.sha256) || !viewStoredAsPattern.MatchString(change.storedAs) {
			return viewStoredText{}, errors.New("stored text unavailable: missing or invalid sanitized identity")
		}
		f, err := openViewStoredText(snapshot.sourceRoot, change.storedAs)
		if err != nil {
			return viewStoredText{}, errors.New("stored text unavailable: cannot open recorded regular file")
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return viewStoredText{}, errors.New("stored text unavailable: cannot stat recorded file")
		}
		if info.Size() > maxViewStoredTextBytes {
			return viewStoredText{}, errors.New("stored text unavailable: exceeds 1 MiB limit")
		}
		data, err := io.ReadAll(io.LimitReader(&viewContextReader{ctx: ctx, reader: f}, maxViewStoredTextBytes+1))
		if err != nil {
			return viewStoredText{}, err
		}
		if err := ctx.Err(); err != nil {
			return viewStoredText{}, err
		}
		if len(data) > maxViewStoredTextBytes {
			return viewStoredText{}, errors.New("stored text unavailable: exceeds 1 MiB limit")
		}
		if !utf8.Valid(data) {
			return viewStoredText{}, errors.New("stored text unavailable: invalid UTF-8")
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != change.sha256 {
			return viewStoredText{}, errors.New("stored text unavailable: digest mismatch")
		}
		return viewStoredText{Path: displayPath, Text: string(data), Attribution: snapshot.changeAttribution}, nil
	}
	return viewStoredText{}, errors.New("stored text unavailable: path not recorded in this snapshot")
}

// Hold each checked directory while descending. The final open is nonblocking:
// even a regular-file-to-FIFO substitution cannot make the open wait for a writer.
func openViewStoredText(root *os.Root, storedAs string) (*os.File, error) {
	parts := strings.Split("git/"+storedAs, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, errors.New("invalid stored path")
		}
	}
	current := root
	for _, part := range parts[:len(parts)-1] {
		info, err := current.Lstat(part)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, errors.New("stored text ancestor is not a directory")
		}
		child, err := current.OpenRoot(part)
		if err != nil {
			return nil, err
		}
		defer child.Close()
		opened, err := child.Stat(".")
		visible, visibleErr := current.Lstat(part)
		if err != nil || visibleErr != nil || !visible.IsDir() || !os.SameFile(info, opened) || !os.SameFile(info, visible) {
			return nil, errors.New("stored text ancestor changed")
		}
		current = child
	}
	name := parts[len(parts)-1]
	info, err := current.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("stored text is not regular")
	}
	f, err := current.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		f.Close()
		return nil, errors.New("stored text changed during open")
	}
	return f, nil
}
