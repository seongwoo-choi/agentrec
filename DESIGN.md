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

No automated WCAG certification or performance score is claimed without measurement. Existing raw provider evidence remains potentially verbose; it must stay inspectable rather than being rewritten. Unrelated backend/storage/release changes are out of scope. User aesthetic approval remains a final human gate; passing tests or a model review does not substitute for it.
