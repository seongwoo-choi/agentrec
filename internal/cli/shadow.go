package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/seongwoo-choi/agentrec/internal/evidence"
	"github.com/seongwoo-choi/agentrec/internal/lock"
	"github.com/seongwoo-choi/agentrec/internal/provider"
	"github.com/seongwoo-choi/agentrec/internal/provider/claude"
	"github.com/seongwoo-choi/agentrec/internal/provider/codex"
	"github.com/seongwoo-choi/agentrec/internal/runner"
	"github.com/seongwoo-choi/agentrec/internal/worktree"
)

const shadowUsage = "usage: agentrec shadow run <task-file> --runner claude --runner codex\n"

// shadowDirName holds the disposable workspaces shadow runs are executed in,
// beside the recorded runs and the locks.
const shadowDirName = "shadow"

// runnerFlag names one agent to record. It is spelled exactly and given once
// per runner: a comparison is between the two runs the operator asked for, and
// an invocation that has to be guessed at is one whose two sides they would
// have to reconstruct from the output.
const runnerFlag = "--runner"

// shadowRunners are the agents agentrec can record, in the order every
// comparison is rendered in. The order is fixed here and not taken from the
// command line, so two operators who asked for the same runs read the same
// comparison whichever order they typed.
var shadowRunners = []string{"claude", "codex"}

// shadowInvocation is one accepted `shadow run`: the task file to read, and the
// runners in the order they will be executed, which is the order they were
// given in.
type shadowInvocation struct {
	taskPath string
	runners  []string
}

// releaseRepository gives the held repository up. It is reached through a
// variable so a test can drive the one difficulty this command cannot be made to
// have from outside it: a lock this process could not give back.
var releaseRepository = (*lock.Repository).Release

// runShadow records one task with each runner, from one committed baseline, and
// renders the two recorded runs side by side.
func runShadow(args []string, stdout, stderr io.Writer) int {
	inv, ok := parseShadow(args)
	if !ok {
		fmt.Fprint(stderr, shadowUsage)
		return exitUsage
	}
	task, err := readTask(inv.taskPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}

	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	pre, err := shadowPreflight(ctx, task)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	// Defensive, and idempotent: the release that decides this command's ending
	// is the one below, and this stands for the paths that return before ever
	// reaching it.
	released := false
	defer func() {
		if released {
			return
		}
		if err := releaseRepository(pre.repo); err != nil {
			fmt.Fprintln(stderr, err)
		}
	}()

	// Everything from here on has prepared something, so nothing from here on is
	// a usage failure: what is left to go wrong is the recording itself.
	group, err := newRunID()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}
	workspaces, err := shadowWorkspaces(pre.dataRoot, group)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailure
	}

	// Installed before the first checkout exists, and buffered, so an interrupt
	// arriving from here on is held rather than ending this process where it
	// stands: a comparison abandoned mid-leg would leave a checkout of the
	// operator's repository behind and a bundle that never says how it ended.
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, handledSignals...)
	defer signal.Stop(interrupt)
	stopHolding := stopHoldingAfterTheFirstSignal()
	defer stopHolding()

	legs, failed, interrupted := shadowLegs(pre, inv, workspaces, interrupt, stderr)

	// Rendered once the checkouts are gone, and read back off disk: what the two
	// runs are compared on is what was persisted about them, not what this
	// process still remembers.
	if err := renderComparison(stdout, pre.runsRoot, legs); err != nil {
		fmt.Fprintln(stderr, err)
		failed = true
	}

	// Given up here rather than only on the way out, so that a lock this process
	// could not give back is part of what the command reports: the repository may
	// still be held, and the next run against it would be refused by a lock
	// nobody means to be holding.
	released = true
	if err := releaseRepository(pre.repo); err != nil {
		fmt.Fprintln(stderr, err)
		failed = true
	}
	// The provider supervisor is not the only phase that can receive the first
	// signal. Cleanup, comparison rendering and lock release are still part of
	// this aggregate command, so latch a signal that arrived after the legs had
	// already returned before deciding the exit.
	if !interrupted && pending(interrupt) {
		interrupted = true
	}

	switch {
	case interrupted:
		return exitInterrupted
	case failed:
		return exitFailure
	}
	return 0
}

