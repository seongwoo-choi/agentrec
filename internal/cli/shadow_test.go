package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/seongwoo-choi/agentrec/internal/lock"
	"github.com/seongwoo-choi/agentrec/internal/runner"
	"github.com/seongwoo-choi/agentrec/internal/storage"
)

// probeWorkspaces collects what each provider stand-in found in the checkout it
// was launched in. The checkout is gone by the time a shadow run returns, so
// this is the only account of what it held while the run was happening.
func probeWorkspaces(t *testing.T) func(name string) workspaceProbe {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(probeEnv, dir)
	return func(name string) workspaceProbe {
		t.Helper()
		var probe workspaceProbe
		readJSONFile(t, filepath.Join(dir, name+".json"), &probe)
		return probe
	}
}

// sourceSnapshot is the observable source state agentrec's own lifecycle must
// leave as it found it: where it stands, what is in it, every ref, every
// worktree, and the bytes and modes of every tracked file.
type sourceSnapshot struct {
	head      string
	status    string
	refs      string
	worktrees string
	tracked   string
}

func snapshotSource(t *testing.T, repo string) sourceSnapshot {
	t.Helper()
	return sourceSnapshot{
		head:      gitIn(t, repo, "rev-parse", "HEAD"),
		status:    gitIn(t, repo, "status", "--porcelain=v1", "--untracked-files=all"),
		refs:      gitIn(t, repo, "for-each-ref", "--format=%(refname) %(objectname)"),
		worktrees: gitIn(t, repo, "worktree", "list", "--porcelain"),
		tracked:   gitIn(t, repo, "ls-files", "--stage"),
	}
}

// wantSourceUnchanged fails when a shadow run left anything of itself in the
// repository it was recorded from: the recorded runs happened elsewhere, and a
// checkout that moved would be one whose operator has to work out which of the
// two agents moved it.
func wantSourceUnchanged(t *testing.T, repo string, before sourceSnapshot) {
	t.Helper()
	after := snapshotSource(t, repo)
	for _, field := range []struct {
		name          string
		before, after string
	}{
		{"HEAD", before.head, after.head},
		{"status", before.status, after.status},
		{"refs", before.refs, after.refs},
		{"worktrees", before.worktrees, after.worktrees},
		{"tracked files", before.tracked, after.tracked},
	} {
		if field.before != field.after {
			t.Errorf("source %s =\n%s\nwant it left as it was:\n%s", field.name, field.after, field.before)
		}
	}
}

// shadowBundles indexes the runs recorded under root by the provider each one
// records, which is how a test asks for one leg's evidence without having to
// know what its run was called.
func shadowBundles(t *testing.T, root string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read runs root: %v", err)
	}
	byProvider := map[string]string{}
	for _, entry := range entries {
		manifest, err := readManifest(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatalf("read manifest of %s: %v", entry.Name(), err)
		}
		if _, ok := byProvider[manifest.Provider]; ok {
			t.Fatalf("two runs recorded for %s, want one leg each", manifest.Provider)
		}
		byProvider[manifest.Provider] = entry.Name()
	}
	return byProvider
}

