# Plumbix

**Version**: 0.1.0 (pre-code) · **Created**: 2026-10-15 · **License**: Apache 2.0

## Purpose

Plumbix is an open-source, self-hosted governed path for agent-written changes. Code stays on an external git host. Instead of humans reading diffs line by line, each change is checked against its stated intent and acceptance criteria by pluggable validators, using evidence that pipelines produce in containers on the user's own machine. The verdict decides what merges, beside a GitHub pull request (verdicts as check runs, the gate as a required check, GitHub merges) or instead of one (Plumbix merges). Everything Plumbix records, changes, verdicts, validator usage, merges and bypasses, is served through public contracts so an observer can read it. Chart-AI, the governance pilot, is the first such observer, and integrating with it sets the order of work (A40). The CI replacement, Plumbix's own pipelines with free, unlimited parallelism and the import of existing ones, is built in full as planned, after and around that path. Kai, with TRACE embedded, is one future client.

## Why it exists

- Hosted tools charge for pipeline parallelism and limit independence.
- They are bloated with features most developers never use.
- Their interfaces date from fifteen years ago.
- Their pull request model was designed for humans reviewing humans, not for agent-written code.

## Users

- Developers working with coding agents, starting with the project owner.
- Client tools that submit changes through the public contracts. Kai is the first planned client.
- Observers that read what Plumbix records: the API, the event stream and the GitHub check runs. Chart-AI, the governance pilot, is the first and sets the order of work (A40).

## Scope

**In**: pipeline execution, change and review management, validation verdicts, a modern minimal web interface, public contracts, git host adapters for GitHub and Azure DevOps.

**Out**: hosting git repositories, running authoring or fixing agents, built-in AI in the core.

## Core concepts and contracts

| Contract | What it is |
|----------|------------|
| Change | What a client submits: repo, branch, intent, acceptance criteria, optional constraints. |
| Pipeline definition | Plumbix's own minimal, strict YAML: steps, images, step dependencies, secrets, validator steps. Versioned schema. |
| Evidence | What a run produces: results, logs, artifacts. |
| Verdict | Validator output: approve, fix with findings, or reject, each reason tied to an acceptance criterion. |
| Events | What Plumbix announces: change created, run finished, verdict issued. |

The fix loop runs across the contract. A "fix" verdict goes back to the client, the client's agent pushes new commits, and Plumbix validates again, up to three rounds before escalating to a human.

## Technical stack

- One Go binary holds the engine, runner and API.
- A TypeScript web interface is served by the binary.
- Each pipeline step runs in its own container (Docker or compatible) on one machine.
- GitHub and Azure DevOps are the first git hosts, each accessed with a personal access token. Plain git repos (local paths or SSH) work without any host account.

## Milestone 1 success criteria

- [x] Every change to Plumbix's own code goes through Plumbix: pipeline runs, the reference validator issues a verdict, and nothing merges without one. Evidence: `.plumbix/pipeline.yaml`; change `c_bzfnjwmsw7yv` (the gitops fix found by this acceptance) was validated by both validators and merged by Plumbix as `2ebba61` on `main`; the full record is under "Acceptance run" in `work_units/WU-012.md`.
- [x] Changes are submitted through the public API, with no internal shortcuts. Evidence: WU-012 submitted its own change with `plumbix submit`, which only speaks HTTP.
- [x] Independent pipeline steps run in parallel containers. Evidence: `build`, `test`, `lint` and `web` have no `needs`; `scheduler.TestIndependentStepsRunConcurrently`; the acceptance run's step timings in WU-012.
- [x] A push to GitHub or Azure DevOps triggers a pipeline run through polling. Evidence: `poller` tests; WU-012 iteration 2 started from a push with no API call.
- [x] Commits that reach a protected branch without a verdict are flagged. Evidence: alert `al_xcncmstxyhxi` for the bootstrap commit `2da8efa` on `main` (WU-012).
- [x] All tests, lint and build pass. Evidence: `make check` and `make integration` at every work unit; the `build`, `test`, `lint` and `web` steps of Plumbix's own pipeline on the iteration that merged each change (earlier iterations failed on real faults, each fixed by its own change, as `work_units/WU-012.md` records).

## Features

