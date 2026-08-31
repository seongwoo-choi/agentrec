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

## Why use agentrec

Use agentrec when an agent run must be more than a transient terminal session — when it becomes input to a code review, incident investigation, handoff, or a decision to trust a new agent or provider version.

- **Review work without trusting a summary.** `report.md` distinguishes provider-reported actions from the process outcome, the measured repository delta, and the checks that actually ran. A reviewer can inspect the claim, the change, and the verification independently.
- **Debug a failed or suspicious run after the fact.** The bundle preserves exit reason, stderr context, warnings, unparsed provider stdout, and the repository state observed across the run. This makes a timeout, parser mismatch, non-zero exit, or unexpected diff diagnosable after scrollback is gone.
- **Make handoffs reproducible.** The bundle pins the starting commit and verification configuration, then records what those checks returned. The next engineer receives durable artifacts and commands, rather than an account of what someone remembers seeing.
- **Compare agents without inventing a winner.** `shadow run` gives Claude and Codex separate worktrees and evidence bundles from one baseline. It presents the recorded facts; it does not turn action counts, diffs, or check results into an ungrounded score.
- **Upgrade providers conservatively.** Unsupported provider versions are refused by default. An explicit override leaves a visible `versionUnverified` mark in the manifest and report, so a parser-risky timeline cannot later be mistaken for fully understood evidence.

agentrec is not a live interactive agent frontend, a cloud telemetry service, or proof that an agent caused every observed file change. It is a local evidence boundary around one non-interactive run: useful precisely because it states what was observed, by whom, and what it cannot establish.

## Maintaining translations

`README.md` is the factual canonical document. A localized README should be written for its readers, not translated word for word, but it must preserve commands, links, supported-version ranges, and every attribution or safety caveat.

Natural prose still needs native-language review. `scripts/check-readme-localizations.py` checks only the contracts automation can prove: heading structure, executable code-block payloads, and external link destinations. It cannot certify that a translation preserves meaning.

```bash
python3 scripts/check-readme-localizations.py
sh scripts/check-readme-localizations_test.sh
```

## Four evidence layers

| Layer | Observer | What it means | Attribution recorded |
|---|---|---|---|
| **Provider-reported actions** | the agent | What the agent said it did — tool calls, shell commands, file reads and edits, MCP calls, Codex file changes. Normalized and summarized, never taken as proof. | `provider_reported` |
| **Supervisor-observed result** | agentrec | How the provider process ended: exit code, exit reason, signal, duration, warning count. | `supervisor_observed` |
| **Repository-observed changes** | agentrec | The difference between the commit pinned before the run and the worktree after it, measured by agentrec itself. | `observed during run, not causal proof` |
| **Verification-observed result** | agentrec | How the repository's own pinned checks ended when agentrec ran them after the provider stopped. Says nothing about how the work was done. | `verification_observed` |

Events carrying only provider progress, collaboration waits or todo-list
lifecycle are stream metadata: they name no action, and do not inflate warnings.

A stdout line that is not a provider event at all — an update banner, a
deprecation warning, anything an agent CLI prints beside its event stream — is
kept in `provider-stdout.unparsed.log`, redacted like everything else, counted
in the manifest as `unparsedLines`, and named in the report. It is not filed
among the events, and it does not fail the run: a provider that printed one line
of prose has still run, and throwing that recording away would destroy the
evidence agentrec exists to keep.

## Quick start

**Prerequisites.** Building from source requires Go 1.26 or newer. `agentrec shadow run` also requires Git 2.36 or newer for `git worktree list --porcelain -z`; `trace` does not have that Git floor. A supported
provider CLI must already be on `PATH`; agentrec launches it, never installs it.
A version outside the range is refused, not recorded on the assumption its event
stream still fits. `agentrec trace --allow-unsupported-version` overrides that
refusal: the run is recorded, the manifest is stamped `versionUnverified`, and
every report says so — the timeline was read by a parser that does not claim to
understand that version's stream, while the other three evidence layers do not
depend on the parser at all. `agentrec shadow run` has no such override, because
a comparison between one timeline that was read properly and one that was not is
not a comparison.

