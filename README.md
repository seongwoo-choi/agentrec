# agentrec

Agentrec records Claude Code and Codex tool calls, commands, results, final repository changes, and independent verification as a replayable local action timeline.

## Status

Provider parsing is implemented for Claude `stream-json` and Codex `--json` fixtures. The normalized action model and synthetic provider fixtures are also in place; the remaining implementation follows `docs/plans/2026-07-27-agentrec-flight-recorder.md`.

## Development

```bash
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/agentrec --help
```
