# Viewer Design Contract

## 1. Atmosphere & Identity

Calm, precise, readable. Primary direction: **operational**, borrowing editorial typography and spacing so recorded evidence—not dashboard chrome—leads. Audience: engineers tracing an agent's request, actions, changed files, and independent verification. Signature: the compact evidence timeline with a restrained selection rail, paired with an inspector. No decorative hero, stock illustration, marketing gradients, or third-party branding.

This is a redesign of the existing Viewer, not a new UI stack. Preserve native controls, existing semantic evidence/status distinctions, exact links, filters, pagination, and read-only defaults. The visual direction retains the current dark identity. Changes must pass source, interaction, and rendered-layout review before publication.

## 2. Color

Preserve the current light/dark semantic palette and system preference support. Dark is the primary reviewed direction: `--bg: #0f1216`, `--panel: #161a21`, `--surface: #1c212a`, `--surface-hover: #232935`, `--text: #e9edf3`, `--secondary: #c7cdd8`, `--muted: #a2aab8`, `--quiet: #8c95a6`, `--accent: #8f98ff`, `--accent-soft: rgba(143,152,255,.14)`, `--border: #2b3340`, `--border-subtle: #222935`. Semantic pass/fail/warn retain existing tokens. Accent belongs to selection, focus, and running state; routine sidebar success stays neutral. Background and content dominate; accent is sparse, not a wash. Contrast floor: WCAG AA for normal text, verified rather than inferred when claimed.

## 3. Typography

Use the existing system sans stack and mono stack; no network fonts. Sans owns human titles and prose; mono only IDs, paths, timestamps, commands and numeric evidence. Scale: 11px metadata, 12px labels/secondary facts, 13px dense navigation, 14px body, 16px section/summary values, 24px run heading (20px on small screens). Weights 400/500/600/700 only. Body/CJK line-height 1.55–1.65; heading 1.3–1.4. No artificial uppercase tracking on CJK; paths and long IDs wrap anywhere when not in a intentionally ellipsized row. Sidebar titles stay one-line ellipsis; full source remains reachable in detail. Main heading may use an already safe loaded summary title, falling back to the ID without deriving a new raw prompt projection; keep the full ID visibly available as secondary metadata.

## 4. Spacing & Layout

Base unit 4px. Scale: 4/8/12/16/20/24/32. Inline icon gaps may use 6px; 1px borders and 2px focus outlines are structural exceptions.

Desktop >=1024px: persistent sidebar approximately 288–304px, own scrollable run list; workspace padding24–32px and no document horizontal overflow. Human heading first; request is a compact native disclosure (preview/label visible, full sanitized text reachable), then one restrained summary strip instead of six equally prominent cards, then timeline and inspector. Process and verification are primary summary items; raw counts are subordinate. Target at 1440x1000: the timeline begins within the top half of the viewport and displays several meaningful action rows without hiding evidence controls.

Tablet/mobile <1024px: page owns vertical scrolling; list retains a visible nonzero height (at least144px with loaded rows), independently scrollable within a bounded region. Remove the existing 280px whole-sidebar cap that squeezes the list to zero. Do not let open advanced filters make runs unreachable. At <=720px the topbar reflows into two rows with a full-width search; brand, language, and compare remain visible. At375px: no horizontal page scroll, clipped controls, zero-height list or CJK glyph clipping. Timeline/inspector stack, metrics wrap intentionally, and touch controls should have at least36px height (44px where space permits).

## 5. Components

