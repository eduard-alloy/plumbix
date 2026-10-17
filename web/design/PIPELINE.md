# Handoff: Pipelines page

Adds an 8th screen to the Plumbix UI (see `README.md` for the shell, tokens and shared components). Reference prototype: `design/Pipeline.dc.html`. **Build layout 2a, "Stages, top-down".** Frames 1a–1c in the same file are earlier explorations; ignore them except as reference for the shared toolbar and inspector.

Open the prototype in a browser with `support.js` alongside. All logic, sample data and layout math are in the class at the bottom of the file: `PIPES`, `levels`, `ordered`, `toYaml`, `diffSummary`, `layoutBand`, `cardOf`.

## Model

The pipeline is a file in the repository, `.plumbix/pipeline.yaml`. The page edits a local copy. **Save never writes to the protected branch.** It pushes a branch and opens a normal change, which Plumbix validates with the pipeline currently on the target branch, so a change cannot weaken its own checks. The new pipeline applies once that change merges.

```yaml
version: 1

steps:
  build:
    image: golang:1.26
    run:
      - go build ./...
    timeout: 10m
    evidence:
      - evidence/build/
  test:
    needs: [build]
    image: golang:1.26
    run:
      - go test ./... -count=1 -race
    timeout: 15m
    retries: 1
    env:
      LEDGER_TEST_TZ: UTC
    evidence:
      - evidence/test/log.txt
  migrate:
    needs: [build]
    image: golang:1.26
    run:
      - go run ./cmd/migrate --check
    timeout: 5m
    env:
      DATABASE_URL: { secret: ledger-ci-db }
  contract:
    needs: [build]
    image: golang:1.26
    run:
      - go run ./cmd/openapi-diff --base $PLUMBIX_BASE --head $PLUMBIX_HEAD
    timeout: 5m
    allow_failure: true
    when:
      paths: ["api/**", "internal/http/**"]

stages:
  db: [migrate, integration]

# validators run after every step, in this order
validators:
  validate:
    image: plumbix/validate:1
    run:
      - plumbix-validate --criteria .plumbix/change.json
    timeout: 2m
```

⚠ The key names (`needs`, `when.paths`, `allow_failure`, `stages`, `{ secret: name }`) and the `plumbix pipeline check` command are placeholders I made up. Map them to the real format.

Step fields: `id` (the key; lowercase, digits, hyphens), `image`, `run[]`, `needs[]`, `timeout`, `retries` (0–5), `allow_failure`, `env{}` (literal values or a secret reference), `evidence[]`, `when.paths[]`, plus `stage` (membership in `stages`). Validators have the same fields minus `needs`, `when` and `stage`. They always run last, in their listed order.

Rules the editor enforces:
- `needs` cannot create a cycle. The add-dependency menu only offers steps that are not downstream of the current step.
- Names are unique. A rename is rejected if the name is taken, and every `needs` reference is updated.
- Removing a step rewires the steps that needed it to the removed step's own `needs`.
- Toggling **Validator** on moves a step to the validator phase, dropping its `needs`, `when` and stage. Its dependents are rewired as for a removal.
- A pipeline needs at least one step to save.

## Layout (2a)

The app shell is unchanged. The sidebar gains **Pipelines** (count = repositories), between Repositories and the footer.

Rows, top to bottom, inside `<main>`:
1. **Header**, 44px: title "Pipeline"; repository segmented control (mono; a `•` marks repos with unsaved edits); mono `{file} at {branch} · {sha}`; on the right, a **Visual / YAML** segmented toggle.
2. **Toolbar**, 44px, with buttons 28px high, 12px text:
   - `+ Step`.
   - `Add after` (one step selected): the new step needs the selected step and joins its stage, so it continues the chain.
   - `Add in parallel` (one step selected): the new step copies the selected step's `needs` and stage.
   - A divider, then `Merge into one step` and `Group as stage` (two or more steps selected).
   - A divider, then a quiet `Remove`.
   - A right-aligned 12px `--text-3` hint that changes with the selection.
   - Disabled buttons sit at opacity .45.
3. **Canvas**: `--surface` background, scrolls both ways, padding 16. It holds the stage bands, with a floating inspector over them.
4. **Edit bar**: shown only when there are edits or right after saving.

### Stage bands