| Provider | Executable | Supported range | Notes |
|---|---|---|---|
| Claude Code | `claude` | `>=2.1.0, <3.0.0` | Requires `-p`/`--print`. agentrec injects `--output-format stream-json --verbose --include-hook-events`. |
| Codex | `codex` | `>=0.144.0, <1.0.0` | `exec` must be the first argument. agentrec injects `--json`. |

Each tagged release carries four archives — `darwin_amd64`, `darwin_arm64`,
`linux_amd64`, `linux_arm64` — plus a `SHA256SUMS` file covering all four. An
archive unpacks to a single directory holding `agentrec`, `LICENSE`,
`THIRD_PARTY_NOTICES.md` and `third_party/licenses/Apache-2.0.txt`.

```bash
# Homebrew
brew install seongwoo-choi/tap/agentrec
agentrec version

# From a release archive — download SHA256SUMS plus the archive for your platform.
archive=agentrec_0.2.0_darwin_arm64.tar.gz
awk -v file="$archive" '$2 == file { print }' SHA256SUMS | shasum -a 256 -c -
tar -xzf "$archive"
./agentrec_0.2.0_darwin_arm64/agentrec version

# On Linux, use `sha256sum -c -` instead of `shasum -a 256 -c -`.
# Or from source
go install github.com/seongwoo-choi/agentrec/cmd/agentrec@v0.2.0
```

`agentrec version` (equivalently `agentrec --version`) prints three lines: the
version, the commit it was built from, and the UTC build time. A release binary
carries the tag, the full commit SHA and an RFC 3339 timestamp; a build made any
other way reports `dev`, `unknown` and `unknown`, so an unstamped binary is never
mistaken for a released one.

The latest tagged release is `v0.2.0`. It includes `shadow run`, `events`, the
read-only viewer, Change Explorer, Unified Overview, and same-path-observed
correlation.

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
agentrec trace claude --timeout 30m -- -p "add a regression test for the parser"

# Codex
agentrec trace codex --verify -- exec "add a regression test for the parser"

# Record against a provider version this parser was not written for
agentrec trace claude --verify --allow-unsupported-version -- -p "..."

# Record the same task with both agents, from one commit
agentrec shadow run task.md --runner claude --runner codex

# Read runs back
agentrec list
agentrec list --cwd /Users/you/code/agentrec
agentrec list --exit-reason nonzero
agentrec list --verification-status FAIL
agentrec list --cwd /Users/you/code/agentrec --exit-reason timeout
agentrec show 20260728T093159.858622000Z-582ee874
agentrec show latest
agentrec events 20260728T093159.858622000Z-582ee874
agentrec events latest --json

# Open the local read-only Action Timeline
agentrec view latest
agentrec view 20260728T093159.858622000Z-582ee874

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

`--exit-reason` exact-matches the recorded value shown in the `EXIT` column; it
does not group different outcomes under a synthetic failure category. It can be
combined with `--cwd` in either order. No match prints `No runs.` and exits zero.

The `VERIFICATION` column shows `PASS`, `FAIL`, the uppercased recorded status,
or `UNAVAILABLE` when the run has no verification artifact.
`--verification-status` exact-matches that terminal-safe displayed value. All
three filters compose in any order; no synthetic non-passing category is added.

`agentrec events <run-id>|latest` reads the optional sanitized provider-event
JSONL artifact. Human output reports only `provider_reported` attribution, the
event count, and sorted top-level `type` counts; it does not render nested
provider payloads. `--json` emits no prose and uses the stable wrapper
`{"schemaVersion":1,"runId":...,"attribution":"provider_reported","artifactPresent":...,"events":[...]}`.
An older bundle without the artifact is reported as `artifactPresent: false`
with an empty event list. Both modes refuse symlinks and non-regular files,
enforce file, line, event-count, JSON-token, and nesting bounds, and require
exactly one JSON object per JSONL line. Human type names use collision-free
terminal-safe quoting; JSON mode preserves the validated sanitized objects as
valid JSON. Events are not converted to actions, scored, compared, or treated as
causal proof.

