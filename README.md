<p align="center">
  <a href="assets/agentrec-wordmark.svg"><img src="assets/agentrec-wordmark.svg" alt="agentrec — a flight recorder for coding agents" width="100%"></a>
</p>

<table align="center">
  <tr>
    <td width="50%" align="center">
      <a href="assets/viewer-en-light.png"><img src="assets/viewer-en-light.png" alt="The agentrec viewer: a recorded session read as a conversation with its tool calls, the summary strip and the evidence inspector"></a><br>
      <sub><b>One run, read back.</b><br>What the agent said, what the process did, what the repository shows, what the checks returned — kept apart.</sub>
    </td>
    <td width="50%" align="center">
      <a href="assets/agentrec-evidence-layers.svg"><img src="assets/agentrec-evidence-layers.svg" alt="The four evidence layers of an agentrec bundle"></a><br>
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

**agentrec** records one Claude Code or Codex run into a bundle: the actions the
provider reported, the supervised process result, the repository difference
across the run window, and the outcome of checks the repository itself pinned.
Each comes from a different observer and the bundle keeps them apart — so a code
review, an incident investigation, a handoff, or a decision to trust a new agent
version starts from what was observed rather than from a summary.

[Release notes](docs/releases/v0.15.0.md) ·
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

**Install.** Homebrew is the easiest; a checksummed archive or `go install` also works.

```sh
brew install seongwoo-choi/tap/agentrec
agentrec version
```

```sh
archive=agentrec_0.15.0_darwin_arm64.tar.gz
awk -v file="$archive" '$2 == file { print }' SHA256SUMS | shasum -a 256 -c -
tar -xzf "$archive"
./agentrec_0.15.0_darwin_arm64/agentrec version
```

```sh
go install github.com/seongwoo-choi/agentrec/cmd/agentrec@v0.15.0
```

Each release carries `darwin_amd64`, `darwin_arm64`, `linux_amd64` and
`linux_arm64` archives plus one `SHA256SUMS`. `agentrec version` prints the tag,
commit and UTC build time; any other build reports `dev`. Building from source
needs Go 1.26 or newer; `shadow run` also needs Git 2.36 or newer. If several
installations may be present, `agentrec version --verbose` names the executable
actually invoked and every `agentrec` on `PATH`.

**Pin the checks a run is verified against** by committing `.agentrec.yaml`
(copy `.agentrec.example.yaml`). Each command is launched directly, without a
shell.

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

**Record a run agentrec launches.** The working directory must be a clean Git
checkout; one traced run at a time per repository.

```sh
agentrec trace claude -- -p "add a regression test for the parser"
agentrec trace claude --verify -- -p "add a regression test for the parser"
agentrec trace claude --timeout 30m -- -p "add a regression test for the parser"
agentrec trace codex --verify -- exec "add a regression test for the parser"
agentrec trace claude --verify --allow-unsupported-version -- -p "..."
```

**Record the interactive sessions you already have.** `setup` installs the
provider hooks (user or project file, existing hooks kept, a backup written
beside the file, idempotent); every session opened afterwards is filed as a run.
Codex needs `/hooks` once, inside Codex, to trust the new hook.

```sh
agentrec setup
agentrec setup --claude --verify
agentrec setup --codex --project
agentrec hooks print --claude
```

**Read it back.** `start` keeps the viewer at `http://127.0.0.1:7788/` in the
background; `view` serves it in the foreground; `list`, `show` and `events` read
the same bundle in the terminal.

```sh
agentrec start
agentrec status
agentrec stop
agentrec view latest
agentrec list
agentrec show latest
agentrec events latest --json
```

## The Viewer