// Both agents are given the same task from the same commit, in separate
// checkouts owned by agentrec. What each leg was started from is the question a
// comparison rests on, so it is asked of the providers themselves while they
// are running, and of the bundles they left once their checkouts are gone.
func TestShadowRecordsBothRunnersFromOneBaseline(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	stubProviders(t, "claude", "codex", verifyHelperName)
	commitVerifyConfig(t, repo, verifyHelperName, "pass")
	probe := probeWorkspaces(t)
	const body = "change the README\n"
	task := writeTask(t, body)
	baseline := gitIn(t, repo, "rev-parse", "HEAD")
	before := snapshotSource(t, repo)

	code, stdout, stderr := run(t, "shadow", "run", task, "--runner", "claude", "--runner", "codex")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}

	// Each leg ran in a private checkout of its own, at the one pinned commit,
	// with nothing already changed in it and the committed configuration the
	// checks are pinned to present.
	data := dataRoot(root)
	for _, name := range shadowRunners {
		got := probe(name)
		if got.CWD == repo || within(got.CWD, repo) {
			t.Errorf("%s ran in %q, want a checkout outside the source repository %q", name, got.CWD, repo)
		}
		if !within(got.CWD, filepath.Join(data, shadowDirName)) {
			t.Errorf("%s ran in %q, want a workspace under %q", name, got.CWD, filepath.Join(data, shadowDirName))
		}
		if got.Head != baseline {
			t.Errorf("%s started at %q, want the pinned baseline %q", name, got.Head, baseline)
		}
		if got.Status != "" {
			t.Errorf("%s started in a checkout holding %q, want a clean entry", name, got.Status)
		}
		if !got.Config {
			t.Errorf("%s started without %s in its checkout", name, verifyConfigFile)
		}
		// A linked worktree can hold private repository content, so its directory
		// is restricted to the operator who recorded it.
		if os.FileMode(got.Mode) != shadowDirMode {
			t.Errorf("%s ran in a checkout with mode %v, want %v", name, os.FileMode(got.Mode), shadowDirMode)
		}
	}
	// The two workspaces are not the same directory: a comparison between two
	// runs of one checkout is not a comparison at all.
	if probe("claude").CWD == probe("codex").CWD {
		t.Errorf("both runners ran in %q, want a checkout each", probe("claude").CWD)
	}

	// Each leg left an ordinary bundle, carrying the task as the prompt the
	// provider was given, and both survive the deletion of the checkouts they
	// were recorded in.
	bundles := shadowBundles(t, root)
	if len(bundles) != len(shadowRunners) {
		t.Fatalf("recorded runs = %v, want one for each runner", bundles)
	}
	for _, name := range shadowRunners {
		runID, ok := bundles[name]
		if !ok {
			t.Fatalf("recorded runs = %v, want one recorded for %s", bundles, name)
		}
		manifest, err := readManifest(filepath.Join(root, runID))
		if err != nil {
			t.Fatalf("read manifest of %s: %v", runID, err)
		}
		want := [][]string{{"-p", "--", body}}
		if name == "codex" {
			want = [][]string{{"exec", "--json", "--", body}}
		}
		for _, seq := range want {
			if !containsSequence(manifest.Argv, seq) {
				t.Errorf("%s argv = %q, want it to carry %q", name, manifest.Argv, seq)
			}
		}
		for _, arg := range manifest.Argv {
			flag, _, _ := strings.Cut(arg, "=")
			if slices.Contains(forbiddenFlags, flag) {
				t.Errorf("%s argv = %q, want no injected %q", name, manifest.Argv, arg)
			}
		}
		if manifest.ExitReason != runner.ReasonCompleted {
			t.Errorf("%s exit reason = %q, want %q", name, manifest.ExitReason, runner.ReasonCompleted)
		}
		wantGitArtifacts(t, filepath.Join(root, runID))
		// Verification is not optional for a comparison: two runs nothing judged
		// are two runs there is nothing to compare.
		if res := readVerifyResult(t, filepath.Join(root, runID)); res.Status != "passed" {
			t.Errorf("%s verification = %q, want the pinned checks run and passed", name, res.Status)
		}
		if _, err := os.Stat(filepath.Join(root, runID, reportFile)); err != nil {
			t.Errorf("%s report: %v", name, err)
		}
		if code, shown, stderr := run(t, "show", runID); code != 0 {
			t.Errorf("show %s exit code = %d, want 0 (stderr %q)", runID, code, stderr)
		} else if !strings.Contains(shown, "SUPERVISOR-OBSERVED RESULT") {
			t.Errorf("show %s =\n%s\nwant the recorded run rendered back", runID, shown)
		}
		if !strings.Contains(stdout, runID) {
			t.Errorf("stdout =\n%s\nwant it to name run %s", stdout, runID)
		}
	}

	// Nothing agentrec prepared outlives the run, and the directory the
	// checkouts were made in is private too. The durable group records only the
	// comparison identity and its legs: its task body belongs only in the
	// ordinary per-run bundles, not in the shared group index.
	shadow := filepath.Join(data, shadowDirName)
	info, err := os.Stat(shadow)
	if err != nil {
		t.Fatalf("stat %s: %v", shadow, err)
	}
	if info.Mode().Perm() != shadowDirMode {
		t.Errorf("%s mode = %v, want %v", shadow, info.Mode().Perm(), shadowDirMode)
	}
	entries, err := os.ReadDir(shadow)
	if err != nil {
		t.Fatalf("read shadow root: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("shadow groups = %d, want one", len(entries))
	}
	groupID := entries[0].Name()
	groupPath := filepath.Join(shadow, groupID)
	groupInfo, err := os.Stat(groupPath)
	if err != nil {
		t.Fatalf("stat group: %v", err)
	}
	if groupInfo.Mode().Perm() != shadowDirMode {
		t.Errorf("group mode = %v, want %v", groupInfo.Mode().Perm(), shadowDirMode)
	}
	groupFile := filepath.Join(groupPath, "group.json")
	groupFileInfo, err := os.Stat(groupFile)
	if err != nil {
		t.Fatalf("stat group file: %v", err)
	}
	if groupFileInfo.Mode().Perm() != 0o600 {
		t.Errorf("group file mode = %v, want 0600", groupFileInfo.Mode().Perm())
	}
	rawGroup, err := os.ReadFile(groupFile)
	if err != nil {
		t.Fatalf("read group file: %v", err)
	}
	if strings.Contains(string(rawGroup), body) {
		t.Errorf("group file = %s, want no raw task body", rawGroup)
	}
	var group struct {
		Schema   int    `json:"schema"`
		Baseline string `json:"baseline"`
		Outcome  string `json:"outcome"`
		Legs     []struct {
			Runner string `json:"runner"`
			RunID  string `json:"runId"`
			Order  int    `json:"order"`
		} `json:"legs"`
	}
	if err := json.Unmarshal(rawGroup, &group); err != nil {
		t.Fatalf("decode group file: %v", err)
	}
	if group.Schema != 1 || group.Baseline != baseline || group.Outcome != "completed" {
		t.Errorf("group = %+v, want schema 1, baseline %q, completed outcome", group, baseline)
	}
	if len(group.Legs) != 2 || group.Legs[0].Runner != "claude" || group.Legs[0].RunID != bundles["claude"] || group.Legs[0].Order != 1 || group.Legs[1].Runner != "codex" || group.Legs[1].RunID != bundles["codex"] || group.Legs[1].Order != 2 {
		t.Errorf("group legs = %+v, want ordered recorded bundle IDs", group.Legs)
	}
	if _, err := os.Stat(filepath.Join(groupPath, "workspaces")); !os.IsNotExist(err) {
		t.Errorf("group workspaces = %v, want removed", err)
	}
	if code, shown, stderr := run(t, "shadow", "show", groupID); code != 0 {
		t.Errorf("shadow show exit code = %d, want 0 (stderr %q)", code, stderr)
	} else if shown != stdout {
		t.Errorf("shadow show =\n%s\nwant original comparison\n%s", shown, stdout)
	}
	wantSourceUnchanged(t, repo, before)
}