- Run row: title first, metadata second, independent process/verification third; hover surface, accent selection rail, visible keyboard focus. Preserve warning/failure emphasis.
- Sidebar: heading, search, project choice, advanced disclosure, visible list. Quiet unboxed counts; move verbose scope help into an accessible disclosure if needed, but keep loaded-only scope discoverable and aria descriptions valid.
- Request: native keyboard-accessible disclosure; preview capped at160 Unicode code points plus an ellipsis only when truncated, derived solely from already-sanitized detail text; explicit expand/collapse affordance on the same line; never remove the full recorded text. Reset expansion on run change if needed and cover it with a test.
- Summary strip: one surface with subtle internal dividers, not six equal isolated boxes. Desktop9-track layout gives process, verification and repository evidence2 tracks each, raw counts1 each; tablet facts use two equal columns, with process/verification full-row on mobile. All facts and verification controls retained; primary process/verification, quieter counts.
- Timeline/inspector: stable toolbar and rows, clear tab selection/focus, muted empty inspector without an oversized dashed placeholder. Selection still reveals exact sanitized evidence.
- Buttons/inputs: existing native controls, 8px radius, border/surface states; no new generic component framework.
- Every changed control keeps default/hover/focus-visible/active/disabled semantics. Loading/empty/error are designed states, not blank content; errors remain visible and live announcements remain intact.

## 6. Motion & Interaction

120ms color/border transitions only; no layout animation or attention-seeking motion. Existing live pulse communicates running state. Preserve reduced-motion behavior. No new keyboard shortcut that steals typing. Native disclosure and tab semantics; focus not lost on polling. No per-row detail requests or new endpoint.

## 7. Depth & Surface

Flat layered dark surfaces, 1px subtle boundaries, 8px controls/rows and 10–12px major panels. No dark shadows or blur. Light theme may keep its existing subtle shadow. Spacing and text hierarchy, not extra boxes, separate content.

## 8. Accessibility Constraints & Accepted Debt

Preserve focus order, status/live regions, tab/tabpanel ARIA, search semantics, and full evidence access. EN/KO/JA/ZH labels must remain coherent. Test1440/768/375 with realistic long titles and CJK, loaded list, selected action, advanced filters expanded, request expanded/collapsed, empty/loading/error states and compare overlay. Assert list height as well as width: zero-height content is not a responsive pass.

## 9. Reading-first Action Timeline

The default action timeline is **Reading view**, with **All actions** as the explicit exhaustive alternative. This is deterministic presentation of recorded evidence, not an LLM summary, inferred plan, causality claim, or new canonical record. Keep chronology; do not invent analysis/build/completion stages.

- Keep user prompts, agent messages, file mutations, verification-like actions, failures, warnings, running/pending/unknown states, and unsupported action types individually visible. Preserve existing observed changes, independent verification, and failure-triage surfaces above/beside the timeline rather than inventing summaries.
- Only consecutive eligible completed/success tool actions (known shell/tool/MCP/read/search categories) with matching parent scope may form a collapsed native disclosure, and only when at least two qualify. Any boundary item breaks the group. Explicit structured error/nonzero exit evidence prevents folding even if the provider status says completed; do not parse arbitrary stdout as authoritative failure evidence. Unknown fields/statuses are not proof of success.
- Verification-like means a bounded known runner invocation at an explicit command boundary, not test/check/build words in filenames or search arguments. This is a conservative signature list, not shell evaluation or a general parser.
- A group labels only localized readable tool kinds/counts and loaded-scope facts. Use neutral language: completed provider records are not verification success. Every member retains its original action object, ID, stream index and byte-page cursor. No recording deletion, retention change, backend endpoint or canonical schema change.
- Text search and active action-type filters must expose matching actions directly, not hide hits inside collapsed groups. Exact action links/search navigation reveal the target group automatically and preserve selection, inspector, URL and copy-link cursor behavior. All-actions mode restores chronological individual rows.
- Expanding a group is keyboard accessible. Do not collapse an expanded group or lose focused/selected evidence during append, live refresh, locale switch or mode changes. A fresh run starts with bounded state; do not carry group expansion across unrelated runs. Do not merge across missing/error/loading page boundaries or imply unloaded records were summarized.
- Keep the selector and groups legible at1440/768/375 in EN/KO/JA/ZH. Preserve the current palette/type/spacing tokens; avoid a second dense dashboard. Show loaded action count separately from top-level entries (individual actions plus groups); expanded children do not increase this count. Group identity uses the first exact ID, parent and byte-page anchor, not the changing last ID/count.
- Acceptance: grouping/failure/boundary/append/filter/polling/deep-link/back-forward/keyboard regressions pass, actual saved runs show fewer top-level rows while every action remains reachable, and current rendered screenshots are reviewed. Visible-row reduction is supporting evidence, not a score to optimize by hiding important actions.
- If a browser cannot format an unusually deep payload, show an explicit localized display-limit warning, retain the original recorded evidence, and keep selection and subsequent records usable. Do not silently omit the payload or disguise unrelated errors as formatting limits.

