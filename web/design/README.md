# Handoff: Plumbix web interface

## Overview
Plumbix is a self-hosted, single-binary tool (listens on `127.0.0.1:6400`) that replaces the pull request for agent-written code. Agents submit a **change** (branch, target, title, intent, acceptance criteria, constraints). Plumbix runs the repo pipeline in containers, and **validators** issue a **verdict**. A human merges approved changes with one click.

The UI serves one developer who supervises several agents and checks in a few times an hour. On every change, the first screenful shows the intent, the verdict and the evidence. The diff is one click away and never above the fold.

Screens: Sign-in landing · Changes (home) · Change (core) · Step log panel · Alerts · Repositories · **Pipelines (see `PIPELINE.md`)** · plus a Design System reference.

## About the design files
The files in `design/` are **design references built in HTML**. They are working prototypes of the intended look and behaviour, not production code. Recreate them in the target stack: **React + TypeScript + plain CSS variables, served from a Go binary**. Do not use a heavy component library. `tokens.css` is production-ready and can be used as is.

To view the prototypes, open `design/Plumbix.dc.html` in a browser (keep `support.js` next to it). The prototype has three tweak props, set as defaults in the `data-props` JSON of each file:
- `theme`: system / light / dark
- `dataState`: populated / loading / empty
- `mergeResult`: success / source_moved / target_moved / push_rejected / invalid_state

`Sign-in.dc.html` has `variant`: signed_in / used / expired.

The logic class at the bottom of `Plumbix.dc.html` has all the sample data (`C`, `D`, `L`, `ALERTS`, `REPOS`) and the view-model derivations. Read it as a spec for what each screen computes from the API.

## Fidelity
**High fidelity.** Colours, type, spacing, copy and interactions are final. Match them closely.

---

## Global layout
- App shell: CSS grid `208px | minmax(0,1fr)`, `height:100vh`, `overflow:hidden`. Only `<main>` scrolls.
- **Sidebar** (`--surface`, right border `--border`):
  - Header: 44px tall. A 10×10 square (radius 2, `--text`) plus the wordmark `plumbix` in mono 13/600.
  - Nav items: 28px tall, radius 5, 13/500, label left, mono 11 `--text-3` count right.
    - Changes: needs-you count. Alerts: open count. Repositories: total.
    - Active item: bg `--surface-2`, colour `--text`. Inactive: `--text-2`. Hover: bg `--hover`. Active item gets `aria-current="page"`.
  - Footer (top border): a 6px `--s-blue` dot, "Live", and mono `127.0.0.1:6400`. Below that, the last event in mono 11 `--text-3`, truncated, e.g. `step finished · build · c_h9d3s7l2m5qe · 14s ago`. This is the only activity indicator. **No toasts.**
- **Page header:** sticky, 44px, bottom border, padding 0 20px (0 28px on Change). Title 14/600 plus a mono 12 `--text-3` count.
- Base text 13px/1.45 IBM Plex Sans. Mono (IBM Plex Mono) for ids, SHAs, branches, paths, durations, counts, logs, diffs and commands.
- Min width target 1024; primary 1280–1440. No phone layout.

## State encoding (one component: `StatePill`)
There is one badge shape everywhere: a rounded rect, radius 4, height 20, padding `0 7px 0 4px`, gap 5, 12/500. The icon is a 14px SVG on a 16 viewBox: a circle `r=5.75`, `stroke-width 1.5`, plus one glyph path (see `StatePill.dc.html` for exact paths).

Modes:
- `pill`: tinted bg, coloured border, icon plus label. Used in list rows and headers.
- `plain`: icon plus coloured label, no bg.
- `icon`: icon only, with the label in a visually hidden span for screen readers. Used in dense rows.
- `text`: coloured label only, inline before a reason.

| key | label | palette | glyph |
|---|---|---|---|
| open | Open | open (blue) | empty circle |
| running | Running | running (indigo) | half-filled circle |
| approved | Approved | approved (green) | check |
| fix_requested | Fix requested | fix-requested (yellow) | return arrow |
| rejected | Rejected | rejected (red) | × |
| needs_human | Needs human | needs-human (amber) | ! |
| escalated | Escalated | escalated (orange) | ↑ |
| merged | Merged | merged (lime, strong fill) | filled circle, knocked-out check |
| closed | Closed | closed (slate) | slash |
| run: finished / errored / cancelled | Finished / Errored / Cancelled | finished / errored / cancelled | check / ! / slash |
| step: succeeded / failed / timed_out / errored / skipped / cancelled | … | succeeded / failed / timed-out / errored / skipped / cancelled | check / × / clock / ! / dash / slash |
| queued (display only) | Queued | queued | dashed circle |
| criterion: met / unmet / unknown | … | met / unmet / unknown | check / × / ? |
| pending (display only, before verdict) | Pending | pending | dashed circle |
| severity: blocker / major / minor | … | blocker (red, strong fill) / major / minor | ! / ! / i |
| human_required | Human required | needs-human | ! |
| alert open / resolved | Open / Resolved | needs-human / closed | ! / check |
| info (notices) | Next step | minor | i |

