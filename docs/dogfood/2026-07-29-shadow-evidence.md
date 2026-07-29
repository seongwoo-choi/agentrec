# Shadow Runner dogfood evidence — 2026-07-29

One real `agentrec shadow run` against the installed Claude Code and Codex CLIs,
recorded from agentrec commit `b9031133fdb8b5624e3ede41c0551a867d80289b`.
Sections marked **Observed** are readings from the command output or persisted run
bundles. Sections marked **Conclusion** are inference on top of those readings.

## Task and command

The task asked each provider to add the same pure Go helper and table-driven test
under `internal/action`, without changing any other package or documentation:

```go
func IsRepositoryMutation(actionType string) bool
```

The command was:

```bash
agentrec shadow run /tmp/agentrec-shadow-task.md \
  --runner claude \
  --runner codex
```

The committed `.agentrec.yaml` pinned two mandatory checks for each leg:

```text
go test ./... -count=1 -timeout=420s
go vet ./...
```

## Persisted comparison

**Observed.** The command exited `0` and rendered runners in the requested order:

| Provider | Run | Process | Verification | Repository | Actions | Warnings |
|---|---|---|---|---|---:|---:|
| Claude | `20260728T170948.094172000Z-0761cb2e` | completed, exit 0, 63.232s | PASS: go-test, go-vet | AVAILABLE, 2 tracked, +36/-0 | 8 | 0 |
| Codex | `20260728T171155.415989000Z-7afdb2bc` | completed, exit 0, 171.347s | PASS: go-test, go-vet | AVAILABLE, 2 tracked, +35/-0 | 23 | 0 |

Both bundles recorded:

- baseline commit `b9031133fdb8b5624e3ede41c0551a867d80289b`;
- verification config SHA-256
  `e20695bb3ebee3381b54da6fc46b6b1efa1adc9b87a5eb99b45505b5dbdfae3f`;
- the task after an option delimiter: Claude `-p -- <task>`, Codex
  `exec --json -- <task>`;
- `internal/action/action.go` and `internal/action/action_test.go` as the only
  changed paths;
- a finalized `manifest.json`, `process/result.json`, Git evidence,
  `verification/results.json`, `actions.jsonl`, and `report.md` after the
  disposable worktrees were removed.

The stored patch SHA-256 values differ:

- Claude: `d92c04597220621ad9b44c6248ba28d9b4aa83f9f08be1babdabe2907d1935be`
- Codex: `93fab6fe68ba7421cc136125f04f87a88daa1277ff1da53d93441de444c86be6`

Both implementations returned true only for `TypeFileWrite` and `TypeFileEdit`
and added coverage for every other declared type, empty input and an unknown
future type. The patches differed in comment and table-test structure; Shadow
reported evidence and did not call either one a winner.

**Conclusion.** This run demonstrates the successful vertical path with real
provider CLIs: same committed baseline, independent worktrees and bundles,
mandatory pinned verification, deterministic comparison order and no
score/recommendation. Passing checks establish only that each persisted checkout
passed the pinned commands; they do not prove the provider-reported action stream
caused the repository delta.

## Source checkout and cleanup

**Observed.** Before and after the command:

```text
HEAD  b9031133fdb8b5624e3ede41c0551a867d80289b
TREE  5e71e963320960269d2d0bee924e84703400068d
git status --porcelain=v2  (empty)
```

After completion, `git worktree list --porcelain` contained only the source
checkout at `<source-checkout>`. The private Shadow workspace root was empty,
while both run bundle directories remained readable.

**Conclusion.** For this successful run, the original checkout's committed HEAD,
index tree and worktree status were unchanged, both linked-worktree registrations
were removed, and evidence survived cleanup.

## Durable group and replay — 2026-07-30

This second real-provider run exercised the durable Shadow group path from
agentrec commit `762d22e`. It used an isolated committed repository at baseline
`9f2b07803f988c6c397d3904ff5aedd3a03c5ba7`, a task outside that repository,
and a private temporary `AGENTREC_HOME`. The task instructed both providers to
inspect the repository without changing files. The committed verification ran
`git diff --check` for each leg.

**Observed.** `agentrec shadow run` exited `0`:

| Provider | Run | Process | Verification | Repository | Actions |
|---|---|---|---|---|---:|
| Claude | `20260729T155520.942938000Z-ed7b744e` | completed, exit 0, 23.545s | PASS: repository-clean | AVAILABLE, 0 files, +0/-0 | 4 |
| Codex | `20260729T155545.271163000Z-fe102c7b` | completed, exit 0, 27.770s | PASS: repository-clean | AVAILABLE, 0 files, +0/-0 | 5 |

The run created private
`$AGENTREC_HOME/shadow/20260729T155520.217860000Z-fd6e3e1a/group.json` with
mode `0600`. Its recorded document contained schema `1`, the committed baseline,
the two run IDs in Claude-then-Codex execution order, and outcome `completed`.
It did not contain the task text. The group's `workspaces/` child was absent
after completion, and the source repository remained clean.

`agentrec shadow show 20260729T155520.217860000Z-fd6e3e1a` exited `0` and
re-rendered the evidence-only comparison from the ordinary bundles after the
workspaces had been removed.

**Conclusion.** This run establishes one successful macOS vertical path for
persisting and replaying a Shadow comparison: private group metadata references
ordinary finalized bundles, does not retain the raw task body, and survives
worktree cleanup. It does not establish real-provider failure, interruption,
source-drift, parent-directory replacement, or Linux behavior. Those lifecycle
and containment paths remain controlled-test coverage, not observations from
this run.

## Action-layer discrepancy

**Observed.** The action-type distributions were:

```text
Claude  file.read=2  file.edit=2  shell.exec=4
Codex   agent.message=6  file.edit=2  shell.exec=14  provider.error=1
```

Codex's `provider.error` was a provider-reported startup warning that the local
Codex configuration had an under-development feature enabled. The supervised
process still exited `0`; both pinned checks passed; the Shadow summary showed
`Warnings 0` because parser warnings and normalized `provider.error` actions are
different fields.

**Conclusion.** The four evidence layers must remain separate. A
provider-reported error-shaped event does not override a supervisor-observed exit
or verification verdict. The compact comparison currently requires opening the
bundle to see this action-type detail.

## What this run does not establish

This is one successful-path run on macOS. It does not independently establish:

- provider failure, verification failure or signal cleanup under real CLIs;
- cleanup after a partially created worktree in a real Git failure;
- containment of a provider: linked worktrees share common Git refs and are not
  a sandbox; this run only observed that the source snapshot did not drift;
- byte-hermetic checkouts under custom Git attributes, filters or hooks;
- equal provider credentials, caches, network state or other external initial
  conditions between the serialized legs;
- Linux runtime behavior;
- causal attribution, patch quality ranking or provider superiority.

Those lifecycle and failure paths are covered by repository tests using
controlled stand-ins. This document does not turn those tests into real-provider
observations.