// stopHoldingAfterTheFirstSignal hands the two signals back to the operating
// system as soon as one of them arrives, and returns the function that ends the
// watch.
//
// The signal is watched on a channel of this command's own, so the first one is
// observed here whichever part of the recording took it: the supervisor ending
// the provider's process group, or the recorder finishing the evidence
// afterwards. Holding a signal is a promise to write the stopped run down and
// take the checkouts back out, and a comparison has both of those left to do
// for two legs — but it is not a claim on the operator's terminal. An operator
// who asks a second time has decided that wait is over.
func stopHoldingAfterTheFirstSignal() func() {
	stopped := make(chan os.Signal, 1)
	signal.Notify(stopped, handledSignals...)
	done := make(chan struct{})
	go func() {
		select {
		case <-stopped:
			// Reset rather than Stop, because the second ask has to end this
			// process whichever channel is listening for it.
			signal.Reset(handledSignals...)
		case <-done:
		}
		signal.Stop(stopped)
	}()
	return func() { close(done) }
}

// shadowDirMode keeps a comparison's checkouts readable only by the operator who
// recorded them: a linked worktree holds the whole committed repository, which
// may be a private one.
const shadowDirMode os.FileMode = 0o700

// shadowWorkspaces prepares the private directory this comparison's checkouts
// are created in, under the agentrec data root and never inside the repository
// being recorded.
func shadowWorkspaces(dataRoot, group string) (string, error) {
	root := filepath.Join(dataRoot, shadowDirName)
	if err := os.Mkdir(root, shadowDirMode); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("cli: create the shadow workspace root %s: %w", strconv.Quote(root), err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("cli: inspect the shadow workspace root %s: %w", strconv.Quote(root), err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("cli: shadow workspace root %s is not a directory owned by agentrec", strconv.Quote(root))
	}
	if err := os.Chmod(root, shadowDirMode); err != nil {
		return "", fmt.Errorf("cli: restrict %s: %w", strconv.Quote(root), err)
	}
	dir := filepath.Join(root, group)
	if err := os.Mkdir(dir, shadowDirMode); err != nil {
		return "", fmt.Errorf("cli: create the shadow workspace %s: %w", strconv.Quote(dir), err)
	}
	if err := os.Chmod(dir, shadowDirMode); err != nil {
		return "", fmt.Errorf("cli: restrict %s: %w", strconv.Quote(dir), err)
	}
	return dir, nil
}

// leg is one recorded side of a comparison: which agent was recorded, and the
// run its evidence is under. It carries no verdict of its own — what the two
// runs came to is read back from their bundles.
type leg struct {
	runner string
	runID  string
}

// shadowLegs records each runner in the order the operator gave them, one after
// another so that neither verification nor any external state either run
// touches is shared with a run happening at the same time. Every checkout it
// prepares is removed before it returns, whatever happened in it.
func shadowLegs(pre *prepared, inv shadowInvocation, workspaces string, interrupt <-chan os.Signal, stderr io.Writer) (legs []leg, failed, interrupted bool) {
	var trees []*worktree.Worktree
	defer func() {
		if err := removeWorkspaces(trees, workspaces); err != nil {
			fmt.Fprintln(stderr, err)
			failed = true
		}
	}()

	for _, name := range inv.runners {
		// Asked before every leg, so an interrupt that arrived while the last one
		// was being finished or this one prepared never launches another agent.
		if pending(interrupt) {
			return legs, failed, true
		}

		versionCtx, cancelVersion := context.WithTimeout(context.Background(), versionTimeout)
		cmd, parse, err := shadowCommand(versionCtx, name, pre.task)
		cancelVersion()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return legs, true, false
		}

		gitCtx, cancelGit := context.WithTimeout(context.Background(), gitTimeout)
		tree, err := worktree.Add(gitCtx, pre.repo.Root(), filepath.Join(workspaces, name), pre.baseline)
		cancelGit()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return legs, true, false
		}
		trees = append(trees, tree)
		// A linked worktree holds the whole committed repository, which may be a
		// private one, and Git creates its directory against the operator's
		// umask. It is narrowed to the operator alone, the way everything else
		// agentrec writes is.
		if err := os.Chmod(tree.Path(), shadowDirMode); err != nil {
			fmt.Fprintf(stderr, "cli: restrict %s: %v\n", strconv.Quote(tree.Path()), err)
			return legs, true, false
		}

		runID, err := newRunID()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return legs, true, false
		}
		// Asked again here, with nothing left to prepare: preparing this leg asked
		// the provider its version and checked a worktree out, and an interrupt
		// that arrived during either of those is one the check at the top of the
		// loop could not have seen. The checkout is taken back out on the way past
		// this by the cleanup already deferred.
		if pending(interrupt) {
			return legs, failed, true
		}

		out := record(recordRequest{
			Provider: name,
			Command:  cmd,
			Parser:   parse,
			Prompt:   pre.task,
			// The checkout is both where the agent works and what its evidence is
			// measured in: a run recorded here says what it did to the repository
			// it was given, and the operator's own checkout is not that repository.
			CWD:      tree.Path(),
			RepoRoot: tree.Path(),
			RunsRoot: pre.runsRoot,
			RunID:    runID,
			// Not an option: two runs nothing judged are two runs there is nothing
			// to compare.
			Verify:    true,
			Interrupt: interrupt,
		}, stderr)

		// A run that never reached its provider left a finalized bundle saying so
		// and nothing to compare, so the comparison stops there.
		if !out.Recorded {
			return legs, true, false
		}
		legs = append(legs, leg{runner: name, runID: runID})
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), gitTimeout)
		cleanupErr := tree.Remove(cleanupCtx)
		cancelCleanup()
		if cleanupErr != nil {
			fmt.Fprintln(stderr, cleanupErr)
			return legs, true, out.Interrupted || out.Result.ExitReason == runner.ReasonInterrupted
		}
		stateCtx, cancelState := context.WithTimeout(context.Background(), gitTimeout)
		state, stateErr := readRepositoryState(stateCtx, pre.repo.Root())
		cancelState()
		if stateErr != nil {
			fmt.Fprintln(stderr, stateErr)
			return legs, true, out.Interrupted || out.Result.ExitReason == runner.ReasonInterrupted
		}
		if changed := changedRepositoryFields(pre.source, state); len(changed) > 0 {
			fmt.Fprintf(stderr, "cli: source repository changed during shadow run (%s); refusing to launch another provider; changes were not restored\n", strings.Join(changed, ", "))
			return legs, true, out.Interrupted || out.Result.ExitReason == runner.ReasonInterrupted
		}
		switch {
		case out.Interrupted || out.Result.ExitReason == runner.ReasonInterrupted:
			return legs, failed, true
		case out.Incomplete:
			// The evidence this leg was to be compared on is short, and the next
			// leg would be compared against a gap.
			return legs, true, false
		}
		// How the agent's own run ended is evidence and not a reason to abandon
		// the other one: a failed run is exactly the run worth comparing. It does
		// fail the command, once both legs have been recorded.
		if out.RunErr != nil || out.Result.ExitReason != runner.ReasonCompleted ||
			out.Verification.Status != evidence.VerificationPassed {
			failed = true
		}
	}
	return legs, failed, false
}

