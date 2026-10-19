# Plumbix Architecture

**Version**: 0.1 (milestone 1 baseline) · **Date**: 2026-10-15
**Inputs**: PROJECT.md, DISCOVERY.md (Q00-Q14), CONSTRAINTS.md

## 1. Overview

Plumbix is one Go binary. `plumbix serve` runs the API, the web interface, the scheduler, the container runner and the git host poller in one process, listening only on a local address. The same binary is also the command-line client, and it talks to the server only through the public HTTP API.

Code lives on the git host. Plumbix keeps a bare mirror of each registered repo, creates a temporary worktree per run, executes pipeline steps in containers, collects evidence, and runs validator steps that each write a verdict file. Plumbix combines those verdicts into the change's outcome. When a change is approved and a human clicks merge, Plumbix merges in its mirror and pushes the result to the host.

Clients see Plumbix only through its contracts: the HTTP API, the pipeline file format, the validator input and output files, and the event stream. The web interface is a client of the same API.

```
                       ┌──────────────────────────── plumbix serve (127.0.0.1) ─────────────────────────────┐
  CLI / Kai / scripts  │                                                                                     │
  ───── HTTP API ────▶ │  API ──▶ Changes ──▶ Scheduler ──▶ Runner ──▶ Docker Engine ──▶ step containers   │
  Browser ── UI ─────▶ │   │         │            ▲            │                                             │
                       │   │         ▼            │            ▼                                             │
                       │   │      Validation ◀────┴───── Evidence store (files)                              │
                       │   ▼         │                                                                       │
                       │  Events ◀───┤                                                                       │
                       │             ▼                                                                       │
                       │          Store (SQLite)        Poller ──▶ Host adapters ──▶ Git ops (mirrors)     │
                       └──────────────────────────────────────────────────────────┬──────────────────────────┘
                                                                                  │ git over HTTPS + PAT
                                                                                  ▼
                                                                  GitHub        Azure DevOps
```

## 2. Components

Each package lives under `internal/` unless noted.

| Component | Package | Responsibility |
|-----------|---------|----------------|
| CLI | `cmd/plumbix` | Entry point. `serve` starts the server. Other subcommands (`submit`, `change`, `review`, `mcp`) are thin HTTP clients of the public API. |
| Images | `internal/images` | What the server knows about the images its pipelines run (WU-032, A32): inspects each referenced image through the Docker client, knows the three Plumbix ships (`plumbix-reference-validator`, `plumbix-ai-validator`, `plumbix-claude-code-validator`) with the version label their build carries, and rebuilds them from the build context embedded at the module root (`embed.go`: `go.mod`, `go.sum`, `internal/`, `validators/`), one build at a time, logs under `images/builds/`. Own images (WU-033): a Dockerfile written on the page is stored under `images/own/<name>/`, which is the build context, built as `<name>:latest` with labels naming the image and the Dockerfile's hash, so the page can say when the file changed since the last build. |
| MCP server | `internal/mcpserver` | `plumbix mcp`: an MCP server over stdio (official Go SDK) whose tools wrap the API for an agent session: register a repository (no token), submit, follow, read verdicts, merge, refresh, and the reviewer loop (`next_review`, `post_verdict`); `approve_change` only with `--allow-approve`, since approval is a person's act (A23). Each process acts as `mcp:<name>:<id>`, used as `submitted_by` and as the reviewer name, so a session never reviews its own change (WU-024). |
| API | `api` | HTTP JSON handlers for `/api/v1`. Validates requests against the contracts. Serves the embedded web interface. |
| Changes | `change` | Change lifecycle state machine, iterations, fix-round counting, escalation. |
| Pipeline | `pipeline` | Parses and validates `.plumbix/pipeline.yaml` and builds the step graph (a DAG). |
| Scheduler | `scheduler` | Turns a step graph into ready jobs, respects `needs`, caps parallelism, handles cancellation. |
| Runner | `runner` | Runs one step in a container through the Docker Engine API. Mounts the workspace and evidence, streams logs, enforces timeouts. |
| Evidence | `evidence` | Stores step logs, exit codes and declared artifacts on disk, with metadata in the store. |
| Validation | `validation` | Prepares validator inputs, reads and checks each `verdict.json`, combines verdicts into the change outcome. |
| Host adapters | `host`, `host/github`, `host/azuredevops` | The narrow host boundary: remote URL and credentials, branch heads, push. One implementation per host. |
| Git ops | `gitops` | Wraps the system `git` command: mirrors, worktrees, diffs, merges, pushes with an expected old value. |
| Poller | `poller` | Checks every registered repo's branch heads on an interval. Starts runs on new commits and flags bypass commits. |
| Events | `events` | Append-only event log in the store, published to clients as a Server-Sent Events (SSE) stream. |
| Store | `store` | SQLite database plus embedded migrations. |
| Config | `config` | Loads config and secrets, checks file permissions, enforces the local-only bind rule. |
| Web interface | `web/` | React and TypeScript app built with Vite, embedded into the binary at build time (`web/embed.go`, SPA fallback, CSP). One store loaded once and kept fresh by refetching what each event names; pure view models under `web/src/vm`. The design handoff lives in `web/design/`. |
| Reference validator | `validators/reference/` | Deterministic validator: a small Go program shipped as a container image. It maps criteria to step results and commands. |
| AI validator | `validators/ai/` | A Go program shipped as a container image. It asks a model to judge the change against its intent and criteria and writes the same verdict file: Claude through Anthropic's SDK by default, or any OpenAI-compatible endpoint (`PLUMBIX_AI_PROVIDER=openai`, WU-025). |

## 3. Core model