| ID | Feature | Milestone |
|----|---------|-----------|
| F-001 | Change management owned by Plumbix (submit, view, merge) | M1 |
| F-002 | Minimal pipeline definition format with schema | M1 |
| F-003 | Container runner with parallel steps | M1 |
| F-004 | Modern minimal web interface: intent, verdict, evidence first, diff on demand | M1 |
| F-005 | Git host adapters for GitHub and Azure DevOps: fetch, poll for pushes, push merges | M1 |
| F-006 | Verdict contract and deterministic reference validator | M1 |
| F-015 | AI validator using Claude, key from `.env` | M1 |
| F-016 | Plain git repos (local or SSH), no host account | M1 |
| F-007 | Bypass detection on protected branches | M1 |
| F-008 | Authentication. M1: a local API token (WU-013). Later: browser login (WU-011), accounts and per-client tokens | M1 / Later |
| F-009 | Push notifications from git hosts, such as Azure DevOps service hooks, instead of polling | Later |
| F-010 | Separate Plumbix identity and host-side branch enforcement. First slice (WU-054): a GitHub App identity, pull requests gated through check runs, the hold as a required check, GitHub merging. Later: Entra ID, Azure DevOps PR statuses and policies | M2 / Later |
| F-011 | Kai as a client | Later |
| F-012 | Runners on several machines | Later |
| F-013 | Additional git hosts | Later |
| F-014 | Pipeline import, Azure Pipelines first: every source pipeline and step brought in, unsupported steps disabled with a reason (A36, A37) | I1 |

## Roadmap: Milestone 1

Work unit IDs are stable identifiers, and the table lists execution order. Ordered so the thin validation loop works first, then the git host paths. GitHub comes first because Plumbix's own repo lives there. Estimates are preliminary.

