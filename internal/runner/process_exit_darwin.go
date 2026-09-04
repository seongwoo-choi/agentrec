//go:build darwin

package runner

import (
	"errors"
	"fmt"
	"syscall"
)

// ObserveLeaderExit reports process exit without reaping it. Keeping the leader
// as a zombie preserves its process-group id until Run has finished every signal.
func ObserveLeaderExit(pid int) <-chan error {
	done := make(chan error, 1)
	kq, err := syscall.Kqueue()
	if err != nil {
		done <- fmt.Errorf("runner: observe provider exit: %w", err)
		return done
	}
	change := syscall.Kevent_t{
		Ident:  uint64(pid),
		Filter: syscall.EVFILT_PROC,
		Flags:  syscall.EV_ADD | syscall.EV_ONESHOT,
		Fflags: syscall.NOTE_EXIT,
	}
	var registerErr error
	for {
		_, registerErr = syscall.Kevent(kq, []syscall.Kevent_t{change}, nil, nil)
		if !errors.Is(registerErr, syscall.EINTR) {
			break
		}
	}
	if registerErr != nil {
		syscall.Close(kq)
		if errors.Is(registerErr, syscall.ESRCH) || errors.Is(registerErr, syscall.ENOENT) {
			done <- nil
			return done
		}
		done <- fmt.Errorf("runner: observe provider exit: %w", registerErr)
		return done
	}
	go func() {
		defer syscall.Close(kq)
		events := make([]syscall.Kevent_t, 1)
		var n int
		var err error
		for {
			n, err = syscall.Kevent(kq, nil, events, nil)
			if !errors.Is(err, syscall.EINTR) {
				break
			}
		}
		if err == nil && n == 1 && events[0].Flags&syscall.EV_ERROR != 0 {
			err = syscall.Errno(events[0].Data)
		}
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.ENOENT) {
			err = nil
		}
		if err != nil {
			err = fmt.Errorf("runner: observe provider exit: %w", err)
		}
		done <- err
	}()
	return done
}
