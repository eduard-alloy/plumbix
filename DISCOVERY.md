# Plumbix Discovery

**Status**: ✅ Initial discovery complete (Q00-Q14)

## Q00: Initial vision (2026-10-15)

**Answer**: "i want plumbix to be an infrastructure app that allows a developer simple management for git PRs and pipeline definitions"

**Insights**
- The user is a developer. Team usage is not yet clear.
- Two capability areas: pull requests and CI/CD pipeline definitions.
- "Simple" is the value proposition, so existing tools must feel complex or scattered.
- "Infrastructure app" could mean CLI, web dashboard, desktop app or self-hosted service.

**Candidate features**
- F-001 Pull request management
- F-002 Pipeline definition management

**Open risks**
- Overlap with native GitHub, GitLab and Azure DevOps UIs may leave no clear reason to use Plumbix.
- Supporting several git hosts and CI systems multiplies the work.

## Q01: Core problem (2026-10-15)

**Options**: A) PRs/pipelines scattered across repos · B) Pipeline YAML is painful · C) Multiple platforms · D) Too much clicking in native UIs · E) Other

**Answer**: E. "those tools do not provide the level of independence i need . it costs money to have paralelism. and they are like a rocket engine ..not everything they offer i use. And their UIs suck big time. They invented them 15 years ago and that was it. i want something simple with an updated UI and total freedom"

**Insights**
- Independence is the main driver. The user wants to stop depending on a hosted vendor's pricing and limits.
- Paying for pipeline parallelism is a concrete pain. Plumbix should run jobs in parallel on hardware the user controls, with no per-job or per-minute cost.
- Feature bloat is a pain. Plumbix should do a small set of things well and leave the rest out.
- A modern UI is a core requirement, not a polish item.
- "Total freedom" points toward self-hosted and open formats, with no lock-in.

**Candidate features**
- F-001 Pull request management (unchanged)
- F-002 Pipeline definition management (unchanged)
- F-003 Self-hosted pipeline execution with free parallelism
- F-004 Modern, minimal UI

**Open risks**
- Scope could balloon into building a full GitHub replacement: git hosting, code review, CI engine and UI. That is years of work for one developer.
- Free, self-hosted open-source tools already exist, such as Forgejo or Gitea for git and PRs and Woodpecker for CI. Their UIs are dated, but they solve independence and parallelism. Plumbix must decide whether to reuse them or replace them.

## Q02: Build vs borrow boundary (2026-10-15)

**Options**: A) Full replacement · B) Own the engine, keep the host · C) New face on Forgejo + Woodpecker · D) Own git and pipelines, minimal review · E) Other

**Answer**: E, resolving to B. "i do not intend on hosting git repose. so it would be B"

**Decision**: Plumbix does not host git repositories. Code stays on an external git host. Plumbix owns pipeline execution on the user's own machines and provides its own UI for PRs through the host's API.

**Insights**
- Git hosting is out of scope, which removes the largest chunk of work.
- Plumbix depends on the git host's API for PR data and on its webhooks or polling to trigger pipelines.
- The pipeline engine and runners are the part Plumbix fully owns.

**Candidate features**
- F-001 PR management through the host's API (list, view diff, comment, approve, merge)
- F-002 Pipeline definition management (Plumbix's own format)
- F-003 Self-hosted pipeline runners with free parallelism
- F-004 Modern, minimal UI
- F-005 Git host integration: auth, webhooks, status checks reported back to PRs

**Out of scope**
- Hosting git repositories.

## Q03: First git host (2026-10-15)

**Options**: A) GitHub · B) GitLab · C) Azure DevOps · D) Bitbucket · E) Other

**Answer**: C. "start with C then add others"

**Decision**: Azure DevOps is the only host for the first version. Other hosts come later.

**Insights**
- Azure DevOps triggers come through "service hooks", its version of webhooks.
- Build results show on a PR through the PR Status API. This differs from GitHub's checks.
- Auth options are personal access tokens (PATs) or Microsoft Entra ID apps. That choice is still open.
- Because more hosts are planned, host-specific code should sit behind a narrow boundary. A general multi-host abstraction will not be built until the second host arrives.

**Constraints**
- Azure DevOps REST API, service hooks and PR statuses are the integration surface for v1.

## Insight: PRs don't fit agentic coding (2026-10-15)

**User, unprompted after the PR-ownership discussion**: "as I said...these systems were designet 15 years ago...apart from the UI there is one other thing that did not exist 15 years ago. LLMs! the PRs in agent coding times ...they do not really match"

**Insights**
- This is the strongest differentiator so far. The goal is to rethink the PR for agent-written code, not just to rebuild it with a nicer UI.
- It strengthens the case for Plumbix owning PRs. A new review model cannot be built on Azure DevOps's PR data model.
- Pipelines and PRs belong together here. When agents write the code, pipeline evidence becomes the reviewer's main input.

