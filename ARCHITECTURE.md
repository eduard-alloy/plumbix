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
| CLI | `cmd/plumbix` | Entry point. `serve` starts the server. Other subcommands (`submit`, `repo add`, `pipeline check`) are thin HTTP clients of the public API. |
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
| AI validator | `validators/ai/` | A Go program shipped as a container image. It asks a Claude model to judge the change against its intent and criteria, and writes the same verdict file. |

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
| POST | `/api/v1/changes` | Submit a change: repo, source and target branch, title, intent, acceptance criteria, constraints, submitted-by. |
| GET | `/api/v1/changes` | List changes as `{"changes": [...]}`, filtered by `repo_id` and `state`. |
| GET | `/api/v1/changes/{id}` | One change with its iterations, latest outcome and findings. |
| GET | `/api/v1/changes/{id}/diff` | The diff the validators judged: base..head of the latest run with a head, parsed into files and hunks; `stat=true` for counts only. |
| POST | `/api/v1/changes/{id}/merge` | Merge an approved change. Fails if the target branch moved since validation. |
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
| GET, PUT | `/api/v1/settings` | AI model, global review guidance, and which secrets are set (never their values). |
| PUT, DELETE | `/api/v1/settings/secrets/{key}` | Set or remove a secret in the server's secrets file; used from the next fetch on. |
| POST | `/api/v1/settings/secrets/{key}/check` | Run a git host's token check now. |
| GET, PUT | `/api/v1/repos/{id}/settings` | One repository's review guidance. |

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

| Field | Required | Meaning |
|-------|----------|---------|
| `version` | yes | Must be `1`. |
| `steps[].name` | yes | Unique, lowercase letters, digits and dashes. |
| `steps[].image` | yes | Container image. |
| `steps[].run` | yes, except validators | Shell script, run with `sh -c`. |
| `steps[].needs` | no | Steps that must succeed first. Steps without unmet needs run in parallel. |
| `steps[].env` | no | Plain environment variables. |
| `steps[].secrets` | no | Names of secrets from the Plumbix secrets file, exposed as environment variables to this step only. |
| `steps[].artifacts` | no | Workspace paths to keep as evidence after the step. |
| `steps[].timeout` | no | Duration such as `10m`. Default comes from config. |
| `steps[].validator` | no | Marks the step as a validator. It gets validator inputs and must write a verdict. |

Unknown fields are errors. There are no triggers, conditions, matrices or reusable templates in version 1. Every push runs the whole pipeline.

**Policy source**: for a change, the list of required validators comes from the **target branch's** version of the pipeline file, not the change's version. An agent cannot remove or swap a validator in the same change it wants approved. A change that edits `.plumbix/` always ends in `needs_human`.

### 4.3 Validator contract

Plumbix mounts these paths into a validator step's container:

| Path | Access | Content |
|------|--------|---------|
| `/workspace` | read-only | The checked-out commit. |
| `/plumbix/in/change.json` | read-only | Intent, acceptance criteria, constraints, base and head commits, changed files, iteration number, previous findings. |
| `/plumbix/in/evidence/` | read-only | One folder per earlier step: `status.json` (exit code, duration), `log.txt`, `artifacts/`. |
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

- `outcome` is `approve`, `fix` or `reject`. Criterion `status` is `met`, `unmet` or `unknown`. Finding `severity` is `blocker`, `major` or `minor`.
- A validator that exits non-zero or writes an invalid file produces no verdict. That counts as a validator error, never as approval.
- Only steps marked `validator: true` get `/plumbix/out/`. Verdict files from other steps are ignored.
- `change.json` may carry `guidance: {global, repository}`: instructions from the Plumbix operator, set in the settings, never taken from the repository. Validators may follow them; the AI validator renders them as "Operator guidance" outside the untrusted block. Validator steps also receive `PLUMBIX_AI_MODEL` when the operator set a model, unless a secret of that name already supplies one.

### 4.4 Events

