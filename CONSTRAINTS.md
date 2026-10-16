# Plumbix Constraints

Newest first. Each entry cites its source in DISCOVERY.md.

## Security
- Secrets set through the API are written to the configured secrets file by `secrets.Manager` (atomic rename, mode 0600, other lines and comments kept) and are never returned, logged or echoed in errors; only set/unset and the time of the last change through Plumbix are shown. Handlers that receive a secret log the key, never the body. (WU-018, A18)
- Browser sign-in (A17): login links and session cookies are stored as SHA-256 hashes only; links live 10 minutes and work once, sessions 30 days. Every cookie request must carry the listen address as `Host`, and state-changing ones an `Origin` of `http://<listen>`; the bearer token is exempt from both. The sign-in exchange is a POST, never a GET, so prefetchers cannot consume a link. (WU-016)
- The web app is served outside `/api/` with a same-origin Content-Security-Policy and no inline scripts; `/api/` keeps its protection. The container probe also checks `GET /api/v1/changes`. (WU-016)
- On Docker Desktop, step containers reach the host's loopback through `host.docker.internal`, `gateway.docker.internal` and `192.168.65.254`. Loopback binding alone never protects the API. (WU-003)
- Every API route except health needs the bearer token from `<data_dir>/api-token`. The server refuses a token file readable by others. New endpoints go on the protected mux by default. Never mount the data directory into a step container. (WU-013, A14)
- `TestStepCannotUseHostAPI` (in `internal/api`) must keep passing: a container probing the real API must never get data back. (WU-013)
- Secrets live in a dotenv file with mode `0600`. The repo's `.env` is git-ignored, and only `.env.example` is committed. (A13)
- The AI validator treats all repo content as untrusted data. In Plumbix's own pipeline an AI approval alone is never enough; the deterministic validator must also approve. (A12, risk 3)
- Plumbix binds only to local addresses and refuses to start otherwise until authentication exists. (Q12)
- Step containers have no network path to the Plumbix API. Validators report verdicts through step output only. (Q12)
- Each git host PAT (GitHub fine-grained, Azure DevOps) is scoped to code read/write, kept in a local secrets file outside the repo, and never logged. (Q13)
- Git gets tokens only through its process environment and the askpass script in the data directory, never through arguments or URLs. `-c credential.helper=` keeps personal credential helpers out. (WU-008)
- Plain shell execution without container isolation is not allowed. Agent-written code is untrusted. (Q04)

## Integration
- Azure DevOps does not report a personal access token's expiry to the token's holder (its token lifecycle API needs a Microsoft Entra ID sign-in). The expiry warning relies on `PLUMBIX_AZURE_DEVOPS_TOKEN_EXPIRES` in the secrets file; without it there is no warning. Azure DevOps ignores the basic-auth user name when the password is a token. (WU-009)
- Plain git remotes (host `git`): absolute paths, `file://`, `ssh://`, scp-style. No credentials; SSH runs in batch mode unless the user set their own SSH command. Local remotes should be bare repositories. (WU-015)
- GitHub and Azure DevOps are the v1 hosts. Both sit behind one narrow host boundary designed from these two real implementations. (Q03, revised)
- Pushes are detected by polling in v1, because a local-only Plumbix is unreachable for service hooks. (Q12)
- Neither host can enforce merges in v1 because the PAT acts as the user. Plumbix flags bypass commits instead. (Q13)

## Architecture
- Pipeline steps may not set `PLUMBIX_*` environment variables themselves (the parser reserves them), so the AI model reaches validator steps only through the settings (`scheduler.StepSetup.Env`, filled only for names the step or its secrets did not set) or through a secret of that name. Operator guidance is trusted by validators because it comes from the settings, never from the repository. (WU-018, A19)
- The engine and the poller read secrets and host adapters through `host.Source` / `Config.SecretSource` on every use, never from a copy taken at start. New code that needs a token must do the same. (WU-018)
- Events are appended by the store inside the transaction of the write they describe. A new store write that changes what a client sees must emit one; services never emit, the engine appends only `step.finished`. `TestLifecycleEmitsEvents` keeps `events.Types` and `contracts/events.schema.json` the same set and checks every emitted event against the schema. (WU-014, A16)
- `Hub.Publish` never blocks a writer: a subscriber more than `events.SubscriberBuffer` events behind is cut off and replays from the store. The stream handler subscribes before replaying and skips ids already sent, so clients see no gap and no duplicate. Open streams end only when the hub closes, so the server closes it when its context ends. Events are never pruned in v1. (WU-014)
- A change merges only when the source branch still points at the validated commit and the target branch equals the run's base (the source contains the target). The target then receives the validated tree unchanged; Plumbix never computes a merge. (WU-010, A15)
- Bypass detection walks first-parent commits between the last known head and the new head; commits inside a recorded Plumbix merge range are not bypasses. One alert per push, one open alert per kind and key. (WU-010)
- During a merge the engine refuses to start a validation for that change; a close that races a merge leaves the change closed with a merge record, which is logged. (WU-010)
- Validator steps run after their needs finish, whatever the outcome. Normal steps still need their needs to succeed. (WU-007)
- An errored normal step makes the outcome `needs_human`, never `fix_requested`. (WU-007)
- Only a change's active run may move its state, and cancellation is checked before errors: a cancelled run's errors are not infrastructure failures. (WU-007)
- Timing tests assert ordering or overlap, never wall-clock totals, because the full suite runs packages in parallel. (WU-008)
- Tests never reach the network: git URLs are redirected to local repositories with `GIT_CONFIG_COUNT`/`url.<path>.insteadOf`. (WU-007)
- One process in v1. The scheduler calls the runner only through its interface, so runners can move out later. (ARCHITECTURE A1)
- Required validators come from the target branch's pipeline file. Any change touching `.plumbix/` needs a human. (A9)
- A validator crash or invalid verdict is never approval. Criteria without a check are `unknown` and send the change to a human. (A11)
- Merges are refused if the target branch moved since validation. (5.4)
- Contract files in `contracts/` are the source of truth. Contract tests must check real server output against them. (A7)
- Runtime requirements: system `git` and a Docker-compatible engine. (A3, A4)
- Plumbix never depends on Kai or TRACE. Clients depend on Plumbix's public contracts. (Q06)
- Plumbix's own changes go through the public API, never internal shortcuts. (Q11)
- The core contains no AI and holds no model API keys. Validators are pluggable pipeline steps. (Q07)
- Plumbix does not host git repositories and does not run authoring or fixing agents. (Q02, Q07)
- Server and runner stay separable so runners can move to other machines later. (Q04)

