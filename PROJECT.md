# Plumbix

**Version**: 0.1.0 (pre-code) · **Created**: 2026-10-15 · **License**: Apache 2.0

## Purpose

Plumbix is an open-source, self-hosted tool that replaces the pull request for the age of coding agents. Code stays on an external git host. Plumbix runs pipelines in containers on the user's own machine with free, unlimited parallelism. Instead of humans reading diffs line by line, each change is checked against its stated intent and acceptance criteria by pluggable validators, using evidence the pipelines produce. The verdict decides what merges. Plumbix defines public contracts so any tool can use it. Kai, with TRACE embedded, is one future client.

## Why it exists

- Hosted tools charge for pipeline parallelism and limit independence.
- They are bloated with features most developers never use.
- Their interfaces date from fifteen years ago.
- Their pull request model was designed for humans reviewing humans, not for agent-written code.

## Users

- Developers working with coding agents, starting with the project owner.
- Client tools that submit changes through the public contracts. Kai is the first planned client.

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

- [ ] Every change to Plumbix's own code goes through Plumbix: pipeline runs, the reference validator issues a verdict, and nothing merges without one.
- [ ] Changes are submitted through the public API, with no internal shortcuts.
- [ ] Independent pipeline steps run in parallel containers.
- [ ] A push to GitHub or Azure DevOps triggers a pipeline run through polling.
- [ ] Commits that reach a protected branch without a verdict are flagged.
- [ ] All tests, lint and build pass.

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
| F-010 | Separate Plumbix identity (Entra ID) and host-side branch enforcement | Later |
| F-011 | Kai as a client | Later |
| F-012 | Runners on several machines | Later |
| F-013 | Additional git hosts | Later |
| F-014 | Azure Pipelines importer | Later |

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
| WU-012 | Self-hosting: Plumbix validates its own changes on GitHub, milestone acceptance | All M1 | ⚪ |

## Milestone 2 candidates

- Move one of the user's real Azure DevOps projects onto Plumbix pipelines.
- Kai as the first external client.
- Authentication, then service hooks.

## Open ideas

**Plumbix as a local gate in front of the git host (raised 2026-10-16, not decided).** Developers and agents push to a Plumbix-managed local repo. Only changes Plumbix approves and merges are forwarded to the upstream host (GitHub or Azure DevOps), where the host's CI runs on code that was already validated.
- Builds on: the plain git host (WU-015) and merge (WU-010). Forwarding is a merge that pushes to a second remote.
- Gains: agents never touch the shared host directly, only validated code leaves the machine, and host CI minutes go only to validated changes.
- Costs: two copies of the repo can drift if anyone pushes to the host directly, so Plumbix would need to sync from upstream and handle conflicts. Host features that key off branches (PR checks, branch protection) see only the forwarded result.
- Assessment: a strong fit for the agent use case and cheap once merge exists. Candidate for milestone 2, right after merge.

## Open items