// A group document is operator-facing durable evidence. Shadow show must refuse
// a planted link or bytes it cannot validate before it ever looks up the runs
// named by the group.
func TestShadowShowRefusesUnsafeGroupDocument(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(t *testing.T, path string)
	}{
		{
			name: "symlink",
			write: func(t *testing.T, path string) {
				target := filepath.Join(t.TempDir(), "outside.json")
				if err := os.WriteFile(target, []byte(`{"schema":1}`), shadowFileMode); err != nil {
					t.Fatalf("write symlink target: %v", err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatalf("create group symlink: %v", err)
				}
			},
		},
		{
			name: "malformed",
			write: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte(`{"schema":1}`), shadowFileMode); err != nil {
					t.Fatalf("write malformed group: %v", err)
				}
			},
		},
		{
			name: "oversize",
			write: func(t *testing.T, path string) {
				if err := os.WriteFile(path, make([]byte, maxShadowGroupBytes+1), shadowFileMode); err != nil {
					t.Fatalf("write oversize group: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := home(t)
			const groupID = "shadow-group"
			groupDir := filepath.Join(dataRoot(root), shadowDirName, groupID)
			if err := os.MkdirAll(groupDir, shadowDirMode); err != nil {
				t.Fatalf("create group directory: %v", err)
			}
			tc.write(t, filepath.Join(groupDir, shadowGroupFile))

			code, stdout, stderr := run(t, "shadow", "show", groupID)

			if code != exitFailure {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, exitFailure, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, "shadow group") {
				t.Errorf("stderr = %q, want shadow group rejection", stderr)
			}
		})
	}
}

// A root is an ownership handle, not merely a convenient way to spell a path.
// If an untrusted provider replaces the name while a shadow run is active, the
// group artifact must either land in the directory agentrec created or fail —
// never in the replacement.
func TestWriteShadowGroupStaysInHeldDirectoryAfterPathReplacement(t *testing.T) {
	parent := t.TempDir()
	groupDir := filepath.Join(parent, "group")
	if err := os.Mkdir(groupDir, shadowDirMode); err != nil {
		t.Fatalf("create group directory: %v", err)
	}
	root, err := os.OpenRoot(groupDir)
	if err != nil {
		t.Fatalf("hold group directory: %v", err)
	}
	t.Cleanup(func() { root.Close() })
	moved := filepath.Join(parent, "moved")
	if err := os.Rename(groupDir, moved); err != nil {
		t.Fatalf("move held directory: %v", err)
	}
	if err := os.Mkdir(groupDir, shadowDirMode); err != nil {
		t.Fatalf("replace group directory: %v", err)
	}
	group := shadowGroup{Schema: 1, Baseline: strings.Repeat("a", 40), Outcome: "failed"}

	if err := writeShadowGroup(root, group); err != nil {
		t.Fatalf("write held group: %v", err)
	}
	if _, err := os.Stat(filepath.Join(moved, shadowGroupFile)); err != nil {
		t.Errorf("held directory group file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(groupDir, shadowGroupFile)); !os.IsNotExist(err) {
		t.Errorf("replacement directory group file: %v, want absent", err)
	}
}

// A linked worktree is not a sandbox: a provider can name the source checkout
// directly or update a ref through the common Git directory. Shadow must detect
// either observed drift after taking its own worktree back out, fail closed, and
// never launch the second provider. It does not destructively restore provider
// changes.
func TestShadowStopsWhenAProviderMutatesTheSourceRepository(t *testing.T) {
	for _, mutation := range []string{"file", "assume-unchanged", "skip-worktree", "ref", "head", "config"} {
		t.Run(mutation, func(t *testing.T) {
			root := home(t)
			repo := cleanRepo(t)
			stubProviders(t, "claude", "codex", verifyHelperName)
			commitVerifyConfig(t, repo, verifyHelperName, "pass")
			if mutation == "head" {
				gitIn(t, repo, "branch", "provider-alternate-source")
			}
			probeWorkspaces(t)
			t.Setenv(mutateSourceEnv, "claude:"+mutation+":"+repo)

			code, stdout, stderr := run(t, "shadow", "run", writeTask(t, "change the README\n"), "--runner", "claude", "--runner", "codex")

			if code != exitFailure {
				t.Fatalf("exit code = %d, want %d (stderr %q)", code, exitFailure, stderr)
			}
			if !strings.Contains(stdout, "claude") || !strings.Contains(stdout, "codex") || !strings.Contains(stdout, "(not run)") {
				t.Errorf("stdout = %q, want the recorded first leg and an explicit not-run second leg", stdout)
			}
			if !strings.Contains(stderr, "source repository changed") {
				t.Errorf("stderr = %q, want source drift reported", stderr)
			}
			switch mutation {
			case "file", "assume-unchanged", "skip-worktree":
				if raw, err := os.ReadFile(filepath.Join(repo, "README.md")); err != nil || string(raw) != "mutated outside the shadow worktree\n" {
					t.Errorf("source file = %q, %v; want provider change left for manual recovery", raw, err)
				}
			case "ref":
				if _, err := os.Stat(filepath.Join(repo, ".git", "refs", "heads", "provider-mutated-source")); err != nil {
					t.Errorf("provider-created ref: %v; want it left for manual recovery", err)
				}
			case "head":
				if raw, err := os.ReadFile(filepath.Join(repo, ".git", "HEAD")); err != nil || !strings.Contains(string(raw), "provider-alternate-source") {
					t.Errorf("source HEAD = %q, %v; want provider branch switch left for manual recovery", raw, err)
				}
			case "config":
				if got := gitIn(t, repo, "config", "--local", "--get", "agentrec.provider-mutated"); got != "true" {
					t.Errorf("source config value = %q, want provider change left for manual recovery", got)
				}
			}
			if _, err := os.Stat(filepath.Join(os.Getenv(probeEnv), "claude.json")); err != nil {
				t.Errorf("first provider probe: %v", err)
			}
			if _, err := os.Stat(filepath.Join(os.Getenv(probeEnv), "codex.json")); !os.IsNotExist(err) {
				t.Errorf("second provider probe error = %v, want it not launched", err)
			}
			if entries, err := os.ReadDir(filepath.Join(dataRoot(root), shadowDirName)); err != nil || len(entries) != 1 {
				t.Errorf("shadow groups = %v, %v; want one persisted group", entries, err)
			} else if group, err := readShadowGroup(dataRoot(root), entries[0].Name()); err != nil {
				t.Errorf("read persisted group: %v", err)
			} else if group.Outcome != "source_drift" {
				t.Errorf("group outcome = %q, want source_drift", group.Outcome)
			}
			wantNoWorkspace(t, root)
		})
	}
}

func TestGitIndexDigestTracksFlagsInMainAndLinkedWorktrees(t *testing.T) {
	repo := cleanRepo(t)
	linked := filepath.Join(t.TempDir(), "linked")
	gitIn(t, repo, "worktree", "add", "--detach", linked, "HEAD")

	for _, root := range []string{repo, linked} {
		t.Run(filepath.Base(root), func(t *testing.T) {
			before, err := gitIndexDigest(context.Background(), root)
			if err != nil {
				t.Fatalf("digest before index flag: %v", err)
			}
			gitIn(t, root, "update-index", "--assume-unchanged", "README.md")
			after, err := gitIndexDigest(context.Background(), root)
			if err != nil {
				t.Fatalf("digest after index flag: %v", err)
			}
			if before == after {
				t.Fatal("index digest did not change after assume-unchanged")
			}
		})
	}
}

// dataRoot is the private directory the runs root sits under, which is where
// shadow puts the workspaces it prepares.
func dataRoot(runsRoot string) string { return filepath.Dir(runsRoot) }

// wantNoWorkspace fails when a refused command nevertheless left a disposable
// linked-worktree directory behind. Durable groups are expected to remain.
func wantNoWorkspace(t *testing.T, runsRoot string) {
	t.Helper()
	shadow := filepath.Join(dataRoot(runsRoot), shadowDirName)
	groups, err := os.ReadDir(shadow)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatalf("read shadow root: %v", err)
	}
	for _, group := range groups {
		if _, err := os.Stat(filepath.Join(shadow, group.Name(), shadowWorkspaceName)); err == nil {
			t.Errorf("shadow group %s retains a workspace", group.Name())
		} else if !os.IsNotExist(err) {
			t.Errorf("stat shadow group %s workspace: %v", group.Name(), err)
		}
	}
}

