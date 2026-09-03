package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// `agentrec start` keeps the viewer running in the background and opens it, so
// reading sessions back is one command and one browser tab rather than a
// foreground process per look. `stop` ends it and `status` says where things
// stand. The mechanism is the ordinary one: a detached process, a pid file, a
// SIGTERM the viewer already shuts down cleanly on.

const (
	startUsage  = "usage: agentrec start [--listen <loopback-address>] [--no-open] [--allow-run]\n"
	stopUsage   = "usage: agentrec stop\n"
	statusUsage = "usage: agentrec status\n"

	// defaultStartListen is a fixed loopback address, so the viewer's URL is
	// the same every day and can be bookmarked. --listen chooses another.
	defaultStartListen = "127.0.0.1:7788"

	viewerDirName  = "viewer"
	viewerPIDFile  = "pid"
	viewerURLFile  = "url"
	viewerLogFile  = "log"
	viewerModeFile = "mode"
	viewerAllowRun = "allow-run"
	// Long enough for the viewer's own shutdown: connections drain for a
	// few seconds, then a comparison still running is interrupted and given
	// a moment before it is killed.
	viewerStopWait = 15 * time.Second
	// viewerStartWait bounds waiting for a freshly started viewer to say
	// where it is listening.
	viewerStartWait = 10 * time.Second
)

// viewerStateDir is where the background viewer's pid, URL and log live: next
// to the runs, under the agentrec data directory.
func viewerStateDir() (string, error) {
	root, err := runsRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(root), viewerDirName), nil
}

type viewerState struct {
	dir      string
	pid      int
	url      string
	allowRun bool
}