## 10. Readable Changes and Provider Events

These tabs have different jobs. Changes is a file-oriented view of observed repository evidence; Provider events is a diagnostic view of sanitized provider records, not independent execution proof. Publication requires passing source, behavior, rendered-layout, and CI gates.

- **Changes:** default Folder view with an explicit All files alternative. Group loaded records by their exact immediate directory: split the canonical path at its last literal `/` without normalization, and place paths with no `/` in an explicit repository-root group. Keep full canonical paths/indexes/cursors and original objects; basename display must retain full-path accessible context. Do not normalize identifiers, reorder canonical arrays, infer file deletion/rename from line counts, or hide generated/vendor/doc files. Preserve tracked/untracked/binary facts and measured per-file counts, plus observed-not-causal attribution. Folder counts cover loaded records only, never unseen totals.
- Filters expose matching files directly. Exact file links, search, history and selection open the containing folder and keep the same changeCursor and patch behavior. Live working-tree evidence keeps its separate attribution and unsupported-copy/patch boundaries; grouping must not turn live observations into stored evidence. Keep selection/focus on append/live/locale changes and clear disappearing live selections as before.
- Basenames belong only inside visible directory groups. All-files and filtered flat lists show full paths so identical basenames remain distinguishable without hovering. Closed event groups lead with a neutral record count and distinct tool-name count; long technical tool identifiers stay in the expanded records/inspector rather than dominating the summary.
- **Provider events:** default Event summary with an explicit All events alternative. Preserve chronology and lifecycle/request/response boundaries. Only consecutive, structurally recognized `PostToolUse` records with a non-empty tool name may fold, and only within the same loaded page and exact session token; session/page boundaries, explicit errors/failures, dropped/incomplete artifacts, uncertain shapes and unsupported types stay separate. Use bounded existing structured inspection; no stdout interpretation or new success inference. `PostToolUse` is not proof of tool success; `Stop` is a response/turn stop record, not task completion.
- Summary labels are human-readable and localized; raw event kinds and original sanitized payloads/indexes remain inspectable. Support established hook/provider shapes conservatively. Do not add invented phases, LLM summaries, canonical event IDs or provider-event permalinks that the current API cannot support.
- Keep view choices independent across Actions/Changes/Events. Existing Actions reading behavior and original path/byte-cursor navigation must remain intact. Use native disclosures and current tokens, not a new UI framework.
- Grouped Changes/Events must retain manual Load more and must not auto-fetch an entire stream just because collapsed summaries leave a sentinel visible. Keep raw exhaustive-view paging behavior; aggregates remain explicitly loaded-scoped. Preserve error/retry paths.
- Verify folder/full view, event summary/full view, original record recovery, unknown/error/drop visibility, selected group expansion, page boundaries, live changes, exact file-link round trips, filters, keyboard/focus and EN/KO/JA/ZH at1440/768/375. Capture actual saved runs plus clearly labeled synthetic edge cases. Row reduction supports readability but is not a score that justifies concealing evidence.

No automated WCAG certification or performance score is claimed without measurement. Existing raw provider evidence remains potentially verbose; it must stay inspectable rather than being rewritten. Unrelated backend/storage/release changes are out of scope. Visual changes require current rendered evidence; passing source tests alone is not visual verification.

## 11. Loaded-run Overview

The run list answers "which run", never "what is in the store". Once dozens of runs accumulate, the only way to see their shape is to open them one at a time. Give the sidebar a deterministic count of the runs the page has already loaded, grouped by the facts the list already carries. This is arithmetic over loaded summary fields, not a new API, a stored aggregate, a trend, a score, or a quality judgement.

