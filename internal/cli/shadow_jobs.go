package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// A comparison launched from the viewer is `agentrec shadow run` as a child
// of the viewer: the same command an operator would type, in the repository
// they name, with its output kept for the page to show. It exists only when
// the viewer was started with --allow-run — a viewer that launches agents is
// a different thing from one that reads what they did — and one runs at a
// time, which is also what the repository lock allows.

const (
	shadowTaskDirName  = "shadow-tasks"
	shadowTaskLimit    = 64 << 10
	shadowJobOutputCap = 1 << 20
	shadowJobHistory   = 20
	shadowJobStopWait  = 5 * time.Second
)

var shadowRunnerNames = []string{"claude", "codex"}

var (
	errShadowDisabled = errors.New("cli: running comparisons from the viewer is off; start it with --allow-run")
	errShadowBusy     = errors.New("cli: a comparison is already running")
	errShadowNotFound = errors.New("cli: no such comparison")
	errShadowNotOpen  = errors.New("cli: the comparison is not running")
)

type shadowJobRequest struct {
	CWD     string   `json:"cwd"`
	Task    string   `json:"task"`
	Runners []string `json:"runners"`
}

// shadowJobInfo is what the page is told about a comparison.
type shadowJobInfo struct {
	ID        string     `json:"id"`
	Status    string     `json:"status"`
	CWD       string     `json:"cwd"`
	Runners   []string   `json:"runners"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	ExitCode  *int       `json:"exitCode,omitempty"`
	RunIDs    []string   `json:"runIds"`
}

type shadowJob struct {
	info shadowJobInfo

	mu        sync.Mutex
	output    []byte
	dropped   int64
	truncated bool
	cancelled bool
	// reaped is set the moment Wait returns: from then on the pid may name
	// an unrelated process, and no signal is aimed at it.
	reaped bool
	cmd    *exec.Cmd
	done   chan struct{}
	before map[string]bool
	task   string
}

type shadowJobView struct {
	shadowJobInfo
	Chunk     string `json:"chunk"`
	Offset    int64  `json:"offset"`
	Truncated bool   `json:"truncated"`
}

type shadowRunner struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
}

type shadowOverview struct {
	AllowRun bool            `json:"allowRun"`
	Runners  []shadowRunner  `json:"runners"`
	Jobs     []shadowJobView `json:"jobs"`
}

type shadowJobs struct {
	root     string
	allowRun bool
	mu       sync.Mutex
	jobs     map[string]*shadowJob
	order    []string
	running  *shadowJob
	closed   bool
}

var errShadowClosing = errors.New("cli: the viewer is shutting down")

func newShadowJobs(root string, allowRun bool) *shadowJobs {
	return &shadowJobs{root: root, allowRun: allowRun, jobs: map[string]*shadowJob{}}
}

func (s *shadowJobs) overview() shadowOverview {
	out := shadowOverview{AllowRun: s.allowRun, Runners: make([]shadowRunner, 0, len(shadowRunnerNames)), Jobs: []shadowJobView{}}
	for _, name := range shadowRunnerNames {
		_, err := exec.LookPath(name)
		out.Runners = append(out.Runners, shadowRunner{Name: name, Available: err == nil})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.order) - 1; i >= 0; i-- {
		out.Jobs = append(out.Jobs, s.jobs[s.order[i]].view(-1))
	}
	return out
}

// validateShadowRequest says what is wrong with a request before anything is
// launched: the repository must be a directory named absolutely, the task
// must be text of a size a task file would hold, the runners known.
func validateShadowRequest(req shadowJobRequest) error {
	if !filepath.IsAbs(req.CWD) {
		return errors.New("cli: the repository path must be absolute")
	}
	if info, err := os.Stat(req.CWD); err != nil || !info.IsDir() {
		return errors.New("cli: the repository path is not a directory")
	}
	if req.Task == "" || len(req.Task) > shadowTaskLimit || !utf8.ValidString(req.Task) {
		return fmt.Errorf("cli: the task must be UTF-8 text of 1 to %d bytes", shadowTaskLimit)
	}
	for i, name := range req.Runners {
		if !slices.Contains(shadowRunnerNames, name) || slices.Contains(req.Runners[:i], name) {
			return fmt.Errorf("cli: unknown or repeated runner %q", name)
		}
	}
	// shadow run compares; it needs both.
	if len(req.Runners) != len(shadowRunnerNames) {
		return fmt.Errorf("cli: a comparison needs both runners: %s", strings.Join(shadowRunnerNames, " and "))
	}
	return nil
}

// start launches one comparison and returns its id at once; the job carries
// on in the background and its output is read as it comes.
func (s *shadowJobs) start(req shadowJobRequest) (string, error) {
	if !s.allowRun {
		return "", errShadowDisabled
	}
	if err := validateShadowRequest(req); err != nil {
		return "", err
	}
	exe, err := sessionExecutable()
	if err != nil {
		return "", fmt.Errorf("cli: locate agentrec: %w", err)
	}
	id, err := newRunID()
	if err != nil {
		return "", err
	}
	taskDir, err := filepath.Abs(filepath.Join(filepath.Dir(s.root), shadowTaskDirName))
	if err != nil {
		return "", fmt.Errorf("cli: locate task directory: %w", err)
	}
	if err := os.MkdirAll(taskDir, 0o700); err != nil {
		return "", fmt.Errorf("cli: create task directory: %w", err)
	}
	// Absolute, because the child runs in the repository, not here.
	taskPath := filepath.Join(taskDir, id+".md")
	if err := os.WriteFile(taskPath, []byte(req.Task), 0o600); err != nil {
		return "", fmt.Errorf("cli: write task: %w", err)
	}
	// The runs this comparison records are the ones that are not there yet.
	before := map[string]bool{}
	if runs, _, err := listRuns(s.root, ""); err == nil {
		for _, run := range runs {
			before[run.ID] = true
		}
	}

	args := []string{"shadow", "run", taskPath}
	for _, name := range req.Runners {
		args = append(args, runnerFlag, name)
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = req.CWD
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	job := &shadowJob{info: shadowJobInfo{ID: id, Status: "running", CWD: req.CWD, Runners: slices.Clone(req.Runners), StartedAt: time.Now(), RunIDs: []string{}}, cmd: cmd, done: make(chan struct{}), before: before, task: taskPath}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		os.Remove(taskPath)
		return "", errShadowClosing
	}
	if s.running != nil {
		s.mu.Unlock()
		os.Remove(taskPath)
		return "", errShadowBusy
	}
	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		pw.Close()
		return "", fmt.Errorf("cli: start comparison: %w", err)
	}
	s.running = job
	s.jobs[id] = job
	s.order = append(s.order, id)
	if len(s.order) > shadowJobHistory {
		delete(s.jobs, s.order[0])
		s.order = s.order[1:]
	}
	s.mu.Unlock()

	go job.read(pr)
	go s.wait(job, pw)
	return id, nil
}

// read keeps the newest output, up to the cap; what falls off the front is
// counted so offsets stay meaningful to a reader that saw it go by.
func (j *shadowJob) read(r io.Reader) {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			j.mu.Lock()
			j.output = append(j.output, buf[:n]...)
			if len(j.output) > shadowJobOutputCap {
				cut := len(j.output) - shadowJobOutputCap/2
				// Never in the middle of a character.
				for cut < len(j.output) && !utf8.RuneStart(j.output[cut]) {
					cut++
				}
				j.dropped += int64(cut)
				j.output = append([]byte(nil), j.output[cut:]...)
				j.truncated = true
			}
			j.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (s *shadowJobs) wait(job *shadowJob, pw *io.PipeWriter) {
	err := job.cmd.Wait()
	job.mu.Lock()
	job.reaped = true
	job.mu.Unlock()
	pw.Close()
	code := 0
	if exitErr := (*exec.ExitError)(nil); errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		code = -1
	}
	recorded := s.recordedBy(job)
	os.Remove(job.task)
	now := time.Now()
	s.mu.Lock()
	job.mu.Lock()
	job.info.EndedAt, job.info.ExitCode, job.info.RunIDs = &now, &code, recorded
	switch {
	case code == 0:
		// Ended on its own terms, whatever was asked of it meanwhile.
		job.info.Status = "completed"
	case job.cancelled:
		job.info.Status = "cancelled"
	default:
		job.info.Status = "failed"
	}
	job.mu.Unlock()
	if s.running == job {
		s.running = nil
	}
	s.mu.Unlock()
	close(job.done)
}

// view is the job with the output after since; since -1 asks for none.
func (j *shadowJob) view(since int64) shadowJobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := shadowJobView{shadowJobInfo: j.info}
	out.Offset = j.dropped + int64(len(j.output))
	if since < 0 {
		return out
	}
	start := since - j.dropped
	if start < 0 {
		start, out.Truncated = 0, true
	}
	// A character still arriving is held back for the next read rather than
	// sent as two halves that decode to nothing.
	end := len(j.output)
	for end > int(start) && end > len(j.output)-utf8.UTFMax && !utf8.FullRune(j.output[lastRuneStart(j.output, end):end]) {
		end = lastRuneStart(j.output, end)
	}
	out.Offset = j.dropped + int64(end)
	if start < int64(end) {
		out.Chunk = string(j.output[start:end])
	}
	return out
}

// lastRuneStart is the index of the last rune start before end.
func lastRuneStart(b []byte, end int) int {
	i := end - 1
	for i > 0 && !utf8.RuneStart(b[i]) {
		i--
	}
	return i
}

func (s *shadowJobs) get(id string, since int64) (shadowJobView, error) {
	s.mu.Lock()
	job, ok := s.jobs[id]
	s.mu.Unlock()
	if !ok {
		return shadowJobView{}, errShadowNotFound
	}
	// While the comparison runs, the runs it has recorded so far are what
	// there is to show of its progress: shadow run itself prints only at the
	// end.
	job.mu.Lock()
	running := job.info.Status == "running"
	job.mu.Unlock()
	if running {
		ids := s.recordedBy(job)
		job.mu.Lock()
		if job.info.Status == "running" {
			job.info.RunIDs = ids
		}
		job.mu.Unlock()
	}
	return job.view(since), nil
}

// recordedBy names the runs a comparison recorded: the ones that were not
// there when it started and were recorded in a shadow workspace — not a
// session someone else happened to record meanwhile.
func (s *shadowJobs) recordedBy(job *shadowJob) []string {
	ids := []string{}
	runs, _, err := listRuns(s.root, "")
	if err != nil {
		return ids
	}
	workspaces := filepath.Join(filepath.Dir(s.root), shadowDirName)
	for _, run := range runs {
		if job.before[run.ID] {
			continue
		}
		if m, err := readManifest(filepath.Join(s.root, run.ID)); err == nil && within(m.CWD, workspaces) {
			ids = append(ids, run.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

// cancel asks a running comparison to stop the way a Ctrl-C would: the
// command holds the interrupt and ends after the leg it is on.
func (s *shadowJobs) cancel(id string) error {
	s.mu.Lock()
	job, ok := s.jobs[id]
	running := ok && s.running == job
	s.mu.Unlock()
	if !ok {
		return errShadowNotFound
	}
	if !running {
		return errShadowNotOpen
	}
	return job.signal(syscall.SIGINT)
}

// signal sends sig to the comparison's process group, unless the child has
// already been reaped and the number may belong to someone else.
func (j *shadowJob) signal(sig syscall.Signal) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.reaped {
		return nil
	}
	j.cancelled = true
	if err := syscall.Kill(-j.cmd.Process.Pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("cli: signal comparison: %w", err)
	}
	return nil
}

// Close ends a comparison still running when the viewer goes: interrupted,
// given a moment to file what it has, then killed.
func (s *shadowJobs) Close() {
	s.mu.Lock()
	s.closed = true
	job := s.running
	s.mu.Unlock()
	if job == nil {
		return
	}
	s.cancel(job.info.ID)
	select {
	case <-job.done:
	case <-time.After(shadowJobStopWait):
		job.signal(syscall.SIGKILL)
		<-job.done
	}
}