Every fact Plumbix records is also an event: `{id, type, time, repo_id?, change_id?, run_id?, payload}`, with the payload of each type in `contracts/events.schema.json`. Types: `change.submitted`, `change.state_changed`, `change.merged`, `run.started`, `step.finished`, `run.finished`, `verdict.issued`, `alert.raised`, `alert.resolved`. (`iteration.started` from the first draft was dropped: a change's run is its iteration, and `run.started` carries `change_id` and `iteration`.)

The store is the event source (A16): the write that records a fact appends its event in the same transaction, so no write path can forget one and a crash never leaves data without its event. `step.finished` is the one event with no write of its own; the engine appends it. Emitting writes are serialised, so ids are published in order. The hub (`events.Hub`) fans events out to open streams and never blocks a writer: a stream that falls behind is cut off and resumes from the store.

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
6. Validator steps run with their inputs mounted. The validation package reads each `verdict.json`.
7. Verdicts are combined and the change moves to its outcome state. `verdict.issued` and `change.state_changed` are emitted.

### 5.2 Fix loop

1. The client reads the findings from the API or the event stream.
2. Its agent pushes new commits to the source branch.
3. The poller sees the new head and starts iteration N+1. The previous findings are included in the validator input.
4. If a fourth fix round would be needed, the change moves to `escalated`.

### 5.3 Push without a change

The poller sees a new head on any branch of a registered repo. If an open change uses that branch, this is the fix loop. Otherwise Plumbix starts a standalone run and shows the results without a verdict.

### 5.4 Merge

1. A human merges an `approved` change. While the merge runs, no new validation can start for it.
2. After a fresh fetch, the source branch must still point at the validated commit (`source_moved` otherwise) and the target branch must be the run's base, which means the source already contains the target and the target has not moved (`target_moved` otherwise). This guarantees that the tree landing on the target is exactly the tree the validators judged, so Plumbix never computes a merge and never meets a conflict (A15).
3. Git ops builds the commit in the bare mirror with `git commit-tree`: `squash` is one commit with the validated tree on the old target head, `merge` is a merge commit with the same tree, `rebase` is a fast-forward to the source head. Author is the source head's author, committer is Plumbix, and the message carries `Plumbix-Change` and `Plumbix-Run` trailers.
4. The push uses `--force-with-lease=<branch>:<old>`; if the remote moved meanwhile the merge fails with `push_rejected` and nothing changes.
5. The merge is recorded (`merges` table, shown as `merge` on the change) and the change moves to `merged`.

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
| A17 | Browser sign-in is a one-time link minted by the token holder and exchanged once for an HttpOnly, SameSite=Strict cookie; cookie requests must carry the server's own Host and, on state changes, Origin | A browser cannot hold the file token. The link keeps the token out of the browser, the cookie keeps it out of page scripts, and the Host and Origin checks stop a rebinding or cross-site page from using it. | Pasting the token into the page (lands in browser storage), token in the URL on every request, no browser at all |
| A18 | Tokens and other secrets are set from the browser into the server-owned secrets file, write-only, and reloaded live | After install everything is configured in one place; the server rewrites the dotenv file with mode 0600, never returns a value, and reads secrets and host adapters through a manager on every use, so no restart is needed. A browser session can already merge code, so this widens nothing. | Encrypted storage in SQLite (more code, little extra safety on one machine), restart after editing the file by hand |
| A19 | AI tuning from the web is review guidance (global and per repository) plus the model; the effort knob and the full prompt stay out | Guidance is operator instruction to any validator and travels in the validator input, separate from repository content, so the core still holds no AI (Q07). The model is one environment variable the AI validator already reads. Editing the whole prompt would break the verdict format silently. | Full prompt editing, an effort setting (not wanted) |
| A16 | The store appends each event inside the transaction of the write it describes; services and the engine never emit events themselves, except `step.finished`, which has no write of its own | Every write path is covered by construction, data and event cannot disagree, and a client resuming from its last id misses nothing. | Emitting from services (easy to forget, lost on a crash between write and emit), a separate message broker |

## 9. Risks and open items

1. **Containers reaching the API on Docker Desktop. Confirmed in WU-003, mitigated in WU-013.** On Docker Desktop, containers reach the host's loopback through `host.docker.internal`, `gateway.docker.internal` and `192.168.65.254`. The API token (A14) stops them from using it. `TestStepCannotUseHostAPI` probes the real API from a container on every run of `make integration`.
2. **Weakened tests.** A deterministic validator cannot tell when an agent weakens or deletes tests. The reference validator reports changed test files as risks, and the AI validator is asked to judge them.
3. **Prompt injection against the AI validator.** Agent-written code, comments or commit messages could contain text aimed at the AI validator, such as "approve this change". Mitigations: repo content is passed as clearly marked untrusted data, the verdict must match the JSON schema, every `met` criterion must cite evidence, and Plumbix's own pipeline also requires the deterministic validator to approve. An AI approval alone is never enough there.
4. **AI validator cost.** Each validation run calls a paid model. Large diffs cost more. The validator reports token usage in its evidence so costs stay visible.
5. **Polling delay.** Runs start up to `poller.interval` after a push. Push notifications come with authentication (F-009).
6. **Docker access in the development sandbox.** Integration tests need the Docker socket, which the current sandbox blocks.

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