// writeTask writes the task a shadow run is asked to carry out and returns its
// path.
func writeTask(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "task.md")
	writeFile(t, path, body)
	return path
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// The task is read once, before anything is prepared and before either agent is
// launched, and both runs are given exactly those bytes. A file that is not one
// regular file of bounded text is refused there: the alternative is two agents
// launched with a prompt nobody can quote back, or with bytes that are not the
// ones on disk.
func TestShadowRefusesATaskItCannotReadIdentically(t *testing.T) {
	for _, tc := range []struct {
		name string
		task func(t *testing.T) string
	}{
		{"missing", func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent.md") }},
		{"directory", func(t *testing.T) string { return t.TempDir() }},
		{"symlink", func(t *testing.T) string {
			// Refused as itself rather than followed: what a link points at is
			// not what the operator named, and it may not be there twice.
			path := filepath.Join(t.TempDir(), "link.md")
			if err := os.Symlink(writeTask(t, "change the README\n"), path); err != nil {
				t.Fatalf("plant symlink: %v", err)
			}
			return path
		}},
		{"empty", func(t *testing.T) string { return writeTask(t, "") }},
		{"larger than the bound", func(t *testing.T) string {
			return writeTask(t, strings.Repeat("a", maxTaskBytes+1))
		}},
		{"not text", func(t *testing.T) string { return writeTask(t, "change the \xff\xfe README\n") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := home(t)
			cleanRepo(t)
			started := providerStarted(t)
			stubProviders(t, "claude", "codex")

			code, stdout, stderr := run(t, "shadow", "run", tc.task(t), "--runner", "claude", "--runner", "codex")

			if code != exitUsage {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, exitUsage, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, "task") {
				t.Errorf("stderr = %q, want it to say what is wrong with the task file", stderr)
			}
			wantNothingRecorded(t, root, started)
			wantNoWorkspace(t, root)
		})
	}
}

// wantComparison is the whole comparison for the two recorded runs the fixtures
// below write: each runner, in the fixed order, and each one's fields in theirs.
// It carries no verdict of any kind — which run is better is not something the
// evidence says, and a comparison that answered it would be answering with
// something other than evidence.
const wantComparison = `SHADOW COMPARISON

claude
  Run ID       run-a
  Order        1
  Verification PASS
  Config SHA-256 ` + evidenceConfigSum + `
  Exit Reason  completed
  Exit Code    0
  Duration     1.5s
  Repository   AVAILABLE  3 files (2 tracked, 1 untracked)  +32/-8, 0 binary
  Recorded Actions 1
  Warnings     0
  Unparsed     0

codex
  Run ID       run-b
  Order        2
  Verification FAIL
  Config SHA-256 ` + evidenceConfigSum + `
  Exit Reason  nonzero
  Exit Code    0
  Duration     1.5s
  Repository   AVAILABLE  3 files (2 tracked, 1 untracked)  +32/-8, 0 binary
  Recorded Actions 1
  Warnings     0
  Unparsed     0

` + sequenceNote + `
`

// failedVerification is a verification whose one pinned check ran and failed.
func failedVerification() map[string]any {
	doc := passedVerification()
	doc["status"] = "failed"
	doc["checks"] = []map[string]any{{
		"name":       "test",
		"command":    []string{"./gradlew", "test"},
		"timeout":    "30s",
		"status":     "failed",
		"exitCode":   1,
		"durationMs": 8210,
	}}
	return doc
}

// The comparison is built by reading the two bundles back off disk, and it reads
// the same whichever order the runs were asked for and executed in: two
// operators who recorded the same two runs must be able to compare what they
// were shown.
func TestShadowComparisonIsFixedInOrderAndReadFromTheBundles(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")
	writeGit(t, root, "run-a", availableGit())
	writeVerification(t, root, "run-a", passedVerification())
	writeRun(t, root, "run-b", "codex", early, "nonzero")
	writeGit(t, root, "run-b", availableGit())
	writeVerification(t, root, "run-b", failedVerification())

	// The same two legs, holding the same recorded execution order, handed over
	// in either slice order: what a leg ran under travels with the leg, so how
	// the caller happened to collect them changes nothing about the rendering.
	asked := []leg{
		{runner: "claude", runID: "run-a", order: 1},
		{runner: "codex", runID: "run-b", order: 2},
	}
	reversed := []leg{asked[1], asked[0]}

	for _, legs := range [][]leg{asked, reversed} {
		var out strings.Builder
		if err := renderComparison(&out, root, legs); err != nil {
			t.Fatalf("render comparison: %v", err)
		}
		if out.String() != wantComparison {
			t.Errorf("comparison =\n%s\nwant\n%s", out.String(), wantComparison)
		}
		// Which run to prefer is the operator's judgement to make from the
		// evidence, and a comparison that made it for them would be making it
		// out of the same fields with none of their context.
		for _, evaluative := range []string{"winner", "score", "rank", "recommend", "better", "best"} {
			if strings.Contains(strings.ToLower(out.String()), evaluative) {
				t.Errorf("comparison =\n%s\nwant no %q in it", out.String(), evaluative)
			}
		}
	}
}

func TestShadowComparisonRefusesAnUnparsedStreamThatDoesNotMatchTheManifest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		lines   []string
		wantErr bool
	}{
		{name: "missing file", wantErr: true},
		{name: "too few lines", lines: []string{"one line"}, wantErr: true},
		{name: "too many lines", lines: []string{"one line", "two lines", "three lines"}, wantErr: true},
		{name: "matching file", lines: []string{"one line", "two lines"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := home(t)
			b, err := storage.Create(root, "run-a", storage.Manifest{
				Provider:  "claude",
				Argv:      []string{"claude", "-p", "hello"},
				CWD:       "/tmp",
				StartedAt: late,
			})
			if err != nil {
				t.Fatalf("create run: %v", err)
			}
			if err := b.WriteAction(readAction(late)); err != nil {
				t.Fatalf("write action: %v", err)
			}
			for _, line := range tc.lines {
				if err := b.WriteUnparsedLine([]byte(line)); err != nil {
					t.Fatalf("write unparsed line: %v", err)
				}
			}
			if err := b.Finalize(storage.Finalization{
				EndedAt:       late.Add(time.Second),
				ExitReason:    "completed",
				UnparsedLines: 2,
			}); err != nil {
				t.Fatalf("finalize: %v", err)
			}

			var out strings.Builder
			err = renderComparison(&out, root, []leg{{runner: "claude", runID: "run-a", order: 1}})
			if tc.wantErr {
				if err == nil {
					t.Fatal("render comparison error = nil, want inconsistent unparsed evidence refused")
				}
				if !strings.Contains(err.Error(), "provider-stdout.unparsed.log") {
					t.Errorf("render comparison error = %q, want the inconsistent artifact named", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("render comparison: %v", err)
			}
			if !strings.Contains(out.String(), "Unparsed     2") {
				t.Errorf("comparison =\n%s\nwant the verified unparsed count", out.String())
			}
		})
	}
}

// wantPartialComparison is what a comparison says when one leg recorded less
// than a whole run and the other never started: what is missing is stated, and
// nothing missing is rendered as a zero that was measured.
const wantPartialComparison = `SHADOW COMPARISON

claude
  Run ID       run-c
  Order        1
  Verification (none)
  Exit Reason  unknown
  Duration     unknown
  Repository   (none)
  Recorded Actions 1
  Warnings     0
  Unparsed     0

codex
  (not run)
`

func TestShadowComparisonStatesWhatALegDidNotRecord(t *testing.T) {
	root := home(t)
	// No process result, no repository evidence and no verification: the run
	// this leg left is a run that stopped before any of it was written.
	writeRun(t, root, "run-c", "claude", late, "")

	var out strings.Builder
	if err := renderComparison(&out, root, []leg{{runner: "claude", runID: "run-c", order: 1}}); err != nil {
		t.Fatalf("render comparison: %v", err)
	}
	if out.String() != wantPartialComparison {
		t.Errorf("comparison =\n%s\nwant\n%s", out.String(), wantPartialComparison)
	}
	// One recorded run is not a sequence, so there is no ordering caveat to
	// state: a note about what a later leg may have observed, printed under a
	// comparison with no later leg, would be describing something that never
	// happened.
	if strings.Contains(out.String(), sequenceNote) {
		t.Errorf("comparison =\n%s\nwant no sequence note for a single recorded leg", out.String())
	}
}

// The order a leg is shown in is the order it ran in, not the position it is
// rendered at: the runner blocks are printed in a fixed order so two operators
// read the same comparison, and that fixed order is exactly what would otherwise
// hide which agent went first.
func TestShadowComparisonShowsRecordedExecutionOrderNotRenderOrder(t *testing.T) {
	root := home(t)
	writeRun(t, root, "run-a", "claude", late, "completed")
	writeRun(t, root, "run-b", "codex", early, "completed")

	// Codex was asked for first and ran first; Claude is still rendered first.
	legs := []leg{
		{runner: "codex", runID: "run-b", order: 1},
		{runner: "claude", runID: "run-a", order: 2},
	}
	var out strings.Builder
	if err := renderComparison(&out, root, legs); err != nil {
		t.Fatalf("render comparison: %v", err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "Run ID       run-a\n  Order        2\n") {
		t.Errorf("comparison =\n%s\nwant claude's leg recorded as having run second", rendered)
	}
	if !strings.Contains(rendered, "Run ID       run-b\n  Order        1\n") {
		t.Errorf("comparison =\n%s\nwant codex's leg recorded as having run first", rendered)
	}
	if strings.Index(rendered, "\nclaude\n") > strings.Index(rendered, "\ncodex\n") {
		t.Errorf("comparison =\n%s\nwant the runner blocks in the fixed order whatever ran first", rendered)
	}
	if !strings.Contains(rendered, sequenceNote) {
		t.Errorf("comparison =\n%s\nwant the ordering caveat stated", rendered)
	}
}

// A comparison is between what the two agents did, and a leg that failed is
// exactly the leg worth reading: the other one is still recorded, both bundles
// are kept, both checkouts are still removed, and the command reports the
// failure with its own exit code rather than passing an agent's on.
func TestShadowRecordsBothLegsWhenARunFails(t *testing.T) {
	for _, tc := range []struct {
		name       string
		task       string
		check      []string
		wantReason string
		wantVerify string
	}{
		{
			name:       "provider ended nonzero",
			task:       failPrompt,
			check:      []string{verifyHelperName, "pass"},
			wantReason: runner.ReasonNonzero,
			wantVerify: "passed",
		},
		{
			name:       "verification did not pass",
			task:       "change the README\n",
			check:      []string{verifyHelperName, "fail", "3"},
			wantReason: runner.ReasonCompleted,
			wantVerify: "failed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := home(t)
			repo := cleanRepo(t)
			stubProviders(t, "claude", "codex", verifyHelperName)
			commitVerifyConfig(t, repo, tc.check...)
			task := writeTask(t, tc.task)
			before := snapshotSource(t, repo)

			code, stdout, stderr := run(t, "shadow", "run", task, "--runner", "claude", "--runner", "codex")

			// The agent's own exit code is evidence in its bundle, not this
			// command's ending: a comparison that exited 7 would be one an
			// operator reads as agentrec having failed in some seventh way.
			if code != exitFailure {
				t.Fatalf("exit code = %d, want %d (stderr %q)", code, exitFailure, stderr)
			}
			bundles := shadowBundles(t, root)
			if len(bundles) != len(shadowRunners) {
				t.Fatalf("recorded runs = %v, want the failing leg not to have stopped the other", bundles)
			}
			for _, name := range shadowRunners {
				runID := bundles[name]
				manifest, err := readManifest(filepath.Join(root, runID))
				if err != nil {
					t.Fatalf("read manifest of %s: %v", runID, err)
				}
				if manifest.ExitReason != tc.wantReason {
					t.Errorf("%s exit reason = %q, want %q", name, manifest.ExitReason, tc.wantReason)
				}
				if res := readVerifyResult(t, filepath.Join(root, runID)); res.Status != tc.wantVerify {
					t.Errorf("%s verification = %q, want %q", name, res.Status, tc.wantVerify)
				}
				wantGitArtifacts(t, filepath.Join(root, runID))
				if !strings.Contains(stdout, runID) {
					t.Errorf("stdout =\n%s\nwant the comparison to name run %s", stdout, runID)
				}
			}
			wantNoWorkspace(t, root)
			wantSourceUnchanged(t, repo, before)
		})
	}
}

// lfsPointer is what Git stores in place of a large file tracked by Git LFS.
// Hydration depends on local Git configuration and object availability, which
// is not a reproducible workspace contract for two Shadow legs.
const lfsPointer = "version https://git-lfs.github.com/spec/v1\n" +
	"oid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393\n" +
	"size 12\n"

const extendedLFSPointer = "version https://git-lfs.github.com/spec/v1\n" +
	"ext-0-agentrec sha256:64e4f5f6c445b85f410b1b8f75f81f170f93c2f8a5cb51cd45cc0d8f7812d95f\n" +
	"oid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393\n" +
	"size 12\n"

// Everything a comparison rests on is settled before either agent is launched:
// one commit both runs start from, one verification configuration both are
// judged by, a private workspace outside the repository, and a repository Git
// can check out without unsupported project material. A repository that cannot
// give all of that is refused where nothing has been prepared yet, rather than
// halfway through the first run.
func TestShadowRefusesARepositoryItCannotCompareIn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  bool
		want    string
		prepare func(t *testing.T, repo string)
	}{
		{
			name:   "checkout that is already dirty",
			config: true,
			want:   "commit",
			prepare: func(t *testing.T, repo string) {
				writeFile(t, filepath.Join(repo, "scratch.txt"), "the operator's own\n")
			},
		},
		{
			name: "verification configuration that was never committed",
			want: verifyConfigFile,
		},
		{
			name: "verification configuration that is only in the worktree",
			want: verifyConfigFile,
			prepare: func(t *testing.T, repo string) {
				// Ignored, so the checkout stays clean while the configuration
				// the checks would be pinned to is not part of the baseline
				// either run starts from.
				writeFile(t, filepath.Join(repo, ".gitignore"), verifyConfigFile+"\n")
				gitIn(t, repo, "add", ".gitignore")
				gitIn(t, repo, "commit", "-m", "ignore the configuration")
				writeVerifyConfig(t, repo, "version: 1\nverify: []\n")
			},
		},
		{
			name:   "data root inside the repository being recorded",
			config: true,
			want:   "inside",
			prepare: func(t *testing.T, repo string) {
				t.Setenv("AGENTREC_HOME", filepath.Join(repo, "data"))
			},
		},
		{
			name:   "committed submodule declaration",
			config: true,
			want:   ".gitmodules",
			prepare: func(t *testing.T, repo string) {
				writeFile(t, filepath.Join(repo, ".gitmodules"),
					"[submodule \"vendor\"]\n\tpath = vendor\n\turl = https://example.invalid/vendor.git\n")
				gitIn(t, repo, "add", ".gitmodules")
				gitIn(t, repo, "commit", "-m", "declare a submodule")
			},
		},
		{
			name:   "committed Git LFS pointer",
			config: true,
			want:   "LFS",
			prepare: func(t *testing.T, repo string) {
				writeFile(t, filepath.Join(repo, "asset.bin"), lfsPointer)
				gitIn(t, repo, "add", "asset.bin")
				gitIn(t, repo, "commit", "-m", "store a pointer")
			},
		},
		{
			name:   "committed extended Git LFS pointer",
			config: true,
			want:   "LFS",
			prepare: func(t *testing.T, repo string) {
				writeFile(t, filepath.Join(repo, "asset.bin"), extendedLFSPointer)
				gitIn(t, repo, "add", "asset.bin")
				gitIn(t, repo, "commit", "-m", "store an extended pointer")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home(t)
			repo := cleanRepo(t)
			started := providerStarted(t)
			stubProviders(t, "claude", "codex", verifyHelperName)
			if tc.config {
				commitVerifyConfig(t, repo, verifyHelperName, "pass")
			}
			if tc.prepare != nil {
				tc.prepare(t, repo)
			}
			task := writeTask(t, "change the README\n")
			// Read after the case has prepared, because one of them moves the
			// data root to prove it is refused for being inside the repository.
			root, err := runsRoot()
			if err != nil {
				t.Fatalf("locate runs root: %v", err)
			}

			code, stdout, stderr := run(t, "shadow", "run", task, "--runner", "claude", "--runner", "codex")

			if code != exitUsage {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, exitUsage, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr = %q, want it to name %q", stderr, tc.want)
			}
			wantNothingRecorded(t, root, started)
			wantNoWorkspace(t, root)
		})
	}
}