`agentrec view [<run-id>|latest]` opens a local read-only web UI over the same
bounded bundle readers. The run sidebar, recorded request, normalized action
timeline, sanitized provider-event stream, and separately attributed process,
repository, usage, and verification evidence remain visible together. The
overview keeps the process outcome and duration, verification verdict, validated
repository status and totals, action/event counts, and warnings in one header. The
`Changes` tab lists tracked and untracked paths and opens the bounded sanitized
patch recorded for a tracked path; it labels repository evidence as observed
during the run, not causal proof.
File actions whose explicit normalized input path exactly matches a displayed
changed path are labelled `same path observed — not causal proof`; command and
result text are never inferred as paths. The recorder resolves explicit paths
to repository-relative `repositoryPaths` while the run's filesystem namespace
still exists, so symlink aliases do not require the viewer to reopen the live
repository. Parent action IDs indent nested agent
work without claiming that provider-reported relationships are OS-observed
causality. Select an action or event to inspect its sanitized input and result;
select a change to inspect repository metadata and its bounded patch. All payloads
are rendered as text, never executable HTML.

Each viewer snapshot pins one run directory, copies immutable action/event
stream and tracked-patch bytes, and validates the indexed Changes documents. It
refuses the snapshot if any displayed artifact changes while it is being
created. The API serves action, event, and change pages with at most 250 records
and a 1 MiB target, while patch pages are bounded to 1 MiB. One valid record may
exceed the stream target, but a maximum-size bundle is never returned or
rendered all at once.

The viewer binds to a random port on `127.0.0.1` and opens the default browser.
Use `--no-open` to print the URL without launching a browser, or `--listen
127.0.0.1:<port>` to choose a stable loopback port. Non-loopback listeners are
refused; the viewer has no mutation endpoint and loads no external assets.

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
source repository's `HEAD`, under `$AGENTREC_HOME/shadow/<group>/workspaces/<runner>`
with mode `0700`, and removed once that leg's evidence has been closed. The
private `$AGENTREC_HOME/shadow/<group>/group.json` then keeps the baseline, the
recorded leg order, run IDs, and terminal outcome, but never the raw task body.
Both legs leave ordinary run bundles, so `agentrec list` and `agentrec show <run-id>`
read them back after the checkouts are gone. The comparison itself is printed to
stdout; each leg's durable `report.md` stays in its own bundle. Re-render the
same evidence-only comparison later with:

```bash
agentrec shadow show <group-id>
```

The comparison prints one block per runner, always in this order and with these
fields in this order — a run ID, how the checks ended and what they were pinned
to, how the process ended, what the run left in its checkout, and how much it
did:

```
SHADOW COMPARISON

claude
  Run ID       20260729T101500.000000000Z-1a2b3c4d
  Order        1
  Verification PASS
  Config SHA-256 e20695bb3ebee3381b54da6fc46b6b1efa1adc9b87a5eb99b45505b5dbdfae3f
  Exit Reason  completed
  Exit Code    0
  Duration     2m50.625s
  Repository   AVAILABLE  1 files (1 tracked, 0 untracked)  +18/-1, 0 binary
  Actions      12
  Warnings     0
  Unparsed     0

codex
  ...

The legs ran in the Order shown, one after another. Provider authentication,
caches, rate limits and any network service both agents use are not reset
between them, so a later leg may observe what an earlier one left.
```

`Order` is the position each leg actually ran in, which is not the order the
blocks are printed in: the runner blocks are always rendered `claude` then
`codex` so two operators read the same comparison, and that fixed order is
exactly what would otherwise hide which agent went first.

What the command does and does not give you:

- **Isolation narrows interference; it is not causal attribution.** Each leg's
  repository delta is still recorded as `observed during run, not causal proof`.
- **No score, no winner, no recommendation.** The comparison shows recorded
  fields and nothing derived from them. Which run to prefer is the reader's
  judgement. Provider-reported usage is shown independently for each leg with
  its provider and `run` or `session` scope; values are never combined or treated
  as equivalent across providers.
- **A Git checkout, not a byte-hermetic sandbox.** Untracked `.env` files and
  local credentials are not copied into a leg. Tracked files are checked out by
  the operator's Git, so configured attributes, filters and hooks still apply.
  Agentrec adds no credential transport or workspace-preparation step.
- **Repositories it cannot prepare are refused, not half-prepared.** A committed
  `.gitmodules` or a committed Git LFS pointer file is rejected before any
  checkout exists.