## Formats
- AI validator output is checked with `ParseVerdict` after conservative normalization: `met` needs evidence, a criterion without a reason is `unknown`, `approve` with an `unmet` becomes `fix`. Repository content never leaves the random untrusted block, and content over 600,000 characters fails instead of being truncated. (WU-006)
- `make live` calls the paid Claude API (about $0.04 per review). It is opt-in and never part of `make check` or `make integration`. (WU-006)
- `contracts/verdict.schema.json` and `contracts/validator-input.schema.json` are checked against Go by contract tests over example files. Rules needing the change's criteria (IDs exist, appear once) are Go-only and listed in the schema description. (WU-005)
- Validator containers run as non-root: Plumbix must make `/plumbix/out` writable and the inputs and workspace readable by any user. (WU-005)
- `.dockerignore` must keep `.env`, `.env.*`, `.git` and `bin` out of every build context. A test enforces it. (WU-005)
- API list endpoints return an object wrapping the array, never a bare array. Field problems use `details.problems: [{field, message}]` and list every problem at once. (WU-004)
- `contracts/openapi.yaml` is checked by `TestResponsesMatchOpenAPI`. Every new endpoint adds its responses to that test. (WU-004)
- The `change` package owns the model and the `Store` interface. Storage code implements it and never holds business rules. (WU-004)
- The pipeline parser and `contracts/pipeline.schema.json` must agree. Every format change adds example files under `internal/pipeline/testdata/` so the contract test covers it. Rules that span fields live in the parser and are listed in the schema's description. (WU-002)
- Invalid input to a check-style endpoint is a 200 result with `valid: false`, not an HTTP error. (WU-002)
- The pipeline format is Plumbix's own minimal, strict YAML with a published, versioned schema. No compatibility with other CI formats. (Q08)

## Development environment
- Web tests run with Vitest, jsdom and Testing Library against a fake `fetch` and `EventSource` (`web/src/test/server.ts`) and API-shaped fixtures. Fake only `Date` (`vi.useFakeTimers({ toFake: ['Date'] })`): faking timers hangs Testing Library's `waitFor`. A StatePill's screen-reader label is part of its parent's accessible name, so queries by name use unanchored patterns. Page tests call `cleanup()` before `vi.unstubAllGlobals()`: hooks run in reverse order, so without it a load that finishes after the test mounts the stream against the real `EventSource`. Tests that emit events first `await streamOpen()`. (WU-011, WU-017)
- The log panel has no Follow mode: the API lists a run's steps only once they finish, so a log can only be opened for a finished step. Follow arrives with running steps in the API. (WU-017)
- The engine records each step on its run as it finishes (`RecordStep`); `FinishRun` replaces the list. Clients may show a run's steps before it ends but never know which step is running. (WU-011)
- `make build` runs `make web` first, which needs Node and npm; `web/dist` is embedded by `web/embed.go`, so a Go-only build still works and serves a "not built" page at `/`. Vite builds with `emptyOutDir` off so `web/dist/.gitkeep` stays. The Vite dev server is not wired to the API: the cookie checks refuse its Host and Origin, so develop against the built app or add a proxy with `changeOrigin` later. (WU-016)
- `make integration` builds the two validator images concurrently from the same context. `TestImageFailsCleanlyWithoutKey` failed once under that load and passes alone; rerun before suspecting a change. (WU-010)
- The Claude Code sandbox blocks local port binding, Go module downloads (TLS through its proxy fails) and the Docker socket. Tests that bind ports, `go get`/`go mod tidy`, and Docker integration tests run outside the sandbox, or with `sandbox.network.allowLocalBinding: true`. (WU-001)
- Once Go's build cache has been written outside the sandbox, the sandbox can't read it. Run all Go commands outside the sandbox. (WU-002)
- `make check` (build, test, vet, gofmt) is the gate for every work unit. (WU-001)
- `make integration` runs Docker-backed tests (build tag `integration`). macOS ships GNU make 3.81, which ignores `.SHELLFLAGS`. Recipes that pipe must use `set -o pipefail` inline. (WU-003)
- Integration tests that check for leftover containers must filter by their own `plumbix.job` label, because packages run in parallel. (WU-003)
- Tests that start an in-process server must call `http.DefaultClient.CloseIdleConnections()` before stopping it. Otherwise a spare connection from Go's shared transport can hold `Shutdown` for 5 seconds. (WU-004)

## Licensing
- Apache 2.0. (Q14)