// removeWorkspaces takes the checkouts back out in the reverse of the order they
// were created, and then the directory that held them. Every failure is
// reported: a checkout left behind is a copy of the operator's repository nobody
// is expecting, and the next comparison would find the administration entry for
// it still in the source repository.
func removeWorkspaces(trees []*worktree.Worktree, workspaces string) error {
	var errs []error
	for i := len(trees) - 1; i >= 0; i-- {
		ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
		err := trees[i].Remove(ctx)
		cancel()
		if err != nil {
			errs = append(errs, err)
		}
	}
	if err := os.Remove(workspaces); err != nil {
		errs = append(errs, fmt.Errorf("cli: remove the shadow workspace %s: %w", strconv.Quote(workspaces), err))
	}
	return errors.Join(errs...)
}

// pending reports whether the operator has already asked for the run to stop.
func pending(interrupt <-chan os.Signal) bool {
	select {
	case <-interrupt:
		return true
	default:
		return false
	}
}

// shadowCommand prepares one agent's invocation through the same adapter a
// traced run goes through, so what a comparison launches is what agentrec
// records everywhere else: the task on the command line, and nothing added to
// widen what the agent may do.
func shadowCommand(ctx context.Context, name, task string) (provider.Command, runner.Parser, error) {
	switch name {
	case "claude":
		cmd, err := claude.PrepareCommand(ctx, []string{"-p", "--", task}, nil)
		return cmd, claudeParser, err
	case "codex":
		// Behind the delimiter, so the task reaches Codex as the one positional
		// prompt whatever it spells: a task file beginning with an option would
		// otherwise be read as that option, and the agent launched with something
		// the operator never asked for.
		cmd, err := codex.PrepareCommand(ctx, []string{"exec", "--", task}, nil)
		return cmd, codexParser, err
	}
	return provider.Command{}, nil, fmt.Errorf("cli: %s is not an agent agentrec records", strconv.Quote(name))
}