**Repo**: id, host (`github` or `azuredevops`), remote URL, protected branches (default: the host's default branch), merge strategy (default `squash`).

**Change**: id, repo, source branch, target branch, title, intent (free text), acceptance criteria, optional constraints, submitted-by (free-text client name), state, current iteration.

**Acceptance criterion**: id (such as `AC-1`), text, optional `check`. A check names one or more pipeline steps that must succeed, or a command the reference validator runs.

**Iteration**: one head commit of the source branch while the change is open. Each iteration has exactly one run. A new push to the source branch creates a new iteration and cancels the previous run if it is still going.

**Run**: one pipeline execution for one commit. A run can belong to an iteration, or stand alone when a push arrives on a branch without an open change.

**Verdict**: one validator step's output. **Outcome**: the combined result for an iteration.

### Change states

```
open ──▶ running ──▶ approved ──(human merge)──▶ merged
             │  ▲
             │  └──── new commits ◀──── fix_requested
             ├──▶ rejected
             ├──▶ needs_human        (unknown criteria, risky areas, validator error)
             └──▶ escalated          (fourth fix round would be needed)
any open state ──(close)──▶ closed
```

- Validators start once their needs have finished, whatever the outcome, because failed steps are the evidence they judge. A normal step that errored (could not run) forces `needs_human`: infrastructure failures are never the author's to fix (WU-007).
- `fix_requested` is the fix loop. The client's agent reads the findings, pushes new commits, and a new iteration starts. After three fix rounds without approval, the change moves to `escalated`.
- `approved` never merges by itself in v1. A human merges from the web interface or the CLI.
- `needs_human`, `escalated` and `rejected` mean a person must look, so they are sticky (A21): a push to the branch starts nothing, the new head is only recorded and shown on the change, and a person presses Validate again, approves with a reason (A23: `needs_human` and `escalated` only; `rejected` is the validators' explicit no) or closes. A person's approval names the validated head and is recorded on the change and as `change.approved_by_person`; the change then merges like any approval, and a later push starts a new run that ends the approval. Pushes keep starting iterations for `open`, `fix_requested` and `approved`, and a push during `running` supersedes the run. A change whose branch Plumbix created (`plumbix_branch`, the pipeline editor's) can be brought up to date with its target by a person (`POST /changes/{id}/refresh`, Update from main, A24): Plumbix merges the target in as author, pushes under a lease, records `change.refreshed` and starts a new iteration, which ends any standing approval like every run does.

### Combining verdicts

1. Any validator reports `reject`: the outcome is `rejected`.
2. Otherwise any reports `fix`: the outcome is `fix_requested`, with all findings merged.
3. Any criterion is `unknown`, any validator crashed or wrote an invalid verdict, or a required validator is missing: `needs_human`.
4. All validators `approve` and all criteria `met`: `approved`. Per criterion, `unmet` from any validator wins, then `met` from any validator, otherwise `unknown`. A change with no validators is never approved (WU-005).
5. A change that touches `.plumbix/` or reports risks flagged `human_required` goes to `needs_human` even if rule 4 holds.

## 4. Contracts

The contracts are files in `contracts/`, versioned with the code and published with each release. Go types are written by hand. Contract tests check that the server's real output validates against these schemas, so code and contract cannot drift apart.

| File | Contract |
|------|----------|
| `contracts/openapi.yaml` | HTTP API `v1` |
| `contracts/pipeline.schema.json` | Pipeline file, format version 1 |
| `contracts/validator-input.schema.json` | What a validator step receives |
| `contracts/verdict.schema.json` | What a validator step must write |
| `contracts/events.schema.json` | Event payloads on the stream |

Versioning: every document carries a version (`version: 1` in pipelines, `"plumbix": "verdict/v1"` in verdicts). A breaking change creates a new version. The server accepts the previous version for at least one minor release.

### 4.1 HTTP API v1

The base address comes from config `server.listen`, default `127.0.0.1:6400`. Every route except `GET /api/v1/health` and `POST /api/v1/sessions` needs either `Authorization: Bearer <token>`, where the token is the content of `<data_dir>/api-token` (A14), or the browser's `plumbix_session` cookie. Cookie requests must carry the server's own `Host` (DNS rebinding) and, when they change state, an `Origin` of `http://<that host>` (cross-site requests); failures are 401 and 403. Failures of the token are 401 with `WWW-Authenticate: Bearer`. Paths outside `/api/` serve the embedded web app. Errors use one JSON shape: `{"error": {"code": "...", "message": "...", "details": {...}}}`. Field problems are `invalid_request` with `details.problems: [{field, message}]` listing every problem. IDs are a prefix plus 12 random base32 characters: `r_` for repos, `c_` for changes.

| Method | Path | Purpose |
|--------|------|---------|
| POST | `/api/v1/repos` | Register a repo (host, remote URL, protected branches, merge strategy). |
| GET | `/api/v1/repos` | List repos as `{"repos": [...]}`. |
| GET | `/api/v1/repos/{id}/pipelines` | The triggered pipelines under `.plumbix/pipelines/` at a branch head: name, file, `on`, disabled, blockers, problems (WU-045). |
| POST | `/api/v1/repos/{id}/pipelines/{name}/runs` | Run a triggered pipeline by hand at a branch head; 409 `not_manual`, `pipeline_disabled` or `pipeline_invalid` (WU-045). |
| POST | `/api/v1/changes` | Submit a change: repo, source and target branch, title, intent, acceptance criteria, constraints, submitted-by. |
| GET | `/api/v1/changes` | List changes as `{"changes": [...]}`, filtered by `repo_id` and `state`. |
| GET | `/api/v1/changes/{id}` | One change with its iterations, latest outcome and findings. |
| GET | `/api/v1/changes/{id}/diff` | The diff the validators judged: base..head of the latest run with a head, parsed into files and hunks; `stat=true` for counts only. |
| POST | `/api/v1/changes/{id}/merge` | Merge an approved change. Fails if the target branch moved since validation. |
| POST | `/api/v1/changes/{id}/approve` | A person approves a `needs_human` or `escalated` change with a reason; the change becomes `approved` and merges as usual. |
| POST | `/api/v1/changes/{id}/refresh` | Bring the target branch into a branch Plumbix created (`plumbix_branch`) and validate again (409 `not_plumbix_branch`, `already_current`, `merge_conflict`). |
| GET | `/api/v1/reviews/next` | Claim the oldest review an external validator step is waiting for, long-polling up to `wait` seconds (204 when idle); the brief carries the AI validator's prompt and schema, the commits and a one-time token. |
| POST | `/api/v1/reviews/{id}/verdict` | Post a claimed review's verdict with its token; checked like a verdict file (400 problems, 409 wrong token, answered or expired). |
| POST | `/api/v1/reviews/{id}/log` | Append one progress line from the reviewer holding the claim to the waiting step's log, prefixed `reviewer:`; bounded (300 characters, 300 lines, one every 500 ms, else 429). |
| GET | `/api/v1/reviewers` | The external reviewers the server has heard from since it started, most recent first, with their repository filter (every poll is a heartbeat). |
| POST | `/api/v1/changes/{id}/close` | Close without merging. |
| GET | `/api/v1/runs/{id}` | A run with its steps, status, timings and evidence index. |
| GET | `/api/v1/runs/{id}/steps/{step}/log` | A step's log, streamed while running. |
| GET | `/api/v1/runs/{id}/steps/{step}/artifacts/{path}` | Download an artifact. |
| POST | `/api/v1/pipelines/check` | Validate a pipeline file without running it. |
| GET | `/api/v1/alerts` | Bypass commits and token-expiry warnings, open ones unless `include_resolved=true`. |
| POST | `/api/v1/alerts/{id}/resolve` | Mark an alert as handled. |
| GET | `/api/v1/events` | SSE stream, filtered by `repo_id` and `change_id`. Replays after `Last-Event-ID` (or `after`), then follows live, so clients resume with no gap. |
| POST | `/api/v1/login-links` | Token only. Mints the one-time link `plumbix login` prints; valid 10 minutes. |
| POST | `/api/v1/sessions` | No credentials. Exchanges a link once for the `plumbix_session` cookie (30 days); 401 `link_used`, `link_expired` or `link_invalid`. |
| DELETE | `/api/v1/sessions` | Token only. Signs every browser out (`plumbix logout`). |
| DELETE | `/api/v1/sessions/current` | Cookie only. Signs this browser out. |
| GET | `/api/v1/session` | How the caller is signed in, with the cookie's expiry. |
| GET | `/api/v1/images` | Every image the registered pipelines run plus the shipped two: presence, created, size, digest, users, and for shipped images the version label against the server (WU-032). |
| POST | `/api/v1/images/{name}/build` | Build a shipped image from the embedded sources; 202 with a build to follow, 409 while one runs. A person's act: not offered by the MCP server. |
| GET | `/api/v1/images/builds/{id}` | A build's status and log so far. |
| PUT, DELETE | `/api/v1/images/own/{name}` | Create or replace an own image's Dockerfile, or remove the definition (WU-033). A person's act; not offered by the MCP server. |
| GET, PUT | `/api/v1/settings` | Reviewer profiles with whether each key is set, the default profile, global review guidance, and which secrets are set (never their values); PUT takes `guidance` and `default_reviewer`. |
| POST | `/api/v1/repos/inspect` | Read a local folder's git metadata for an import (WU-041, A35): `{path}` inside the import root (the server user's home, `~` allowed, symlinks resolved) gives `{path, remote, default_branch, has_pipeline, host, url, problem}`; GitHub and Azure DevOps SSH remotes are suggested as HTTPS. |
| POST | `/api/v1/repos/publish` | Create a repository online for a local folder with commits and no remote, then push it (WU-042, A35): `{path, host (github or azuredevops), owner, project, name, private, token}`; GitHub creates under the token's account or the organisation in `owner`, Azure DevOps in organisation `owner` and `project`; `token`, else the host token, travels only in headers. The folder gains `origin` and every branch is pushed, never forced; registration is a separate `POST /repos`. `create_failed` changed nothing; `push_failed` says the repository exists. |
| POST | `/api/v1/repos/{id}/clone` | Clone a registered repository into `{dest}`, a new or empty folder inside the import root under an existing parent, with its own or the host token through askpass (WU-041). `clone_failed` carries git's message. |
| PUT, DELETE | `/api/v1/settings/reviewers/{name}` | Create or replace a reviewer profile (kind `anthropic`, `openai`, `claude-code` or `claude-code-image`, base URL, model, tools, effort for the API kinds that take one, key secret name), or remove one (WU-028, A28, A29, WU-034, WU-035). The key of an API profile is set through the secrets endpoint under the profile's `key_secret` (for `claude-code-image` it holds a token from `claude setup-token` or an API key); a `claude-code` profile has none. |
| PUT, DELETE | `/api/v1/settings/secrets/{key}` | Set or remove a secret in the server's secrets file; used from the next fetch on. |
| POST | `/api/v1/settings/secrets/{key}/check` | Run a git host's token check now. |
| GET, PUT | `/api/v1/repos/{id}/settings` | One repository's review guidance. |
| GET | `/api/v1/repos/{id}/pipeline` | The pipeline file at a branch head, parsed for the editor, with parse problems and last-run step durations. |
| POST | `/api/v1/repos/{id}/pipeline/changes` | Commit an edited pipeline file on a new branch and open a change for it (409 `branch_exists`, `push_denied`). |

### 4.2 Pipeline file, version 1

The file is `.plumbix/pipeline.yaml` in the repo.

```yaml
version: 1
steps:
  - name: test
    image: golang:1.27
    run: go test ./...
  - name: lint
    image: golangci/golangci-lint:v2.5.0
    run: golangci-lint run
  - name: validate
    image: ghcr.io/eduard-alloy/plumbix-reference-validator:v0
    validator: true
    needs: [test, lint]
  - name: ai-review
    image: ghcr.io/eduard-alloy/plumbix-ai-validator:v0
    validator: true
    needs: [test, lint]
    secrets: [ANTHROPIC_API_KEY]
```

With the optional fields added in WU-019:

```yaml
  - name: contract
    image: golang:1.27
    run:
      - go build ./cmd/openapi-diff
      - ./openapi-diff --base "$PLUMBIX_BASE" --head "$PLUMBIX_HEAD"
    needs: [test]
    retries: 1
    allow_failure: true
    when:
      paths: ["api/**", "internal/http/*.go"]
stages:
  checks: [test, lint, contract]
```

| Field | Required | Meaning |
|-------|----------|---------|
| `version` | yes | Must be `1`. |
| `steps[].name` | yes | Unique, lowercase letters, digits and dashes. |
| `steps[].image` | yes, except external or disabled steps | Container image. For a plain step it is where `run` executes; for a validator it *is* the validator: its entrypoint runs with no `run`. Plumbix ships `plumbix-reference-validator`, `plumbix-ai-validator` and `plumbix-claude-code-validator`, built from `validators/` in this repository (`docker build -f validators/ai/Dockerfile -t plumbix-ai-validator:dev .`, or from the Images page); any image honouring 4.3 can be a validator. |
| `steps[].run` | yes, except validators and disabled steps | Shell script, run with `sh -c`; or a list of commands, run in order as one script that stops at the first failure. |
| `steps[].needs` | no | Steps that must succeed first. Steps without unmet needs run in parallel. |
| `steps[].env` | no | Plain environment variables. |
| `steps[].secrets` | no | Names of secrets from the Plumbix secrets file, exposed as environment variables to this step only. |
| `steps[].artifacts` | no | Workspace paths to keep as evidence after the step. |
| `steps[].cache` | no | Absolute container paths kept between runs of the repository (WU-029, A30), such as `/go/pkg/mod`, `/root/.cache/go-build` or `/root/.npm`: each is a directory under the data directory, kept per repository and step, mounted read-write. Plain steps only, at most 8; not the root, the workspace or inside it. Steps run in parallel, so two steps never share a writable cache even for the same path. A cache is never shared across repositories; a change can leave anything in its own repository's caches for later runs, which is the trust the branch already has. |
| `steps[].timeout` | no | Duration such as `10m`. Default comes from config. |
| `steps[].validator` | no | Marks the step as a validator: the image's entrypoint runs with the change's inputs mounted read-only at `/plumbix/in` and the workspace at `/workspace`, and must write `/plumbix/out/verdict.json` (4.3). Which model an AI validator asks comes from its reviewer profile (`reviewer:`), not from the file. |
| `steps[].effort` | no | On a validator only (WU-034): the reasoning effort of the model that judges this step, `low`, `medium` or `high`, overriding its reviewer profile's. Reaches the validator as `PLUMBIX_AI_EFFORT`; the Anthropic path sends it as the request's effort, the OpenAI-compatible path as `reasoning_effort`, dropped once with a log line for a vendor that refuses the field. |
| `steps[].advisory` | no | On a validator only (WU-030, A31): its verdict is recorded in the run in full, tagged `advisory`, and counted for nothing: it cannot approve, reject, request fixes, judge a criterion or call for a person, and the target branch does not require it. For judging every change with a second model (`reviewer: grok-4, advisory: true`) beside the one that decides. |
| `steps[].external` | no | On a validator only: the verdict comes from a reviewer outside the pipeline (WU-023, A26). No `image`, `run`, `env`, `secrets` or `retries`; `timeout` bounds the wait, one hour by default. Plumbix publishes a review brief and a reviewer posts the verdict through the API, typically `plumbix review --watch` on a machine with Claude Code. |
| `steps[].retries` | no | 0 to 5. A failed or timed-out step runs again up to this many more times; only the last attempt counts. The log gets a separator per attempt and the step record says how many attempts there were. |
| `steps[].allow_failure` | no | A failed or timed-out step does not fail the run and does not skip the steps that need it. Its status stays `failed` or `timed_out`, marked as allowed. A criterion that names the step is still unmet. Not on validators. |
| `steps[].when.paths` | no | Globs (`*`, `?`, `**`). For a change's run the step runs only if a changed file matches; otherwise it is `skipped` with reason `paths`, counts as satisfied for its dependents and as met for a criterion that names it. A plain branch run always runs it. Not on validators. |
| `steps[].disabled` | no | `{reason, note}` (WU-044, A36): the step does not run, and neither does its pipeline. `reason` is one of `user`, `docker`, `services`, `host`, `task`, `template`, `condition`, `checkout`, `deployment`, `secrets` (`pipeline.Reasons`, the schema's enum and the OpenAPI copy, kept equal by tests); `note` is free text up to 500 characters. A disabled step needs neither `image` nor `run`; its other fields keep their rules. |
| `stages` | no | Mapping of stage name to step names: a display grouping for the editor and run evidence. A step is in at most one stage; validators are never in one. |

Unknown fields are errors. There are no triggers, matrices or reusable templates in version 1. Every push runs the whole pipeline, minus steps whose paths did not change.

**Disabled steps** (WU-044, A36, A37): pipeline import keeps every step of the source pipeline and marks the ones Plumbix cannot run yet as disabled, with a reason. A pipeline holding any disabled step is disabled: the engine refuses it when it reads the file (`readPipeline`), so a change's run errors and the change waits for a person, and a branch run errors the same way; nothing reaches the runner. `POST /pipelines/check` reports `disabled` and `blockers` (disabled steps per reason). The editor dims disabled cards, names the reason on the card, lists the blockers above the canvas and lets a person disable a step with reason `user` and switch it back on; it never enables a step blocked for any other reason.

**Triggered pipelines** (WU-045, A36): every `.plumbix/pipelines/<name>.yaml` is a pipeline of its own, started by its `on` triggers rather than by changes. It is format v1 plus a required `on` (`push: {branches, paths}`, `manual: true`, or both; globs as in `when.paths`) and an optional pipeline-level `disabled`, whose reasons are the step reasons plus `trigger` (started in a way Plumbix cannot run yet: tags, schedules, manual parameters). It holds no validators, and `.plumbix/pipeline.yaml` refuses `on`. Parsed with `pipeline.ParseTriggered`. A run records its pipeline's name (`runs.pipeline`, `run.started`); the change pipeline's runs leave it empty.

**Policy source**: for a change, the list of required validators comes from the **target branch's** version of the pipeline file, not the change's version. An agent cannot remove or swap a validator in the same change it wants approved. A change that edits `.plumbix/` always ends in `needs_human`.

**Editing the file** (WU-020, A22): the web app's Pipelines screen edits a repository's file visually and saves it as a change. The server reads the file at the branch head (`GET /repos/{id}/pipeline`), the browser applies the edits to that file's text (WU-036, A34: the `yaml` package keeps comments, key order and scalar and list style; an unchanged model saves byte-identical, and the YAML view shows the text that will be committed), and `POST /repos/{id}/pipeline/changes` makes Plumbix commit that one file on the target head, push a new branch without force (an existing branch is refused) and submit the change, which validates with the target's pipeline and waits for a person like every `.plumbix/` change. The editor writes validators with `needs` of every non-validator step, so "validators run last" is literal in the file. The canvas is measured and every stage is laid out to its width (WU-038): when a stage's natural grid is wider, all stages take the columns that fit at 240 px or more per card, cards share the width up to 360 px, and a step past the last column starts a new row with its line dropping from the card it needs; the open inspector narrows the width instead of pushing cards aside, so only vertical scrolling remains. A validator's External switch (WU-037) makes it an external step: no image, commands, env, secrets, retries or effort, and its reviewer list offers only the review loop's `claude-code` profiles, while a container validator's list offers the other kinds; a reviewer the file names outside that list stays selected and marked, never changed silently.

### 4.3 Validator contract

Plumbix mounts these paths into a validator step's container (an `external` validator gets the same inputs, rendered into a review brief instead of a container; see below):

| Path | Access | Content |
|------|--------|---------|
| `/workspace` | read-only | The checked-out commit. |
| `/plumbix/in/change.json` | read-only | Intent, acceptance criteria, constraints, base and head commits, changed files, iteration number, previous findings. |
| `/plumbix/in/evidence/` | read-only | One folder per earlier step: `status.json` (exit code, duration, attempts, whether a failure was allowed, why a skipped step did not run), `log.txt`, `artifacts/`. |
| `/plumbix/out/` | write | The validator writes `verdict.json` here. |

`verdict.json`:

```json
{
  "plumbix": "verdict/v1",
  "outcome": "fix",
  "summary": "Tests pass but AC-2 is not covered.",
  "criteria": [
    {"id": "AC-1", "status": "met", "reason": "step 'test' passed", "evidence": ["test/log.txt"]},
    {"id": "AC-2", "status": "unmet", "reason": "no test exercises the timeout path"}
  ],
  "findings": [
    {"severity": "major", "criterion": "AC-2", "message": "Add a test for the timeout path.", "file": "internal/runner/run.go", "line": 88}
  ],
  "risks": [
    {"area": "tests", "message": "3 test files were modified.", "human_required": false}
  ]
}
```

- `outcome` is `approve`, `fix` or `reject`. Criterion `status` is `met`, `unmet` or `unknown`. Finding `severity` is `blocker`, `major` or `minor`. An optional `usage` block carries `model`, `input_tokens` and `output_tokens`, and for an agentic reviewer `turns` and `cost_usd` (the reviewer's own estimate); the step log ends with a verdict line built from it, for container and external validators alike.
- After the run, the engine appends the verdict's notes to every container validator's log (WU-039): the verdict line with the usage, the rest of the summary, and `finding` and `risk` lines, as an external step's log has them. `usage` may carry `cache_read_tokens` and `cache_write_tokens`, input read from and written to the provider's prompt cache; `input_tokens` then counts only uncached input.
- A validator that exits non-zero or writes an invalid file produces no verdict. That counts as a validator error, never as approval.
- Only steps marked `validator: true` get `/plumbix/out/`. Verdict files from other steps are ignored.
- An `external` validator (A26) runs nothing. When the run reaches it, the engine builds the AI validator's prompt from the same inputs and workspace, stores a review with the brief (prompt, verdict JSON schema with the change's criterion ids, head, base, iteration, repository URL, its `claude-code` reviewer profile's tool policy and model, the one the step names or the external family's default, else the built-in values; an API profile named on an external step is an error, since the loop runs Claude Code on its own subscription (A29)) and an expiry at the step's timeout, emits `review.requested`, and waits. A reviewer claims it with `GET /api/v1/reviews/next` (a fresh token per claim, `review.claimed`; never a change the reviewer submitted; a claim whose poller hung up before the brief reached it is given back, `review.released`) and posts the verdict with `POST /api/v1/reviews/{id}/verdict`; the verdict becomes the step's `verdict.json` and the same checks apply. Presence (WU-026, A27): the engine remembers each reviewer's last poll in memory; when a review is published and nobody whose filter covers the repository polled within two minutes, an alert of kind `no_reviewer` is raised for the repository (one at a time, resolved by the next such poll) and the step log says to start a loop; a review that stays pending is checked at every poll of the waiting step, so a loop that dies mid-wait raises it too. `plumbix review install` keeps a loop alive as a login service (a launchd agent on macOS, a systemd user unit on Linux) with the installer's PATH so `claude` is found; `uninstall` and `status` complete it. The server never spawns the loop. `plumbix review --watch` is the reference reviewer: it long-polls, checks the head out in a worktree under `<data_dir>/review/` with the user's own git access, runs `claude -p --output-format stream-json` there with the brief's system prompt, user prompt and JSON schema, posting each tool use Claude makes as a progress line (`turn 3: Read internal/engine/external.go`) so the step log shows the review working (WU-027), (`--restricted` with `Read`, `Grep` and `Glob` under the read-only policy; `Bash` added under full, never `Edit`), bounded turns and the review's expiry, and posts the structured output as the verdict. It uses whatever authentication Claude Code already has, a subscription included, so it never passes `--bare`. No verdict by the timeout, or a run cancelled meanwhile, leaves the step `timed_out` or `cancelled`: a validator error, never approval.
- `change.json` may carry `guidance: {global, repository}`: instructions from the Plumbix operator, set in the settings, never taken from the repository. Validators may follow them; the AI validator renders them as "Operator guidance" outside the untrusted block. Validator steps also receive their reviewer profile (WU-028, A28, A29: the API profile the step names with `reviewer:`, else the api family's default) as `PLUMBIX_AI_MODEL`, for `openai` kinds `PLUMBIX_AI_PROVIDER` and `PLUMBIX_AI_BASE_URL`, for `claude-code-image` kinds `PLUMBIX_AI_TOOLS`, and the value of the profile's key secret as `ANTHROPIC_API_KEY` or `PLUMBIX_AI_API_KEY` by kind; a secret the step lists itself under the same name wins (`PLUMBIX_` names are reserved, so only the key can be overridden). A step naming a profile nobody defined, or a `claude-code` profile on a container step, errors, so the run ends `needs_human` instead of judging with another model.
- The `plumbix-claude-code-validator` image (WU-035, A33) is Claude Code inside the pipeline: `validators/claude-code` builds the same prompt and schema as the AI validator, runs `claude -p` in `/workspace` through `internal/claudecode`, the runner the review loop uses too (stream-json output, each tool use a progress line in the step log, `--restricted` with `Read`, `Grep` and `Glob` under the read-only policy, `Bash` added under full, never `Edit`, `PLUMBIX_AI_MAX_TURNS` turns, 40 by default), checks the structured output with the same verdict rules and writes `verdict.json` with turns and cost in `usage`. It authenticates with `PLUMBIX_AI_API_KEY` from a `claude-code-image` profile's key secret: a value starting with `sk-ant-oat` (a token from `claude setup-token`, the person's subscription) reaches the CLI as `CLAUDE_CODE_OAUTH_TOKEN`, anything else as `ANTHROPIC_API_KEY`; no login is baked into the image, and the value is never printed. The raw `PLUMBIX_AI_API_KEY` is dropped from the environment the CLI inherits, so the credential exists there under one name only. The workspace is read-only, so under the full policy commands may read and search but not build; the image carries Node and git, not the repository's toolchains. Full tools on this kind means commands run in a container holding the token, and a steered command could read it: the default is read-only, the settings page says so, and the operator accepts the rest knowingly. `effort:` has no effect on this kind.

### 4.4 Events

Every fact Plumbix records is also an event: `{id, type, time, repo_id?, change_id?, run_id?, payload}`, with the payload of each type in `contracts/events.schema.json`. Types: `change.submitted`, `change.state_changed`, `change.merged`, `change.approved_by_person`, `change.refreshed`, `run.started`, `step.started`, `step.finished`, `run.finished`, `verdict.issued`, `alert.raised`, `alert.resolved`, `review.requested`, `review.claimed`, `review.released`. (`iteration.started` from the first draft was dropped: a change's run is its iteration, and `run.started` carries `change_id` and `iteration`.)

The store is the event source (A16): the write that records a fact appends its event in the same transaction, so no write path can forget one and a crash never leaves data without its event. `step.started`, `step.finished` and `change.refreshed` have no row of their own; the engine appends them (a step is recorded in the run as `running` when it starts, WU-027, and the record is replaced when it ends) (the refresh's fact lives in git, as the pushed merge commit). Emitting writes are serialised, so ids are published in order. The hub (`events.Hub`) fans events out to open streams and never blocks a writer: a stream that falls behind is cut off and resumes from the store.

`GET /api/v1/events` serves the stream as Server-Sent Events with `id`, `event` and `data` lines. It replays everything after `Last-Event-ID` (or `after`) before following live, filtered by `repo_id` and `change_id`, and sends a keep-alive comment every 15 seconds. Open streams end when the server stops. Events are notifications: the other endpoints stay the source of truth. Events are not pruned in v1. Outgoing webhooks come later, once Plumbix has authentication.

### 4.5 Host boundary (internal)

The host boundary is not a public contract. It is kept narrow because git itself does most of the work.

```go
type Host interface {
    Kind() string                                        // "github", "azuredevops"
    RemoteURL(repo Repo) string                          // HTTPS clone URL
    Credentials(repo Repo) (user, token string, err error)
    TokenExpiry(ctx context.Context) (time.Time, error)  // for expiry warnings
}
```

Branch heads come from `git ls-remote`, fetches and pushes from `git` with credentials passed through `GIT_ASKPASS`. This works the same on both hosts. Each adapter only knows its URL format, its credential style and how to read token expiry. GitHub reports expiry on every authenticated API call; Azure DevOps reports none, so its adapter reads the date the user recorded in the secrets file. Status reporting back to the host (GitHub commit statuses, Azure DevOps PR statuses) is a later feature.

## 5. Main flows

### 5.1 Submit and validate a change

1. A client posts the change. The API validates it and the change service stores it in state `open`, emitting `change.submitted`.
2. Git ops fetches the source branch into the repo's mirror and records the head commit as iteration 1.
3. The pipeline package reads the pipeline file from that commit, and the validator policy from the target branch.
4. The scheduler starts every step whose needs are met, up to `runner.max_parallel` at once.
5. The runner creates a worktree, starts each step's container, streams its log, and saves evidence when it ends.
6. Validator steps run with their inputs mounted. The validation package reads each `verdict.json`. An `external` validator waits for its verdict from outside instead (4.3).
7. Verdicts are combined and the change moves to its outcome state. `verdict.issued` and `change.state_changed` are emitted.

### 5.2 Fix loop

1. The client reads the findings from the API or the event stream.
2. Its agent pushes new commits to the source branch.
3. The poller sees the new head and starts iteration N+1. The previous findings are included in the validator input.
4. If a fourth fix round would be needed, the change moves to `escalated`.

### 5.3 Push without a change

The poller sees a new head on any branch of a registered repo. If an open change uses that branch, this is the fix loop. Otherwise Plumbix starts a standalone run and shows the results without a verdict.

Independently, every head move, Plumbix's own merges included, goes to `Engine.RunPushed` (WU-045), all moves of one poll together and with one fetch: for each it reads the triggered pipelines at the new head and starts each enabled one whose `push.branches` match the branch and, when `paths` is set, whose paths match a file changed since the previous head (a new branch always matches). Each run is pinned to the pushed commit; a newer run of the same pipeline on the same branch supersedes it. Disabled and invalid pipelines start nothing and are logged.

### 5.4 Merge

1. A human merges an `approved` change. While the merge runs, no new validation can start for it.
2. After a fresh fetch, the source branch must still point at the validated commit (`source_moved` otherwise) and the target branch must be the run's base, which means the source already contains the target and the target has not moved (`target_moved` otherwise). This guarantees that the tree landing on the target is exactly the tree the validators judged, so Plumbix never computes a merge and never meets a conflict (A15).
3. Git ops builds the commit in the bare mirror with `git commit-tree`: `squash` is one commit with the validated tree on the old target head, `merge` is a merge commit with the same tree, `rebase` is a fast-forward to the source head. Author is the source head's author, committer is Plumbix, and the message carries `Plumbix-Change` and `Plumbix-Run` trailers.
4. The push uses `--force-with-lease=<branch>:<old>`; if the remote moved meanwhile the merge fails with `push_rejected`, and if the host refuses the credentials (no token, a bad one, or one without push rights) it fails with `push_denied`. Either way nothing changes.
5. The merge is recorded (`merges` table, shown as `merge` on the change) and the change moves to `merged`.
6. A `target_moved` refusal on a branch Plumbix created is answered by Update from main (`POST /changes/{id}/refresh`): after a fresh fetch, every first-parent commit between the merge base and the branch head must be Plumbix's (`not_plumbix_branch` otherwise), the branch must not already contain the target (`already_current`), `git merge-tree --write-tree` must merge cleanly (`merge_conflict` names the paths), and the merge commit, with Plumbix as author and both heads as parents, is pushed under a lease on the old head. Then `change.refreshed` is recorded and a new iteration starts, whose base is the new target head, so the next merge is a fast path again. Branches with anyone else's commit are never touched: their authors bring the target in.

### 5.5 Bypass detection

When the poller sees a protected branch move, it lists the first-parent commits between the old and new head (only the tip for a branch it has never seen). Commits inside a Plumbix merge's range (`previous..commit`) are Plumbix's own. Any other commit is a bypass: one `bypass_commit` alert per push names the branch and each commit's sha, author, time and subject. A branch moved only by Plumbix merges starts no plain run, since its tree was validated already. Alerts are deduplicated per kind and key (`branch@head` for bypasses, the host kind for token expiry), and token alerts resolve themselves once the token is healthy.

## 6. Storage and files

Everything lives under the data directory, config key `data_dir`, default `~/.plumbix`. When developing Plumbix itself, `secrets_file` points at the git-ignored `.env` in the repo root, with `.env.example` as the committed template.

| Path | Content |
|------|---------|
| `config.yaml` | Server settings. |
| `api-token` | The API bearer token, 64 hex characters. Created on first start, mode `0600`. Delete it and restart to rotate. |
| `.env` | Host tokens and pipeline secrets in dotenv format. Path set by config `secrets_file`. Must be mode `0600` or the server refuses to start. |
| `plumbix.db` | SQLite: repos, changes, iterations, runs, steps, verdicts, events, alerts, merges. |
| `mirrors/<repo-id>.git` | Bare mirrors. |
| `work/<run-id>/` | Temporary worktrees, deleted after the run. |
| `evidence/<run-id>/<step>/` | Logs, status and artifacts. Kept until a configurable retention limit. |
| `images/builds/<id>.log` | The output of an image build started from the Images page. |
| `images/own/<name>/Dockerfile` | An own image's Dockerfile; the directory is its build context. |
| `cache/<repo-id>/<step>/<slug>/` | A step cache (`steps[].cache`), one directory per repository, step and container path, named after the path plus a short hash. Nothing prunes it; deleting a directory resets that cache. |

Key config entries, with defaults:

| Key | Default |
|-----|---------|
| `server.listen` | `127.0.0.1:6400` |
| `runner.max_parallel` | number of CPU cores |
| `runner.default_timeout` | `30m` |
| `poller.interval` | `30s` |
| `fix_rounds.max` | `3` |
| `evidence.retention_days` | `30` |

## 7. Cross-cutting concerns

- **Security**: The server binds only to loopback and refuses any other address (Q12). The API also needs the local token from a file only the owner can read (A14), because Docker Desktop containers can reach the host's loopback. Tokens are never logged, and a log filter masks known secret values. Each step only gets the secrets it names. Validator output goes only through files. The pipeline policy comes from the target branch (4.2).
- **Container isolation**: Every step runs in a fresh container with the workspace mounted, a CPU and memory limit, and no Docker socket. Steps get outbound network access so they can download dependencies.
- **Logging**: Structured logs through Go's `log/slog`, written to standard error.
- **Errors**: Host or Docker failures mark the affected run `errored`, which is never a verdict. The poller backs off on host errors and raises an alert after repeated failures.
- **Crash recovery**: On startup, runs left `running` are marked `errored` and their worktrees removed.
- **Testing**: Unit tests per package. Contract tests check API and verdict output against the schemas. Git ops tests use local bare repos, with no network. Host adapters are tested against fake HTTP servers. Docker tests carry the `integration` build tag. One end-to-end test drives a whole change through a local bare repo standing in for the host.

## 8. Decisions

These are implementation calls made in this architecture step, with the reasons and the rejected options.

| # | Decision | Why | Rejected |
|---|----------|-----|----------|
| A1 | One process for v1, with the runner behind an interface the scheduler calls | Simplest install. The interface keeps remote runners possible later (Q04). | Separate runner process now |
| A2 | SQLite through the pure-Go `modernc.org/sqlite` driver | No database server to install, no C compiler needed, simple cross-compilation. | PostgreSQL (extra service), embedded key-value stores (weak querying) |
| A3 | Git operations through the system `git` command | Merges, rebases and worktrees are complete and battle-tested there. Adds one install requirement. | `go-git` (incomplete merge support) |
| A4 | Docker Engine API through the official Go client; Docker-compatible engines work if they serve the same API | Most widely installed container engine. | Shelling out to `docker`, containerd directly |
| A5 | Events as an SSE stream with resumable ids | Works with plain HTTP, easy for any client, no broker. | WebSockets, outgoing webhooks before authentication exists |
| A6 | React with TypeScript and Vite, embedded with `go:embed` | Largest contributor pool for an open-source interface, single binary stays intact. | Svelte, server-rendered templates |
| A7 | JSON Schemas and an OpenAPI file are the source of truth, with hand-written Go types checked by contract tests | Keeps contracts readable and language-neutral without a code generator in the build. | Generating Go code from OpenAPI |
| A8 | Branch polling with `git ls-remote` for both hosts | Same code for every host, and it doesn't use the hosts' REST API quotas. | Each host's REST API |
| A9 | Required validators come from the target branch's pipeline file, and `.plumbix/` edits always need a human | Stops an agent from removing the checks that judge it. | Using the change's own pipeline file |
| A10 | Squash merge by default, configurable per repo | Agent commit histories are noisy. One commit per change matches one verdict per change. | Merge commits by default |
| A11 | Reference validator v0 is deterministic: each criterion's `check` names steps or a command, and criteria without a check are `unknown` | Free and reproducible. `unknown` sends the change to a human instead of guessing. | Relying on AI judgment alone |
| A12 | An AI validator ships in milestone 1 as a separate validator image. It uses Anthropic's Go SDK with structured JSON output, model `claude-opus-5-5` by default (`PLUMBIX_AI_MODEL`), effort `high` (`PLUMBIX_AI_EFFORT`), and server-side refusal fallbacks. Repository content goes in a randomly named untrusted block | User approved the paid service (2026-10-15). It catches what deterministic checks miss, such as weakened tests or criteria without a check. Keeping it a pluggable step keeps the core free of AI (Q07). | AI inside the Plumbix core, a provider-neutral wrapper library |
| A13 | Secrets live in a dotenv file (`secrets_file`), mode `0600` | The user asked for a `.env` file. Dotenv is the most familiar format for open-source users. | A YAML secrets file |
| A14 | The API needs a local bearer token, generated on first start in `<data_dir>/api-token` (mode `0600`). The browser gets a one-time login link with the web interface (WU-011) | Owner's choice (option A) after WU-003 showed Docker Desktop containers reach host loopback. Containers never see the data directory, so they can't authenticate. | No network for steps by default (partial fix, breaks dependency downloads), accepting the exposure |
| A15 | A change merges only when its source branch already contains the target's head, and the target lands the validated tree byte for byte | The verdict decides what merges, so what merges must be what was judged. A merge Plumbix computed after validation would be untested code. The client brings the target into the source and gets a fresh verdict instead. | Merging as long as the target did not move (lands an untested tree when the source was behind), letting Plumbix resolve merges |
| A16 | The store appends each event inside the transaction of the write it describes; services and the engine never emit events themselves, except `step.started` and `step.finished`, which have no write of their own | Every write path is covered by construction, data and event cannot disagree, and a client resuming from its last id misses nothing. | Emitting from services (easy to forget, lost on a crash between write and emit), a separate message broker |
| A17 | Browser sign-in is a one-time link minted by the token holder and exchanged once for an HttpOnly, SameSite=Strict cookie; cookie requests must carry the server's own Host and, on state changes, Origin | A browser cannot hold the file token. The link keeps the token out of the browser, the cookie keeps it out of page scripts, and the Host and Origin checks stop a rebinding or cross-site page from using it. | Pasting the token into the page (lands in browser storage), token in the URL on every request, no browser at all |
| A18 | Tokens and other secrets are set from the browser into the server-owned secrets file, write-only, and reloaded live | After install everything is configured in one place; the server rewrites the dotenv file with mode 0600, never returns a value, and reads secrets and host adapters through a manager on every use, so no restart is needed. A browser session can already merge code, so this widens nothing. | Encrypted storage in SQLite (more code, little extra safety on one machine), restart after editing the file by hand |
| A19 | AI tuning from the web is review guidance (global and per repository) plus the model; the effort knob and the full prompt stay out | Guidance is operator instruction to any validator and travels in the validator input, separate from repository content, so the core still holds no AI (Q07). The model is one environment variable the AI validator already reads. Editing the whole prompt would break the verdict format silently. | Full prompt editing, an effort setting (not wanted) |
| A21 | `needs_human`, `escalated` and `rejected` are sticky: only a person's Validate again or Close moves them, never a push | Those states exist to stop the loop until a person looks. Revalidating on push let an agent push through an escalation, and let an operator's own error be "fixed" without anyone looking. | A separate `blocked` state for infrastructure failures that would retry on push (a missing image or failed fetch is not fixed by pushing code) |
| A22 | The pipeline editor is one client of the API: the browser builds the YAML (since A34 by editing the served file's text), the server owns the commit, the push and the change. Validators it writes need every plain step. | The format's parser stays the single judge of a file, host credentials stay on the server, and a validator that needs every step says in the file when it runs. | A server-side YAML editor (duplicates the browser's model); a browser-side push (needs host credentials in the client) |
| A23 | A person approves a `needs_human` or `escalated` change with a required reason; merge stays a separate action | Those states exist for a person to look; without a way to say "looked, fine" nothing that edits `.plumbix/` could ever merge through Plumbix. The reason is the audit trail. | Moving the pipeline out of `.plumbix/` (the rule is about editing the policy, not the folder); approve-and-merge in one step; letting a person approve `rejected` |
| A24 | Plumbix brings the target into branches it created itself (`plumbix_branch`), on a person's request, with a real merge commit pushed under a lease; a refresh starts a new run and a standing approval is given again | The editor's branches are Plumbix's own work, so refreshing them is editing its own work, and the only alternative was a terminal. The approval named a head; after the refresh the merge would land a different tree, so a person looks once more, which is cheap and keeps the audit honest. | Refreshing any change's branch (an agent's branch is the agent's; Plumbix would be pushing merge commits onto work it does not own); carrying an approval across a refresh when the diff is unchanged (the landed tree still differs, and a machine would be re-approving for a person); refreshing by rebase (rewrites commits a run already judged and needs a forced push) |
| A25 | The AI validator asks Claude through Anthropic's SDK or any OpenAI-compatible endpoint through one small client of its own (`PLUMBIX_AI_PROVIDER`, `PLUMBIX_AI_BASE_URL`, the key in `PLUMBIX_AI_API_KEY` or `OPENAI_API_KEY`); a vendor that rejects `json_schema` gets `json_object`, then a plain request, and the prompt carries the schema either way | Nearly every vendor speaks the OpenAI chat format (OpenAI, Gemini's compatible endpoint, xAI, Moonshot, DeepSeek, Groq, Ollama, vLLM), so one code path covers them; the verdict parser already refuses malformed output, so a weak vendor degrades to a validator error and never to a false approval. Supersedes A12's Anthropic-only wording; the core still holds no model client. | A native SDK per vendor (one client per vendor to maintain); a provider-neutral wrapper library (a dependency for a format this small) |
| A26 | AI review by pull: an `external` validator step publishes a review brief and waits; a reviewer outside the pipeline (a Claude Code session with the Plumbix MCP, or `plumbix review --watch` wherever Claude Code is installed) claims it and posts the verdict. The server stays the author of the prompt and the gate on the verdict; the token is minted per claim; a reviewer never gets its own change | Claude Code cannot be called back by an MCP server (no sampling support), so the direction is pull. People use the Claude they already have, with no OAuth token in a container and no Node image, and the core still holds no AI (Q07): it publishes a prompt and checks a verdict. | Waiting for sampling support; a containerized Claude Code image first (kept as a later item for unattended servers); letting the reviewer post without a claim; handing a session its own change's review |
| A27 | The server observes reviewers and alerts when none is there; the operating system keeps the loop alive (`plumbix review install`); the server never spawns it | Every poll is already a heartbeat, so presence and a `no_reviewer` alert cost little and tell a person within minutes instead of after the step's timeout. A loop the server spawned would die with every server restart, works only where the server's machine has a Claude login, and on a service-account deployment would review as the server with no person behind it. | The server starting the loop as a child process (kept possible as a single-machine opt-in later, not the mechanism); persisting presence (a restarted server has heard from nobody, which is the truth) |
| A28 | A reviewer profile (name, kind anthropic or openai, base URL, model, tool policy, key secret name) is a row of its own; a validator step binds to one with `reviewer: <name>`, the default applies otherwise; the key stays a secret in the existing store, set through the write-only secret endpoint | Several models can judge side by side, each with its own key and endpoint; keys never travel through settings rows or API responses; a pipeline file holds a name, never a URL or key. | One global provider and model (WU-025, superseded), keys as profile fields, profiles inside the pipeline file |
| A29 | Claude Code is its own reviewer kind (`claude-code`, no key, tools), beside the API kinds `anthropic` and `openai`; each family has its own default profile, and a step binds within its family only | They are different reviewers: the API is billed per token to a key the operator holds, Claude Code runs on a person's machine on its subscription quota with tools. One profile serving both (A28's first form) hid the cost difference and made "the default" ambiguous, which surprised the owner. | One default with a family-dependent fallback; a per-step flag instead of a kind; supersedes that part of A28 |
| A30 | Step caches are host directories kept per repository and step, named by container path, mounted read-write into plain steps only | Dependencies downloaded on every run were most of a step's time; a per-repository cache is the smallest mechanism that removes that, keeps repositories apart, and asks nothing of the image. A validator must not depend on what an earlier change left behind, so it gets none. The accepted risk, a change poisoning its own repository's cache for later runs, is the trust the branch already has in a single-operator tool, and a cache can be deleted. | Named presets (`go`, `npm`) that guess paths per image; per-branch caches (cold on every first run); content-addressed caches |
| A31 | An advisory validator's verdict is recorded in full and tagged, and never counted: not the state, not a criterion, not the need for a person, not the required list | A second model can judge every change for comparison without being able to block or approve one; weighting it in would defeat the comparison, hiding it would too. The flag is on the step, not the profile, since the same model may be advisory in one repository and binding in another. | Weighted verdicts; advisory as a profile property; hiding advisory output from the page |
| A32 | Plumbix owns the images its pipelines run: an Images page inspects them and builds the shipped two from sources embedded in the binary; own Dockerfiles will live in Settings, never in the judged repository; a build is a person's act | Images were opaque and unmanaged, and a stale shipped validator went unnoticed. A validator image must not come from the repository it judges, or a branch could rewrite its own judge. A build is arbitrary code on the server's Docker daemon outside the step sandbox, which for a single operator is the trust a pipeline edit already carries, said plainly on the page. | A `plumbix images` command alone; validator Dockerfiles in the repository; a build sandbox |
| A33 | Claude Code also judges from inside the pipeline: a third shipped image, `plumbix-claude-code-validator`, runs `claude -p` through the runner the review loop uses, on a long-lived token the person mints (`claude setup-token`) or an API key, as reviewer kind `claude-code-image` in the api family | An unattended server with no loop signed in had no Claude Code review; Claude Code and Grok could not sit in one run. One shared runner keeps the loop and the image judging the same way. The token is the person's subscription, handled like a key: a named secret, never in the image or a log. | Making the external `claude-code` kind also run in containers (two auth stories under one name, right after A29 split them); baking a login into the image; a reasoning effort for this kind now (the CLI's effort flag is not relied on yet) |
| A34 | The pipeline editor edits the served file's text with the `yaml` package in the browser: the model is applied as targeted edits on a document that keeps comments, key order, scalar style and list style; an unchanged model saves byte-identical; the YAML view shows the text that will be committed; the from-scratch writer stays as the fallback for a file the library cannot read | Every save regenerated the file and stripped its documentation (the header stating A12 twice) and reformatted untouched lines; both reviewers flagged a one-word change for it, and the person saving cannot control what the editor writes. | A hand-written line patcher (brittle on block scalars, anchors, nested maps); merging on the server with yaml.v3 (the browser preview would differ from the commit, the same work twice); asking people to restore comments by hand |
| A35 | Repositories can be imported from the Repositories page: a local folder with a remote (its git metadata read once to prefill the registration), a remote cloned into a local folder, and (WU-042) a local folder published to a new remote; paths resolve inside the server user's home directory and are read through git only | The owner wanted to bring repositories in from the browser, not only with `plumbix repo add`; the server is local and single-person, so reading a folder it is pointed at is the trust of the CLI, bounded by the home directory. Plumbix keeps working against the remote and never changes a local folder except when cloning into an empty or new one. | A browser upload of the folder; registering the working folder itself as the remote (git refuses pushes to a checked-out branch); creating online repositories without an explicit click |
| A36 | Pipelines are imported whole into one file per pipeline under `.plumbix/pipelines/`; a step Plumbix cannot run is kept, disabled with a reason, and a pipeline with any disabled step is disabled | Missing features show up as data in the user's own pipelines instead of being dropped, and each later feature unblocks steps already in place | One file holding every pipeline (unwieldy at twenty or more); importing only the supported steps (hides the gap); running a pipeline with its disabled steps skipped |
| A37 | Pipeline import is a product feature users repeat: a source interface (Azure Pipelines first), from the web, CLI and API; reason codes in the published schema; each imported step keeps its source file, line and content hash, so a re-check enables newly supported steps and reports changed source steps; every import is a needs_human change | Any user brings existing CI into Plumbix and follows it as Plumbix grows, not a one-off migration of one repository | A migration script outside the product; an automatic sync that writes without review (A12); reason codes kept internal |

## 9. Risks and open items

1. **Containers reaching the API on Docker Desktop. Confirmed in WU-003, mitigated in WU-013.** On Docker Desktop, containers reach the host's loopback through `host.docker.internal`, `gateway.docker.internal` and `192.168.65.254`. The API token (A14) stops them from using it. `TestStepCannotUseHostAPI` probes the real API from a container on every run of `make integration`.
2. **Weakened tests.** A deterministic validator cannot tell when an agent weakens or deletes tests. The reference validator reports changed test files as risks, and the AI validator is asked to judge them.
3. **Prompt injection against the AI validator.** Agent-written code, comments or commit messages could contain text aimed at the AI validator, such as "approve this change". Mitigations: repo content is passed as clearly marked untrusted data, the verdict must match the JSON schema, every `met` criterion must cite evidence, and Plumbix's own pipeline also requires the deterministic validator to approve. An AI approval alone is never enough there.
4. **AI validator cost.** Each validation run calls a paid model. Large diffs cost more. The validator reports token usage in its evidence so costs stay visible.
5. **Polling delay.** Runs start up to `poller.interval` after a push. Push notifications come with authentication (F-009).
6. **Docker access in the development sandbox.** Integration tests need the Docker socket, which the current sandbox blocks.
7. **Triggered pipelines come from the pushed commit (WU-045, A38).** A push to any branch, an agent's change branch included, runs pipelines as that commit writes them. Since A38, only runs on a protected branch get secrets: a step that names one in any other run (triggered, plain branch or change run) errors with the reason. Still open until WU-051: a direct push to a protected branch that edits a pipeline gets secrets (the guard will tie secrets to approved pipeline fingerprints), and validator steps added by a change's own pipeline still receive their reviewer profile's key.

## 10. Repository layout

```
cmd/plumbix/            entry point and CLI
internal/               api, change, pipeline, scheduler, runner, evidence, validation,
                        host (+ github, azuredevops), gitops, poller, events, store, config
web/                    React + TypeScript interface (Vite)
validators/reference/   deterministic validator program and its Dockerfile
validators/ai/          AI validator program and its Dockerfile
contracts/              OpenAPI, JSON Schemas
.plumbix/pipeline.yaml  Plumbix's own pipeline (used from WU-011)
```