A stage is a named entry in `stages`. Steps without a stage are grouped into unnamed stages by start wave (the depth of their `needs` chain). Bands are ordered by their earliest start wave; when two tie, unnamed comes before named, then alphabetical. Validators are always the final band.

- **Band**: full canvas width (`width:max-content; min-width:100%`), 1px `--border`, radius 8, padding `10px 12px 12px`, background = stage tint.
- **Between bands**: an 18px gap with a 2px `--border-strong` connector at x = 22px.
- **Band header**, gap 8:
  - Number badge: 22px square, radius 5, background = stage chip colour, text = stage fg, mono 12/600. Validators use `V`.
  - Name: mono 13/600. Unnamed stages show their first two step ids joined with ` + ` (`+N` if there are more).
  - Description: 12px `--text-2`, e.g. `2 steps · unnamed · starts first` or `2 steps · starts after stage 1`. Validators: `always last · run in this order after every stage`.
  - Estimate: mono 11px `--text-3`, inline after the description (never right-aligned, so the panel can't cover it), e.g. `~2m 39s`. It is the longest chain inside the stage, summed from last-run durations.

Stage colours rotate through hues `[200, 330, 70, 150, 250, 20]` at low chroma:
- fg `light-dark(oklch(0.47 0.1 h), oklch(0.8 0.09 h))`
- band background `light-dark(oklch(0.983 0.007 h), oklch(0.19 0.012 h))`
- chip `light-dark(oklch(0.93 0.04 h), oklch(0.3 0.05 h))`
- line `light-dark(oklch(0.7 0.09 h), oklch(0.58 0.09 h))`

The validators band is neutral: `--text-2` fg, `--surface` background, `--surface-2` chip, `--border-strong` line.

### Step grid inside a band

Cards are **360×60**, with 40px between columns and 10px between rows, on a grid **at least 3 columns wide** (wider if a chain is longer). Placement:
1. Each root step that has dependents in the same stage starts a new row at column 0. Its chain extends to the right on the same row: a child goes in column = max(parent columns) + 1.
2. A fork (a second child of the same parent, or a slot already taken) goes on a new row in the same column.
3. Steps whose stage parents are elsewhere start new rows.
4. Independent steps (no dependencies within the stage) fill free cells left to right, then top to bottom, including the gaps beside chains.
5. Validators always chain in order on one row.

Lines between steps in the same stage are drawn in an SVG under the cards. On the same row the line is straight; between rows it is a cubic curve (control points ±24px). Each line ends in an r=3 dot at the child's left edge. Line colour = stage line, 1.5px. When either end is selected, the line is `--accent` at 2px.

Dependencies on steps in **other** stages are not drawn as lines. They appear as `after {id}` chips, coloured in the source stage's fg, chip and line colours.

**Card**: `--bg`, 1px `--border-strong`, radius 6, padding `0 10px`, two lines with a gap of 5:
- Line 1: mono 13/500 name (max 150px, ellipsis), then mono 11px `--text-3` image (flex, ellipsis), then on the right a succeeded icon plus mono 11px `--text-2` last duration, or `not run yet`.
- Line 2: mono 11px `--text-2` `$ first command` (flex, ellipsis, `+N` when there are more commands), then chips. Chips are mono 11px, line-height 16, padding `0 5px`, radius 3, 1px border. They appear in this order: the cross-stage `after X` chips, timeout, `retries N`, `allowed to fail`, `if {paths}`, `N evidence`, `N secret`. Show only the chips that fit whole within about 210px. The rest collapse into a `+N` chip whose `title` lists them. Never show a chip cut in half.
- Selected: border `--accent`, ring `0 0 0 2px var(--accent-bg)`. Hover: border `--text-3`.

### Selection
- Click selects one step. Clicking it again deselects.
- Shift-, Cmd- or Ctrl-click adds or removes steps from a multi-selection.
- Clicking empty canvas, the panel's ×, or Esc (outside inputs) clears the selection.
- Each card is a `<button aria-pressed>`.

### Floating inspector
- **Open only while at least one step is selected.**
- Position: absolute, inset 12px from the top, right and bottom of the canvas, width 340, z-index 2, radius 10. Scrolls internally.
- Frosted glass: background `color-mix(in oklch, var(--bg) 58%, transparent)`, `backdrop-filter: blur(18px) saturate(1.5)`, border 1px `color-mix(in oklch, var(--border-strong) 70%, transparent)`, shadow `inset 0 1px 0 color-mix(in oklch, white 40%, transparent), 0 16px 40px -16px oklch(0 0 0/.28)`.
- The stage bands keep running under the panel so their tints show through.
- While the panel is open, every band's card area gets `margin-right: 360px`, so the last column can be scrolled clear of the panel. While it is closed, the margin is 0 and bands fill the width.
- A 26px × close button sits at top 8, right 8. Header right padding is 44px to clear it.

**One step selected**:
- Header: `Step`, `Step · joins N`, `Step · forks to N` or `Validator`, with `last run 1m 36s` / `not run yet` on the right.
- Fields, top to bottom (inputs 28px, `--border-strong`, radius 5; mono where the value is code):
  - Name.
  - Image.
  - Commands: a textarea, one per line, run in order.
  - **Needs**: removable chips plus a dashed `+ add` select that only offers cycle-free choices. With none, show "none · starts right away".
  - **Run only if these paths changed**: removable glob chips plus a dashed input; Enter adds a glob.
  - Timeout and Retries (−/+ stepper, 0–5), side by side.
  - Switches (`role="switch"`, 28×16 track; `--text` when on, `--border-strong` when off): **Allowed to fail** and **Validator**, each with a one-line consequence below.
  - **Environment and secrets**: rows of name, value and a `secret` toggle that turns the value into a secret name with a `--surface` background, plus remove. A `+ variable` button below.
  - **Evidence outputs**: path rows and `+ path`.
  - `In stage {name}` with `Remove from stage`.
  - `Remove step`, with a note when other steps will be rewired.
- Validators show a fixed-position explanation in place of Needs and paths.

**Several steps selected**:
- `{N} steps selected`, then id chips.
- **Merge into one step**: one container runs their commands in order, with one log. The merged step takes the union of `needs` (minus internal ones), env and evidence, the longest timeout, and the highest retries. It is allowed to fail only if all of them were. It keeps path conditions only if all had some. It takes the first step's image, with an amber warning when images differ. Its id is the joined ids, made unique. Dependents are rewired to it.
- **Group as stage**: a name input (default `checks`), then `Group`. The steps stay separate, with separate logs, and show as one row in run evidence.

### YAML view
- Read-only, full width, with the inspector hidden.
- Mono 12/20, with a 52px line-number gutter.
- Lines added or changed against the original are marked `+` in `--s-green` on `--diff-add`.
- Footnote: "Read-only here. Edit visually, or change the file in the repo and submit it as a change."

### Edit bar (bottom of main, top border `--border-strong`)
- **Dirty state**:
  - `N edits` in bold, then a mono 12px `--text-2` summary from `diffSummary`, e.g. `added step after test · contract: needs build, migrate · removed lint`.
  - An optional red error, then `Discard` (secondary) and `Save as change` (primary).
- **Save as change** expands a 3-column form:
  - Branch: mono, default `pipeline/{slug}`.
  - Title: default `Pipeline: {first two edits}`.
  - Intent: textarea, pre-filled from the summary.
  - Auto-added criteria: AC-1 "The pipeline file parses and has no cycles" (`plumbix pipeline check`) and AC-2 "Every step image can be pulled" (`plumbix pipeline check --pull`), with the note that the change is validated with the pipeline on main.
  - `Push branch and open change`, which reads "Pushing…" while it works.
- **After a push**: the edits clear and a `role="status"` row shows a running icon and "**Change opened.** `{id}` on `{branch}` is validating with the pipeline on main. The new pipeline applies when it merges.", with [Open change] and a dismiss button. No toast, no modal.

## API needed (suggested)
- `GET /repos/{id}/pipeline` returns `{ file, ref, sha, steps[], validators[], stages{} }`, plus the last-run duration per step from the latest run on the target branch.
- `POST /repos/{id}/pipeline/changes` takes `{ branch, title, intent, pipeline }`. Plumbix writes the file, pushes the branch and submits a change. It returns the change.
- Validation errors (cycle, duplicate name, unknown need, image not found) come back per step, so they can be shown inline in the inspector.

## Keyboard and accessibility
- Esc clears the selection and closes the panel.
- All cards, toggles and segments are buttons with `aria-pressed` or `role="switch"`.
- Focus is visible everywhere (shared `:focus-visible` rule).
- Stage colour is never the only cue: every stage has a number and a name, and every cross-stage chip names the step.