func TestShadowAcceptsARepositoryThatOnlyMentionsTheLFSPointerHeader(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	stubProviders(t, "claude", "codex", verifyHelperName)
	commitVerifyConfig(t, repo, verifyHelperName, "pass")
	writeFile(t, filepath.Join(repo, "lfs-doc.txt"), "The LFS header is: "+lfsPointerPrefix+"\n")
	gitIn(t, repo, "add", "lfs-doc.txt")
	gitIn(t, repo, "commit", "-m", "document the LFS header")
	task := writeTask(t, "change the README\n")

	code, _, stderr := run(t, "shadow", "run", task, "--runner", "claude", "--runner", "codex")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 for prose that only mentions the header (stderr %q)", code, stderr)
	}
	wantNoWorkspace(t, root)
}

func TestShadowRefusesASymlinkedWorkspaceRoot(t *testing.T) {
	runs := home(t)
	repo := cleanRepo(t)
	stubProviders(t, "claude", "codex", verifyHelperName)
	commitVerifyConfig(t, repo, verifyHelperName, "pass")
	target := t.TempDir()
	shadow := filepath.Join(dataRoot(runs), shadowDirName)
	if err := os.Symlink(target, shadow); err != nil {
		t.Fatalf("plant workspace symlink: %v", err)
	}
	task := writeTask(t, "change the README\n")

	code, _, _ := run(t, "shadow", "run", task, "--runner", "claude", "--runner", "codex")

	if code != exitFailure {
		t.Fatalf("exit code = %d, want %d for a symlinked workspace root", code, exitFailure)
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Fatalf("symlink target entries = %v, %v, want untouched", entries, err)
	}
}

