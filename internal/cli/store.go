package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// storeBytes sums the regular files under dir: what a store, a trash, or
// one run takes on disk. Symlinks are neither followed nor counted.
func storeBytes(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// humanBytes renders a size the way a person reads it: 0 B, 512 B, 48 KB,
// 312 MB, 1.2 GB.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	value := float64(n)
	for _, suffix := range []string{"KB", "MB", "GB", "TB"} {
		value /= unit
		// unit-0.5, not unit: 1023.6 KB would otherwise be rounded to the
		// nonsense "1024 KB" instead of rolling over to "1.0 MB".
		if value < unit-0.5 || suffix == "TB" {
			if value < 10 {
				return fmt.Sprintf("%.1f %s", value, suffix)
			}
			return fmt.Sprintf("%.0f %s", value, suffix)
		}
	}
	return ""
}

var errBadAge = errors.New("cli: an age is a number followed by h, d, or w, like 30d")

// parseAge reads an age such as 12h, 30d, or 2w.
func parseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return 0, errBadAge
	}
	n, err := strconv.ParseUint(s[:len(s)-1], 10, 32)
	if err != nil || n == 0 {
		return 0, errBadAge
	}
	var unit time.Duration
	switch s[len(s)-1] {
	case 'h':
		unit = time.Hour
	case 'd':
		unit = 24 * time.Hour
	case 'w':
		unit = 7 * 24 * time.Hour
	default:
		return 0, errBadAge
	}
	if n > uint64(math.MaxInt64/unit) {
		return 0, errBadAge
	}
	return time.Duration(n) * unit, nil
}

// sweepResult says what a sweep did: the runs moved to the trash, the ones
// kept because a recorder still holds them, and the ones that failed.
type sweepResult struct {
	Moved  []string
	Kept   []string
	Failed []error
	// Skipped counts what the sweep could not judge: a run whose manifest
	// gives no start, and one that could not be read at all. Saying only
	// how many moved would read as "everything else was recent".
	Skipped int
}

// sweepRuns moves every run that started more than age ago to the trash.
// A run a recorder still holds is kept, as the trash always keeps it.
func sweepRuns(root string, age time.Duration, now time.Time, dryRun bool) (sweepResult, error) {
	runs, unreadable, err := listRuns(root, "")
	if err != nil {
		return sweepResult{}, err
	}
	cutoff := now.Add(-age)
	result := sweepResult{Skipped: unreadable}
	for _, run := range runs {
		if run.StartedAt.IsZero() {
			result.Skipped++
			continue
		}
		if !run.StartedAt.Before(cutoff) {
			continue
		}
		if dryRun {
			dir := filepath.Join(root, run.ID)
			if m, err := readManifest(dir); err == nil && runOpen(root, dir, m) != nil {
				result.Kept = append(result.Kept, run.ID)
			} else {
				result.Moved = append(result.Moved, run.ID)
			}
			continue
		}
		switch err := trashRun(root, run.ID); {
		case err == nil:
			result.Moved = append(result.Moved, run.ID)
		case errors.Is(err, errRunOpen) || errors.Is(err, errRunClosing):
			result.Kept = append(result.Kept, run.ID)
		default:
			result.Failed = append(result.Failed, err)
		}
	}
	return result, nil
}
