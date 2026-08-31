//go:build !darwin && !linux

package storage

import "testing"

func setPartialWriteLimit(t *testing.T, _ uint64) {
	t.Helper()
	t.Skip("partial file writes are not reproducible on this platform")
}
