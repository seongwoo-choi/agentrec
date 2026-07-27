# Agentrec Flight Recorder Implementation Plan

> **For Hermes:** Use subagent-driven-development skill to implement this plan task-by-task.

**Goal:** Build a local CLI that records Claude Code and Codex tool actions as a replayable timeline, correlates them with final Git changes and independent verification, and later compares isolated runs through a Shadow Runner.

**Architecture:** `agentrec` is a Go control-plane CLI with explicit Claude and Codex adapters. Provider JSONL is normalized into a small common Action model; supervisor-observed process results, Git evidence, and verification remain separate evidence classes. OS-level auditing is an optional later extension, not part of the core MVP.

**Tech Stack:** Go 1.26+, standard library first, Git CLI, `gopkg.in/yaml.v3` only when verification configuration is introduced. macOS arm64 is the first execution target; Linux arm64/amd64 support is required before public release.

---

## 1. Product Contract

### 1.1 Core claim

> Agentrec records Claude Code and Codex tool calls, commands, results, final repository changes, and independent verification as a replayable local action timeline.

The report must distinguish the origin and assurance of every record:

- `provider_reported`: emitted by Claude Code or Codex structured output.
- `supervisor_observed`: directly observed by the `agentrec` process.
- `repository_observed`: derived from Git and filesystem snapshots.
- `verification_observed`: produced by commands pinned before the agent starts.
- `os_observed`: reserved for the later audit extension.

Do not claim syscall completeness or causal attribution in native mode.

### 1.2 Commands

Core:

```bash
agentrec trace claude -- -p "fix the failing test"
agentrec trace codex -- exec "fix the failing test"
agentrec list
agentrec show <run-id>
```

Evidence extension:

```bash
agentrec trace claude --verify -- -p "fix the failing test"
agentrec verify <run-id>
```

Later:

```bash
agentrec shadow run task.md --runner claude --runner codex
agentrec audit claude -- -p "fix the failing test"
```

`trace` explicitly enables structured provider output. `agentrec` must not silently transform arbitrary commands passed through a generic wrapper.

### 1.3 Verified provider capabilities

Verified locally on 2026-07-27:

Claude Code `2.1.220`:

```text
-p --output-format stream-json --verbose --include-hook-events
```

Observed events include:

- assistant `tool_use`
- tool result
- `PreToolUse` / `PostToolUse`
- Read path and response
- Bash command, stdout, stderr, duration
- session ID
- token usage and cost
- final result

Codex CLI `0.144.6`:

```text
codex exec --json
```

Observed events include:

- `thread.started`
- `turn.started`
- `item.started`
- `item.completed`
- `command_execution`
- command, output, exit code and status
- agent messages
- `turn.completed` token usage

### 1.4 Non-goals for the core MVP

- Full syscall tracing
- Endpoint Security or eBPF
- Docker/VM sandboxing
- Network interception
- External mutation gateway
- GUI or web server
- SQLite
- OpenTelemetry
- Interactive TUI session capture
- Windows support
- Automatic code-quality score
- Automatic permission bypass
- Provider-independent plugin framework

---

## 2. Common Action Model

### 2.1 Normalized action

Create a deliberately small model:

```go
type Assurance string

const (
    AssuranceProviderReported     Assurance = "provider_reported"
    AssuranceSupervisorObserved   Assurance = "supervisor_observed"
    AssuranceRepositoryObserved   Assurance = "repository_observed"
    AssuranceVerificationObserved Assurance = "verification_observed"
)

type Action struct {
    ID          string          `json:"id"`
    ParentID    string          `json:"parentId,omitempty"`
    Type        string          `json:"type"`
    Provider    string          `json:"provider,omitempty"`
    Assurance   Assurance       `json:"assurance"`
    StartedAt   time.Time       `json:"startedAt,omitempty"`
    FinishedAt  time.Time       `json:"finishedAt,omitempty"`
    Status      string          `json:"status,omitempty"`
    Input       json.RawMessage `json:"input,omitempty"`
    Result      json.RawMessage `json:"result,omitempty"`
}
```

Initial action types:

```text
agent.message
file.read
file.write
file.edit
shell.exec
search
web.fetch
mcp.call
subagent.spawn
tool.call
run.result
```

