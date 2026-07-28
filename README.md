# agentrec

Agentrec records Claude Code and Codex tool calls, commands, results, final repository changes, and independent verification as a replayable local action timeline.

## Status

MVP tasks 1–15 are implemented: the local `trace`, `list`, and `show` slice for Claude Code and Codex, with provider parsing, structural secret redaction, secure run-bundle persistence, explicit command preparation, Unix process-group supervision, deterministic timeline rendering, non-blocking clean-repository locking, repository-observed final delta capture, pinned independent verification, and integrated evidence reporting.

## What a report says, and who observed it

A report keeps its evidence sources apart, because they are not worth the same:

- **Provider-reported actions** are what the agent said it did. They are normalized and summarized, never taken as proof. MCP calls from either provider, and Codex file changes, are reported this way and stand in the Action Timeline beside commands and file reads. Events that only carry the provider's own progress, its collaboration waits, or its todo-list lifecycle are recognized as stream metadata: they name no action, and they do not count towards the warnings a run reports.
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
agentrec list
agentrec list --cwd /Users/you/code/agentrec
agentrec show 20260728T093159.858622000Z-582ee874
```

With `--verify`, the checks in the repository's own `.agentrec.yaml` (see `.agentrec.example.yaml`) are pinned before the provider starts and run against the work after it stops. A configuration the run rewrote is refused rather than executed, and a verification that did not pass exits non-zero.

Ctrl-C and SIGTERM are both held rather than obeyed where they land — an operator types the one, a parent runner or a container sends the other, and either way the recorder stops the provider's process group, closes out the manifest, measures the repository, writes the report, and exits 130. A run ended that way says how it ended, and its manifest and Git evidence are recorded as they were observed rather than left standing at `PENDING`.

`agentrec list` prints the recorded runs, newest first:

```
RUN ID  PROVIDER  PROJECT  STARTED  EXIT
20260728T093159.858622000Z-582ee874  claude  agentrec  2026-07-28T09:31:59Z  completed
20260728T093025.570600000Z-8abfc728  codex  hermes-sustain  2026-07-28T09:30:25Z  completed
```

PROJECT names the checkout a run was recorded in, by the last element of the working directory its manifest recorded. Only an absolute path names a directory on the machine the run happened on, so a manifest holding anything else reports `unknown` rather than a guess.

`agentrec list --cwd <path>` narrows the table to the runs recorded in one checkout. The path given is made absolute and cleaned, and a run is kept only when the manifest's own working directory — itself absolute, and cleaned the same way — is exactly it. It is a match on one directory, not a prefix: a subdirectory of that checkout is a different path, and so is another way in through a symlink.

## Development

```bash
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/agentrec --help
```
