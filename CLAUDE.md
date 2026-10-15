# Plumbix: TRACE Slim

This project uses a slimmed TRACE methodology. These rules override the full TRACE v3.0 framework in the parent CLAUDE.md wherever they conflict.

## What stays
- Test-first. A failing test exists before implementation code.
- Small work units, each finishable in one session.
- Ask one discovery question at a time, with 3-4 options plus "Other".
- Decisions are recorded with the why (Kai record_decision plus the doc below).
- Done means tests pass, lint and build pass, no TODOs or debug statements.

## What changes
- **Five documents only** at the root:
  - `PROJECT.md`: goal, users, success criteria, stack, features, roadmap. Replaces PROJECT_MANIFEST, FEATURES and ROADMAP.
  - `DISCOVERY.md`: the question log, one short section per question. No per-question files.
  - `ARCHITECTURE.md`: components, data flow and contracts in one file.
  - `CONSTRAINTS.md`: discovered constraints and decisions, newest first.
  - `CONTEXT_LOG.md`: one line per session. No session files.
- **One file per work unit**: `work_units/WU-NNN.md` holds the spec, the step list, and a short completion section at the bottom. No separate PROMPT, COMPLETION or VALIDATION files.
- **Validation and remediation are not separate roles.** Fixes to a work unit go in its file under a "Fixes" heading. The three-attempt escalation rule still applies.
- **No role-selection prompt at session start.** Read `CONTEXT_LOG.md` and continue from the last line.
- **Kai journey.** The plan lives in Kai under workstream `F8QK66` (Plumbix v1). At session start, run `kai plan plumbix` (or `get_intent`) and declare the session's work with `kai plan plumbix work <ref>`. When a work unit finishes, mark its task done and its acceptance criteria met (`kai plan plumbix status <ref> done`, `kai plan plumbix edit <ref> --met N`). New work units and milestones are added to the journey as they are planned. A decision that replaces an earlier one is recorded as a new decision that names the one it supersedes.
- **No timed checkpoints.** Update docs when a work unit or discovery question finishes.
- Work unit numbering is flat: WU-001, WU-002, and so on.