**Open**
- Q02 decision (PRs via host API) is under reconsideration. It has not been changed yet.
- Q04 (runner model) is paused.
- The concrete agent-era review workflow is not yet defined.

## Q05: Review model for agent-written code (2026-10-15)

**Options**: A) Faster review of agent output · B) Approve intent, evidence approves code · C) The change is a loop, not a PR · D) Mixed team · E) Other

**Answer**: E. "I think these new project interweavs with kai and TRACE. in TRACE there is the validation agent. for plumbix this would need be taken to next level"

**Interpretation**
- Plumbix's replacement for the PR is an independent validation of a change against its stated intent. It builds on the TRACE Validation Agent: check the acceptance criteria, run the tests independently, then approve, send back for fixes, or reject.
- The three tools split cleanly. Kai holds intent and memory: goals, acceptance criteria, decisions. TRACE is the method that shapes work into units. Plumbix supplies proof and the gate: it runs pipelines and validation agents and decides what can merge.
- Kai only observes and never runs agents. Plumbix would be the part that executes.

**What "next level" could mean**
- The validator runs inside Plumbix, separate from the agent that wrote the code. The author does not grade its own work.
- Validation checks the change against intent: the acceptance criteria from the Kai node or work unit, plus recorded decisions and constraints. It does not just read the diff.
- Evidence is produced by actually running things in Plumbix pipelines: tests, builds, and possibly a running preview.
- The verdict drives the merge. "Approve" merges or waits for one human click. "Fix" sends findings back to the authoring agent automatically, up to three loops. "Reject" or a failed escalation goes to a human.
- The human sees intent, verdict, evidence and risky spots first. The raw diff is available on demand.

**Implication**
- This requires Plumbix to own the review. Q02 is revised: Plumbix owns PRs/changes, and the git host stores git only.

**Risks**
- Coupling Plumbix to Kai and TRACE limits who else can use it.
- LLM validators can wrongly approve. Deterministic evidence such as tests must be the ground truth, and risky areas should keep a human gate.
- Validation agents spend model tokens, which is a paid-service cost.

## Q06: Audience (2026-10-15)

**Options**: A) Just me · B) Me first, others later · C) Teams from day one · D) Open source · E) Other

**Answer**: D. "trace eventually is embedded into kai so KAI will use plumbix. so plumbix should be open to other tool, kai will be a user. hence plumbix needs to define its contracts"

**Decision**: Plumbix is open source and tool-agnostic. It defines its own public contracts. Kai, with TRACE embedded, is one client among many and gets no special access.

**Insights**
- Dependency direction: Kai depends on Plumbix. Plumbix never depends on Kai or TRACE.
- Plumbix is contract-first. The public contracts are the product's real interface, and the UI is one client of them.
- Open source raises the bar on self-hosting, documentation, install experience and license choice.

**Candidate contracts (first sketch)**
- Change: what a client submits. Repo, branch, intent and acceptance criteria, plus optional constraints.
- Pipeline definition: Plumbix's own file format for jobs and steps.
- Evidence: what a pipeline run produces. Results, logs, artifacts.
- Verdict: the validation outcome. Approve, fix with findings, or reject, each with reasons tied to the acceptance criteria.
- Events: what Plumbix announces to clients, such as change created, run finished or verdict issued.
- Host adapter: the internal boundary to Azure DevOps and future hosts.

**Open**
- License (to decide before the first public release).
- Which agents Plumbix itself runs (Q07).

## Q07: Which agents Plumbix runs (2026-10-15)

**Options**: A) Validators only · B) Validators and fixers · C) No built-in AI, validators are pluggable pipeline steps · D) Everything · E) Other

**Answer**: C. "yes i also want C"

**Decision**: Plumbix has no built-in AI. A validator is a pipeline step that outputs a verdict in Plumbix's verdict format. Plumbix ships a reference validator that checks a change against its acceptance criteria. Authoring and fix rounds stay with the client.

**Insights**
- The verdict contract is now central. Every validator, deterministic or AI, must produce it.
- Plumbix needs no model API keys of its own. Users bring their own model through the validator step they choose.
- The fix loop runs across the contract: Plumbix issues a "fix" verdict with findings, the client's agent pushes new commits, and Plumbix validates again.
- Validators run on the same runners as any other step, so the runner model (Q04) also decides how validators are isolated.

**Out of scope**
- Running authoring or fixing agents inside Plumbix.

## Q04: Runner model (2026-10-15, resumed after Q07)

**Options**: A) Containers on one machine · B) Agents on several machines · C) Kubernetes · D) Plain shell, no containers · E) Other

**Answer**: A. "start with A"

**Decision**: The first version runs every step in its own container on a single machine, as many in parallel as the hardware allows. The design keeps the server and the runner separable so multi-machine runners can come later.

