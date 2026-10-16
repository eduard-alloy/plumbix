# Plumbix web interface: design brief

This is the prompt handed to Claude Design for WU-011. It is kept here so the
design and the code it leads to stay traceable. Everything below the line is
the prompt.

---

Design the web interface for **Plumbix**, an open-source, self-hosted tool that replaces the pull request for code written by coding agents. Produce a compact design system and the screens listed under "What to deliver", in light and dark, as a cohesive, build-ready design.

## What Plumbix is

A developer runs one binary on their own machine. It listens on `127.0.0.1:6400`. Their code stays on GitHub, Azure DevOps or a plain git remote. Agents (or the developer) push branches and submit a **change**: the branch, the target branch, a title, the **intent** in prose, and a list of **acceptance criteria**. Plumbix fetches the branch, runs the repository's pipeline in containers, and **validators** judge the change against its intent and criteria using the evidence the pipeline produced. The result is a **verdict**: approved, fix requested, or rejected. A human merges an approved change with one click. Nothing merges without a verdict.

The interface replaces "read the diff line by line" with "read the intent, the verdict and the evidence; open the diff only if you need it". Its user is one developer supervising several agents at once. They open it a few times an hour, decide quickly, and leave.

## Design direction: a quiet instrument

Calm, dense, near-monochrome. Colour is spent almost only on verdicts and states. It should feel like a precision tool for one person, not a team dashboard or a marketing surface. Reference points for tone, not for copying: Linear, Vercel's dashboard, Raycast.

Principles, in order:
1. **Intent and verdict first.** On any change, the first screenful answers: what was this change supposed to do, did it, and why. The diff is one click away and never above the fold.
2. **Evidence is a first-class citizen.** Every claim the validators make points at evidence: a step, a log, a file and line. The design makes those links visible and cheap to follow.
3. **State is always legible.** Nine change states, four run statuses, six step statuses, three criterion statuses. Each has one consistent visual encoding (colour, icon, label) used everywhere, from list rows to detail headers.
4. **Live, without noise.** The server streams events. Rows update in place, a running step shows elapsed time, a new verdict arrives without reload. No toasts for routine events; a quiet activity indicator is enough.
5. **Density over decoration.** A laptop screen should show twenty changes in the list and one whole change's verdict without scrolling. No cards with large padding, no hero areas, no illustrations.
6. **Typography does the work.** One UI typeface, one monospace for identifiers, SHAs, file paths, logs and diffs. Clear hierarchy through size and weight, not boxes.

## The data, exactly as the API exposes it

Use these names and values in the design. Realistic sample content is given below; use it rather than lorem ipsum.

**Change**: `id` (like `c_k7m2q9x4w1zp`), `repo_id`, `source_branch`, `target_branch`, `title`, `intent` (one to three paragraphs), `acceptance_criteria[]` each with `id` (`AC-1`…), `text`, and optionally a `check` (named pipeline steps or a command that proves it), `constraints[]` (short rules like "no new dependencies"), `submitted_by` (an agent or person name), `state`, `merge` (once merged: `commit`, `previous`, `strategy`, `merged_at`), `created_at`, `updated_at`.

**Change states** and what each means for the user:
- `open` – submitted, not yet validating
- `running` – a validation is in progress
- `approved` – ready for the human to merge
- `fix_requested` – sent back to the author with findings; a new push revalidates
- `rejected` – the validators found it wrong at the root
- `needs_human` – infrastructure failed or a criterion could not be judged; a person must look
- `escalated` – still not approved after three fix rounds; a person must decide
- `merged` – Plumbix landed it on the target branch
- `closed` – abandoned without merging

**Run** (one validation of a change; also plain runs of pushes that belong to no change): `id` (`run_…`), `change_id` (absent for plain runs), `branch`, `iteration` (1, 2, 3…), `head` and `base` (SHAs), `status` (`running`, `finished`, `errored`, `cancelled`), `outcome` (a change state, when finished), `error` (text, when errored), `steps[]`, `started_at`, `finished_at`, and `result` (below).

**Step**: `name`, `validator` (boolean), `status` (`succeeded`, `failed`, `timed_out`, `errored`, `skipped`, `cancelled`), `exit_code`, `duration_ms`, `error`. Each step has a log, fetched on demand as plain text.

**Result** (the combined verdict of a run): `state` (the outcome), `criteria[]` each with `id`, `text`, `status` (`met`, `unmet`, `unknown`) and `assessments[]` (per validator: `validator` name, `status`, `reason`, `evidence[]` paths such as `evidence/test/log.txt`), `findings[]` (`validator`, `severity` `blocker`/`major`/`minor`, `message`, optional `criterion`, `file`, `line`), `risks[]` (`validator`, `area`, `message`, `human_required`), `reasons[]` (plain sentences on why the change was rejected or needs a human).

**Merge**: the human clicks Merge on an `approved` change. It can be refused with one of four clear reasons the UI must present as next steps, not as errors: `source_moved` (new commits were pushed; validate again), `target_moved` (the target branch moved; bring it into the branch and push), `push_rejected` (the remote changed during the merge; try again), `invalid_state` (not approved any more). Strategies are per repository: squash, merge, rebase.

**Alert**: `id`, `kind` (`bypass_commit`: commits reached a protected branch without a verdict, with `details.commits[]` of `sha`, `author`, `time`, `subject`; `token_expiry`: a git host token expires soon or failed its check), `message`, `created_at`, `resolved_at`. A person resolves alerts.