// readViewerState reports the viewer the pid file names, if it is still alive.
// A pid file whose process is gone is stale and is cleaned up here, so a
// viewer that was killed outright does not read as running forever.
func readViewerState() (viewerState, bool, error) {
	dir, err := viewerStateDir()
	if err != nil {
		return viewerState{}, false, err
	}
	state := viewerState{dir: dir}
	raw, err := os.ReadFile(filepath.Join(dir, viewerPIDFile))
	if errors.Is(err, os.ErrNotExist) {
		return state, false, nil
	}
	if err != nil {
		return state, false, fmt.Errorf("cli: read viewer pid: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 || !processAlive(pid) {
		clearViewerState(dir)
		return state, false, nil
	}
	state.pid = pid
	if url, err := os.ReadFile(filepath.Join(dir, viewerURLFile)); err == nil {
		state.url = strings.TrimSpace(string(url))
	}
	if mode, err := os.ReadFile(filepath.Join(dir, viewerModeFile)); err == nil {
		state.allowRun = strings.TrimSpace(string(mode)) == viewerAllowRun
	}
	return state, true, nil
}

func clearViewerState(dir string) {
	os.Remove(filepath.Join(dir, viewerPIDFile))
	os.Remove(filepath.Join(dir, viewerURLFile))
	os.Remove(filepath.Join(dir, viewerModeFile))
}

// processAlive reports whether a process with this pid exists. Signal 0 tests
// existence without touching the process; EPERM means it exists as someone
// else, which for a pid this program wrote is still "alive".
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func runStart(args []string, stdout, stderr io.Writer) int {
	listen, open, allowRun := defaultStartListen, true, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-open":
			open = false
		case "--allow-run":
			allowRun = true
		case "--listen":
			if i+1 >= len(args) || !isLoopbackListen(args[i+1]) {
				fmt.Fprint(stderr, startUsage)
				return exitUsage
			}
			listen = args[i+1]
			i++
		default:
			fmt.Fprint(stderr, startUsage)
			return exitUsage
		}
	}

	state, running, err := readViewerState()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	if running {
		fmt.Fprintf(stdout, "agentrec viewer already running: %s (pid %d)\n", state.url, state.pid)
		if allowRun && !state.allowRun {
			fmt.Fprintln(stdout, "It was started without --allow-run; run 'agentrec stop' and start it again to run comparisons from the page.")
		}
		if open && state.url != "" {
			if err := openViewBrowser(state.url); err != nil {
				fmt.Fprintf(stderr, "Warning: browser did not open: %v\n", err)
			}
		}
		return 0
	}
	if err := os.MkdirAll(state.dir, 0o700); err != nil {
		fmt.Fprintf(stderr, "cli: create %s: %v\n", strconv.Quote(state.dir), err)
		return exitFailure
	}
	exe, err := sessionExecutable()
	if err != nil {
		fmt.Fprintf(stderr, "cli: locate agentrec: %v\n", err)
		return exitFailure
	}

	// The viewer runs as an ordinary `agentrec view`, detached into a session
	// of its own so closing this terminal does not end it, with its output in
	// a log beside the pid file. Its first line names the URL it bound.
	logPath := filepath.Join(state.dir, viewerLogFile)
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(stderr, "cli: open viewer log: %v\n", err)
		return exitFailure
	}
	startOffset, _ := logFile.Seek(0, io.SeekEnd)
	viewArgs := []string{"view", "--no-open", "--listen", listen}
	if allowRun {
		viewArgs = append(viewArgs, "--allow-run")
	}
	cmd := exec.Command(exe, viewArgs...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		fmt.Fprintf(stderr, "cli: start viewer: %v\n", err)
		return exitFailure
	}
	logFile.Close()
	pid := cmd.Process.Pid
	cmd.Process.Release()

	url, err := awaitViewerURL(logPath, startOffset, pid)
	if err != nil {
		fmt.Fprintf(stderr, "cli: %v (see %s)\n", err, logPath)
		return exitFailure
	}
	if err := os.WriteFile(filepath.Join(state.dir, viewerPIDFile), []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		fmt.Fprintf(stderr, "cli: write viewer pid: %v\n", err)
		return exitFailure
	}
	if err := os.WriteFile(filepath.Join(state.dir, viewerURLFile), []byte(url+"\n"), 0o600); err != nil {
		fmt.Fprintf(stderr, "cli: write viewer url: %v\n", err)
		return exitFailure
	}
	mode := ""
	if allowRun {
		mode = viewerAllowRun
	}
	if err := os.WriteFile(filepath.Join(state.dir, viewerModeFile), []byte(mode+"\n"), 0o600); err != nil {
		fmt.Fprintf(stderr, "cli: write viewer mode: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "agentrec viewer: %s (pid %d)\n", url, pid)
	if open {
		if err := openViewBrowser(url); err != nil {
			fmt.Fprintf(stderr, "Warning: browser did not open: %v\n", err)
		}
	}
	return 0
}

// awaitViewerURL reads the URL the viewer prints once it is listening, or
// reports why it never did.
func awaitViewerURL(logPath string, from int64, pid int) (string, error) {
	deadline := time.Now().Add(viewerStartWait)
	for {
		f, err := os.Open(logPath)
		if err == nil {
			f.Seek(from, io.SeekStart)
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				if url, ok := strings.CutPrefix(sc.Text(), "agentrec viewer: "); ok {
					f.Close()
					return strings.TrimSpace(url), nil
				}
			}
			f.Close()
		}
		if !processAlive(pid) {
			return "", errors.New("the viewer exited before it started listening")
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("the viewer did not report an address within %s", viewerStartWait)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func runStop(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprint(stderr, stopUsage)
		return exitUsage
	}
	state, running, err := readViewerState()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	if !running {
		fmt.Fprintln(stdout, "agentrec viewer is not running")
		return 0
	}
	// SIGTERM is what the viewer shuts down on; SIGKILL only if it does not.
	if err := syscall.Kill(state.pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		fmt.Fprintf(stderr, "cli: stop viewer (pid %d): %v\n", state.pid, err)
		return exitFailure
	}
	deadline := time.Now().Add(viewerStopWait)
	for processAlive(state.pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(state.pid) {
		syscall.Kill(state.pid, syscall.SIGKILL)
		fmt.Fprintf(stdout, "agentrec viewer (pid %d) did not stop in %s and was killed\n", state.pid, viewerStopWait)
	} else {
		fmt.Fprintf(stdout, "agentrec viewer stopped (pid %d)\n", state.pid)
	}
	clearViewerState(state.dir)
	return 0
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprint(stderr, statusUsage)
		return exitUsage
	}
	state, running, err := readViewerState()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	if running {
		mode := ""
		if state.allowRun {
			mode = ", comparisons allowed"
		}
		fmt.Fprintf(stdout, "viewer    running at %s (pid %d%s)\n", state.url, state.pid, mode)
	} else {
		fmt.Fprintln(stdout, "viewer    not running (agentrec start)")
	}
	root, err := runsRoot()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	runs, unreadable, err := listRuns(root, "")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "runs      %d recorded under %s", len(runs), root)
	if unreadable > 0 {
		fmt.Fprintf(stdout, " (%d unreadable)", unreadable)
	}
	fmt.Fprintf(stdout, ", %s on disk\n", humanBytes(storeBytes(root)))
	if trashed, trashUnreadable, err := listRuns(trashRootFor(root), ""); err == nil && len(trashed)+trashUnreadable > 0 {
		fmt.Fprintf(stdout, "trash     %d run(s), %s (agentrec trash empty)\n", len(trashed)+trashUnreadable, humanBytes(storeBytes(trashRootFor(root))))
	}
	// A running viewer's stream copies are the store's too; they go when it
	// stops, and saying nothing about them would understate the disk.
	cache := filepath.Join(filepath.Dir(root), viewCacheDirName)
	if size := storeBytes(cache); size > 0 {
		fmt.Fprintf(stdout, "cache     %s in %s (a running viewer's copies, removed when it stops)\n", humanBytes(size), cache)
	}
	for _, provider := range []string{"claude", "codex"} {
		path, err := hooksFile(provider, false)
		if err != nil {
			continue
		}
		fmt.Fprintf(stdout, "%-9s %s\n", provider, describeHooks(path, provider))
	}
	return 0
}

// describeHooks says whether the recorder's hooks are installed for a
// provider, reading the same file setup writes.
func describeHooks(path, provider string) string {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "hooks not installed (agentrec setup)"
	}
	if err != nil {
		return "hooks file unreadable: " + err.Error()
	}
	doc, err := decodeOrderedObject(raw)
	if err != nil {
		return "hooks file is not a JSON object"
	}
	hooksRaw, ok := doc.get("hooks")
	if !ok {
		return "hooks not installed (agentrec setup)"
	}
	hooks, err := decodeOrderedObject(hooksRaw)
	if err != nil {
		return "hooks file is not a JSON object"
	}
	installed := 0
	for _, event := range hookEvents[provider] {
		raw, ok := hooks.get(event)
		if !ok {
			continue
		}
		var groups []json.RawMessage
		if json.Unmarshal(raw, &groups) != nil {
			continue
		}
		for _, group := range groups {
			if groupIsAgentrec(group, provider) {
				installed++
				break
			}
		}
	}
	switch {
	case installed == 0:
		return "hooks not installed (agentrec setup)"
	case installed < len(hookEvents[provider]):
		return fmt.Sprintf("hooks installed for %d of %d events in %s (agentrec setup to complete)", installed, len(hookEvents[provider]), displayPath(path))
	}
	return "hooks installed in " + displayPath(path)
}
