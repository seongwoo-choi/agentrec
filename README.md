# agentrec

Agentrec records Claude Code and Codex tool calls, commands, results, final repository changes, and independent verification as a replayable local action timeline.

## Status

Initial project scaffold. See `docs/plans/2026-07-27-agentrec-flight-recorder.md` for the implementation plan.

## Development

```bash
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/agentrec --help
```