**Repo**: `id`, `host` (`github`, `azuredevops`, `git`), `url`, `protected_branches[]`, `merge_strategy`.

**Events** (live stream): change submitted, state changed, merged; run started, step finished, run finished; verdict issued; alert raised, resolved. Every event carries the ids it concerns.

### Sample content

Change `c_k7m2q9x4w1zp` on repo `plumbix` (GitHub), branch `feat/retry-uploads` → `main`, state `approved`, submitted by `kai`.
Title: "Retry failed artifact uploads". Intent: "Uploads to the evidence store fail a few times a day on transient network errors and the whole run is marked errored. Retry an upload up to three times with exponential backoff before giving up, and record each attempt in the step log so the failure is visible when it does happen." Criteria: AC-1 "A transient upload error is retried up to three times" (check: step `test`); AC-2 "The retry attempts appear in the step log" (check: step `test`); AC-3 "No change to the public API or the pipeline format" (no check). Constraints: "no new dependencies".
Run 2 of 2, finished, outcome approved, head `9f3c1e2`, base `a81d7c0`, steps: `build` succeeded 42 s, `test` succeeded 1 m 58 s, `lint` succeeded 11 s, `validate` (validator) succeeded 4 s, `ai-review` (validator) succeeded 38 s.
Result: AC-1 met (validate: "step test succeeded"; ai-review: "TestUploadRetriesThrice covers three attempts with backoff"), AC-2 met, AC-3 unknown by validate ("no check given"), met by ai-review ("no files under contracts/ or .plumbix/ changed"). Findings: minor, ai-review, "Backoff ceiling is hard-coded at 8 s; consider a constant", file `internal/evidence/upload.go` line 71. Risks: ai-review, area "tests", "2 test files were modified", human_required false. Iteration 1 was `fix_requested` with a blocker finding "AC-1 unmet: only one retry attempted".

A second change on the same repo, `c_p2v8n4r1t6ya`, state `fix_requested`, iteration 1, title "Add Azure DevOps token expiry warning", with two blocker findings. A third, `c_h9d3s7l2m5qe`, state `running`, step `test` running for 1 m 12 s, `build` succeeded, `lint` queued. A fourth, `c_w4x6z1b8c3nf`, `needs_human`, reason "step build could not run: image golang:1.27 not found". One alert, `bypass_commit`: "commit 3b7f2a9d1c0e by eduard reached main without a verdict".

## Screens and states to deliver

1. **Sign-in landing.** The developer runs `plumbix login` in a terminal, which prints a one-time link. Design the page that link opens: a brief confirmation that this browser is now signed in, and the two failure states: link already used, link expired. Explain in one sentence how to get a new one. No password, no account, no form.

2. **Changes (home).** All changes, newest activity first, filterable by repository and state, with a strong "needs me" grouping at the top: `approved` (merge?), `needs_human`, `escalated`. Each row: state, title, repository and branches, iteration count, who submitted it, when it last moved, and the verdict summary in a few words (criteria met count, blocker count). Running changes show the current step and elapsed time. Live updates. Design empty state (no repositories yet; points to the CLI), and a loading state.

3. **Change.** The core screen. Above the fold: title, state, repository and branches with SHAs, intent, the acceptance criteria each with its combined status and the one-line reason, the verdict summary with reasons, and the primary action: Merge (for `approved`) or Close. Then: findings (grouped by severity, each linking to file and line, and to the criterion), risks (with a clear mark for `human_required`), iterations (a compact timeline of runs with outcome per iteration; the current one open), evidence (steps with status, duration, exit code; click opens the log), and a **Diff** section that is collapsed by default and loads on demand. Design these states: `approved` with Merge, `fix_requested` with findings, `running` live, `needs_human` with reasons, `merged` (shows the merge record), and the four merge refusals as inline next steps.

4. **Step log.** A full-height panel or page for one step's log: monospace, line numbers, wrap toggle, search, follows while the step runs. Header carries step, status, duration, exit code.

5. **Alerts.** Open alerts first, resolved ones behind a toggle. A bypass alert lists the commits with author, time and subject, in monospace where that fits. Resolve is a quiet secondary action.

6. **Repositories.** A read-only list: host, URL, protected branches, merge strategy. Registering happens in the CLI in this release; say so in the empty state.

7. **Design system.** Tokens for light and dark (background, surfaces, text levels, borders, one accent, and the state colours), a type scale with one UI face and one monospace face, and the component set the screens use: state pill, criterion row, finding row, step row, iteration timeline, log viewer, inline next-step notice, filter bar, empty state. Show the state encoding table once: every change, run, step and criterion status with its colour, icon and label.

## Constraints

- Desktop first, laptop width (1280 to 1440) is the primary target; everything must still work at 1024. No phone layout needed.
- Light and dark, both first-class, switching with the system.
- WCAG AA contrast for all text and state colours, including in dark. State must never rely on colour alone.
- Keyboard: focus visible everywhere; the list is navigable with arrow keys; `/` focuses the filter.
- Built later with React, TypeScript and plain CSS variables, served from a Go binary. Prefer designs that a small component set and tokens can express. No heavy component library look.
- Copy is short, concrete and in plain words. Say "merge", "validate again", "bring main into the branch", not "synchronise".

## Avoid

Marketing surfaces, hero sections, illustrations, gradients, cards with large padding, badges in several shapes, more than one accent colour, modal dialogs for routine actions, toasts for routine events, charts (there is nothing to chart yet), and anything that reads as "DevOps dashboard".