- The viewer auto-selects a run on load, so an empty workspace is not a reachable resting state; the overview lives in the sidebar beside the run list it describes. Keep it a collapsed native disclosure so it costs one row until asked for, and keep the run view untouched.
- Count only runs currently loaded in the page. Never project unloaded runs, and never present a loaded count as a store total. When the store holds more than the page has loaded, say so explicitly and keep the existing Load more control as the only way to widen the scope.
- Group by the recorded facts the run list already carries: provider, verification result, and project. Preserve each recorded value verbatim, including `PENDING`, `NOT RUN`, `TAINTED` and any unrecognized value; do not merge them into a residual bucket, re-rank them by desirability, or translate recorded status values.
- A verification value is verification-observed evidence for that run; a provider exit is a process record. Neither is independent proof that the task succeeded. Label groups neutrally and keep existing attribution wording; do not introduce success rates, health scores, pass percentages or trend claims.
- Selecting a group applies the matching existing run-list filter rather than navigating or creating a new view, so the overview and the list can never disagree. The filter, its URL parameter, the advanced-filter applied count and the loaded-scope note keep their current meaning.
- A facet with no recorded values is hidden rather than shown empty, and an empty store hides the overview entirely instead of displaying zeroes. Provider has no run-list filter of its own, so it counts without pretending to be actionable.
- Keep it keyboard reachable and legible at 1440/768/375 in EN/KO/JA/ZH with the current tokens; it is a short summary, not a second dashboard.
- Acceptance: counts match the loaded summaries for real saved runs, partial-load wording appears whenever more runs remain, group selection drives the existing filters, and rendered screenshots are reviewed.

## 12. Discoverable later verification

A viewer started without `--allow-run` silently omits the **Verify now** control, so a reader never learns that a run can be verified again from the page. The comparison panel already says how to enable running; the Verification block must say the same for the same reason. Measured cause: 34 recorded runs and zero later verifications.

- When the viewer may not run things and the run is not live, the Verification block shows one short, neutral sentence naming the exact command (`agentrec start --allow-run`) and, as the equivalent that needs no restart, `agentrec verify <run-id>`. It is guidance about the viewer's own permission, not a claim about the run.
- The sentence appears only where **Verify now** would have appeared: never for live runs, never when running is already allowed, and never inside the run's own recorded verdict or the later-verification section. It does not change verdicts, attribution wording, or the existing "not verified when it ended" caveat.
- Existing `--allow-run` behavior, the verify endpoint, its 403/409/422 handling, and the CLI are unchanged. No new endpoint or setting.
- Localized in EN/KO/JA/ZH; the commands stay verbatim in code spans.
- Acceptance: with `allowRun=false` the hint renders and **Verify now** does not; with `allowRun=true` the button renders and the hint does not; live runs show neither; a real viewer started without the flag shows the hint on a stored run.

## 13. Duration on the run row

Every stored run already carries a measured duration (35 of 35 in the local store), yet the run row shows only when it started; how long it ran is a click away. Duration is the fact a reader uses to tell a two-second probe from a two-hour session, and it belongs beside the relative time.

- `/api/runs` summaries gain `durationMillis`, computed exactly as the detail page and `agentrec show` already do: the recorded process result first, else `endedAt − startedAt` from the manifest. Omitted when neither exists. No new file is read; the summary reader already opens both documents. The CLI `agentrec list` schema is unchanged.
- The row shows the duration after the relative time as a compact, localized `2s` / `14m` / `1h` / `1h 12m` token; the title carries the recorded value to the millisecond in Go's duration spelling. The detail page keeps its finer-grained string; the two agree to the millisecond. Live and duration-less runs show nothing rather than `0s` or a dash.
- Duration is a measurement of the recorded process window, not effort, cost or quality; do not colour it, rank by it, or total it anywhere. It is not a filter.
- Acceptance: summary JSON carries the same value the detail `Duration` field shows for real runs; the row token matches; live runs and a manifest without `endedAt` show nothing; EN/KO/JA/ZH at 375px keep the row on its current lines.