| WU | Title | Features | Status |
|----|-------|----------|--------|
| WU-001 | Project skeleton: Go module, `plumbix serve` on localhost only, test, lint and build setup | - | ✅ |
| WU-002 | Pipeline format v0: schema, parser, validation errors | F-002 | ✅ |
| WU-003 | Container runner: run steps in isolated containers, capture logs, run independent steps in parallel | F-003 | ✅ |
| WU-013 | API token: generated on first start, required on every route except health (added after the WU-003 security finding) | F-008 | ✅ |
| WU-004 | Change contract and storage, submitted through the public HTTP API and a small CLI | F-001 | ✅ |
| WU-005 | Verdict contract and reference validator step | F-006 | ✅ |
| WU-006 | AI validator: Claude judges the change against intent and criteria, same verdict contract | F-015 | ✅ |
| WU-007 | Validation loop end to end: change, run, verdicts (events split to WU-014) | F-001, F-006, F-015 | ✅ |
| WU-008 | GitHub adapter and the host boundary: PAT config, fetch branches, poll for pushes | F-005 | ✅ |
| WU-015 | Plain git host: local paths, file:// and SSH remotes, no account needed (runs before WU-009) | F-016 | ✅ |
| WU-009 | Azure DevOps adapter behind the same boundary | F-005 | ✅ |
| WU-010 | Merge on approval and bypass detection, for both hosts | F-001, F-007 | ✅ |
| WU-014 | Event stream: SSE with resumable ids, events contract (split from WU-007) | F-001 | ✅ |
| WU-016 | Browser session (one-time login link, cookie, Host and Origin checks), diff endpoint, web app served from the binary (split from WU-011) | F-004, F-008 | ✅ |
| WU-011 | Web interface, core screens: shell, sign-in, changes list, change view with verdict, evidence, merge | F-004 | ✅ |
| WU-017 | Web interface, the rest: log panel, diff view, alerts, repositories, design-system page | F-004 | ✅ |
| WU-012 | Self-hosting: Plumbix validates its own changes on GitHub, milestone acceptance | All M1 | ✅ |
| WU-018 | Settings page: tokens and secrets written by the server, review guidance for validators, AI model | F-004, F-005, F-015 | ✅ |
| WU-019 | Pipeline format: run lists, retries, allow_failure, path conditions, stages (the editor design needs them) | F-002, F-003 | ✅ |
| WU-020 | Pipeline editor: edit `.plumbix/pipeline.yaml` in the browser (design handoff 2, layout 2a) and propose it as a change | F-002, F-004 | ✅ |
| WU-021 | Human approval: a person approves a `needs_human` or `escalated` change with a reason (API, CLI, web) | F-001, F-004 | ✅ |
| WU-022 | Update from main: Plumbix brings the target into a branch it created and validates again (API, CLI, web) | F-002, F-004 | ✅ |
| WU-023 | External validator step and `plumbix review --watch`: AI review by pull, with the Claude Code a person already has | F-003, F-004 | ✅ |
| WU-026 | Reviewer presence, the `no_reviewer` alert, and `plumbix review install|uninstall|status` | F-003, F-004 | ✅ |
| WU-024 | Plumbix MCP server: `plumbix mcp` with the change, verdict, action and reviewer tools for agent sessions | F-001, F-003, F-004 | ✅ |
| WU-025 | Any model for the container AI validator: an OpenAI-compatible provider with base URL and model in Settings | F-003 | ✅ |
| WU-027 | Live steps and external review progress: running steps in the run, `step.started`, reviewer progress lines in the step log, a log panel that follows | F-003, F-004 | ✅ |
| WU-028 | Reviewer profiles: named model configurations bound to validator steps with `reviewer:`, keys per profile, the Settings page redesigned around them | F-003, F-004 | ✅ |
| WU-029 | Step caches: `cache:` paths on plain steps become per-repository directories mounted between runs | F-002 | ✅ |
| WU-030 | Advisory validators: `advisory: true` records a verdict without counting it, for a second model beside the first | F-003, F-004 | ✅ |
| WU-031 | Validator steps say what they run: a progress line when the AI validator's request goes out; schema, docs and editor explain that the image is the validator | F-003, F-004 | ✅ |
| WU-032 | Images page, inspect layer: every referenced image with status and users; the shipped two with a version check and a build from embedded sources | F-002, F-004 | ✅ |
| WU-033 | Images page, own images: a Dockerfile written on the page, built on the server, named in pipelines; edited, rebuilt when stale, deleted | F-002, F-004 | ✅ |
| WU-034 | Reasoning effort for API reviewers: per profile and per validator step, sent as reasoning_effort, reasoning tokens in the usage | F-003, F-004 | ✅ |
| WU-035 | Containerised Claude Code validator image: `plumbix-claude-code-validator` runs Claude Code inside the pipeline on a `claude setup-token` token, through the runner the loop uses; kind `claude-code-image` | F-003, F-004 | ✅ |
| WU-036 | The pipeline editor keeps the file's text: edits applied to the served YAML with comments and formatting kept, byte-identical when unchanged | F-004 | ✅ |
| WU-037 | The editor can make a validator external: an External switch, reviewer profiles filtered by step kind | F-004 | ✅ |
| WU-038 | Stage cards fit the window width: columns follow the measured canvas, cards narrow to a minimum, chains wrap; no horizontal scroll | F-004 | ✅ |
| WU-039 | Verdict notes in every validator log; cached input tokens recorded in usage for all reviewers | F-003, F-004 | ✅ |
| WU-040 | Repositories as two-line cards: name, host, changes and merge on top; URL, protected branches and token below | F-004 | ✅ |
| WU-041 | Import a repository from the Repositories page: a local folder with a remote, or a remote cloned into a local folder, with a token choice and an access check | F-001, F-004 | ✅ |
| WU-042 | Publish a local folder with no remote: create the repository on GitHub or Azure DevOps, push every branch, register it | F-001, F-004 | ✅ |
| WU-043 | Import polish: decoded repository names, a warning when the host has no token, a plain error when git has no credentials | F-001, F-004 | ✅ |
| WU-053 | Validator model usage on the run result: each validator's reported `usage` on `result.validators`, served by the run endpoints for observers such as Chart-AI; OpenAPI `Usage` schema mirrors the verdict's | F-003, F-004 | ✅ |
| WU-054 | The gate on GitHub pull requests: App identity beside the token, pull requests as changes (polling and webhook), verdicts as `plumbix/<validator>` and `plumbix/gate` check runs with the embedded verdict, review approval as a person's approval, GitHub merges and Plumbix records it, `gate_bypassed` alerts | F-010, F-005 | ✅ |
| WU-055 | Pull-request API calls go out as the App (only an App may create check runs), with the token as the fallback; `pull_requests` and `merger` settable on a registered repository (`PUT /repos/{id}/settings`, `plumbix repo set`, shown on the repository card); the App's id and key checkable in Settings | F-010 | ✅ |
| WU-057 | Origin of a change in the web: a `PR #n` or `API` tag on the changes list and the change page header (the pull request linked), tinted but never colour alone, and an origin filter kept in the URL | F-010, F-004 | ✅ |
| WU-058 | Changes list rows on two lines: state, origin, title, by, moved; then repository, branch, verdict, iteration; header, skeleton and design page follow | F-004 | ✅ |

