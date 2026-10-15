# Plumbix Constraints

Newest first. Each entry cites its source in DISCOVERY.md.

## Security
- On Docker Desktop, step containers reach the host's loopback through `host.docker.internal`, `gateway.docker.internal` and `192.168.65.254`. Loopback binding alone never protects the API. (WU-003)
- Every API route except health needs the bearer token from `<data_dir>/api-token`. The server refuses a token file readable by others. New endpoints go on the protected mux by default. Never mount the data directory into a step container. (WU-013, A14)
- `TestStepCannotUseHostAPI` must keep passing: a container probing the real API must never get data back. (WU-013)
- Secrets live in a dotenv file with mode `0600`. The repo's `.env` is git-ignored, and only `.env.example` is committed. (A13)
- The AI validator treats all repo content as untrusted data. In Plumbix's own pipeline an AI approval alone is never enough; the deterministic validator must also approve. (A12, risk 3)
- Plumbix binds only to local addresses and refuses to start otherwise until authentication exists. (Q12)
- Step containers have no network path to the Plumbix API. Validators report verdicts through step output only. (Q12)
- Each git host PAT (GitHub fine-grained, Azure DevOps) is scoped to code read/write, kept in a local secrets file outside the repo, and never logged. (Q13)
- Plain shell execution without container isolation is not allowed. Agent-written code is untrusted. (Q04)

## Integration
- GitHub and Azure DevOps are the v1 hosts. Both sit behind one narrow host boundary designed from these two real implementations. (Q03, revised)
- Pushes are detected by polling in v1, because a local-only Plumbix is unreachable for service hooks. (Q12)
- Neither host can enforce merges in v1 because the PAT acts as the user. Plumbix flags bypass commits instead. (Q13)

## Architecture
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
- The Claude Code sandbox blocks local port binding, Go module downloads (TLS through its proxy fails) and the Docker socket. Tests that bind ports, `go get`/`go mod tidy`, and Docker integration tests run outside the sandbox, or with `sandbox.network.allowLocalBinding: true`. (WU-001)
- Once Go's build cache has been written outside the sandbox, the sandbox can't read it. Run all Go commands outside the sandbox. (WU-002)
- `make check` (build, test, vet, gofmt) is the gate for every work unit. (WU-001)
- `make integration` runs Docker-backed tests (build tag `integration`). macOS ships GNU make 3.81, which ignores `.SHELLFLAGS`. Recipes that pipe must use `set -o pipefail` inline. (WU-003)
- Integration tests that check for leftover containers must filter by their own `plumbix.job` label, because packages run in parallel. (WU-003)
- Tests that start an in-process server must call `http.DefaultClient.CloseIdleConnections()` before stopping it. Otherwise a spare connection from Go's shared transport can hold `Shutdown` for 5 seconds. (WU-004)

## Licensing
- Apache 2.0. (Q14)
