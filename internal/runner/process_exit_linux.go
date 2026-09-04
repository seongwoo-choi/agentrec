//go:build linux

package runner

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const leaderExitPollInterval = 5 * time.Millisecond

// ObserveLeaderExit reports process exit without reaping it. An unreaped child
// remains in /proc with zombie state, which keeps its process-group id reserved.
func ObserveLeaderExit(pid int) <-chan error {
	done := make(chan error, 1)
	path := "/proc/" + strconv.Itoa(pid) + "/stat"
	go func() {
		for {
			raw, err := os.ReadFile(path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					done <- errors.New("runner: provider disappeared before its exit could be observed")
				} else {
					done <- fmt.Errorf("runner: observe provider exit: %w", err)
				}
				return
			}
			end := strings.LastIndexByte(string(raw), ')')
			if end < 0 || end+2 >= len(raw) {
				done <- errors.New("runner: observe provider exit: malformed /proc stat")
				return
			}
			state := raw[end+2]
			if state == 'Z' || state == 'X' {
				done <- nil
				return
			}
			time.Sleep(leaderExitPollInterval)
		}
	}()
	return done
}