## 14. Skip links

Measured from a fresh load: the first run row is the ninth Tab stop, behind global search, compare, language, list search, project select and three collapsed disclosures. Every keyboard reader pays that on every visit. The fix is the standard one: two skip links, visible only while focused, as the first stops on the page.

- The first Tab stop is **Skip to run list**, the second **Skip to run evidence**. Both are ordinary anchors so the browser handles them; the targets receive programmatic focus so the next Tab continues from there rather than from the top.
- The links are visually hidden until focused, then appear at the top-left in the current tokens. They never take space from the header and never appear on pointer use.
- Nothing else about tab order, disclosures, focus restoration or the existing sidebar controls changes; the disclosures stay reachable in place.
- Localized in EN/KO/JA/ZH.
- Acceptance: from a fresh load, Tab then Enter lands focus inside the run list and the next Tab reaches the first run row; the second link reaches the run view; the links are not visible before focus; all existing keyboard tests pass unchanged.

## 15. The agent's last message beside the request

The request card shows what was asked; nothing shows what the agent said last. In the local store 17 of 35 runs record `agent.message` actions and in 16 of them the last one is the final action — the agent's closing report — yet a reader must scroll to the end of the timeline to find it. Put it beside the request, with the same disclosure shape.

- The run detail carries `lastAgentMessage`: the `text` of the last recorded `agent.message` action, the action's `id`, and its 1-based position among recorded actions. It is found in the pass that already counts actions, so no new file is read. Absent when the run has no such action or the text is empty; a message that is not the final action still qualifies, and its position says so.
- Bounded: the server truncates the text at 64 KiB of UTF-8 and marks `truncated: true`; the card previews the first 160 characters like the request and shows the full stored text when opened.
- It is the provider's own last message, verbatim, with the run's existing redaction already applied to the action stream. It is not a summary, not a verdict, and not proof the work happened; the card's label says whose words they are. Do not read anything from it into status.
- The card links to the action so the reader can inspect the original record and what came after it; the position is shown so a message followed by more actions is not mistaken for a closing report.
- Live runs do not show it; the timeline is the live surface.
- Acceptance: for the real store, the card text equals the last `agent.message` `input.text` in the action stream and its position matches; runs without one show no card; the link opens the same action the timeline shows; EN/KO/JA/ZH; source tests and rendered captures reviewed.

## 16. Folding hook lifecycle records in the event summary

Measured on the local store: across 17 Claude runs, 5,656 of 6,439 provider events (87%) are `system` records with subtype `hook_started`, `hook_response`, `hook_progress` or `thinking_tokens` — the lifecycle of agentrec's own hooks and token-count ticks. In 13 of the 17 runs they are at least half of the stream; the largest run shows 1,691 events of which 1,523 are these. The Event summary of section 10 folds only `PostToolUse`, so on such a run it renders 224 top-level rows, 199 of them this noise, and the reader scrolls past hook bookkeeping to find the conversation.

- The Event summary folds a second family under the same discipline as section 10: consecutive records whose `type` is exactly `system` and whose `subtype` is one of `hook_started`, `hook_response`, `hook_progress`, `thinking_tokens`, within the same loaded page and exact session token. Any other `system` subtype (`init`, `task_started`, `task_notification`, unknown), any record with an explicit error/failure field, and any dropped or incomplete stub stays a separate row, as does everything else.
- A closed group leads with a neutral count and the subtypes it holds (`412 hook lifecycle records · hook_started 175 · hook_response 175 · …`) in chronological order of first appearance; the `hook_name` values stay in the expanded records and the inspector.
- Folding never crosses a `PostToolUse` group, a lone row, a page boundary or a session change; chronology is preserved exactly. Original objects, indexes and byte cursors remain the same; All events is unchanged.
- A group is not an interpretation: it does not say the hooks succeeded, ran in order, or belong to a given tool call.
- Acceptance: on the real run `20260728T114417.388867000Z-45057bb7` the summary's top-level entry count falls from 224 with the hook rows folded into groups; a `system` `init` record between two runs of hook records keeps them apart; a hook record on the next loaded page starts a new group; the full-view count and every existing event test pass unchanged; the summary line is localized in EN/KO/JA/ZH.