Palette → `color: var(--status-<key>-fg)`, `background: var(--status-<key>-bg)`, `border: 1.5px solid var(--status-<key>-border)`. The keys and base colours are `status-palette.css` and `status-palette.md` in this folder (handoff of 2026-10-17); the fg, bg and border values in `src/tokens.css` are sampled from the reference renders `status-palette-light.png` and `status-palette-dark.png`, since the export's derivation formulas gave much fainter fills than the renders, which include the badge's glow. They are `light-dark()` pairs so the pill follows the system theme. The pill keeps its 20px shape and inline SVG icons; the palette's 38px badge with icon fonts and dark glow was not adopted. Colour never stands alone: there is always an icon and a label (visible or screen-reader only).

---

## Screens

### 1. Sign-in landing (`Sign-in.dc.html`)
- Top bar: 44px, wordmark left, mono host right. Content column max-width 440, centred, top padding 18vh, gap 12.
- Layout: StatePill (with a custom label), h1 20/26/600, body 14 `--text-2`, then an action, then a footnote (12 `--text-3`, top border).
- States and exact copy:
  - **signed_in**: pill approved "Signed in". Title "This browser is signed in". Body "Plumbix on 127.0.0.1:6400 will recognise this browser until you run plumbix logout or the session expires in 30 days." Primary button "Open changes", followed by "You can close the terminal prompt."
  - **used**: pill rejected "Link already used". Title "This sign-in link was already used". Body "Each link signs in one browser, once. Run plumbix login in your terminal to get a new link." Code block `$ plumbix login`.
  - **expired**: pill closed "Link expired". Title "This sign-in link expired". Body "Links expire 10 minutes after they are printed. Run plumbix login in your terminal to get a new link." Code block `$ plumbix login`.
- No form, no password. ⚠ The CLI subcommands other than `plumbix login` (`logout`, `logout --all`, `repo add`, `change submit`) and the durations (30 days, 10 minutes) are placeholders. Confirm them against the real CLI.

### 2. Changes (home)
- **Header:** "Changes" plus the filtered count. Right side: keyboard hints `↑↓ move · ↵ open · / filter` (12 `--text-3`).
- **Filter bar** (padding 8px 20px, bottom border, wraps):
  - Search input: 260×28, radius 5, `--border-strong`, placeholder "Filter by title, id or branch", with a `/` kbd hint inside on the right. Matches title, id and branch.
  - Repository segmented control: 2px padding, `--border`, radius 6. Segments 22px tall, 12/500. Selected segment `--surface-2`. Uses `aria-pressed`.
  - State `<select>` (All states plus the 9 states).
  - A quiet "Clear" button when any filter is active.
- **Column header:** 28px, 11 `--text-3`: State · Change · Repository · branch · Verdict · Iter · By · Moved.
- **Row grid:** `116px minmax(120px,1.3fr) minmax(110px,1fr) 136px 28px 48px 44px`, gap 12, **height 36**, padding 0 20, bottom border. Each row is a `<button>`, so the whole row is clickable and focusable. Hover: `--hover`.
  - Title: 13/500, ellipsis.
  - Repository and branches: mono 12 `--text-2`, as `repo · src → tgt` with the separators in `--text-3`.
  - Verdict column, mono 12 `--text-2`:
    - running: `step · 1m 12s`, ticking every second
    - open: `waiting to validate`
    - needs_human: a short reason
    - otherwise: `3/3 met · 0 blockers`. When there are blockers, show `· N blockers` in `--s-red`.
  - Iter, By and Moved: mono or 12px, `--text-3`. Moved uses `now / 4m / 2h / yesterday / 3d`.
- **Grouping:** "Needs you" comes first and holds approved, needs_human and escalated, newest activity first. Its header is 13/600 h2 plus a count plus "Ready to merge, or waiting for a person". The group header and rows sit on `--surface`. "Everything else" follows on `--bg`, newest first. Running rows sort to the top.
- **Keyboard:**
  - ↑/↓ (and j/k when not typing) move focus between rows; they also work from inside the search box. The list auto-scrolls to keep the focused row visible.
  - Enter opens the focused row.
  - `/` focuses the search from anywhere (and jumps to Changes first if needed).
  - Esc blurs inputs.
