# Plumbix Constraints

Newest first. Each entry cites its source in DISCOVERY.md.

## Security
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
- The pipeline format is Plumbix's own minimal, strict YAML with a published, versioned schema. No compatibility with other CI formats. (Q08)

## Licensing
- Apache 2.0. (Q14)