## Roadmap: Pipeline import (Kai workstream TCVR9R)

| WU | Title | Features | Status |
|----|-------|----------|--------|
| WU-044 | Disabled steps: a reason per step, a pipeline with one is disabled and never runs, the editor dims and explains them | F-002, F-014 | ✅ |
| WU-045 | Triggered pipelines: one file each under `.plumbix/pipelines/`, run on matching pushes or by hand (API, CLI) | F-002, F-014 | ✅ |
| WU-046 | Import source interface and the Azure importer core, with a preview (API, CLI) | F-014 | ✅ |
| WU-047 | Azure importer: templates (local and registered repositories), template expressions, parameters, extends | F-014 | ✅ |
| WU-052 | Azure importer: run variables (branch, commit, run) and simple conditions (`when.branches`); fields read as booleans (`enabled`, `continueOnError`) accept `True` and `False` in any case, as WU-047 splices whole-value booleans as text | F-014 | ⚪ |
| WU-048 | Import from the CLI and API as a change; re-check that replaces what the import wrote and keeps what a person edited | F-014, F-004 | ✅ |
| WU-049 | User docs: importing pipelines, the reason codes, what each release supports | F-014 | ⚪ |
| WU-051 | Pipeline guard: built-in check of every change to `.plumbix/`, approved pipeline fingerprints gate secrets on protected branches (A38) | F-014 | ✅ |
| WU-050 | Pipelines page: every pipeline of a repository, its triggers and blockers, edit any, run one by hand, import and re-check from the browser | F-004, F-014 | ⚪ |

## Roadmap: Milestone 2, the governed path for Chart-AI (A40)

Order of precedence, decided 2026-10-28: what Chart-AI consumes comes first; the pipeline import workstream above is built in full, but after and around it, as the evidence engine of the governed path. A unit on the CI line never jumps a ready unit on this one.

Landed so far: WU-053 (validator usage on the run), WU-054 and WU-055 (the gate beside GitHub pull requests), WU-057 and WU-058 (origin of a change in the web).

| WU | Title | Features | Status |
|----|-------|----------|--------|
| WU-061 | GitHub integration per repository: a GitHub panel on the repository card with the pull-request switches and live checks (App installed, `plumbix/gate` required per protected branch, webhook or polling), the report on the API and CLI | F-010, F-004 | ✅ |
| WU-059 | Plumbix's own merges satisfy a required `plumbix/gate`: the landing commit goes to a holding ref, the gate check is posted on it, then the target moves; a push refused by repository rules is reported as such | F-010, F-001 | ✅ |
| WU-060 | A person's approval names the person: `change.approved_by_person` and the change's approval carry who approved and through which surface (Approve action or GitHub review) | F-010, F-001 | ✅ |
| WU-056 | Pull requests with `merger: plumbix`: close the pull request after the merge or document the shape as unsupported; never reopen a change for a merged head | F-010 | ⚪ |
| next | Chart-AI's requirement list, one unit per surface it needs (added as each requirement arrives) | F-010 | ⚪ |
| later | Azure DevOps pull-request statuses and branch policies, the same gate on the second host | F-010 | ⚪ |

Behind it: Kai as the first external client; authentication, then service hooks; the pipeline import workstream (I1, I2).

## Open ideas

**Plumbix as a local gate in front of the git host (raised 2026-10-16, not decided).** Developers and agents push to a Plumbix-managed local repo. Only changes Plumbix approves and merges are forwarded to the upstream host (GitHub or Azure DevOps), where the host's CI runs on code that was already validated.
- Builds on: the plain git host (WU-015) and merge (WU-010). Forwarding is a merge that pushes to a second remote.
- Gains: agents never touch the shared host directly, only validated code leaves the machine, and host CI minutes go only to validated changes.
- Costs: two copies of the repo can drift if anyone pushes to the host directly, so Plumbix would need to sync from upstream and handle conflicts. Host features that key off branches (PR checks, branch protection) see only the forwarded result.
- Assessment: a strong fit for the agent use case and cheap once merge exists. Candidate for milestone 2, right after merge.

## Open items