// prepared is everything a comparison rests on, settled before either agent is
// launched: the held source repository, the one commit both runs are checked
// out from, the task both are given, and where their evidence and their
// workspaces go.
type prepared struct {
	repo     *lock.Repository
	baseline string
	source   repositoryState
	task     string
	runsRoot string
	dataRoot string
}

// repositoryState is what Shadow can observe about the source repository. A
// linked worktree is not a sandbox, so this is a drift detector rather than a
// claim that a provider cannot reach the source checkout or common refs.
type repositoryState struct {
	head      string
	status    string
	index     string
	refs      string
	worktrees string
}

// shadowPreflight settles all of it, or refuses. Nothing here writes into the
// source repository and nothing here prepares a workspace: everything this
// function can refuse is refused while the only thing that has happened is that
// questions were asked.
func shadowPreflight(ctx context.Context, task string) (*prepared, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("cli: locate working directory: %w", err)
	}
	runs, err := runsRoot()
	if err != nil {
		return nil, err
	}
	data, err := filepath.Abs(filepath.Dir(runs))
	if err != nil {
		return nil, fmt.Errorf("cli: resolve data directory: %w", err)
	}

	// Asked before the lock is taken, because taking it writes a lock file: a
	// data directory inside the repository being recorded would be written into
	// the very checkout this command must leave untouched, and the run that
	// found it there would measure agentrec's own bookkeeping as the agent's
	// work.
	root, err := gitToplevel(ctx, cwd)
	if err != nil {
		return nil, err
	}
	if within(data, root) {
		return nil, fmt.Errorf("cli: the agentrec data directory %s is inside the repository %s being recorded: set AGENTREC_HOME to a directory outside it",
			strconv.Quote(data), strconv.Quote(root))
	}

	// The repository is taken before it is judged: a comparison recorded against
	// a repository another run is already changing cannot say afterwards which
	// changes were whose. It is held until this command returns, which is after
	// both runs have been recorded and their workspaces removed.
	repo, err := lock.Acquire(ctx, filepath.Join(data, locksDirName), cwd)
	if err != nil {
		return nil, err
	}
	pre := &prepared{repo: repo, task: task, runsRoot: runs, dataRoot: data}
	if err := shadowCheckRepository(ctx, pre); err != nil {
		if rerr := repo.Release(); rerr != nil {
			return nil, errors.Join(err, rerr)
		}
		return nil, err
	}
	return pre, nil
}