// A comparison is only worth reading when it is between the two runs the
// operator asked for, one of each. An invocation naming the same runner twice,
// naming one agentrec cannot record, or leaving one out is refused before
// anything is prepared: the alternative is a comparison whose two sides the
// operator has to reconstruct from the output.
func TestShadowRejectsInvocationsItCannotCompare(t *testing.T) {
	for _, args := range [][]string{
		{"shadow"},
		{"shadow", "walk", "task.md", "--runner", "claude", "--runner", "codex"},
		{"shadow", "run"},
		{"shadow", "run", "task.md"},
		{"shadow", "run", "task.md", "--runner", "claude"},
		{"shadow", "run", "task.md", "--runner", "claude", "--runner", "claude"},
		{"shadow", "run", "task.md", "--runner", "codex", "--runner", "codex"},
		{"shadow", "run", "task.md", "--runner", "claude", "--runner", "gemini"},
		{"shadow", "run", "task.md", "--runner", "claude", "--runner", "codex", "--runner", "codex"},
		{"shadow", "run", "task.md", "--runner", "claude", "--runner", "codex", "extra"},
		{"shadow", "run", "task.md", "--runner=claude", "--runner=codex"},
		{"shadow", "run", "task.md", "--runner", "claude", "--runner"},
		{"shadow", "run", "--runner", "claude", "--runner", "codex"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := home(t)
			started := providerStarted(t)
			stubProviders(t, "claude", "codex")

			code, stdout, stderr := run(t, args...)

			if code != exitUsage {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, exitUsage, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, "agentrec shadow run") {
				t.Errorf("stderr = %q, want it to state the usage", stderr)
			}
			wantNothingRecorded(t, root, started)
			wantNoWorkspace(t, root)
		})
	}
}

