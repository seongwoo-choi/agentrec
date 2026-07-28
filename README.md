# agentrec

Agentrec records Claude Code and Codex tool calls, commands, results, final repository changes, and independent verification as a replayable local action timeline.

## Status

The local `trace`, `list`, and `show` vertical slice is implemented for Claude Code and Codex, including provider parsing, structural secret redaction, secure run-bundle persistence, explicit command preparation, Unix process-group supervision, deterministic timeline rendering, non-blocking clean-repository locking, repository-observed final delta capture, and pinned independent verification. Integrated evidence reporting remains under construction.

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
