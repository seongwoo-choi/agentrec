<div align="center">

# agentrec

**A flight recorder for coding agents — every run leaves a local, attributed evidence bundle you can read after the terminal is gone.**

[![CI](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml/badge.svg)](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml)
[![Go 1.26](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

English | [한국어](README.ko.md) | [日本語](README.ja.md) | [简体中文](README.zh-CN.md)

</div>

## The problem

When a coding agent finishes, what you have is scrollback. It scrolls away, it
mixes what the agent *said* it did with what actually happened, and it says
nothing about whether the repository still builds.

agentrec records one non-interactive Claude Code or Codex run into a bundle: a
normalized action timeline, the supervised process result, the repository
difference across the run window, and the outcome of checks the repository itself
pinned. Each comes from a different observer, and the bundle keeps them apart.

## Four evidence layers

| Layer | Observer | What it means | Attribution recorded |
|---|---|---|---|
| **Provider-reported actions** | the agent | What the agent said it did — tool calls, shell commands, file reads and edits, MCP calls, Codex file changes. Normalized and summarized, never taken as proof. | `provider_reported` |
| **Supervisor-observed result** | agentrec | How the provider process ended: exit code, exit reason, signal, duration, warning count. | `supervisor_observed` |
| **Repository-observed changes** | agentrec | The difference between the commit pinned before the run and the worktree after it, measured by agentrec itself. | `observed during run, not causal proof` |
| **Verification-observed result** | agentrec | How the repository's own pinned checks ended when agentrec ran them after the provider stopped. Says nothing about how the work was done. | `verification_observed` |

Events carrying only provider progress, collaboration waits or todo-list
lifecycle are stream metadata: they name no action, and do not inflate warnings.

## Quick start

**Prerequisites.** Building from source requires Go 1.26 or newer. A supported
provider CLI must already be on `PATH`; agentrec launches it, never installs it.
A version outside the range is refused, not recorded on the assumption its event
stream still fits.

| Provider | Executable | Supported range | Notes |
|---|---|---|---|
| Claude Code | `claude` | `>=2.1.0, <3.0.0` | Requires `-p`/`--print`. agentrec injects `--output-format stream-json --verbose --include-hook-events`. |
| Codex | `codex` | `>=0.144.0, <1.0.0` | `exec` must be the first argument. agentrec injects `--json`. |

Each tagged release carries four archives — `darwin_amd64`, `darwin_arm64`,
`linux_amd64`, `linux_arm64` — plus a `SHA256SUMS` file covering all four. An
archive unpacks to a single directory holding `agentrec`, `LICENSE`,
`THIRD_PARTY_NOTICES.md` and `third_party/licenses/Apache-2.0.txt`.

```bash
# From a release archive — download SHA256SUMS plus the archive for your platform.
archive=agentrec_0.1.0_darwin_arm64.tar.gz
awk -v file="$archive" '$2 == file { print }' SHA256SUMS | shasum -a 256 -c -
tar -xzf "$archive"
./agentrec_0.1.0_darwin_arm64/agentrec version

# On Linux, use `sha256sum -c -` instead of `shasum -a 256 -c -`.
# Or from source
go install github.com/seongwoo-choi/agentrec/cmd/agentrec@v0.1.0
```

`agentrec version` (equivalently `agentrec --version`) prints three lines: the
version, the commit it was built from, and the UTC build time. A release binary
carries the tag, the full commit SHA and an RFC 3339 timestamp; a build made any
other way reports `dev`, `unknown` and `unknown`, so an unstamped binary is never
mistaken for a released one.

**Commit the verification config.** A run is verified only against checks the
repository already held. Copy `.agentrec.example.yaml` to `.agentrec.yaml` and
commit it — each command is launched directly, with no shell, so an argument is
an argument and nothing else:

```yaml
version: 1
verify:
  - name: go-test
    command: ["go", "test", "./...", "-count=1", "-timeout=420s"]
    timeout: 8m
  - name: go-vet
    command: ["go", "vet", "./..."]
    timeout: 5m
```

**Record a run.** The working directory must be a Git checkout with no
uncommitted changes and no operation in progress, so the run's own changes can be
told apart. One run at a time per repository: a second is refused, not queued.

```bash
# Claude Code
agentrec trace claude -- -p "add a regression test for the parser"
agentrec trace claude --verify -- -p "add a regression test for the parser"

# Codex
agentrec trace codex --verify -- exec "add a regression test for the parser"

# Record the same task with both agents, from one commit
agentrec shadow run task.md --runner claude --runner codex

# Read runs back
agentrec list
agentrec list --cwd /Users/you/code/agentrec
agentrec show 20260728T093159.858622000Z-582ee874
agentrec show latest

# Report the build this binary came from
agentrec version
```

`agentrec list` prints runs newest first, with a `PROJECT` column taken from the
last element of the working directory the manifest recorded; a manifest holding
anything but an absolute path reports `unknown` rather than a guess.

`--cwd` matches **one directory exactly**, not a prefix: the path given is made
absolute and cleaned, and a run is kept only when the manifest's own working
directory — itself absolute, cleaned the same way — is exactly it. A subdirectory
is a different path, and so is another way in through a symlink.

## What a report looks like

`agentrec show` is read-only: it renders a run from its bundle and writes
nothing. Excerpt from a real recorded run (`582ee874`, trimmed to one action):

```
PROVIDER-REPORTED ACTIONS
09:34:32  EDIT  /Users/csw/code/agentrec/README.md
  Source       claude
  Assurance    provider_reported
  Result       success
  Duration     1.128s

SUPERVISOR-OBSERVED RESULT
  Provider     claude
  Version      2.1.220
  Exit Reason  completed
  Exit Code    0
  Duration     2m50.625s
  Warnings     0

REPOSITORY-OBSERVED CHANGES
  Status       AVAILABLE
  Files        1 (1 tracked, 0 untracked)
  Diff         +18/-1, 0 binary
  Stored Text  0
  Baseline     43d37240e960ad2f321276045b2bb8d710f5a4db
  Attribution  observed during run, not causal proof

VERIFICATION-OBSERVED RESULT
  Status       PASS
  Config       .agentrec.yaml
  Config SHA-256 e20695bb3ebee3381b54da6fc46b6b1efa1adc9b87a5eb99b45505b5dbdfae3f
  Check        PASS go-test  "go" "test" "./..." "-count=1" "-timeout=420s"  42.486s  exit 0
  Check        PASS go-vet  "go" "vet" "./..."  348ms  exit 0
  Attribution  verification_observed
```

`agentrec trace` writes the same reading of the same bundle to `<run>/report.md`
before printing anything, once and never again: a report already standing at that
name is refused rather than overwritten.

## Comparing two agents on one task

`agentrec shadow run` records one task twice — once with Claude Code, once with
Codex — from a single committed baseline, and prints the two recorded runs side
by side:

```bash
agentrec shadow run task.md --runner claude --runner codex
```

Each leg is recorded in a disposable **detached Git worktree** created from the
source repository's `HEAD`, under `$AGENTREC_HOME/shadow/<group>/<runner>` with
mode `0700`, and removed once that leg's evidence has been closed. Both legs
leave ordinary run bundles, so `agentrec list` and `agentrec show <run-id>` read
them back after the checkouts are gone. The comparison itself is printed to
stdout; each leg's durable `report.md` stays in its own bundle.

The comparison prints one block per runner, always in this order and with these
fields in this order — a run ID, how the checks ended and what they were pinned
to, how the process ended, what the run left in its checkout, and how much it
did:

```
SHADOW COMPARISON

claude
  Run ID       20260729T101500.000000000Z-1a2b3c4d
  Verification PASS
  Config SHA-256 e20695bb3ebee3381b54da6fc46b6b1efa1adc9b87a5eb99b45505b5dbdfae3f
  Exit Reason  completed
  Exit Code    0
  Duration     2m50.625s
  Repository   AVAILABLE  1 files (1 tracked, 0 untracked)  +18/-1, 0 binary
  Actions      12
  Warnings     0

codex
  ...
```

What the command does and does not give you:

- **Isolation narrows interference; it is not causal attribution.** Each leg's
  repository delta is still recorded as `observed during run, not causal proof`.
- **No score, no winner, no recommendation.** The comparison shows recorded
  fields and nothing derived from them. Which run to prefer is the reader's
  judgement. Provider-reported cost and token fields are not in the recorded
  evidence today, so the comparison does not show them.
- **A Git checkout, not a byte-hermetic sandbox.** Untracked `.env` files and
  local credentials are not copied into a leg. Tracked files are checked out by
  the operator's Git, so configured attributes, filters and hooks still apply.
  Agentrec adds no credential transport or workspace-preparation step.
- **Repositories it cannot prepare are refused, not half-prepared.** A committed
  `.gitmodules` or a committed Git LFS pointer file is rejected before any
  checkout exists.
- **The task is one command-line argument.** The task file is read once — one
  regular, non-symlink, UTF-8 file of at most 64 KiB — and handed to each agent
  as `claude -p <task>` and `codex exec <task>`. A prompt on stdin, or spread
  over several arguments, is not supported here.
- **Verification is mandatory, and the legs are serialized.** Both runs are
  verified against the committed `.agentrec.yaml`, and they execute one after
  another. Their checks do not overlap, but mutable authentication, caches,
  network services and other external state are not reset between legs; input
  runner order can therefore affect what the second provider observes.
- **A linked worktree is not a security boundary.** It shares the repository's
  common Git directory and refs, and a provider can explicitly reach the source
  checkout. The lock coordinates agentrec processes only. After removing each
  owned worktree, agentrec compares the source `HEAD`, status, index, refs and
  worktree list with its preflight snapshot. Observed drift stops the next leg
  and exits `1`; agentrec reports it and does not destructively restore it.

Exit codes: `2` for a usage or preflight refusal — a runner named twice or
missing, an unreadable task file, a dirty checkout, an uncommitted
`.agentrec.yaml`, an `AGENTREC_HOME` inside the repository — all of which happen
before any checkout or provider exists. Then `0` when both legs completed and
both verifications passed, `1` when a leg failed, ended incomplete, changed the
source repository, or its checkout could not be removed, and `130` when the run
was interrupted. **A
provider's own exit code is evidence in its bundle and is never passed through**
by the aggregate command.

An interrupt stops the current leg's process group, finalizes that leg's
evidence, removes the checkouts and never launches the leg that had not started;
the comparison then shows the runner that did not run as `(not run)`. If
agentrec is killed outright — `SIGKILL`, or the machine going down — the
leftover checkout is recovered by running `git worktree prune` in the source
repository and deleting the leftover directory under `$AGENTREC_HOME/shadow`.
There is no automatic stale-worktree collection.

## Where runs are stored

Under `$AGENTREC_HOME/runs` when that is set, otherwise
`~/.local/share/agentrec/runs`. Run directories are created `0700` and every file
in them `0600`, `report.md` included — a bundle may quote a private repository,
so it is readable only by the user who recorded it. One directory per run holds
`manifest.json`, `prompt.txt`, the sanitized event stream and stderr,
`actions.jsonl`, `process/result.json`, `git/` (baseline, result, untracked
bodies), `verification/results.json` and `report.md`.

## Statuses and exit codes

A status is shown as it was recorded, never inferred:

- **Repository** — `AVAILABLE` (measured), `UNAVAILABLE` (no measurement
  produced), `PENDING` (written before the run, never answered). Counts are shown
  only for `AVAILABLE`: a `PENDING` run's zeros mean *not measured*, not
  *measured as nothing*.
- **Verification** — `PASS`, `FAIL`, `TIMEOUT`, `ERROR`, `TAINTED`. A run that
  requested no verification shows `(none)`, which is not a check that passed.
- **Config taint** — `--verify` pins `.agentrec.yaml` and its SHA-256 before the
  provider starts. If the run rewrote that file the verification is recorded
  `TAINTED`, reason `config_changed`, **nothing is executed**, and the pinned
  checks stay `PENDING`.

Exit codes: `0` provider completed and any verification passed; `1`–`125` the
provider's own exit code passed through; `1` recording, rendering or verification
failed; `2` agentrec was called wrongly; `130` interrupted.

Ctrl-C and SIGTERM are both held rather than obeyed where they land, for the
whole recording and not only while the provider runs: agentrec stops the
provider's process group, closes out the manifest, measures the repository, runs
the pinned checks, files the report, and exits `130` — so a run interrupted at
any point in that sequence says how it ended instead of being left standing at
`PENDING`. The first signal is the last one held: the disposition then goes back
to the operating system, so a second Ctrl-C ends the process where it stands.

`process/result.json` records an exit code when the process exited and the
terminating signal when it was killed by one. A process killed by a signal has
no exit code, and neither field is inferred from the other.

## Security

- **Structural redaction before persistence.** Provider events and stderr are
  redacted before they are written. Values under field names whose canonicalized
  form ends in one of 13 secret suffixes (`TOKEN`, `SECRET`, `PASSWORD`,
  `APIKEY`, `AUTHORIZATION`, `COOKIE`, …), plus `NAME=VALUE` assignments and
  token shapes, become `[REDACTED:n]`; the rule version is stamped per manifest.
- **Untracked file bodies are stored** under `git/untracked/`, hashed over
  sanitized text — a hash of raw text would hand a short secret back by guessing.
- **Reports never embed the raw provider event stream, tracked patch, or untracked
  body.** They do carry normalized provider-derived summaries: an action is
  reduced to a label, one allowlisted detail field and fixed summary fields with
  control characters escaped, so no provider string can forge a timeline row or
  drive the terminal. Bundles are read back defensively, symlinks are refused
  rather than followed, and sizes, line lengths and item counts are bounded.
- **A zero redaction count is not a secret-absence claim.** It means no rule
  matched — a secret in an unnamed field, in prose rather than an assignment, or
  shorter than the minimum length produces the same zero.

## Non-goals

- **Not syscall-complete.** Nothing observes the agent while it is working.
  agentrec records what the provider reported, what the repository looked like
  either side of the run, and what independent checks said afterwards.
- **A repository delta is not causal attribution.** The changes happened during
  the run; that is not the same as the agent having made them. Anything else
  editing the checkout lands in the same delta, and every report says so. A
  passing verification likewise only says the pinned checks passed on the tree
  the run left behind.
- **Interactive sessions are not recorded**, and there is no policy engine, no
  sandbox and no remote upload — agentrec observes and writes locally.

**Supported scope: macOS and Linux.** Process-group supervision is built for
`darwin || linux` only (`internal/runner/process_unix.go`), so Windows is unbuilt
and unverified.

## Evidence

Behavior claims are backed by
[docs/dogfood/2026-07-28-evidence.md](docs/dogfood/2026-07-28-evidence.md) — a
fixed 20-attempt checkpoint plus follow-on real mutations, covering verification
`FAIL`, provider nonzero, config `TAINTED`, interruption, and what those runs do
**not** establish.

The successful real-provider path for `agentrec shadow run` is covered by
[docs/dogfood/2026-07-29-shadow-evidence.md](docs/dogfood/2026-07-29-shadow-evidence.md):
one macOS run against Claude Code and Codex from the same commit, with both
pinned verifications passing, both worktrees removed and both bundles retained.
Real-provider failure, interruption and Linux runtime paths are not established
by that run; controlled repository tests cover those lifecycle paths.

## Development

```bash
go test ./... -count=1 -timeout=420s
go test -race ./... -count=1 -timeout=600s
go vet ./...
gofmt -l .
go build ./...

# Build the release archives locally; publishes nothing.
# The output directory must not already exist.
scripts/build-release.sh v0.1.0 "$(git rev-parse HEAD)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" dist
```

`.github/workflows/release.yml` runs the same script on a `v*.*.*` tag, checks
every archive's inventory and the version output of the binary it built, and
publishes only then. It refuses to run against a release that already exists.

## License

agentrec is available under the [MIT License](LICENSE). Third-party attributions
and dependency licenses are preserved in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