## 17. Codex patch rows name their files

Measured on the local store: every one of the 65 codex `file.edit` actions carries its edit as an `apply_patch` document in `input.command`, and the row detail shows its first 180 characters — `*** Begin Patch *** Update File: /Users/…` — so the timeline reads `*** Begin Patch` sixty-five times and the file is visible only when the absolute path fits in what remains. Six of those patches touch more than one file. Claude's edits carry `file_path` and already read as a path.

- When an action's detail would come from a `command` string that begins with `*** Begin Patch`, the row summary instead lists the file headers found in it — the verbs and paths verbatim (`Update File: internal/cli/app.js`), in document order, joined with ` · ` — so one patch that touches four files names all four. The path is shown as recorded; no normalization, no basename-only display.
- When the document carries no recognizable `*** Add|Update|Delete File:` header the row keeps the current first-180-characters detail. Nothing is parsed beyond those header lines; hunks stay in the inspector.
- Search still matches the whole command text, as before. The inspector, the Changes tab, canonical stored records and the CLI are untouched.
- This is a reading aid, not a claim that the patch applied: status, exit and the Changes tab remain the evidence.
- Acceptance: a fixture patch with two `Update File` headers and one `Add File` header renders those three headers in order as the row summary; a `command` that does not begin with `*** Begin Patch` is unchanged; on the real store, every codex `file.edit` row names at least one file.

## 18. A compact summary strip

Measured at 1440×900 on the local store: the summary grid above the timeline is laid out in nine columns of which three are always empty, and two of its six cards — Normalized actions and Provider events — repeat the counts already printed in the tab labels directly below (`Actions 42 · Changes 6 · Provider events 44`). At 375 px the grid is 346 px tall and the first timeline row lands at y ≈ 2052, two and a half screens down.

- The summary keeps four cards: Process outcome, Verification verdict, Repository evidence, Warnings. The two count cards are removed; their numbers remain in the tab labels, which stay the canonical place for them.
- The strip lays the cards out in a four-column grid on desktop and two columns at 375 px, with the same border, tone classes and text. No card, label, tone or detail sentence changes; the failure triage, evidence sections and tests that read them are untouched.
- Nothing is folded or hidden: this is a reduction of duplication and empty space, not a disclosure.
- Measured effect, stated plainly: at 375 px the strip shrinks from 346 px to 286 px (two columns; the two dropped cards were one full row) and the first timeline row rises by those 60 px. At 1440 the strip was already a single row, so its height (103–137 px) and the first-row position do not change; the gain there is the three permanently empty columns and two duplicate numbers, not vertical space. The remaining desktop stack above the first row — top bar 52, header 111, failure triage 131 on failed runs, request 44, reply 44, strip 103, toolbar 51, filter row 69 — is a separate question for a later cycle.
- Acceptance: the strip renders exactly the four cards with their current labels; the tab labels still carry the action, change and event counts; `Warnings` keeps its `warn` tone when the count is positive; the desktop grid has four columns with no empty tracks; at 375 the strip is two columns and 60 px shorter on the measured run.

## 19. The timeline panel fits the screen it is on

Measured on the local store at 1440×720, 1440×800, 1440×900 and 1920×1080: the timeline and inspector panels are sized by `clamp(520px, 100vh − 340px, 900px)`, which assumes 340 px of page above them. The measured stack above the panel is 565–583 px (top bar, header, failure triage on failed runs, request, reply, summary strip), so the panel's bottom edge lands 225–383 px below the viewport on every desktop size, and the reader scrolls the page to reach the panel's own scrollbar — two scrollbars for one list, and the bottom of the timeline is never visible together with its top.

