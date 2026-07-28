# Shadow Runner v0.2 Implementation Plan

> **For Hermes:** Use subagent-driven-development skill to implement this plan task-by-task.

**Goal:** Add `agentrec shadow run <task-file> --runner claude --runner codex`, recording two isolated runs from one committed baseline and rendering a deterministic evidence-only comparison.

**Architecture:** Acquire one lock on the source repository, pin its clean `HEAD`, committed `.agentrec.yaml`, task bytes and observable source state, then run Claude and Codex serially in disposable detached Git worktrees under the private agentrec data root. Reuse the existing bundle, repository evidence, verification, and report path through an extracted recording core; remove each worktree after its capture closes. Linked worktrees are not a sandbox: after each removal, detect source checkout/ref drift, stop before another provider, report it and do not restore it automatically.

**Tech Stack:** Go 1.26 standard library, Git CLI, existing provider/evidence/storage/report packages, repository-native Go tests.

---

## Product contract

Command:

```bash
agentrec shadow run task.md --runner claude --runner codex
```

Constraints:

- accept exactly one `claude` and one `codex`; execute flags in input order but render comparison in fixed runner-name order;
- read one bounded, regular, non-symlink task file before creating worktrees;
- require a clean source checkout and committed `HEAD:.agentrec.yaml`;
- resolve the baseline SHA once and create one detached worktree per runner from that SHA;
- run both legs with verification enabled and retain both ordinary run bundles;
- use existing evidence attribution; isolation narrows interference but does not prove causal attribution;
- never inject permission bypass, sandbox bypass, merge, scoring, winner, rank, or recommendation behavior;
- execute legs serially so verification and shared external state are not concurrent, while documenting that mutable external state is not reset between legs;
- first handled SIGINT/SIGTERM cancels the current process group, finalizes evidence when possible, removes worktrees, and exits 130; restore default signal behavior so a second signal remains an escape hatch;
- provider failure, verification failure, evidence failure, source drift, or cleanup failure exits 1; usage/preflight rejection before worktree creation exits 2; both successful verified legs exit 0; an observed interrupt exits 130 even when cleanup also reports an error;
- provider exit codes are evidence fields and are not passed through by the aggregate command.

Private workspace policy:

- use `<dataRoot>/shadow/<group-id>/<runner>` with private directory modes;
- refuse an agentrec data/shadow root nested inside the source checkout;
- untracked `.env` files and local credentials are not copied, but tracked checkout bytes remain subject to the operator's configured Git attributes, filters and hooks;
- provider CLIs use their existing mutable authentication/cache/network environment; agentrec adds no credential transport and does not equalize external initial conditions;
- reject repositories whose committed tree contains `.gitmodules` or Git LFS pointer files in this vertical slice rather than silently preparing incomplete workspaces;
- no workspace preparation command is added in this slice; committed project setup and `.agentrec.yaml` checks are the only preparation contract;
- after abnormal process death, documented recovery is `git worktree prune`; no automatic stale-worktree GC command is added.

Comparison fields, in fixed order:

1. runner and run ID;
2. verification status and config SHA-256;
3. provider exit reason/code/signal and duration;
4. repository changed-file/count/diff summary;
5. action and warning counts;
6. provider-reported cost/token fields only when already available in recorded evidence.

The comparison is informational only. This slice prints it to stdout; each leg's durable `report.md` remains in its ordinary bundle. Durable group storage and `shadow show` are deferred.

---

### Task 1: Fix trace signal lifecycle and extract a reusable recording core

**Objective:** Make trace hold SIGINT/SIGTERM through repository finalization/reporting and extract one recording operation without changing normal trace output.

**Files:**
- Modify: `internal/cli/trace.go`
- Modify: `internal/cli/trace_signal_unix_test.go`
- Modify: `internal/cli/cli_test.go` only if an existing helper must be reused

**Steps:**

1. Add a failing out-of-process regression proving a signal after provider exit but during repository finalization still produces finalized repository evidence/report and exits 130.
2. Run the focused test and confirm RED for the current signal-handler gap.
3. Install one signal lifecycle before work begins. The first handled signal marks the aggregate interrupted and immediately restores default disposition so a second signal can terminate; the buffered first signal remains available through capture finalization, verification, report installation and cleanup.
4. Extract a context/signal-driven recording function that accepts prepared provider command/parser/prompt, worktree, runs root, run ID, and verification setting, and returns structured outcome plus persisted report.
5. Keep source-repository lock and CLI argument parsing in `runTrace`; preserve existing trace exit-code passthrough and terminal output byte-for-byte.
6. Split report installation from terminal printing so Shadow can retain `report.md` without interleaving full timelines.
7. Run focused tests, `go test ./internal/cli -count=1`, then full tests.

### Task 2: Add private detached-worktree lifecycle

**Objective:** Create and remove a detached linked worktree at an exact commit without leaving refs, administration entries, or source checkout changes.

**Files:**
- Create: `internal/worktree/worktree.go`
- Create: `internal/worktree/worktree_test.go`

**Steps:**