Do not model provider-specific fields in the common struct. Keep them in `Input` and `Result` until repeated use proves that stronger shared types are needed.

### 2.2 Correlation rules

Claude:

```text
assistant tool_use
+ matching tool_result
+ matching PostToolUse event
= one normalized Action
```

Use `tool_use_id` as the primary correlation key.

Priority:

1. assistant `tool_use` and user `tool_result`
2. provider result event
3. `PostToolUse` enrichment
4. `PreToolUse` enrichment
5. plugin-specific hooks

Hook events must not create duplicate actions.

Codex:

```text
item.started(command_execution)
+ item.completed(same item.id)
= one shell.exec Action
```

Use `item.id` as the primary correlation key.

Unknown event types:

- must not fail the run;
- increment `unknownEventCount`;
- remain in the sanitized provider event stream when raw retention is enabled;
- must be covered by a parser fixture before being normalized in a later release.

---

## 3. Run Bundle

Default location:

```text
~/.local/share/agentrec/runs/<run-id>/
```

Layout:

```text
<run-id>/
├── manifest.json
├── prompt.txt
├── actions.jsonl
├── provider-events.sanitized.jsonl
├── process/
│   ├── stderr.sanitized.log
│   └── result.json
├── git/
│   ├── baseline.json
│   ├── tracked.patch
│   ├── tracked-stat.json
│   ├── untracked.json
│   └── untracked/
├── verification/
│   └── results.json
└── report.md
```

Security defaults:

- run directory mode `0700`;
- files mode `0600`;
- exact provider raw output is not retained by default;
- `--retain-sensitive-raw` is an explicit later option;
- provider JSON strings are structurally redacted before persistence;
- stored text patches, logs and untracked text files use the same redactor;
- binary untracked files store path, mode, size and hash only by default;
- redaction placeholders use opaque run-local IDs such as `[REDACTED:1]`, never an unsalted secret hash.

No SQLite index in the core MVP. `list` scans manifests until measured performance demonstrates a need for indexing.

---

## 4. Repository Layout

Target project root:

```text
/Users/csw/code/agentrec
```

Provisional module:

```text
github.com/seongwoo-choi/agentrec
```

Expected layout:

```text
agentrec/
├── cmd/agentrec/main.go
├── internal/
│   ├── cli/
│   │   ├── root.go
│   │   ├── trace.go
│   │   ├── list.go
│   │   └── show.go
│   ├── action/
│   │   ├── action.go
│   │   └── writer.go
│   ├── provider/
│   │   ├── claude/
│   │   │   ├── command.go
│   │   │   ├── parser.go
│   │   │   └── parser_test.go
│   │   └── codex/
│   │       ├── command.go
│   │       ├── parser.go
│   │       └── parser_test.go
│   ├── runner/
│   │   ├── runner.go
│   │   ├── process_unix.go
│   │   └── runner_test.go
│   ├── redaction/
│   │   ├── redactor.go
│   │   └── redactor_test.go
│   ├── storage/
│   │   ├── bundle.go
│   │   └── bundle_test.go
│   ├── report/
│   │   ├── terminal.go
│   │   ├── markdown.go
│   │   └── report_test.go
│   ├── evidence/
│   │   ├── git.go
│   │   ├── git_test.go
│   │   ├── verification.go
│   │   └── verification_test.go
│   └── lock/
│       ├── repository.go
│       └── repository_test.go
├── testdata/
│   ├── claude/
│   │   ├── read-and-bash.jsonl
│   │   └── duplicate-hooks.jsonl
│   ├── codex/
│   │   ├── command-execution.jsonl
│   │   └── unknown-event.jsonl
│   └── redaction/
├── docs/
│   ├── assurance-model.md
│   ├── provider-support.md
│   └── limitations.md
├── .agentrec.example.yaml
├── go.mod
├── README.md
└── LICENSE
```

Fixtures must be synthetic and contain no private source, prompt, absolute user path, session token or credential.

---

# Phase 1: Provider Action Recorder

## Task 1: Initialize the Go CLI repository

**Objective:** Create the minimum compilable `agentrec` project without feature abstractions.

**Files:**

- Create: `go.mod`
- Create: `cmd/agentrec/main.go`
- Create: `internal/cli/root.go`
- Create: `README.md`
- Create: `.gitignore`

**Steps:**