<table align="center">
  <tr>
    <td width="50%" align="center">
      <a href="assets/viewer-en-dark.png"><img src="assets/viewer-en-dark.png" alt="The agentrec viewer in dark mode"></a><br>
      <sub><b><code>agentrec view</code>.</b> Read-only, loopback-only, no external assets.</sub>
    </td>
    <td width="50%" align="center">
      <a href="assets/agentrec-evidence-layers.svg"><img src="assets/agentrec-evidence-layers.svg" alt="The four evidence layers"></a><br>
      <sub><b>The same bundle, four layers.</b> Every summary is a fold, a label or a position over the unchanged record.</sub>
    </td>
  </tr>
</table>

- **Run list** — title-first rows with provider, project, time, duration and the
  separate process and verification verdicts; search, project selector and
  collapsed advanced filters; a collapsed count of loaded runs by provider,
  verification result and project.
- **Run detail** — the request and the agent's last message side by side; a
  four-card summary (process, verification, repository, warnings); failure
  triage on failed runs; **Verify now** or, without `--allow-run`, the command
  to verify later.
- **Timeline** — **Reading view** folds routine tool actions and keeps prompts
  (`You · 3 of 12`), replies, edits, failures and unknown states visible;
  **Changes** groups files by directory; **Provider events** folds `PostToolUse`
  and hook-lifecycle records. **All actions / files / events** is one toggle
  away, and every row opens its original record in the inspector.
- **Across runs** — search every run for a word and land on the matching action
  or changed file; compare any two runs side by side; copy a local evidence link
  to the exact row.
- **Live** — a run still going keeps its page up to date and shows the working
  tree as it is now.

## Four evidence layers

| Layer | Observer | What it means | Attribution recorded |
| --- | --- | --- | --- |
| 🗣️ **Provider-reported actions** | the agent | What the agent said it did — tool calls, shell commands, file reads and edits, MCP calls, Codex file changes. Normalized and summarized, never taken as proof. | `provider_reported` |
| 👁️ **Supervisor-observed result** | agentrec | How the provider process ended: exit code, exit reason, signal, duration, warning count. `NOT OBSERVED` for a session agentrec did not launch. | `supervisor_observed` |
| 🌳 **Repository-observed changes** | agentrec | The difference between the commit pinned before the run and the worktree after it, measured by agentrec itself. | `observed during run, not causal proof` |
| ✅ **Verification-observed result** | agentrec | How the repository's own pinned checks ended when agentrec ran them after the provider stopped. Says nothing about how the work was done. | `verification_observed` |

## Two ways to record

| | 🚀 `agentrec trace` | 🎧 Interactive session |
| --- | --- | --- |
| Who starts the provider | agentrec, as the parent process | You, as always; the provider's hooks report to agentrec |
| Supervisor-observed result | exit code, signal, duration | `NOT OBSERVED`; `Ended By` says whether the `SessionEnd` hook reported the end or the recorder gave up (`session_lost`, after eight hours without a hook) |
| Baseline | pinned before the process starts | pinned when the `SessionStart` hook arrives |
| Checkout state | must be clean; one run per repository | dirty checkouts and concurrent sessions are recorded, not refused |
| Verification | `--verify` pins `.agentrec.yaml` before launch | only for a fragment printed with `--verify`, and only when `.agentrec.yaml` is tracked and identical to `HEAD` |

Codex sends no `PostToolUseFailure`, so a failed command appears as a completed
action whose response says so; its `apply_patch` edits name their files in the
patch headers. A hook the session disabled leaves a gap, not an absence.

## Commands