// The lock is what keeps two runs from recording one repository at the same
// time, and a lock this command could not give back is a repository the next run
// will be refused by. That is the command's own failure and is reported as one:
// printing it and exiting 0 hands the operator a comparison that reads as
// everything having gone well.
func TestShadowFailsWhenTheHeldRepositoryCannotBeReleased(t *testing.T) {
	root := home(t)
	repo := cleanRepo(t)
	stubProviders(t, "claude", "codex", verifyHelperName)
	commitVerifyConfig(t, repo, verifyHelperName, "pass")
	task := writeTask(t, "change the README\n")
	// The lock is really given up, because this process goes on to record other
	// runs; what is driven here is the report that giving it up failed, which
	// nothing outside this command can provoke.
	refused := errors.New("lock: unlock \"the-lock\": synthetic refusal")
	release := releaseRepository
	releaseRepository = func(r *lock.Repository) error {
		release(r)
		return refused
	}
	t.Cleanup(func() { releaseRepository = release })

	code, stdout, stderr := run(t, "shadow", "run", task, "--runner", "claude", "--runner", "codex")

	if code != exitFailure {
		t.Fatalf("exit code = %d, want %d for a lock that could not be released (stderr %q)", code, exitFailure, stderr)
	}
	if !strings.Contains(stderr, refused.Error()) {
		t.Errorf("stderr = %q, want it to report %q", stderr, refused)
	}
	// Said once: the release that decides the ending is the only one with
	// anything to report, and a difficulty reported twice reads as two.
	if n := strings.Count(stderr, refused.Error()); n != 1 {
		t.Errorf("stderr reports the refusal %d times, want once:\n%s", n, stderr)
	}
	// Both legs were still recorded and still compared: what went wrong is the
	// lock, and what the two agents did is what the operator asked for.
	if bundles := shadowBundles(t, root); len(bundles) != len(shadowRunners) {
		t.Errorf("recorded runs = %v, want one recorded for each runner", bundles)
	}
	if !strings.Contains(stdout, comparisonHeader) {
		t.Errorf("stdout =\n%s\nwant the comparison still rendered", stdout)
	}
	wantNoWorkspace(t, root)
}