**Insights**
- Containers isolate agent-written code and give each step, validators included, its own secrets.
- A single-machine install keeps open-source onboarding to roughly one command.
- Parallelism is bounded only by the machine's resources, with no per-job cost.

**Constraints**
- A container runtime (Docker or compatible) is required on the runner machine.
- Plain shell execution without isolation is ruled out because it runs unread agent code with full access.

## Q08: Pipeline definition format (2026-10-15)

**Options**: A) New, minimal YAML · B) GitHub Actions compatible · C) Azure Pipelines compatible · D) Pipelines as code · E) Other

**Answer**: A.

**Decision**: Plumbix has its own minimal, strict YAML format. It covers steps, container images, dependencies between steps, secrets and validator steps, and nothing more in v1. There is no compatibility with GitHub Actions or Azure Pipelines.

**Insights**
- A small strict format is easy for agents to write and for validators to check.
- The format is a public contract, so it needs a published schema and versioning from the start.
- An Azure Pipelines importer is a possible later feature. The format will not bend to match other systems.

**Constraints**
- Every new field must justify itself against the "learn it in ten minutes" goal.

## Q09: First end-to-end slice (2026-10-15)

**Options**: A) Pipeline engine first · B) Thin validation loop · C) Interface first · D) Contracts first · E) Other

**Answer**: B + A.

**Decision**: The first milestone delivers both paths:
- Validation loop: a client submits a change with acceptance criteria, Plumbix runs a pipeline that includes the reference validator, and the verdict comes back through the contract.
- Pipeline engine: a push to Azure DevOps triggers a Plumbix pipeline, steps run in parallel containers, and the result is visible.

**Sequencing (implementation call)**
- The thin validation loop is built first, on a minimal engine. Push triggers from Azure DevOps and real parallelism follow inside the same milestone. Both paths share one engine, so the engine is built once.

**Insights**
- The milestone is larger than either option alone. It must stay thin: one reference validator, a minimal interface, and only the pipeline fields needed by these two paths.

## Q10: Technology stack (2026-10-15)

**Options**: A) Go server + TypeScript web UI · B) TypeScript everywhere · C) Rust + TypeScript · D) Python + TypeScript · E) Other

**Answer**: A.

**Decision**: The engine, runner and API are a single Go binary. The web interface is a TypeScript app that the binary serves.

**Insights**
- This matches the language the user already uses in other projects.
- A single binary keeps open-source installation to one download plus a container runtime.
- Go has mature Docker and Git libraries.
- The web framework and storage choice are implementation calls for the architecture step.

## Q11: Milestone 1 success criteria (2026-10-15)

**Options**: A) Plumbix checks itself · B) A real Azure DevOps project moves over · C) Kai gets a verdict · D) All three · E) Other

**Answer**: A. "A...kai is not yet ready for C's scenario"

**Decision**: Milestone 1 is done when every change to Plumbix's own code goes through Plumbix. Its pipeline runs, the reference validator issues a verdict, and nothing merges without one.

**Insights**
- Kai is not ready to act as a client yet, so Kai integration moves to a later milestone.
- To keep the public contract honest without an outside client, Plumbix's own changes must be submitted through the public API. Internal shortcuts are not allowed.
- Moving one of the user's real Azure DevOps projects onto Plumbix is a candidate for milestone 2.

## Q12: Access and authentication (2026-10-15)

**Options**: A) Local only, no login · B) One admin + API tokens · C) Multiple users with roles · D) Sign in through Microsoft · E) Other

**Answer**: A, for now. "A for now"

**Decision**: v1 listens only on the local machine and has no login. Accounts and API tokens are deferred.

**Consequences (implementation calls)**
- Azure DevOps cannot reach a local-only Plumbix, so push triggers use polling: Plumbix checks Azure DevOps for new commits on a short interval. Service hooks wait until Plumbix supports authentication.
- Plumbix refuses to start on a non-local address while authentication does not exist. This protects open-source users from exposing it by accident.
- Step containers get no network path to the Plumbix API. Validators report verdicts through their step output, never by calling the API. This stops agent-written code under test from approving itself.

**Risks**
- Any local process can call the API. Acceptable for a single developer's machine, not for a shared one.
- Polling adds a delay between a push and a run, and it uses Azure DevOps API quota.

## Q13: Azure DevOps credential (2026-10-15)

**Options**: A) Personal access token · B) Microsoft Entra ID app · C) SSH key for git + narrow API token · D) Other

**Answer**: A.

**Decision**: v1 authenticates to Azure DevOps with a personal access token scoped to code read/write. It is stored in a local secrets file outside the repo and never logged.

