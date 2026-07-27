//go:build darwin || linux

package runner

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// The signals the supervisor sends, named here so the supervision logic reads
// as intent rather than as platform constants.
const (
	sigInterrupt = syscall.SIGINT
	sigTerminate = syscall.SIGTERM
	sigKill      = syscall.SIGKILL
)

// setProcessGroup puts the provider in a process group of its own, with itself
// as leader. An agent CLI spawns hooks, MCP servers and shells of its own, and
// a signal sent to the provider alone leaves all of them running with the run's
// pipes still open; the group is what makes the whole tree addressable.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalProcessGroup sends sig to every process in the group led by pid. A
// group that has already gone is the outcome the caller asked for, so ESRCH is
// success rather than a failure to report.
func signalProcessGroup(pid int, sig syscall.Signal) error {
	if err := syscall.Kill(-pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// groupSignaller is the supervisor's one way of reaching the provider's process
// group. It stops signalling the moment the leader has been reaped: from then
// on the pid, and with it the group id, may name an unrelated process, and a
// late SIGKILL would be aimed at whatever inherited the number.
type groupSignaller struct {
	mu     sync.Mutex
	pid    int
	reaped bool
}

func (g *groupSignaller) send(sig syscall.Signal) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.reaped {
		return nil
	}
	return signalProcessGroup(g.pid, sig)
}

// stop closes the window in which signalling is still safe. It is called as
// soon as the provider has been waited for.
func (g *groupSignaller) stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reaped = true
}

// exitStatus reports how a finished process ended: its exit code, or no code
// and the name of the signal that killed it. A signalled process has no exit
// code of its own, and inventing one would record the supervisor's action as
// the provider's answer.
func exitStatus(ps *os.ProcessState) (*int, string) {
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return nil, ws.Signal().String()
	}
	code := ps.ExitCode()
	return &code, ""
}
