<p align="center">
  <img src="assets/agentrec-wordmark.svg" alt="agentrec — a flight recorder for coding agents" width="100%">
</p>

<table align="center">
  <tr>
    <td width="50%" align="center">
      <img src="assets/agentrec-report.svg" alt="agentrec show latest rendering an interactive session as an action timeline with four evidence sections"><br>
      <sub><b>One run, read back.</b><br>What the agent said, what the process did, what the repository shows, what the checks returned — kept apart.</sub>
    </td>
    <td width="50%" align="center">
      <img src="assets/agentrec-evidence-layers.svg" alt="The four evidence layers of an agentrec bundle"><br>
      <sub><b>Four observers, four attributions.</b><br>Nothing is combined into a score, and unavailable evidence is never a pass.</sub>
    </td>
  </tr>
</table>

# agentrec

<div align="center">

English | [한국어](README.ko.md) | [日本語](README.ja.md) | [简体中文](README.zh-CN.md)

[![CI](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml/badge.svg)](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/seongwoo-choi/agentrec?logo=github)](https://github.com/seongwoo-choi/agentrec/releases)
[![Go 1.26](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![GitHub stars](https://img.shields.io/github/stars/seongwoo-choi/agentrec?style=flat&logo=github)](https://github.com/seongwoo-choi/agentrec)

</div>

<p align="center">
  <strong>Every coding-agent run leaves a local, attributed evidence bundle you can read after the terminal is gone.</strong><br>
  <em>Launched by agentrec or recorded from an interactive session. Provider claims, process result, repository delta and pinned checks — each from its own observer, never merged into a score.</em>
</p>

**agentrec** records one Claude Code or Codex run into a bundle: a normalized
action timeline, the supervised process result, the repository difference across
the run window, and the outcome of checks the repository itself pinned. Each
comes from a different observer, and the bundle keeps them apart — so a code
review, an incident investigation, a handoff, or a decision to trust a new agent
version starts from what was observed rather than from a summary.

[Release notes](docs/releases/v0.3.0.md) ·
[Design notes](docs/plans/2026-07-27-agentrec-flight-recorder.md) ·
[Shadow runner design](docs/plans/2026-07-29-shadow-runner.md) ·
[Dogfood evidence](docs/dogfood/2026-07-28-evidence.md) ·
[Third-party notices](THIRD_PARTY_NOTICES.md)

> [!NOTE]
> agentrec is not a live agent frontend, a cloud telemetry service, or proof that
> an agent caused every observed file change. It is a local evidence boundary
> around one run — useful precisely because it states what was observed, by whom,
> and what it cannot establish.

## Quick start

> **Status:** v0.3.0 is the latest release. It adds interactive session recording
> for Claude Code and Codex, pins repository evidence to Git's defaults, and keeps
> redaction from growing a line past the stream limit.

**Pick one install. Homebrew is the easiest.**

```sh
brew install seongwoo-choi/tap/agentrec
agentrec version
```

```sh
archive=agentrec_0.3.0_darwin_arm64.tar.gz
awk -v file="$archive" '$2 == file { print }' SHA256SUMS | shasum -a 256 -c -
tar -xzf "$archive"
./agentrec_0.3.0_darwin_arm64/agentrec version
```

```sh
go install github.com/seongwoo-choi/agentrec/cmd/agentrec@v0.3.0
```

Each tagged release carries `darwin_amd64`, `darwin_arm64`, `linux_amd64` and
`linux_arm64` archives plus one `SHA256SUMS` covering all four. On Linux, use
`sha256sum -c -` in place of `shasum -a 256 -c -`. `agentrec version` prints the
tag, the commit and the UTC build time; a build made any other way reports `dev`,
so an unstamped binary is never mistaken for a released one. Building from source
needs Go 1.26 or newer; `shadow run` also needs Git 2.36 or newer.

**⭐ Commit the verification config (recommended):**

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

Copy `.agentrec.example.yaml` to `.agentrec.yaml` and commit it. A run is verified
only against checks the repository already held, and each command is launched
directly, with no shell: an argument is an argument and nothing else.

**Record a run agentrec launches:**

```sh
agentrec trace claude -- -p "add a regression test for the parser"
agentrec trace claude --verify -- -p "add a regression test for the parser"
agentrec trace claude --timeout 30m -- -p "add a regression test for the parser"
agentrec trace codex --verify -- exec "add a regression test for the parser"
agentrec trace claude --verify --allow-unsupported-version -- -p "..."
```

The working directory must be a Git checkout with no uncommitted changes and no
operation in progress, so the run's own changes can be told apart. One traced run
at a time per repository: a second is refused, not queued.

**Record the interactive sessions you already have:**

```sh
agentrec setup
agentrec setup --claude --verify
agentrec setup --codex --project
agentrec hooks print --claude
```

On a terminal, `agentrec setup` asks which agent to record (Claude Code, Codex or
both), whether to run the checks pinned in `.agentrec.yaml` after each session
(`--verify`), and whether to write your user file (`~/.claude/settings.json`,
`~/.codex/hooks.json`) or the project's (`.claude/settings.json`, `.codex/hooks.json`).
Flags skip the questions. Existing hooks are kept, a backup is written beside the
file, and running it again changes nothing. Codex needs `/hooks` once, inside Codex,
to trust the new hook. `hooks print` shows the fragment instead of installing it.
Every session opened afterwards is filed as a run; sessions already open are not.

**Read it back — in the browser, where most people will want it:**

```sh
agentrec start
agentrec status
agentrec stop
agentrec view latest
agentrec list
agentrec show latest
agentrec events latest --json
```

`agentrec start` keeps the viewer running in the background at `http://127.0.0.1:7788/`
and opens it; `status` says whether it is running, how many runs are recorded and
whether the hooks are installed; `stop` ends it. `view` serves the same pages in the
foreground.

| Provider | Executable | Supported range | What agentrec injects |
| --- | --- | --- | --- |
| Claude Code | `claude` | `>=2.1.0, <3.0.0` | `trace` requires `-p`/`--print` and adds `--output-format stream-json --verbose --include-hook-events` |
| Codex | `codex` | `>=0.144.0, <1.0.0` | `trace` requires `exec` first and adds `--json` |

A provider version outside the range is refused, not recorded on the assumption
its event stream still fits. `--allow-unsupported-version` records anyway and
stamps the manifest and every report `versionUnverified`; `shadow run` has no
such override, because a comparison between one timeline that was read properly
and one that was not is not a comparison.

## What agentrec shows you

<table align="center">
  <tr>
    <td width="50%" align="center">
      <img src="assets/agentrec-report.svg" alt="agentrec show latest"><br>
      <sub><b><code>agentrec show</code>.</b> The same reading is filed as <code>report.md</code> beside the evidence.</sub>
    </td>
    <td width="50%" align="center">
      <img src="assets/agentrec-evidence-layers.svg" alt="The four evidence layers"><br>
      <sub><b><code>agentrec view</code>.</b> A read-only, loopback-only viewer over the same bundle.</sub>
    </td>
  </tr>
</table>

What the timeline and the viewer put in front of you:

- **Action timeline** — every tool call, shell command, file read and edit the
  provider reported, normalized across providers, each carrying its `Source` and
  `Assurance`.
- **Change Explorer** — tracked, untracked, binary, addition and deletion
  evidence, separated from unavailable or malformed capture states.
- **Unified Overview** — process outcome, verification verdict, repository
  evidence, actions, events, duration and warnings together, without converting
  unavailable evidence into success.
- **Same-path observations** — a file action whose explicit path matches a
  changed path is linked and labelled `same path observed — not causal proof`;
  command and result text are never inferred as paths.
- **Provider events and usage** — bounded provider events, non-event stdout, and
  provider-reported token usage stay separate from normalized actions.

## Four evidence layers

| Layer | Observer | What it means | Attribution recorded |
| --- | --- | --- | --- |
| 🗣️ **Provider-reported actions** | the agent | What the agent said it did — tool calls, shell commands, file reads and edits, MCP calls, Codex file changes. Normalized and summarized, never taken as proof. | `provider_reported` |
| 👁️ **Supervisor-observed result** | agentrec | How the provider process ended: exit code, exit reason, signal, duration, warning count. `UNAVAILABLE` for a session agentrec did not launch. | `supervisor_observed` |
| 🌳 **Repository-observed changes** | agentrec | The difference between the commit pinned before the run and the worktree after it, measured by agentrec itself. | `observed during run, not causal proof` |
| ✅ **Verification-observed result** | agentrec | How the repository's own pinned checks ended when agentrec ran them after the provider stopped. Says nothing about how the work was done. | `verification_observed` |

Events carrying only provider progress, collaboration waits or todo-list lifecycle
are stream metadata: they name no action and do not inflate warnings. A stdout
line that is not a provider event at all — an update banner, a deprecation
warning — is kept in `provider-stdout.unparsed.log`, redacted like everything
else, counted in the manifest as `unparsedLines`, and named in the report. It
does not fail the run: a provider that printed one line of prose has still run.

## Two ways to record

| | 🚀 `agentrec trace` | 🎧 Interactive session |
| --- | --- | --- |
| Who starts the provider | agentrec, as the parent process | You, as always; the provider's hooks report to agentrec |
| Supervisor-observed result | exit code, signal, duration | `UNAVAILABLE`; `Ended By` says whether the `SessionEnd` hook reported the end or the recorder gave up (`session_lost`, after eight hours without a hook) |
| Baseline | pinned before the process starts | pinned when the `SessionStart` hook arrives; the `Window` line says so |
| Checkout state | must be clean; one run per repository | dirty checkouts and concurrent sessions are recorded, not refused |
| Verification | `--verify` pins `.agentrec.yaml` before launch | only for a fragment printed with `--verify`, and only when `.agentrec.yaml` is tracked and identical to `HEAD` |
| Provider events | the event stream agentrec reads | `SessionStart`, `UserPromptSubmit`, `PostToolUse`, `PostToolUseFailure`, `SessionEnd` payloads |

The first hook of a session starts a recorder for it; the recorder pins the
baseline, takes every event the hooks deliver, and closes the run out when the
session ends. No single delivery can end the recording of the ones after it, and a
session resumed under the same ID gets a recorder of its own. Actions carry the
provider's `tool_use_id` and `duration_ms`, and a subagent's calls carry its
`agent_id`; a hook the session disabled leaves a gap, not an absence.

Codex sends no `PostToolUseFailure`, so a command that failed appears as a
completed action whose response says so, and its `apply_patch` edits name their
files in the patch headers, which is where the repository paths come from. The
payload shapes were confirmed against Codex 0.150.1 in `codex exec`; hooks in
the interactive TUI follow the same documented contract.

## Commands

| Command | What it does |
| --- | --- |
| 🚀 `agentrec trace <claude\|codex> [--verify] [--allow-unsupported-version] [--timeout <d>] -- <args...>` | Records one non-interactive run agentrec launches and supervises. |
| 🧩 `agentrec setup [--claude] [--codex] [--verify] [--project] [--uninstall]` | Installs the hooks that record interactive sessions; without flags, on a terminal, it asks which agent, whether to verify and where. |
| ▶️ `agentrec start [--listen <loopback-address>] [--no-open]` | Starts the viewer in the background and opens it. |
| ⏹️ `agentrec stop` | Stops the background viewer. |
| ℹ️ `agentrec status` | Reports the viewer, the run count and whether the hooks are installed. |
| 🎧 `agentrec hooks print --claude\|--codex [--verify]` | Prints the hooks fragment `setup` would install, for installing by hand. |
| ⚖️ `agentrec shadow run <task-file> --runner claude --runner codex` | Records one task twice, from one committed baseline, in isolated worktrees. |
| ⚖️ `agentrec shadow show <group-id>` | Re-renders a recorded comparison, evidence only. |
| 📋 `agentrec list [--cwd <path>] [--exit-reason <reason>] [--verification-status <status>]` | Lists runs newest first. |
| 📄 `agentrec show <run-id>\|latest` | Renders one run from its bundle; writes nothing. |
| 🧾 `agentrec events <run-id>\|latest [--json]` | Summarises or dumps the recorded provider events. |
| 🖥️ `agentrec view [<run-id>\|latest] [--listen <loopback-address>] [--no-open]` | Serves the read-only viewer on loopback. |
| 🏷️ `agentrec version` | Prints the tag, commit and UTC build time. |

`agentrec hook <provider>` and `agentrec session serve` exist too; the provider
runs the first and the first hook starts the second. Neither is meant to be typed.

## What a report looks like

`agentrec show` is read-only: it renders a run from its bundle and writes nothing.
Excerpt from a real recorded run (`582ee874`, trimmed to one action):

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

```sh
agentrec shadow run task.md --runner claude --runner codex
agentrec shadow show <group-id>
```

`shadow run` records one task twice — once with Claude Code, once with Codex —
from a single committed baseline, each in a disposable detached Git worktree
under `$AGENTREC_HOME/shadow/<group>/workspaces/<runner>`, removed once that
leg's evidence is closed. Both legs leave ordinary run bundles; the private
`group.json` keeps the baseline, leg order, run IDs and outcome, never the task
body. The comparison prints one block per runner — run ID, verification and its
pinned config, process result, repository delta, action count — always `claude`
then `codex`, with `Order` recording which actually ran first.

| It gives you | It does not give you |
| --- | --- |
| Two runs from the same commit, verified against the same committed `.agentrec.yaml`, one after another | A score, a winner or a recommendation — the reader judges |
| Isolation that narrows interference between the legs | Causal attribution — each delta is still `observed during run, not causal proof` |
| Source drift detection after each leg (`HEAD`, status, index, refs, worktrees, config) that stops the next leg | A sandbox — a linked worktree shares the common Git directory, and untracked `.env` files are not copied in |
| Exit `2` for a refusal before anything exists, `0`/`1` for the legs, `130` when interrupted | The provider's own exit code — that is evidence in its bundle, never passed through |

A committed `.gitmodules` or Git LFS pointer file is refused before any checkout
exists. The task is one regular UTF-8 file of at most 64 KiB, handed to each
agent as one argument. If agentrec is killed outright, recover the leftover
checkout with `git worktree prune` and delete the directory under
`$AGENTREC_HOME/shadow`; there is no automatic stale-worktree collection.

## Evidence before claims

agentrec only says that what it saw happened. A status is shown as it was
recorded, never inferred:

| Shown | Means |
| --- | --- |
| `AVAILABLE` | The repository was measured. Counts are shown only here. |
| `UNAVAILABLE` | No measurement was produced — or, for a session, no process was supervised. Neutral, never a pass. |
| `PENDING` | Written before the run and never answered. Its zeros mean *not measured*, not *measured as nothing*. |
| `PASS` / `FAIL` / `TIMEOUT` / `ERROR` | How the pinned checks ended on the tree the run left behind. |
| `TAINTED` | The run rewrote `.agentrec.yaml` after it was pinned: **nothing was executed**, and the checks stay `PENDING`. |
| `(none)` | No verification was requested. This is not a check that passed. |
| `completed` / `nonzero` / `timeout` / `interrupted` | How agentrec saw the supervised process end. |
| `session_ended` / `session_lost` | The session's `SessionEnd` hook reported the end — or the recorder stopped waiting for it. |
| `running` | The session is still open and its recorder is alive. |
| `unknown` | The recorder ended without writing how the session ended. |

| Exit code | Meaning |
| --- | --- |
| `0` | The provider completed and any verification passed. |
| `1`–`125` | The provider's own exit code, passed through by `trace`. |
| `1` | Recording, rendering or verification failed. |
| `2` | agentrec was called wrongly. |
| `130` | Interrupted. |

`--timeout` bounds only the provider process: at the deadline agentrec sends
SIGTERM to the process group, waits five seconds, then SIGKILL, and files the run
as `timeout`. Ctrl-C and SIGTERM are held rather than obeyed for the whole
recording — the provider group is stopped, the repository measured, the checks
run and the report filed, and the run exits `130` instead of standing at
`PENDING`. The first signal is the last one held: a second ends the process where
it stands. `process/result.json` records an exit code when the process exited and
the terminating signal when it was killed, and never infers one from the other.

What agentrec does not claim:

- **Not syscall-complete.** Nothing observes the agent while it works; the
  record is what the provider reported, what the repository looked like either
  side of the run, and what independent checks said afterwards.
- **A repository delta is not causal attribution.** Anything else editing the
  checkout lands in the same delta, and every report says so.
- **A session's end is the provider's word.** Anything running as you can send a
  `SessionEnd`; the report says who ended the run.
- **No policy engine, no sandbox, no remote upload.** agentrec observes and
  writes locally. Windows is unbuilt and unverified; macOS and Linux are supported.

## Security

- **Structural redaction before persistence.** Provider events, stderr and
  non-event stdout are redacted before they are written. Values under field names
  whose canonicalized form ends in one of 17 secret suffixes (`TOKEN`, `SECRET`,
  `PASSWORD`, `APIKEY`, `PASSPHRASE`, `AUTHORIZATION`, `COOKIE`, …), `NAME=VALUE`
  assignments and 13 vendor token shapes (GitHub, OpenAI, AWS, Google, Stripe,
  JWT, Slack, GitLab, npm, Hugging Face, PyPI) become `[REDACTED:n]`. Matching on
  the suffix keeps `PUBLIC_KEY`, `primaryKey` and `token_id` readable. The rule
  version is stamped per manifest; bundles judged by different rules have
  redaction counts that are not comparable.
- **A zero redaction count is not a secret-absence claim.** A secret in an
  unnamed field, in prose, or shorter than the minimum length produces the same
  zero.
- **Untracked file bodies are stored** under `git/untracked/`, hashed over
  sanitized text — a hash of raw text would hand a short secret back by guessing.
- **Reports never embed the raw event stream, tracked patch or untracked body.**
  An action is reduced to a label, one allowlisted detail and fixed summary fields
  with control characters escaped, so no provider string can forge a timeline row
  or drive the terminal. Bundles are read back defensively: symlinks refused,
  sizes, line lengths and item counts bounded.
- **Repository evidence is pinned to Git's defaults.** The tracked diff runs with
  textconv, colour, prefixes, context, algorithm and indent heuristic fixed, and
  every evidence command runs with `core.fsmonitor` off, so repository attributes
  and operator configuration cannot rewrite the patch.
- **The viewer is read-only, loopback-only and loads no external assets.** It is
  not authenticated against other users of the same host.
- **Release archives are checksummed, not signed.** `SHA256SUMS` establishes
  artifact identity, not publisher identity.

## Where runs are stored

Under `$AGENTREC_HOME/runs` when that is set, otherwise
`~/.local/share/agentrec/runs`. Run directories are created `0700` and every file
in them `0600`, `report.md` included — a bundle may quote a private repository.
One directory per run holds `manifest.json`, `prompt.txt`, the sanitized event
stream and stderr, `actions.jsonl`, `process/result.json` (traced runs only),
`git/` (baseline, result, untracked bodies), `verification/results.json` and
`report.md`. `provider-stdout.unparsed.log` joins them only when the provider
printed something on stdout that was not an event. `AGENTREC_HOME` must lie
outside the repository being recorded; the interactive recorder keeps its socket
and lock under the system temporary directory.

## Documentation

- [Release notes for v0.3.0](docs/releases/v0.3.0.md) · [v0.2.0](docs/releases/v0.2.0.md) · [v0.1.0](docs/releases/v0.1.0.md)
- [Flight recorder design](docs/plans/2026-07-27-agentrec-flight-recorder.md)
- [Shadow runner design](docs/plans/2026-07-29-shadow-runner.md)
- [Dogfood evidence — recorder](docs/dogfood/2026-07-28-evidence.md): a fixed
  20-attempt checkpoint plus real mutations covering verification `FAIL`,
  provider nonzero, config `TAINTED`, interruption, and what those runs do
  **not** establish.
- [Dogfood evidence — shadow run](docs/dogfood/2026-07-29-shadow-evidence.md):
  one macOS run against Claude Code and Codex from the same commit.
- [Third-party notices](THIRD_PARTY_NOTICES.md)

## Development

```sh
go test ./... -count=1 -timeout=420s
go test -race ./... -count=1 -timeout=600s
go vet ./...
gofmt -l .
go build ./...
scripts/build-release.sh v0.3.0 "$(git rev-parse HEAD)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" dist
```

`scripts/build-release.sh` builds the release archives locally and publishes
nothing; its output directory must not already exist. `.github/workflows/release.yml`
runs the same script on a `v*.*.*` tag, checks every archive's inventory and the
version output of the binary it built, and publishes only then. It refuses to run
against a release that already exists. The public Homebrew tap validates each new
release with a real `brew install` and `brew test` before updating its formula.

## Maintaining translations

`README.md` is the factual canonical document. A localized README should be
written for its readers, not translated word for word, but it must preserve
commands, links, supported-version ranges, and every attribution or safety caveat.
Natural prose still needs native-language review. The checker below proves only
what automation can: heading structure, executable code-block payloads, and
external link destinations.

```sh
python3 scripts/check-readme-localizations.py
sh scripts/check-readme-localizations_test.sh
```

## License

agentrec is available under the [MIT License](LICENSE). Third-party attributions
and dependency licenses are preserved in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