- **Loading:** 10 skeleton rows with the same grid, 10–12px bars in `--surface-2`, `aria-busy`.
- **Empty (no repos):** left-aligned at padding 64/40, max-width 520. "No repositories yet". Body: "Plumbix validates changes for repositories you register from the terminal. Register one, then point your agents at it." Then a code block `$ plumbix repo add https://github.com/you/repo` and `$ plumbix change submit --help`.
- **No match:** "No changes match these filters." plus a "Clear filters" button.
- **Live:** rows update in place from the event stream (state changed, step finished, run finished, verdict, merged). No reload, no toast.

### 3. Change (core screen)
Sticky header: a "Changes" back button, `/`, the mono id, then on the right "Updated 4 min ago" (or "Live" while running) and "esc back". Content padding `20px 28px 96px`, max-width 1240.

**Title block.** StatePill plus h1 20/26/600 (letter-spacing −0.01em). Below it a mono 12 meta line with UI-font labels in `--text-3`: `repo GitHub · src → tgt · head 9f3c1e2 · base a81d7c0 · iteration 2 of 2 · by kai · submitted 08:12`.

**Actions (right):**
- approved: secondary "Close" and primary "Merge". While merging, the primary reads "Merging…" and is disabled.
- needs_human / escalated: "Close" and primary "Validate again".
- open / running / fix_requested / rejected: "Close" only.
- merged / closed: none.

Buttons are 30px. Primary: `--text` fill, `--bg` text, hover opacity .86. Secondary: `--bg`, `--border-strong`, hover `--hover`. **No confirm modals.** Close is immediate and shows a notice with Undo.

**Inline notice** (directly under the title):
- Grid `16px 1fr auto`, padding 10/12, `--border-strong`, radius 6, `--surface`. Info icon, title 600, body `--text-2`, optional mono code block, action buttons, and × to dismiss. Uses `role="status"`.
- Merge refusals are next steps, not errors. Exact copy (branch, SHAs and target come from the data):
  - `source_moved`: "Not merged: new commits on {src}". "The verdict is for {head}. The branch now points at {newHead}. Validate again; merge becomes available if the new head is approved." Action: [Validate again].
  - `target_moved`: "Not merged: {tgt} moved". "{tgt} is now at {sha}; the verdict was for base {base}. Bring {tgt} into the branch and push. Plumbix validates the new head." Code: `git fetch origin && git merge origin/{tgt} && git push`. Action: [Copy command].
  - `push_rejected`: "Not merged: the remote changed during the merge". "Nothing was pushed and the verdict still stands. Try the merge again." Action: [Merge again].
  - `invalid_state`: "Not merged: this change is no longer approved". "A new push started another validation. Merge becomes available again if it is approved." Action: [Show iterations].
- Other notices:
  - After Validate again: "Validation queued". "Plumbix validates {src} again when a runner is free. This page updates when it starts."
  - After Close: "Closed". "The branch is left as it is. Nothing was merged." Action: [Undo].

**Two-column area:** grid `minmax(0,1fr) minmax(260px,340px)`, gap 28.
- Left column:
  - **Intent:** label 12/600 `--text-3`, then prose 14/1.55, max 74ch.
  - **Constraints:** chips, 12px, 1px `--border`, radius 4.
  - **Acceptance criteria:** label plus a mono count ("3 of 3 met"). Each row is a grid `14px 34px 1fr`, padding 10/8, bottom border.
    - Status icon, mono `AC-1`, criterion text 14/500, and on the right mono 11 `check: step test` (or "no check").
    - Second line, 12.5 `--text-2`: the status label in text mode, the one-line reason from the leading validator, the validator name in mono `--text-3`, the evidence paths as mono accent links, and a "+1 validator" toggle.
    - Expanding the toggle lists the other validators' assessments (plain pill plus reason plus validator plus evidence).
    - Evidence path `evidence/<step>/…` opens that step's log.
    - Pending criteria (no verdict yet) read "Waiting for step test". needs_human criteria read "Not judged: the pipeline stopped at build".