| Command | What it does |
| --- | --- |
| 🚀 `agentrec trace <claude\|codex> [--verify] [--allow-unsupported-version] [--timeout <d>] -- <args...>` | Records one non-interactive run agentrec launches and supervises. |
| 🧩 `agentrec setup [--claude] [--codex] [--verify] [--project] [--uninstall]` | Installs the hooks that record interactive sessions; without flags it asks. |
| ▶️ `agentrec start [--listen <loopback-address>] [--no-open] [--allow-run]` | Starts the viewer in the background; with `--allow-run`, comparisons and later verification can be launched from the page. |
| ⏹️ `agentrec stop` · ℹ️ `agentrec status` | Stops the background viewer · reports the viewer, the run count and whether the hooks are installed. |
| 🖥️ `agentrec view [<run-id>\|latest] [--listen <loopback-address>] [--no-open] [--allow-run]` | Serves the read-only viewer in the foreground. |
| 📋 `agentrec list [--cwd <path>] [--exit-reason <reason>] [--verification-status <status>] [--failures-only] [--json]` | Lists runs newest first; `--json` is schema-versioned. |
| 📄 `agentrec show <run-id>\|latest [--failures-only] [--json]` | Renders one run from its bundle. Writes nothing. |
| 🗂️ `agentrec changes <run-id>\|latest [--json]` | Lists the changed-file inventory (up to 250 files) without patch or file content. |
| 🧾 `agentrec events <run-id>\|latest [--json]` | Summarises or dumps the recorded provider events. |
| ✅ `agentrec verify <run-id>\|latest` | Runs the committed checks now, against the repository as it is today, and files the result beside the run as a later measurement. |
| 🗑️ `agentrec trash [restore <run-id> \| empty \| sweep <age>]` | Lists, restores, erases or sweeps runs deleted from the viewer. |
| 🎧 `agentrec hooks print --claude\|--codex [--verify]` | Prints the hooks fragment `setup` would install. |
| ⚖️ `agentrec shadow run <task-file> --runner claude --runner codex` · `shadow show <group-id>` | Records one task twice from one committed baseline in isolated worktrees · re-renders the comparison. |
| 🏷️ `agentrec version [--verbose]` | Prints the tag, commit and UTC build time; `--verbose` lists every `agentrec` on `PATH`. |

Every command accepts `-h`/`--help`. `agentrec hook <provider>` and
`agentrec session serve` exist too; the provider runs the first and the first
hook starts the second.

## What a report looks like

`agentrec show` renders a run from its bundle and writes nothing. Excerpt from a
real run, trimmed to one action:

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

`agentrec trace` writes the same reading to `<run>/report.md` once; a report
already standing at that name is refused rather than overwritten.

## Comparing two agents on one task

```sh
agentrec shadow run task.md --runner claude --runner codex
agentrec shadow show <group-id>
```

One task, recorded once with Claude Code and once with Codex, from a single
committed baseline, each in a disposable detached worktree under
`$AGENTREC_HOME/shadow/<group>/`. Both legs leave ordinary run bundles.

| It gives you | It does not give you |
| --- | --- |
| Two runs from the same commit, verified against the same committed `.agentrec.yaml` | A score, a winner or a recommendation — the reader judges |
| Isolation that narrows interference between the legs; source drift after a leg stops the next | Causal attribution — each delta is still `observed during run, not causal proof` |
| Exit `2` for a refusal before anything exists, `0`/`1` for the legs, `130` when interrupted | A sandbox — a linked worktree shares the common Git directory, and untracked `.env` files are not copied in |

## Evidence before claims

A status is shown as it was recorded, never inferred:

| Shown | Means |
| --- | --- |
| `AVAILABLE` | The repository was measured. Counts are shown only here. |
| `NOT RUN` | No verification was requested. Neutral, never a pass. |
| `NOT OBSERVED` | No process was supervised: a session agentrec did not launch. |
| `NOT RECORDED` | No repository measurement was made. Neutral, never a pass. |
| `PENDING` | Written before the run and never answered. Its zeros mean *not measured*. |
| `PASS` / `FAIL` / `TIMEOUT` / `ERROR` | How the pinned checks ended on the tree the run left behind. |
| `TAINTED` | The run rewrote `.agentrec.yaml` after it was pinned: **nothing was executed**. |
| `completed` / `nonzero` / `timeout` / `interrupted` | How agentrec saw the supervised process end. |
| `session_ended` / `session_lost` / `running` / `unknown` | The `SessionEnd` hook reported the end — or the recorder stopped waiting, is still waiting, or ended without writing how. |

