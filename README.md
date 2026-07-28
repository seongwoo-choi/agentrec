# agentrec

Agentrec records Claude Code and Codex tool calls, commands, results, final repository changes, and independent verification as a replayable local action timeline.

## Status

MVP tasks 1–15 are implemented: the local `trace`, `list`, and `show` slice for Claude Code and Codex, with provider parsing, structural secret redaction, secure run-bundle persistence, explicit command preparation, Unix process-group supervision, deterministic timeline rendering, non-blocking clean-repository locking, repository-observed final delta capture, pinned independent verification, and integrated evidence reporting.

## What a report says, and who observed it

A report keeps its evidence sources apart, because they are not worth the same:

- **Provider-reported actions** are what the agent said it did. They are normalized and summarized, never taken as proof.
- **Supervisor-observed result** is how this recorder saw the provider process end.
- **Repository-observed changes** are the difference between the commit pinned before the run and the worktree after it, measured by agentrec itself. It is recorded as `observed during run, not causal proof`: the changes happened during the run, which is not the same as the agent having made them.
- **Verification-observed result** is how the repository's own pinned checks ended when this recorder ran them after the provider stopped. It says nothing about how the work was done.

A status is shown as it was recorded. An existing artifact can explicitly report `PENDING`, `UNAVAILABLE`, or `TAINTED` with its reason; a run from before that evidence existed, or one that did not request verification, shows `(none)`. Neither is presented as a run that changed nothing or a check that passed.

This is not a syscall-complete audit. Nothing here observes what the agent did while it was doing it: agentrec records what the provider reported, what the repository looked like either side of the run, and what independent checks said afterwards.

## Reports on disk

`agentrec trace` writes `<run>/report.md` — the same reading of the same bundle as the terminal timeline, in Markdown — before it prints anything. It is created once, mode `0600`, and never replaced: a report already standing there is refused rather than overwritten. It holds only normalized actions and evidence summaries, never raw provider events, the tracked patch, or an untracked file's body.

`agentrec show` is read-only. It renders a run and writes nothing, so a bundle recorded before reports existed stays as it was recorded.

## Usage

```bash
agentrec trace claude -- -p "read the README"
agentrec trace claude --verify -- -p "read the README"
```

With `--verify`, the checks in the repository's own `.agentrec.yaml` (see `.agentrec.example.yaml`) are pinned before the provider starts and run against the work after it stops. A configuration the run rewrote is refused rather than executed, and a verification that did not pass exits non-zero.

## Development

```bash
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/agentrec --help
```