1. Write a CLI smoke test that invokes the binary with `--help`.
2. Run it and verify failure because the command does not exist.
3. Initialize the Go module.
4. Implement manual subcommand dispatch with the standard library; do not introduce Cobra yet.
5. Verify:

```bash
go test ./...
go run ./cmd/agentrec --help
```

Expected help must list `trace`, `list` and `show`.

**Commit:**

```text
chore(project): initialize agentrec CLI
```

## Task 2: Define the Action and Run models

**Objective:** Establish the smallest provider-neutral persistence contract.

**Files:**

- Create: `internal/action/action.go`
- Create: `internal/action/action_test.go`
- Create: `internal/action/writer.go`

**Steps:**

1. Write failing JSON round-trip tests for `Action`.
2. Verify missing required fields are rejected at the writer boundary.
3. Implement `Action` and `Assurance` constants.
4. Implement a streaming JSONL writer; do not buffer a full run.
5. Run:

```bash
go test ./internal/action -v
```

**Commit:**

```text
feat(action): define normalized action events
```

## Task 3: Build synthetic provider fixtures

**Objective:** Freeze the event behavior already verified against live Claude and Codex executions without copying real session data.

**Files:**

- Create: `testdata/claude/read-and-bash.jsonl`
- Create: `testdata/claude/duplicate-hooks.jsonl`
- Create: `testdata/codex/command-execution.jsonl`
- Create: `testdata/codex/unknown-event.jsonl`

**Fixture requirements:**

Claude fixture contains:

- one Read `tool_use`;
- one matching Read `tool_result`;
- duplicate Pre/Post hooks for the same `tool_use_id`;
- one Bash tool call and result;
- one final result with usage and cost.

Codex fixture contains:

- `thread.started`;
- `turn.started`;
- one agent message;
- command execution started/completed;
- final agent message;
- token usage;
- one unknown event fixture.

Use paths such as `/workspace/project/README.md`, never `/Users/csw/...`.

**Commit:**

```text
test(provider): add synthetic action fixtures
```

## Task 4: Parse Claude actions

**Objective:** Convert Claude stream-json into deduplicated normalized actions.

**Files:**

- Create: `internal/provider/claude/parser.go`
- Create: `internal/provider/claude/parser_test.go`

**Steps:**

1. Write a failing test expecting exactly two actions from the Read+Bash fixture.
2. Assert the Read path, Bash command, stdout, status and duration.
3. Assert duplicate hook events do not create duplicate actions.
4. Assert malformed and unknown lines increment warnings but do not abort parsing.
5. Implement streaming line-by-line parsing keyed by `tool_use_id`.
6. Run:

```bash
go test ./internal/provider/claude -v
```

Expected: all parser tests pass and the fixture yields exactly two actions.

**Commit:**

```text
feat(claude): normalize tool actions
```

## Task 5: Parse Codex actions

**Objective:** Convert Codex exec JSONL into normalized command and message actions.

**Files:**

- Create: `internal/provider/codex/parser.go`
- Create: `internal/provider/codex/parser_test.go`

**Steps:**

1. Write a failing test expecting one shell action from the command fixture.
2. Assert command, aggregated output, exit code and completed status.
3. Assert started/completed events correlate by `item.id`.
4. Assert unknown events increment warnings without failing the run.
5. Implement streaming parsing.
6. Run:

```bash
go test ./internal/provider/codex -v
```

**Commit:**

```text
feat(codex): normalize command actions
```

## Task 6: Implement structural redaction

**Objective:** Ensure provider event streams and normalized action payloads do not persist known secrets by default.

**Files:**

- Create: `internal/redaction/redactor.go`
- Create: `internal/redaction/redactor_test.go`
- Create: `testdata/redaction/provider-events.jsonl`

**Coverage:**

- environment values whose variable names end with `TOKEN`, `SECRET`, `PASSWORD`, `API_KEY` and whose values meet a minimum length;
- GitHub token patterns;
- common API-key patterns;
- bearer tokens;
- JWT-shaped values;
- PEM private-key blocks;
- nested JSON string fields;
- token split across input read boundaries;
- no unsalted secret hash in output.

Redaction IDs are run-local opaque values:

```text
[REDACTED:1]
```

Run:

```bash
go test ./internal/redaction -v
```