// shadowCheckRepository asks the held repository whether it can be compared in
// at all, and pins the commit both runs start from.
func shadowCheckRepository(ctx context.Context, pre *prepared) error {
	root := pre.repo.Root()
	if err := lock.CheckClean(ctx, root); err != nil {
		return err
	}

	// Resolved once, and both worktrees are created from this exact commit: two
	// runs started from two readings of a branch are two runs that were never
	// comparable.
	baseline, err := shadowGit(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	pre.baseline = baseline

	// The configuration the checks are pinned to has to be part of what each
	// worktree is given from the committed tree: a
	// configuration only in the operator's own checkout would leave both runs
	// unverifiable and neither of them saying so.
	entry, err := shadowGit(ctx, root, "ls-tree", "HEAD", "--", verifyConfigFile)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(entry, "100644 blob ") && !strings.HasPrefix(entry, "100755 blob ") {
		return fmt.Errorf("cli: %s is not a committed file at HEAD of %s: commit it, so both runs are verified against the configuration they were started from",
			verifyConfigFile, strconv.Quote(root))
	}

	// A linked worktree starts from the committed tree through the operator's Git
	// checkout configuration. Where committed entries require project material
	// agentrec does not prepare, the workspace is refused rather than silently
	// prepared incomplete.
	modules, err := shadowGit(ctx, root, "ls-tree", "HEAD", "--", gitmodulesFile)
	if err != nil {
		return err
	}
	if modules != "" {
		return fmt.Errorf("cli: repository %s commits %s: agentrec cannot yet prepare submodules in a linked worktree, so this repository is not one it can compare runs in",
			strconv.Quote(root), gitmodulesFile)
	}
	pointers, err := commitsLFSPointer(ctx, root)
	if err != nil {
		return err
	}
	if pointers {
		return fmt.Errorf("cli: repository %s commits Git LFS pointer files: a linked worktree would hold the pointers rather than the files, so this repository is not one agentrec can compare runs in",
			strconv.Quote(root))
	}
	pre.source, err = readRepositoryState(ctx, root)
	if err != nil {
		return err
	}
	return nil
}

func readRepositoryState(ctx context.Context, root string) (repositoryState, error) {
	var state repositoryState
	readings := []struct {
		name string
		dst  *string
		args []string
	}{
		{"HEAD", &state.head, []string{"rev-parse", "HEAD"}},
		{"status", &state.status, []string{"status", "--porcelain=v2", "--untracked-files=all"}},
		{"index", &state.index, []string{"ls-files", "--stage", "-z"}},
		{"refs", &state.refs, []string{"for-each-ref", "--format=%(refname) %(objectname)"}},
		{"worktrees", &state.worktrees, []string{"worktree", "list", "--porcelain", "-z"}},
	}
	for _, reading := range readings {
		value, err := shadowGit(ctx, root, reading.args...)
		if err != nil {
			return repositoryState{}, fmt.Errorf("cli: snapshot source repository %s: %w", reading.name, err)
		}
		*reading.dst = value
	}
	return state, nil
}

func changedRepositoryFields(before, after repositoryState) []string {
	var changed []string
	for _, field := range []struct {
		name          string
		before, after string
	}{
		{"HEAD", before.head, after.head},
		{"status", before.status, after.status},
		{"index", before.index, after.index},
		{"refs", before.refs, after.refs},
		{"worktrees", before.worktrees, after.worktrees},
	} {
		if field.before != field.after {
			changed = append(changed, field.name)
		}
	}
	return changed
}

// gitmodulesFile is where Git records a repository's submodules. Only the one
// at the top level is Git's own, which is the one a linked worktree would need
// and the one this command refuses.
const gitmodulesFile = ".gitmodules"

// lfsPointerPrefix is the first line every Git LFS pointer file starts with. It
// is what a repository's committed tree holds in place of a large file.
const lfsPointerPrefix = "version https://git-lfs.github.com/spec/v1"

// within reports whether path is the directory root or sits inside it. Both are
// resolved as far as they exist, so a symlink on the way to either cannot make
// two names for one directory read as two directories.
func within(path, root string) bool {
	rel, err := filepath.Rel(resolved(root), resolved(path))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// resolved follows the links in the part of path that exists and keeps the rest
// as it was given, so a directory that has not been created yet is still
// compared as the place it would be created in.
func resolved(path string) string {
	rest := ""
	for dir := filepath.Clean(path); ; dir = filepath.Dir(dir) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Clean(path)
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

// gitToplevel is the repository containing cwd, resolved the way the lock
// resolves it, so the two always name the same directory.
func gitToplevel(ctx context.Context, cwd string) (string, error) {
	out, err := shadowGit(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("cli: %s is not inside a Git repository: agentrec records runs against a repository (%w)", strconv.Quote(cwd), err)
	}
	real, err := filepath.EvalSymlinks(out)
	if err != nil {
		return "", fmt.Errorf("cli: resolve repository root %s: %w", strconv.Quote(out), err)
	}
	return real, nil
}

// shadowGit asks one question of the repository at dir, launching Git directly
// with no shell anywhere. LC_ALL=C keeps Git's own messages in the language this
// file reads and reports, and GIT_OPTIONAL_LOCKS=0 keeps a question from
// refreshing an index the operator did not ask to have written.
func shadowGit(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitOutput(ctx, dir, args...)
	if err != nil {
		return "", gitError(args, err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

func gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0")
	return cmd.Output()
}

// gitError reports what Git said, with its own words made safe to print: the
// escapes in them would otherwise drive the terminal they are reported on.
func gitError(args []string, err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		return fmt.Errorf("cli: git %s: %s", strings.Join(args, " "), oneLine(string(exit.Stderr)))
	}
	return fmt.Errorf("cli: git %s: %w", strings.Join(args, " "), err)
}

// commitsLFSPointer finds blobs that mention the LFS header and then validates
// the canonical three-line pointer format. A source file or document that merely
// quotes the header is not an LFS-managed file and must not make the repository
// unusable for Shadow runs.
func commitsLFSPointer(ctx context.Context, dir string) (bool, error) {
	args := []string{"grep", "-l", "-z", "--fixed-strings", "-e", lfsPointerPrefix, "HEAD"}
	out, err := gitOutput(ctx, dir, args...)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return false, nil
		}
		return false, gitError(args, err)
	}
	for _, hit := range strings.Split(string(out), "\x00") {
		if hit == "" {
			continue
		}
		blob, err := gitOutput(ctx, dir, "cat-file", "blob", hit)
		if err != nil {
			return false, gitError([]string{"cat-file", "blob", hit}, err)
		}
		if isLFSPointer(string(blob)) {
			return true, nil
		}
	}
	return false, nil
}

func isLFSPointer(text string) bool {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) != 3 || lines[0] != lfsPointerPrefix || !strings.HasPrefix(lines[1], "oid sha256:") || !strings.HasPrefix(lines[2], "size ") {
		return false
	}
	oid := strings.TrimPrefix(lines[1], "oid sha256:")
	if len(oid) != 64 {
		return false
	}
	for _, r := range oid {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	_, err := strconv.ParseUint(strings.TrimPrefix(lines[2], "size "), 10, 64)
	return err == nil
}

// parseShadow reads the invocation exactly. Every argument is required to be
// the one that belongs in its place: agentrec is about to launch two agents
// against a repository, and an argument list it had to interpret is one the
// operator cannot check afterwards.
func parseShadow(args []string) (shadowInvocation, bool) {
	if len(args) < 2 || args[0] != "run" {
		return shadowInvocation{}, false
	}
	inv := shadowInvocation{taskPath: args[1]}
	rest := args[2:]
	if len(rest) != 2*len(shadowRunners) {
		return shadowInvocation{}, false
	}
	for i := 0; i < len(rest); i += 2 {
		name := rest[i+1]
		if rest[i] != runnerFlag || !slices.Contains(shadowRunners, name) || slices.Contains(inv.runners, name) {
			return shadowInvocation{}, false
		}
		inv.runners = append(inv.runners, name)
	}
	return inv, true
}

// maxTaskBytes bounds the task file. The task is handed to each agent as one
// argument, so it is a request an operator wrote and not a corpus: a file past
// this size is one this command would be guessing about.
const maxTaskBytes = 64 << 10

// readTask reads the request both runs are given, once, before anything is
// prepared. It is one regular file of bounded text: a directory, a symlink, a
// file that grew past the bound or one holding bytes that are not text is
// refused rather than passed on, because what reaches the two agents has to be
// the bytes an operator can quote back off disk.
func readTask(path string) (string, error) {
	// O_NOFOLLOW, so a symlink at the name the operator gave is refused as
	// itself rather than followed: what a link points at is not what they named,
	// and it need not still point there for the second run.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("cli: read task file %s: %w", strconv.Quote(path), err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("cli: read task file %s: %w", strconv.Quote(path), err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("cli: task file %s is %s, want a regular file", strconv.Quote(path), info.Mode().Type())
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxTaskBytes+1))
	if err != nil {
		return "", fmt.Errorf("cli: read task file %s: %w", strconv.Quote(path), err)
	}
	switch {
	case len(raw) == 0:
		return "", fmt.Errorf("cli: task file %s is empty", strconv.Quote(path))
	case len(raw) > maxTaskBytes:
		return "", fmt.Errorf("cli: task file %s is larger than %d bytes", strconv.Quote(path), maxTaskBytes)
	case !utf8.Valid(raw):
		return "", fmt.Errorf("cli: task file %s is not UTF-8 text", strconv.Quote(path))
	}
	return string(raw), nil
}