1. Write a failing real-Git test that creates a temporary repository, adds a detached worktree at a pinned SHA, and proves its HEAD and clean status.
2. Implement direct argv Git execution with `LC_ALL=C`; do not invoke a shell.
3. Write failing tests for removal of dirty worktrees, cleanup idempotence after partial creation, and refusal of pre-existing/symlink paths.
4. Implement forced `git worktree remove` for the exact owned path, preserving and returning cleanup errors; do not run repository-global prune during normal cleanup.
5. Assert agentrec's own lifecycle leaves source `HEAD`, index/status, tracked-byte/mode digest, full refs, and worktree list identical after cleanup.
6. Run package tests and race tests.

### Task 3: Add Shadow preflight and CLI contract

**Objective:** Reject ambiguous or incomparable runs before any worktree/provider mutation.

**Files:**
- Create: `internal/cli/shadow.go`
- Create: `internal/cli/shadow_test.go`
- Modify: `internal/cli/root.go`
- Modify: `internal/cli/list.go` or add a small data-root helper file

**Steps:**

1. Write table-driven RED tests for missing/duplicate/unknown/extra runners and command shape errors.
2. Add dispatch and exact argument parser for `shadow run`.
3. Write RED tests for missing/directory/symlink/oversized/non-UTF-8 task files; implement one-time bounded regular-file read.
4. Write RED tests for dirty repository, uncommitted `.agentrec.yaml`, nested `AGENTREC_HOME`, committed `.gitmodules`, and standard or extended committed LFS pointer files; assert no shadow directory/provider start.
5. Implement source lock, clean check, baseline resolution, committed-config check, data-root confinement, and explicit unsupported-repository checks.
6. Run focused and full CLI tests.

### Task 4: Execute both isolated recording legs

**Objective:** Run Claude and Codex from identical committed state and retain ordinary evidence bundles.

**Files:**
- Modify: `internal/cli/shadow.go`
- Modify: `internal/cli/shadow_test.go`

**Steps:**

1. Write RED integration test with fake provider executables asserting each CWD differs from source, both HEADs equal pinned SHA, entry status is clean, `.agentrec.yaml` exists, and prompt argv equals task bytes.
2. Prepare explicit commands through existing Claude/Codex adapters; use `-p <task>` for Claude and `exec <task>` for Codex without bypass flags.
3. Create and record legs serially in flag order with verification mandatory.
4. Write RED test proving both bundles remain readable with `agentrec show` after worktree deletion.
5. Ensure the recording core closes repository capture and temporary refs before worktree removal.
6. Run focused and full CLI tests.

### Task 5: Handle failures and signals without leaking worktrees

**Objective:** Meet cleanup and evidence retention requirements for every supported ending.

**Files:**
- Modify: `internal/cli/shadow.go`
- Modify: `internal/cli/shadow_test.go`
- Create or modify: `internal/cli/shadow_signal_unix_test.go`

**Steps:**

1. Add RED subtests for provider nonzero and verification failure; require exit 1, retained bundle(s), removed worktrees, and an evidence-only comparison for all legs that started.
2. Remove each owned worktree immediately after its leg closes; deferred idempotent cleanup remains a safety net. Cleanup failure itself makes exit 1 and is printed explicitly.
3. Add out-of-process RED tests for SIGINT and SIGTERM during each provider leg; require exit 130, process-group termination, no worktree/admin/ref leak, retained finalized started bundle, and unchanged source snapshot.
4. Check pending signal before starting each leg so an interrupt during setup never launches the next provider.
5. Stop custom signal handling after the first signal is observed so a second signal keeps the default escape hatch.
6. Run signal tests repeatedly and under `-race`.

### Task 6: Render deterministic comparison output

**Objective:** Produce stable, bounded, non-evaluative comparison text from persisted bundle evidence.

**Files:**
- Create: `internal/cli/shadow_report.go`
- Modify: `internal/cli/shadow_test.go`

**Steps:**

1. Write a RED golden/behavior test with input legs in both orders and assert fixed runner-name order and field order.
2. Build comparison data only by reading persisted bundles; do not trust in-memory provider results as durable evidence.
3. Reuse existing sanitization/bounded read/report fields; avoid map iteration in output.
4. Assert output contains no automatic evaluation vocabulary or fields (`winner`, `score`, `rank`, `recommendation`).
5. Add deterministic rendering tests for partial/failing legs and missing optional values.
6. Run focused and full tests.

### Task 7: Documentation, real dogfood, and release gate

**Objective:** Prove the command with real Claude/Codex runs and synchronize public claims.

**Files:**
- Modify: `README.md`
- Modify: `docs/plans/2026-07-27-agentrec-flight-recorder.md`
- Create: `docs/dogfood/2026-07-29-shadow-evidence.md`

**Steps:**

1. Document command, isolation boundary, credential/cache policy, task-on-argv limitation, unsupported submodule/LFS policy, serialized verification, aggregate exit codes, stale recovery, and no-score/no-causality claims.
2. Correct the signal claim to match the now-tested full trace lifecycle and clarify that process evidence records an exit code or terminating signal when observed.
3. Build a local binary from the changed tree.
4. In a fresh throwaway Git repository with committed `.agentrec.yaml`, run one harmless deterministic code-change task through real Claude and Codex.
5. Record pinned baseline/config SHA, two run IDs, exit/verification/repository summaries, source before/after digest, worktree/ref cleanup evidence, and comparison output.
6. Verify no secrets or company-specific paths/prompts enter committed dogfood evidence; redact or use a neutral temp path/task.
7. Run:

```bash
gofmt -w <changed-go-files>
go test ./... -count=1 -timeout=420s
go test -race ./... -count=1 -timeout=600s
go vet ./...
go build ./...
git diff --check
```

8. Run independent spec-compliance review, then correctness/security/test-quality review. Fix all actionable findings and re-run reviews.
9. Check README, plan, dogfood evidence, AGENTS/CHANGELOG presence and relevance before commit.
10. Commit with `feat(shadow): compare isolated agent runs`, push normal `main`, and verify GitHub CI green. Do not tag or publish v0.2.0 without a separate release decision.

---

## Implementation status (2026-07-29)

Tasks 1–7 are complete. The implementation, real-provider dogfood,
documentation, independent reviews, normal `main` push and GitHub CI gate all
completed without a tag or release.

Done:

- Tasks 1–3 as written: trace signal lifecycle, `internal/worktree`, and the
  Shadow argument/task/repository preflight.
- Task 4: both legs are recorded serially in flag order, in detached worktrees
  created from one pinned baseline, through the existing Claude and Codex
  adapters with the task bytes on argv behind an option delimiter and no
  permission-widening flags; both bundles are readable with `agentrec show`
  after the checkouts are gone.
- Task 5: per-leg owned cleanup, cleanup failure exits 1 and is printed, one
  operating-system signal subscription durably latches the first signal and
  restores default disposition before forwarding it, and serializes the final
  provider launch through that latch. A deterministic boundary seam proves a
  signal queued before that launch decision prevents it; out-of-process tests
  cover setup/version discovery without provider launch, both leg positions,
  repository release, plus the second-signal escape hatch without fixed sleeps.
- Source drift checks include the common repository config digest in addition
  to `HEAD`, status, index, refs and worktree registrations; observed config
  mutation stops the second provider without destructive restoration.
- Task 6: `renderComparison` builds every field by reading the persisted bundles
  and renders them in fixed runner and field order, with no evaluation
  vocabulary.
- Task 7 steps 1–9: README and plan synchronization, local verification,
  independent reviews, and a real Claude/Codex run recorded in
  `docs/dogfood/2026-07-29-shadow-evidence.md`. Both legs used baseline
  `b9031133fdb8b5624e3ede41c0551a867d80289b` and config SHA-256
  `e20695bb3ebee3381b54da6fc46b6b1efa1adc9b87a5eb99b45505b5dbdfae3f`,
  passed `go-test` and `go-vet`, removed both worktrees, and retained both
  bundles.

Decisions taken while implementing, which the plan above did not settle:

- **A leg that failed on its own does not stop the other one.** A provider that
  exited nonzero and a verification that did not pass are evidence about that
  agent, and the comparison is the reason both were recorded, so the second leg
  still runs and the command exits 1 at the end. A failure of the recorder
  itself — a bundle that could not be opened, evidence that could not be
  completed, a checkout that could not be created — stops the comparison there.
- **Comparison field 6 (cost/token) renders nothing**, because no provider cost
  or token value reaches a bundle today. Duration, which is measured by the
  supervisor, is shown.
- **A leg that never started is shown as `(not run)`** rather than omitted, so an
  interrupted comparison says which side is missing.
- **The checkout directory is narrowed to `0700` after Git creates it**, since
  Git creates it against the operator's umask.
- **Linked worktrees are not a provider sandbox.** Shadow snapshots source
  `HEAD`, status, index, refs and worktree list, checks them after each owned
  worktree is removed, and stops before the next provider on observed drift.
  It reports drift and exits 1 without destructive restoration. Git filters,
  hooks, credentials, caches, network services and other external state remain
  part of the operator/provider environment.
- **Normal cleanup never runs repository-global `git worktree prune`.** It
  removes only the exact Shadow-owned path. `prune` remains a manual crash
  recovery command.

Release gate:

- Commits `b903113` and `ac48366` were pushed normally to `main`.
- GitHub CI run
  [30382584369](https://github.com/seongwoo-choi/agentrec/actions/runs/30382584369)
  passed formatting, tests, race detector, vet, release-script boundaries and
  build.
- No tag or release was created.

## Completion evidence

The task is complete only when all are true:

- both real provider legs began from one recorded commit and verification config SHA;
- the clean dogfood source checkout's observed status, HEAD, index, refs and
  worktree list matched before/after; adversarial tests prove file/ref drift is
  detected, stops the next leg and is not automatically restored;
- ordinary bundles and `report.md` survive worktree cleanup;
- success, provider failure, verification failure, SIGINT, and SIGTERM cleanup tests pass;
- comparison output is deterministic and contains no automatic score/winner;
- full tests, race detector, vet, build, and CI pass;
- README and committed dogfood evidence state the limitations precisely.