- On desktop layouts (≥ 1024 px) the workspace becomes a column flex container and the run view and its content grid flex to fill it, so the panels take exactly the height left below whatever sits above them, with a 320 px floor. This is layout, not measurement: no JavaScript reads offsets, no custom property, nothing above the panel moves, folds or resizes. The page still scrolls when the stack above the panel plus the floor exceeds the viewport (a short window or a tall failure triage), and the timeline keeps its own scrollbar for its rows.
- On the narrow layouts (≤ 1023 px) the panels already stack vertically and this rule does not apply; the existing heights there stay.
- Measured with the rule injected into the live stylesheet: at 1920×1080 the page scroll range drops from 273 px to 0 and the panel ends inside the viewport; at 1440×900 the panel sits on its 320 px floor with 3 px of page scroll instead of 291; at 1440×720 the floor leaves 183 px of page scroll instead of 431. The floor is the honest limit: a 900 px-tall window with 583 px of context above the panel has 320 px for it.
- Acceptance: at 1920×1080 the page scroll range on a normal run is zero and the panel bottom is inside the viewport; at 1440×720 the panel is exactly its 320 px floor; at 768 and 375 px nothing changes; every existing keyboard, scroll-restoration and deep-link test passes unchanged.

## 20. The run heading preserves the loaded title

Measured on the local store after the first-sentence display cut shipped: 23 of 31 titled runs lose trailing title text. Several cuts retain only a qualifier (`Test-maintenance task only.`, `Read-only verification task.`, `Long-running interrupt dogfood.`) while removing the action that distinguishes the run. The full title is already safe, redacted and bounded to 120 runes; saving one 32 px heading line is not worth hiding its meaning.

- The heading shows the full loaded title exactly as the run list does. It does not infer sentence boundaries, summarize, or re-read `prompt.txt`.
- The request disclosure remains the source for the full recorded prompt. This rule changes only the bounded title projection.
- Acceptance: titles with multiple sentences, quoted punctuation, CJK punctuation, filenames and no sentence boundary all render unchanged in both the heading and sidebar; the heading has no redundant tooltip copy.

## 21. The evidence-link caption moves under the button

Measured on the local store: after selecting any action, the first payload in the inspector begins 250 px below the panel's top. Above it sit the title (20 px), the Copy evidence link button (31 px), a two-line caption explaining that the link is local (37 px), and the identity pills. The caption is the same sentence on every selection and is read once.

- The caption becomes the button's `title` (tooltip) and its `aria-description`, in the same four locales; the visible paragraph is removed. The status line (`Copied` / clipboard-denied guidance) and the fallback URL input stay exactly where they are, since they carry per-action outcomes.
- The wording does not change: `Local link: requires the same Viewer and recorded data. Not a public share.`
- Acceptance: the button carries the caption as `title` and `aria-description` in EN/KO/JA/ZH; no `.evidence-link-caption` element renders; the first payload rises by the caption's height on the measured run; copy success and denied paths are unchanged.

## 22. Notification-shaped prompts do not assert an operator

Measured across the local store's 35 runs: only one run has prompts whose text begins `<task-notification>`; four of its twelve `user.prompt` actions have that shape. Their sanitized provider events contain the same prompt, a `UserPromptSubmit` hook name and prompt ID, but no independently recorded origin. Text shape alone cannot prove whether Claude Code generated the prompt or an operator submitted matching text. Labelling those four rows **Task notification** overstates the evidence; labelling them **You** also asserts an unrecorded origin.

- A `user.prompt` action whose prompt text, after leading whitespace, begins with `<task-notification>` is labelled **Notification-shaped prompt** instead of **Task notification** or **You**, in the same conversation-row shape, with the existing neutral tone (no accent bar, muted speaker). The text is shown verbatim as it is today; nothing is parsed out of it, nothing is summarized, and the action type, id, status, search, inspector and stored record are unchanged.
- The header's request card, Loaded-run overview and title derivation are unaffected.
- This is a cautious display label for one recognized text shape, not provenance or a classifier: any other prompt, including one that merely mentions the words, is still **You**.
- Localized in EN/KO/JA/ZH.
- Acceptance: on the measured session the four matching rows read **Notification-shaped prompt** and the other eight prompts still read **You**; a prompt that contains `<task-notification>` after other text is still **You**; the inspector for a matching row shows the unchanged `user.prompt` record.