- Right column, the **verdict panel** (`--surface`, `--border`, radius 6):
  - Header: "Verdict" and the mono run id.
  - StatePill plus "iteration N".
  - Sentence 14/500 (copy per state, in `changeVM()` → `S`).
  - Mono counts: `3/3 met · 0 blockers · 1 finding · 1 risk`.
  - Optional sections, each with a top border:
    - **Reasons** list (rejected, needs_human, escalated).
    - **Validators** (step icon, name, duration), or **Pipeline** while running (every step with live elapsed time or "queued").
    - **Merge note** for approved: "Merge squashes 9f3c1e2 onto main (squash, set per repository)."
    - **Merge record** for merged: commit, previous, strategy, merged at, as a 2-column grid with mono values.

**Below the fold.** Sections start with an h2 13/600 plus a mono count and a bottom border, with 28px between sections.
- **Findings:** grouped as Blockers / Major / Minor (sub-label 12 `--text-3`). Row grid `14px 1fr auto`: severity icon; the message, with a second line holding the severity label, validator and an `AC-n` link; on the right the mono accent `file:line` link.
  - `file:line` opens the diff and scrolls to the line, outlined with `inset 0 0 0 1px var(--accent)`.
  - `AC-n` scrolls to the criterion and flashes `--accent-bg` for 1.6s.
- **Risks:** row grid `14px 100px 1fr auto`: icon, mono area, message plus validator. On the right, a `Human required` pill (violet) or "No human needed" in `--text-3`.
- **Iterations:** a compact timeline, newest first. Each row is a button, grid `14px 82px 104px 1fr auto 64px 40px`.
  - A 1px vertical line connects the icons.
  - Columns: "Iteration N", outcome label, headline (e.g. "AC-1 unmet: only one retry attempted"), head SHA, total duration, ago.
  - The selected row has a `--surface` bg and 600 weight. Selecting one switches Evidence to that run.
- **Evidence:** steps of the selected run. Row grid `14px minmax(90px,150px) 70px 1fr 64px 72px 32px`, height 34.
  - Columns: icon, mono name, `validator` tag (11px, bordered), status label, `exit 0`, duration (ticks while running), "Log" link in accent.
  - Queued and skipped steps are disabled.
  - Errored runs show a mono violet `error: …` line above the steps.
- **Diff:** collapsed by default. The header shows mono `4 files · +86 −12 · a81d7c0..9f3c1e2` and a [Load diff] button.
  - Loading: skeleton for about 700ms, button reads "Loading…". When open, the button reads "Hide diff".
  - File rows: 32px, `--surface`, mono path, +add in green, −del in red.
  - Hunk lines: grid `44px 44px 16px max-content`, mono 12/20, `tab-size:4`, bg `--diff-add` / `--diff-del`.
  - Findings render **inline** under their line as a small bordered note (plain severity pill, message, validator).

**States to support:** approved, fix_requested, running (live), needs_human, escalated, rejected, open, merged and closed. All are in the sample data. Use `C` for the list and `D` for the details.

### 4. Step log panel
- A fixed right panel: `width:min(860px, 100vw − 208px)`, full height, `--bg`, left border `--border-strong`, shadow `-24px 0 48px -32px oklch(0 0 0/.4)`. `role="region"`. It is not modal; Esc or × closes it.
- Header: mono 14/600 step name, StatePill, validator tag, "esc close", ×. Meta line in mono 12: change id · run id · iteration · duration · exit code.
- Toolbar:
  - Search (280px) with an `n/N` match count inside. Enter or [Next ↓] jumps to the next match.
  - Matches use `--hit`; the current match uses `--hit-strong`.
  - [Wrap] toggle (aria-pressed; bg `--surface-2` when on).
  - [Follow] toggle, only while the step is running.
- Body: mono 12/20, gutter 56px with right-aligned line numbers in `--text-3` (not selectable).
  - `white-space: pre` with horizontal scroll, or `pre-wrap` when Wrap is on.
  - Lines matching FAIL / Error / errored / failed are `--s-red`. `$ cmd` lines are `--text-2`.
- Follow: while the step runs, new lines append and the panel stays pinned to the bottom. Scrolling up pauses Follow; scrolling back to the end resumes it.
- Footer (30px): "Following · new lines appear as the step writes them" / "Paused · scroll to the end or press Follow" / "Step succeeded. Full log, fetched on open." Line count on the right.
- `/` focuses the log search while the panel is open.

