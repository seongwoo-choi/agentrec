# Real provider sessions and request retrieval

This is a bounded local dogfood checkpoint, not a claim about every provider version or workflow. Baseline: `25115e84f5e193a6d40204a54f081f9a45d44171`; the follow-up report fix changes presentation only. A locally built development binary recorded the sessions. No synthetic hook injection was used for these observations.

## Actual work

In an isolated Git repository, Claude built a standard-library Python Markdown inventory renderer over `agentrec list --json`. Codex added a stdin JSON CLI. Both then used the utility against actual recorded run inventories. The source and logs stayed local; existing user installations and Viewers were not replaced.

| Provider | Main recording | Requests | Resume recording | Observation |
| --- | --- | --- | --- | --- |
| Claude Code 2.1.266 / claude-opus-5 | `20260929T014151.464760000Z-f5586c11` | 2 | `20260929T014504.528534000Z-99a5112c` | Actual test/source writes, recorded end and same-session resume |
| Codex 0.150.1 / gpt-5.6-sol | `20260929T014842.458234000Z-d88f66c2` | 3 | `20260929T015527.331384000Z-195619bc` | Permission probe, CLI tests and implementation; recorded end and same-session resume |

A separate Claude permission-probe recording is not counted as a coding session. Claude's main record contains 17 actions; its resume contains 8. Codex's main record contains 27 actions; its resume contains 7. These are descriptive counts, not quality scores or cross-provider equivalence.

## Verification boundaries

- Parent-run Claude RED exited 1 because the implementation module did not exist; GREEN passed 16 tests. This was a development checkpoint, not an agentrec product regression. The provider's own piped RED command masked its shell exit; the direct parent command is the authoritative RED evidence.
- Codex's direct CLI RED had three assertion failures, followed by 19 passing tests. The tests were committed before implementation and remained unchanged.
- Those parent/provider test results are not agentrec verification. The earlier recordings retain `NOT RUN`.
- Before Codex resume, `.agentrec.yaml` was committed with `/usr/bin/python3 -m unittest discover -v`. The resumed record independently ran it after SessionEnd and recorded `PASS`, exit 0, with the pinned config hash.
- SessionEnd is provider-reported. The recorder did not supervise these provider processes and correctly reports process exit code and signal as `NOT OBSERVED`.

## Viewer readback

An isolated Chrome 154 viewer exercised the four main/resume recordings. For every request interval, selecting each displayed action produced the same ordered IDs as the persisted actions. Original browser checks also exercised request-scope reload and return to the whole timeline.

Detailed readback opened failing and passing command responses, tracked test/code patches, and the resumed independent verification result. Repository changes remained labelled as observed during the run, not causal proof. Untracked-file inspection exposed metadata and whether text was stored, not the stored body itself.

The acceptance script initially assumed untracked bodies were displayed and expected English `PASS` in a Korean UI; those assertions were corrected to the observed surface. It also needed settled navigation before selecting rows. The final detailed check navigated directly to the changes view; it does not prove arbitrary rapid navigation races are absent.

## Finding and bounded fix

Twice, a Codex command failed while its hook supplied `completed` and text output but no integer exit code: the CLI RED tests and malformed JSON rejection. The report layer incorrectly rendered `Result success`.

The fix reports `completed (exit code not reported)` for this exact missing-evidence case. It does not inspect output text to invent an exit code, rewrite stored actions, or alter failure filters. Regression tests cover terminal, Markdown and JSON fields, invalid/missing codes, valid codes, explicit failures and unchanged source data. Fresh CLI readback was checked against the actual Codex recordings; pre-existing stored `report.md` hashes were unchanged.

## Local evidence and limits

The private evidence directory is `agentrec-real-use-20260929/`, containing original run bundles, parent RED/GREEN logs, provider output, exact-ID browser JSON, screenshots and review artifacts. It is not published because provider transcripts and repository data can be sensitive.

This proves the tested sessions and readback paths only. It does not prove OS-level causality, all failure detection, every lifecycle edge, native execution on every distribution target, or that a pre-existing Viewer adopted the release.