| Exit code | Meaning |
| --- | --- |
| `0` | The provider completed and any verification passed. |
| `1`–`125` | The provider's own exit code, passed through by `trace`. |
| `1` | Recording, rendering or verification failed. |
| `2` | agentrec was called wrongly. |
| `130` | Interrupted — the provider group is stopped, the repository measured, the checks run and the report filed first. |

What agentrec does not claim:

- **Not syscall-complete.** The record is what the provider reported, what the
  repository looked like either side of the run, and what independent checks
  said afterwards.
- **A repository delta is not causal attribution.** Anything else editing the
  checkout lands in the same delta, and every report says so.
- **A session's end is the provider's word.** The report says who ended the run.
- **No policy engine, no sandbox, no remote upload.** macOS and Linux are
  supported; Windows is unbuilt and unverified.

## Security

- **The viewer trusts the machine, not the browser.** It listens on loopback
  without authentication; any local process can read every run and move one to
  the trash. Cross-origin pages cannot: deleting requires a token only the
  viewer's own page can read. Only `agentrec trash empty` erases. With
  `--allow-run`, a local process can also launch `agentrec shadow run` as you —
  leave the flag off unless you want that.
- **Structural redaction before persistence.** Values under 17 secret field
  suffixes (`TOKEN`, `SECRET`, `PASSWORD`, `APIKEY`, `COOKIE`, …), `NAME=VALUE`
  assignments and 13 vendor token shapes become `[REDACTED:n]`. A zero redaction
  count is not a secret-absence claim.
- **Reports never embed the raw event stream, tracked patch or untracked body.**
  Actions are reduced to a label and allowlisted fields with control characters
  escaped; bundles are read back defensively (symlinks refused, sizes bounded).
- **Repository evidence is pinned to Git's defaults**, so repository attributes
  and operator configuration cannot rewrite the patch.
- **Release archives are checksummed, not signed.** `SHA256SUMS` establishes
  artifact identity, not publisher identity.

## Where runs are stored

`$AGENTREC_HOME/runs`, otherwise `~/.local/share/agentrec/runs`; directories
`0700`, files `0600`. One directory per run holds `manifest.json`, `prompt.txt`,
the sanitized event stream and stderr, `actions.jsonl`, `process/result.json`
(traced runs), `git/`, `verification/results.json` and `report.md`. Deleted runs
wait in `trash/`. `AGENTREC_HOME` must lie outside the repository being recorded.

## Documentation

- [Release notes](docs/releases/) — one file per release, latest [v0.15.0](docs/releases/v0.15.0.md)
- [Flight recorder design](docs/plans/2026-07-27-agentrec-flight-recorder.md) · [Shadow runner design](docs/plans/2026-07-29-shadow-runner.md)
- [Dogfood evidence — recorder](docs/dogfood/2026-07-28-evidence.md) · [shadow run](docs/dogfood/2026-07-29-shadow-evidence.md)
- [Viewer design contract](DESIGN.md) · [Third-party notices](THIRD_PARTY_NOTICES.md)

## Development

```sh
npm ci --include=dev
npm run test:ui
go test ./... -count=1 -timeout=420s
go test -race ./... -count=1 -timeout=600s
go vet ./...
gofmt -l .
go build ./...
scripts/build-release.sh v0.15.0 "$(git rev-parse HEAD)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" dist
```

`scripts/build-release.sh` builds the archives locally and publishes nothing.
`release.yml` runs the same script on a `v*.*.*` tag, smoke-checks every archive
and publishes only then; it refuses to overwrite an existing release. The
Homebrew tap validates each release with a real `brew install` and `brew test`
before updating its formula.

## Maintaining translations

`README.md` is the canonical document. A localized README is written for its
readers, not translated word for word, but keeps every command, link,
supported-version range and safety caveat. The checker proves what automation
can: heading structure, executable code blocks and external links.

```sh
python3 scripts/check-readme-localizations.py
sh scripts/check-readme-localizations_test.sh
```

## License

agentrec is available under the [MIT License](LICENSE). Third-party attributions
and dependency licenses are preserved in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