**Insights**
- Actions Plumbix takes appear under the user's identity in Azure DevOps.
- With a PAT, Azure DevOps cannot tell Plumbix's pushes from the user's own. The host therefore cannot enforce "nothing merges without a verdict" in v1.
- Implementation call: Plumbix detects commits on protected branches that did not arrive through a verdict and flags them visibly. Enforcement by the host comes with a separate identity (Entra ID app) in a later milestone.

**Risks**
- The token expires and must be rotated by hand. Plumbix should warn before expiry.

## Q14: License (2026-10-15)

**Options**: A) Apache 2.0 · B) MIT · C) AGPL 3.0 · D) Source-available (FSL/BSL) · E) Other

**Answer**: A.

**Decision**: Plumbix is licensed under Apache 2.0. The official text is in `LICENSE`.

**Insights**
- This favors adoption and contributors and includes an explicit patent grant.
- It keeps no legal protection against a third party offering hosted Plumbix commercially. The user accepted that.

---

## Discovery status

Initial discovery is complete. Results are consolidated in `PROJECT.md` and `CONSTRAINTS.md`. Open items carried to architecture are listed in `PROJECT.md`.

## Q03 revised: GitHub and Azure DevOps (2026-10-15)

**Trigger**: The user created Plumbix's public repo on GitHub (https://github.com/eduard-alloy/plumbix). Milestone 1 requires Plumbix to validate its own changes, which an Azure DevOps-only v1 could not do.

**Answer**: "lets start with GitHub and DevOps"

**Decision**: v1 supports both GitHub and Azure DevOps. GitHub is built first because Plumbix's own repo lives there. Azure DevOps follows behind the same host boundary.

**Implementation calls**
- GitHub uses a fine-grained personal access token with contents read/write, matching the Q13 decision for Azure DevOps.
- Push detection polls both hosts, matching Q12.
- With two real hosts, the host boundary is designed now from both implementations.

**Impact**
- Milestone 1 gains one work unit (eleven in total).

## Q15: AI validator in milestone 1 (2026-10-15)

**Answer**: "yes, with AI validator from start. create a .env file where i can put the key"

**Decision**: Milestone 1 ships an AI validator next to the deterministic one. The user approved the paid model usage. Keys go in a git-ignored `.env` file, with `.env.example` committed as the template.

**Implementation calls**
- Anthropic's Go SDK, structured JSON output, default model `claude-opus-5-5`, overridable with `PLUMBIX_AI_MODEL`.
- Host tokens use `PLUMBIX_`-prefixed names so they don't collide with other tools' variables.

## Q16: Protecting the API from step containers (2026-10-16)

**Trigger**: WU-003 showed that on Docker Desktop, step containers can reach the host's loopback and so the unauthenticated API.

**Options**: A) automatic local token · B) no network for steps by default · C) accept for now

**Answer**: "go with A for the answer...generated token"

**Decision**: Plumbix generates a random token on first start in `<data_dir>/api-token` (mode `0600`). Every API route except health needs it. The CLI reads it automatically, and the browser gets a one-time login link with the web interface. Built in WU-013.

## Q17: Plain local git (2026-10-16)

**Question (user)**: "how would it work in a local git environment...no github/devops just plain git"

**Assessment given**: the core already uses plain git; only registration rules and the host lookup block it. Local remotes should be bare repos.

**Answer**: "yes add it before the devops adapter. i think it is more urgent to have it work locally ... plumbix might even be placed in between the user env and the git host CI"

**Decision**: host kind `git` for local paths, `file://` and SSH remotes, built as WU-015 before the Azure DevOps adapter.

**Idea raised, not decided**: Plumbix as a local gate between the developer's environment and the git host's CI. See the assessment recorded under "Open ideas" in PROJECT.md.

## Q18: Visual direction of the web interface (2026-10-16)

**Options**: A) Quiet instrument: calm, dense, near-monochrome, colour only for verdicts and states (Linear, Vercel, Raycast) · B) Editorial: whitespace and typography, intent and verdict read like a document · C) Terminal-native: monospace-led, dark by default · D) Let Claude Design propose directions

**Answer**: A.

**Decision**: the interface is a quiet instrument for one developer supervising agents. Intent and verdict first, evidence linked everywhere, the diff on demand, density over decoration, one accent colour. The full brief handed to Claude Design is `web/DESIGN_BRIEF.md`.

## Q19: Configuration from the browser (2026-10-16)

**User**: "this is something I want to have in the webapp, a settings page where I can provide tokens and also tune the prompts for AI. also the pipelines need an editor."

**Options given**: secrets in a server-owned file, write-only (recommended) vs encrypted in the database; AI tuning as review guidance (recommended), guidance plus model and effort, or the full prompt.

**Answer**: server-owned file; guidance plus the model, no effort knob.

**Decisions**: A18 and A19. The pipeline editor is WU-019: edit in the browser, propose as a change, merge like any other, so A9 still holds.