**Commit:**

```text
fix(security): redact provider event secrets
```

## Task 7: Create secure run bundles

**Objective:** Persist a self-contained action run with restrictive permissions.

**Files:**

- Create: `internal/storage/bundle.go`
- Create: `internal/storage/bundle_test.go`

**Tests:**

- run directory mode is `0700`;
- files are `0600`;
- actions are appended as streaming JSONL;
- partial runs can be finalized after interruption;
- manifest records provider, version, argv, cwd, start/end time, exit reason, warning count and redaction rule version;
- sanitized provider events contain no fixture secret.

Run:

```bash
go test ./internal/storage -v
```

**Commit:**

```text
feat(storage): persist secure run bundles
```

## Task 8: Prepare explicit provider commands

**Objective:** Enable structured output only when the user explicitly chooses a provider-aware trace command.

**Files:**

- Create: `internal/provider/claude/command.go`
- Create: `internal/provider/claude/command_test.go`
- Create: `internal/provider/codex/command.go`
- Create: `internal/provider/codex/command_test.go`

Claude rules:

- executable is `claude`;
- require non-interactive `-p` or `--print`;
- add `--output-format stream-json`, `--verbose`, `--include-hook-events` when absent;
- reject conflicting `--output-format text|json` instead of silently overriding;
- never add permission bypass options.

Codex rules:

- executable is `codex`;
- require `exec` mode;
- add `--json` when absent;
- never change sandbox or approval policy.

Both adapters probe and record executable version. Unsupported versions fail with an actionable error rather than falling back silently.

Run:

```bash
go test ./internal/provider/... -v
```

**Commit:**

```text
feat(provider): prepare explicit trace commands
```

## Task 9: Execute and supervise provider processes

**Objective:** Run the structured provider command, stream actions and terminate the full process group on interrupt or timeout.

**Files:**

- Create: `internal/runner/runner.go`
- Create: `internal/runner/process_unix.go`
- Create: `internal/runner/runner_test.go`

**Tests:**

- stdout is parsed without full buffering;
- stderr is saved separately;
- non-zero provider exit is recorded;
- SIGINT is forwarded;
- timeout sends process-group SIGTERM, waits five seconds, then SIGKILL;
- no descendant remains after cancellation;
- partial bundle finalizes with `exitReason=interrupted|timeout`.

MVP is non-interactive:

```text
stdin = /dev/null
PTY unsupported
```

Run:

```bash
go test ./internal/runner -v
go test -race ./internal/runner
```

**Commit:**

```text
feat(runner): supervise provider trace processes
```

## Task 10: Render deterministic timelines

**Objective:** Make the trace useful without exposing raw JSONL.

**Files:**

- Create: `internal/report/terminal.go`
- Create: `internal/report/markdown.go`
- Create: `internal/report/report_test.go`

Expected shape:

```text
ACTION TIMELINE

13:45:57  READ  README.md
  Source       claude
  Assurance    provider_reported
  Result       success
  Duration     5ms

13:45:58  SHELL  pwd
  Source       claude
  Assurance    provider_reported
  Exit         0
  Duration     226ms
```

Separate sections:

```text
PROVIDER-REPORTED ACTIONS
SUPERVISOR-OBSERVED RESULT
REPOSITORY-OBSERVED CHANGES
VERIFICATION-OBSERVED RESULT
```

Golden tests compare exact output.

**Commit:**

```text
feat(report): render action timelines
```

## Task 11: Wire `trace`, `list` and `show`

**Objective:** Complete the first end-to-end CLI path.

**Files:**

- Create: `internal/cli/trace.go`
- Create: `internal/cli/list.go`
- Create: `internal/cli/show.go`
- Modify: `internal/cli/root.go`
- Test: `internal/cli/cli_test.go`

**Verification:**

```bash
go test ./...
go test -race ./...
go run ./cmd/agentrec trace claude -- -p "Read README.md and report its title"
go run ./cmd/agentrec show latest
go run ./cmd/agentrec trace codex -- exec "Read README.md and report its title"
go run ./cmd/agentrec show latest
```

Expected:

- both providers produce at least one normalized action;
- output is human-readable, not raw JSONL;
- run bundles contain no known test secret;
- `show` works after the provider process has exited.

**Commit:**