### 5. Alerts
- Header: "Alerts" plus "N open". Right side: [Show resolved (N)] toggle.
- Row grid `14px 1fr auto`, padding 14/0:
  - Icon (alert in violet, or resolved).
  - Title line: kind label 600 ("Commits without a verdict" / "Token expires soon" / "Token check failed"), mono kind, mono repo, when.
  - Message.
  - For `bypass_commit`, a bordered mono table with columns `110px 70px 130px 1fr`: sha · author · time · subject.
  - A hint line in 12 `--text-2`.
  - Right: a quiet **Resolve** button (transparent, `--text-2`, hover `--hover`).
- Resolved alerts sit behind the toggle under a "Resolved" label, at opacity .8, with "Resolved {time}".
- Empty: "No open alerts. Plumbix raises one when commits reach a protected branch without a verdict, or when a git host token is about to expire."

### 6. Repositories
- Read-only table, grid `130px 110px 1fr 150px 80px 60px`, rows 38px.
  - Columns: id (mono 500), host (GitHub / Azure DevOps / git), URL (mono, ellipsis), protected branches (mono, comma list), merge strategy (mono), changes count.
- Footnote: "Read-only. Register and edit repositories from the terminal with plumbix repo."
- Empty: "No repositories yet", "In this release, repositories are registered from the terminal. They appear here as soon as you add one." plus the command.

### 7. Design system (`Design System.dc.html`)
The reference page renders light and dark side by side: tokens, state tones, type scale, the full encoding table and every component (pill modes, buttons, filter bar, change row, criterion row, finding row, step row, iteration timeline, log viewer, the four next-step notices, empty state).

---

## Interactions & behaviour summary
- **Live:**
  - Subscribe to the event stream. Patch list rows and the open change in place. Elapsed timers tick client-side from `started_at`.
  - Update the sidebar "last event" line.
  - **No toasts, no reload.**
- **Merge:** click → button reads "Merging…" → either the state becomes merged (pill, verdict panel merge record, row moves out of Needs you) or a refusal notice appears. No modal.
- **Close:** immediate, with an Undo notice. **Validate again:** state → open, "Validation queued" notice.
- **Focus:** `:focus-visible` gives a 2px `--accent` outline with −2px offset on every interactive element. Rows, nav items, segments and toggles expose `aria-pressed` / `aria-current` / `aria-expanded`.
- **Keyboard:** `/` filter (or log search) · ↑↓ / j k rows · Enter open · Esc closes the log, then goes back to the list.
- **Transitions:** none required. Keep it still; respect `prefers-reduced-motion`.

## State (suggested)
- Server data: `changes[]`, `change(id)` with `runs[]` and `result`, `alerts[]`, `repos[]`, and `stepLog(runId, step)` fetched on demand as text. The running step's log is appended from the stream.
- UI state:
  - List filters: `q`, `repo`, `state`. Mirror them in the URL.
  - Change screen: selected iteration per change, diff state per change (`closed | loading | open`), highlighted `file:line`, highlighted criterion, expanded criteria.
  - Log panel: open `{changeId, runIndex, step}`, `logQuery`, `matchIndex`, `wrap`, `follow`.
  - Merge: in-flight flag per change and the last refusal per change.
  - Resolved alerts toggle.

## Design tokens
All tokens are in **`tokens.css`** (oklch, with light and dark resolved via `light-dark()`). Highlights:
- Spacing in use: 2, 4, 6, 8, 10, 12, 14, 16, 20, 28, 32, 40, 64.
- Heights: row 36 · step/iteration rows 34–36 · page header 44 · buttons 30 (header) / 26–28 (toolbar) · inputs 28 · pill 20.
- Radii: 3 / 4 / 5 / 6.
- Shadow: the log panel only.
- Type: 20/26 600 title · 14/1.55 prose · 14/500 criterion · 13 body · 13/600 section · 12/600 label `--text-3` · 12 meta · mono 13/12/11.

## Assets
None. No images or illustrations. Icons are tiny inline SVGs (circle plus one stroke path), defined in `StatePill.dc.html` (`P` and `S` maps). Fonts: IBM Plex Sans and IBM Plex Mono (Google Fonts, OFL). Self-host them in the Go binary.

## Files
- `tokens.css`: production CSS variables and base resets.
- `design/Plumbix.dc.html`: the app prototype (all screens and states, sample data, view models).
- `design/StatePill.dc.html`: the state encoding component (icons, tones, labels, modes).
- `design/Design System.dc.html`: the token and component reference in light and dark.
- `design/Sign-in.dc.html`: sign-in landing, three variants.
- `PIPELINE.md` + `design/Pipeline.dc.html`: pipeline definition editor. Build layout **2a**; 1a–1c are earlier explorations.
- `design/support.js`: runtime needed only to open the prototypes in a browser. Not part of the build.