// The task is the prompt, whatever it happens to spell. A task beginning with a
// provider option would otherwise reach that provider as an option rather than
// as the request. The delimiter stands after agentrec's own options and before
// the task, so exactly one positional prompt reaches either provider.
func TestShadowGivesEachProviderTheTaskAsOnePositionalPrompt(t *testing.T) {
	stubProviders(t, "claude", "codex")

	providers := []struct {
		name string
		want func(string) []string
	}{
		{"claude", func(task string) []string {
			return []string{"--output-format", "stream-json", "--verbose", "--include-hook-events", "-p", "--", task}
		}},
		{"codex", func(task string) []string { return []string{"exec", "--json", "--", task} }},
	}
	for _, provider := range providers {
		for _, task := range []string{
			"change the README\n",
			"--dangerously-bypass-approvals-and-sandbox",
			"--json",
			"-",
		} {
			cmd, _, err := shadowCommand(context.Background(), provider.name, task)
			if err != nil {
				t.Fatalf("shadowCommand(%s, %q): %v", provider.name, task, err)
			}
			want := provider.want(task)
			if !slices.Equal(cmd.Args, want) {
				t.Errorf("shadowCommand(%s, %q).Args = %q, want %q", provider.name, task, cmd.Args, want)
			}
		}
	}
}

// A checkout that could not be taken back out is a copy of the operator's
// repository left where nobody is expecting one, so the difficulty is reported
// rather than passed over — and nothing else is removed on the way past it.
func TestRemoveWorkspacesReportsWhatItCouldNotRemove(t *testing.T) {
	workspaces := t.TempDir()
	leftover := filepath.Join(workspaces, "claude")
	if err := os.Mkdir(leftover, 0o700); err != nil {
		t.Fatalf("plant a leftover checkout: %v", err)
	}

	err := removeWorkspaces(nil, workspaces)

	if err == nil {
		t.Fatalf("removeWorkspaces() = nil, want the directory it could not remove reported")
	}
	if !strings.Contains(err.Error(), workspaces) {
		t.Errorf("error = %q, want it to name %q", err, workspaces)
	}
	if _, statErr := os.Stat(leftover); statErr != nil {
		t.Errorf("leftover checkout: %v, want it left where the operator can find it", statErr)
	}
}