```text
feat(cli): record Claude and Codex traces
```

---

# Phase 2: Git Delta and Independent Verification

## Task 12: Add repository locking and clean-state checks

**Objective:** Prevent concurrent runs from corrupting repository observations.

**Files:**

- Create: `internal/lock/repository.go`
- Create: `internal/lock/repository_test.go`
- Modify: `internal/cli/trace.go`

Lock location:

```text
~/.local/share/agentrec/locks/<sha256(realpath(repo-root))>.lock
```

Use non-blocking `flock`. Acquire before clean check.

Clean means:

- `git status --porcelain` is empty;
- no merge, rebase, cherry-pick or bisect is active.

No `--allow-dirty` in the initial release.

**Commit:**

```text
feat(git): lock clean repositories for tracing
```

## Task 13: Materialize final repository changes

**Objective:** Preserve commits, tracked worktree changes and new untracked files as repository-observed evidence.

**Files:**

- Create: `internal/evidence/git.go`
- Create: `internal/evidence/git_test.go`

Start:

- record baseline HEAD;
- create `refs/agentrec/<run-id>`.

Finalize after process-group recovery:

- materialize tracked binary patch and stat from baseline to final worktree;
- list non-ignored untracked files;
- hash untracked files in-process;
- store sanitized text files under configured limits;
- do not follow symlinks;
- do not store binary bodies by default;
- if baseline is missing, mark `unavailable(baseline_unreachable)` rather than guessing;
- remove temporary ref after materialization.

Test cases:

- two commits plus worktree modification;
- new untracked text file;
- new binary file;
- symlink outside repository;
- ignored file excluded;
- baseline removed mid-run;
- repository deleted after finalization while `show` still works.

**Commit:**

```text
feat(evidence): capture final repository delta
```

## Task 14: Pin and execute verification

**Objective:** Independently verify the final code without allowing the agent to rewrite its verifier.

**Files:**

- Create: `internal/evidence/verification.go`
- Create: `internal/evidence/verification_test.go`
- Create: `.agentrec.example.yaml`

Configuration:

```yaml
version: 1
verify:
  - name: test
    command: ["go", "test", "./..."]
    timeout: 5m
```

Rules:

1. Load config and argv before provider execution.
2. Record config SHA-256.
3. Re-check config after provider execution.
4. If changed, do not execute verification; record `TAINTED(config_changed)`.
5. Snapshot repository before and after verification.
6. If verification mutates the repository, preserve the real exit result and add `verification_mutated_repository` with changed paths.
7. Never use a shell to execute verification commands.
8. Never auto-revert verification changes.

**Commit:**

```text
feat(verify): run pinned independent checks
```

## Task 15: Integrate evidence into reports

**Objective:** Present action, repository and verification evidence without conflating assurance levels.

**Files:**

- Modify: `internal/report/terminal.go`
- Modify: `internal/report/markdown.go`
- Modify: `internal/report/report_test.go`

Example:

```text
PROVIDER-REPORTED ACTIONS
  Read src/AuthService.java
  Bash ./gradlew test
  Edit src/AuthService.java

REPOSITORY-OBSERVED CHANGES
  2 files, +32/-8
  Attribution: observed during run, not causal proof

VERIFICATION-OBSERVED RESULT
  PASS ./gradlew test  8.21s
```

**Commit:**

```text
feat(report): correlate actions and evidence
```

---

# Phase 3: Real-Use Gate

Use the tool for at least 15 real runs before implementing Shadow Runner.

Record:

- provider;
- repository;
- parser warnings;
- unknown event count;
- missing or duplicated actions;
- whether the timeline was useful;
- clean-state refusal count;
- whether verification was useful;
- bundle size;
- sensitive-data incidents.

Gate to continue:

- Claude and Codex each complete at least five real traces;
- no known secret is persisted in default bundles;
- normalized timeline matches provider actions in sampled raw sessions;
- repository delta errors: zero;
- interrupted runs finalize correctly;
- the tool is noticeably missed when not used.

Kill condition:

> If 15 real runs do not make debugging, review or model comparison materially easier, stop before Shadow Runner.

---

# Phase 4: Shadow Runner

Only start after the real-use gate passes.

## Scope

```bash
agentrec shadow run task.md --runner claude --runner codex
agentrec shadow show <group-id>
```

