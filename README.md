# agentrec

Agentrec records Claude Code and Codex tool calls, commands, results, final repository changes, and independent verification as a replayable local action timeline.

## Status

The local `trace`, `list`, and `show` vertical slice is implemented for Claude Code and Codex, including provider parsing, structural secret redaction, secure run-bundle persistence, explicit command preparation, Unix process-group supervision, deterministic timeline rendering, and non-blocking clean-repository locking. Observed final delta, independent verification, and integrated evidence reporting remain under construction.

## Development

```bash
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/agentrec --help
```
