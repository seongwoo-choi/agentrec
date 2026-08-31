//go:build darwin || linux

package storage

import (
	"os/signal"
	"syscall"
	"testing"
)

func setPartialWriteLimit(t *testing.T, limit uint64) {
	t.Helper()
	signal.Ignore(syscall.SIGXFSZ)
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: limit, Max: limit}); err != nil {
		t.Fatalf("set file-size limit: %v", err)
	}
}