Requirements:

- dedicated clean Git worktree per runner;
- same baseline HEAD and task;
- workspace preparation command;
- explicit dependency/cache policy;
- `.env` and credential policy;
- submodule and Git LFS policy;
- separate ports and external state, or serialized verification;
- process-group cleanup;
- stale worktree recovery;
- original repository remains unchanged.

Comparison priority:

1. verification pass/fail;
2. scope violation;
3. final diff;
4. action timeline;
5. cost and duration as informational values only.

Do not generate an automatic quality score.

---

# Phase 5: Audited Execution Research Track

This is explicitly not part of the core MVP.

## Linux spike

Validate in an isolated Docker/Linux environment:

```text
strace -ff -e trace=%file,%process,%network
```

Given an agent-started script that:

- reads a known set of files;
- performs a DNS/network request;
- spawns four child processes;
- writes `/tmp/cache`;

verify that the audit backend records:

- file opens/reads;
- process fork/exec/exit;
- network connection syscalls;
- `/tmp` writes;
- final writable-layer diff.

If useful, replace `strace` later with Go userspace plus `cilium/ebpf` and a small eBPF C probe.

## macOS spike

Compare:

- built-in `eslogger` / Endpoint Security;
- DTrace-family tools under SIP and privilege constraints;
- Docker/Linux sandbox from macOS.

Do not commit to a native macOS audit backend until entitlement, signing, Full Disk Access and distribution constraints are proven.

Potential architecture only after validation:

```text
agentrec                 Go control plane
agentrec-audit-linux     Go + eBPF C
agentrec-audit-darwin    Swift Endpoint Security helper
```

---

# Public Release Gate

Target first public version: `v0.1.0`, not `v0.4.0`.

Required before public release:

- macOS arm64 and Linux arm64/amd64 CI;
- Claude and Codex support matrix;
- synthetic provider fixtures only;
- `SECURITY.md` explaining bundle sensitivity;
- `docs/assurance-model.md` distinguishing provider-reported and observed evidence;
- `docs/limitations.md` explicitly denying syscall completeness in native mode;
- Apache-2.0 license;
- checksummed release binaries;
- README flow from installation to first trace in under ten minutes;
- no company-specific source, path, prompt or credential in repository history.

---

# Verification Checklist

Every implementation task must run the narrow test first, then the full suite before commit.

```bash
go test ./...
go test -race ./...
go vet ./...
```

Before release:

```bash
gofmt -w <changed-go-files>
go test ./...
go test -race ./...
go vet ./...
```

Manual smoke tests:

```bash
agentrec trace claude -- -p "Read README.md and report its title"
agentrec show latest
agentrec trace codex -- exec "Read README.md and report its title"
agentrec show latest
```

Expected outcome:

- readable action timelines for both providers;
- tool/command input and result correlation;
- no duplicate actions from hooks;
- final Git changes and verification shown under separate assurance sections;
- default bundles contain no known fixture secrets;
- no permission bypass or external mutation performed by `agentrec`.

---

# Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Provider JSONL changes | Synthetic fixtures, unknown-event tolerance, version recording, no hard failure on unknown types |
| Hook duplicates | Correlate by provider action ID and define source priority |
| Structured flags change user output | Explicit `trace claude|codex` commands and human renderer; no transparent mutation |
| Secret leakage | Structural redaction, restrictive permissions, no exact raw by default |
| Git attribution overclaim | Label as final repository state observed during run |
| Agent modifies verifier | Pin config and argv before execution; refuse changed config |
| Descendant survives timeout | Process-group termination and regression tests |
| Scope grows into security sandbox | Keep `audit` as a gated research track after core validation |
| Cross-platform audit complexity | Core Go CLI first; OS-specific helpers only after spike evidence |

---

# Final Delivery Sequence

```text
1. Common Action model and synthetic fixtures
2. Claude parser and timeline
3. Codex parser and timeline
4. Secure bundle and explicit trace CLI
5. Real Claude/Codex smoke tests
6. Git delta and pinned verification
7. Fifteen-run value gate
8. Shadow Runner
9. Linux/macOS audit spikes
10. OSS release based on evidence
```

The project remains an Agent Flight Recorder. Shadow comparison and OS-level audit extend it only after the core action timeline proves useful.