## 23. Prompt rows say which turn they are

Measured on the local store's multi-turn session (`20260909T090038`): twelve `user.prompt` rows across 108 actions, all in the same shape, with no way to tell which request a row is or how many follow without counting while scrolling. The header's request card shows the first prompt, the last-message card the last reply; the ten turns between have no landmark.

- The run detail carries `promptCount`: the number of `user.prompt` actions in the record, counted in the pass that already counts actions and finds the last agent message. No new file is read.
- Each `user.prompt` row's speaker line gains an ordinal — `You · 3 of 12` — where the ordinal is the row's rank among all `user.prompt` actions in the record and the total is `promptCount`. The snapshot scan projects that rank onto each prompt, so a page opened directly from a nonzero byte cursor retains its record-wide position. Notification-shaped prompts (section 22) count too because the provider recorded them as prompts; their label reads `Notification-shaped prompt · 7 of 12`. A run with a single prompt shows no ordinal: `1 of 1` says nothing.
- The rank belongs to the record position, not the action ID: duplicate IDs cannot collapse two prompt positions. The reading view's folded groups never contain prompts, so folding does not change ranks.
- This is a position, not a structure: the Viewer does not infer which reply answers which prompt, does not group actions into turns, and does not change the request or last-message cards.
- Localized in EN/KO/JA/ZH.
- Acceptance: on the measured session the twelve prompt rows read `1 of 12` … `12 of 12` in order after every page is loaded; a direct link to a later page keeps the same record-wide ranks; duplicate IDs do not merge positions; a single-prompt run shows no ordinal; the Go detail for the fixture reports `promptCount` equal to the number of `user.prompt` records.

## 24. Relative run times disclose the recorded instant

On the local store's ten initially visible runs, two groups of three rows share the same day-level relative label (`2일 전` and `14일 전`). The exact start is available only after opening a run, so the compact list cannot distinguish those timestamps on hover or through an accessible name.

- Keep the visible relative token and row density unchanged.
- Render that token as a semantic `time` element whose `datetime` is the normalized recorded instant. Its tooltip is the same locale-aware exact timestamp used in run detail, and its accessible label prefixes that timestamp with the localized **Started** label.
- Do not infer precision beyond `startedAt`, alter sorting, or change the canonical record.
- Acceptance: each valid start keeps its relative text, exposes the ISO instant in `datetime`, and exposes the localized exact timestamp in both `title` and its accessible label; an absent zero-time sentinel stays **unknown** without exact-time semantics; no visible text is added and the measured viewports gain no overflow.

## 25. A mobile run selection reveals the selected evidence

Measured on the real local store at 375×812: the run list occupies y=349–609 and the selected run begins at y=658. Activating two different rows leaves document scroll at y=0 both times; for the two measured long titles the heading ends at y=806 and the first evidence below it remains off-screen. The selection rail changes, but the reader must manually scroll past the list after every selection to reach what they selected.

- Only an explicit run-row activation on a narrow layout (<1024 px) moves to the freshly loaded `#run-view`; initial auto-selection, URL/deep-link restoration, history navigation, polling and desktop selection keep their current scroll behavior.
- Move only after that exact run has loaded. A failed, superseded or aborted navigation must not scroll to stale evidence.
- Focus the run view together with the scroll so keyboard and assistive-technology users do not remain focused on an off-screen row replaced during rendering. The existing selected row, URL, list filters and evidence content remain unchanged.
- Use the existing responsive breakpoint and native `scrollIntoView`; add no animation, sticky control or second mobile layout.
- Acceptance: two consecutive row activations at 375×812 each place the selected run view at the top and focus it; at 1024 px the same activation does not request scrolling; the initial load and a failed selection do not request scrolling; browser captures at 375/768/1440 in EN/KO/JA/ZH show no clipping or horizontal overflow.