- **The task is one command-line argument.** The task file is read once — one
  regular, non-symlink, UTF-8 file of at most 64 KiB — and handed to each agent
  as `claude -p -- <task>` and `codex exec --json -- <task>`. A prompt on stdin,
  or spread over several arguments, is not supported here.
- **Verification is mandatory, and the legs are serialized.** Both runs are
  verified against the committed `.agentrec.yaml`, and they execute one after
  another. Their checks do not overlap, but mutable authentication, caches,
  network services and other external state are not reset between legs; input
  runner order can therefore affect what the second provider observes. The
  comparison shows each leg's `Order` and states this, so the two results are
  never read as though they were produced under identical conditions.
- **A linked worktree is not a security boundary.** It shares the repository's
  common Git directory and refs, and a provider can explicitly reach the source
  checkout. The lock coordinates agentrec processes only. After removing each
  owned worktree, agentrec compares the source `HEAD`, status, index, refs,
  worktree list and common repository config with its preflight snapshot.
  Observed drift stops the next leg and exits `1`, unless the run was also
  interrupted, in which case `130` retains precedence. Agentrec reports the
  drift and does not destructively restore it.

Exit codes: `2` for a usage or preflight refusal — a runner named twice or
missing, an unreadable task file, a dirty checkout, an uncommitted
`.agentrec.yaml`, an `AGENTREC_HOME` inside the repository — all of which happen
before any checkout or provider exists. Then `0` when both legs completed and
both verifications passed, `1` when a leg failed, ended incomplete, changed the
source repository, or its checkout could not be removed, and `130` when the run
was interrupted, including when drift was also observed. **A provider's own exit
code is evidence in its bundle and is never passed through** by the aggregate
command.

An interrupt already held or queued at the final provider-launch decision prevents
that provider from starting. One delivered after that userspace decision stops
the current leg's process group; POSIX signal delivery and process start are not
one atomic operation. Agentrec finalizes that leg's evidence, removes the
checkouts and never launches the next leg; the comparison then shows the runner
that did not run as `(not run)`. If
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
`provider-stdout.unparsed.log` joins them only when the provider printed
something on stdout that was not an event; a run that emitted nothing but events
leaves no empty file claiming otherwise.

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

`--timeout <duration>` accepts a positive Go duration such as `30s`, `5m`, or
`2h` and bounds only the provider process. At the deadline agentrec sends
SIGTERM to the provider process group, waits the fixed five-second kill grace,
then uses SIGKILL if the group is still running. It finalizes the bundle with
exit reason `timeout` and exits `1`; provider events emitted before the deadline
remain recorded. Omitting the flag preserves an unbounded provider run governed
by Ctrl-C or SIGTERM. Repository measurement, verification checks, and report
writing retain their own bounds and are not covered by this provider timeout.

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

- **Structural redaction before persistence.** Provider events, stderr and
  non-event stdout lines are redacted before they are written. Values under field
  names whose canonicalized form ends in one of 17 secret suffixes (`TOKEN`,
  `SECRET`, `PASSWORD`, `APIKEY`, `PASSPHRASE`, `AUTHORIZATION`, `COOKIE`, …),
  plus `NAME=VALUE` assignments and 13 vendor token shapes (GitHub, OpenAI, AWS,
  Google, Stripe, JWT, Slack tokens and webhooks, GitLab, npm, Hugging Face,
  PyPI), become `[REDACTED:n]`. Matching on the suffix rather than a substring is
  what keeps `PUBLIC_KEY`, `primaryKey` and `token_id` readable. The rule version
  is stamped per manifest — bundles stamped `1` and `2` were judged by different
  rules, and their redaction counts are not comparable.
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

**Supported scope: macOS and Linux.** Windows is unbuilt and unverified: the port needs process-group supervision (`internal/runner/process_unix.go`), verification process control (`internal/evidence/verification.go`), and repository locking (`internal/lock/repository.go`) rather than only one platform file.

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
scripts/build-release.sh v0.2.0 "$(git rev-parse HEAD)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" dist
```

`.github/workflows/release.yml` runs the same script on a `v*.*.*` tag, checks
every archive's inventory and the version output of the binary it built, and
publishes only then. It refuses to run against a release that already exists.

## License

agentrec is available under the [MIT License](LICENSE). Third-party attributions
and dependency licenses are preserved in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
