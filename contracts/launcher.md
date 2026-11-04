# The launcher marker

When Plumbix starts a Claude Code session, the session's environment says who launched it and for what, so an observer's hook running inside that session can state the facts without a rule on a machine path (WU-069). This file is the contract a reader of that environment can rely on. The Plumbix API (`openapi.yaml`) and the event stream (`events.schema.json`) stay the source of truth for everything the variables name.

## Variables

| Variable | Value | Set by |
|---|---|---|
| `PLUMBIX_LAUNCHER` | `review` for a session `plumbix review --watch` starts to answer an external review step; `validator` for a session the AI validator image (`plumbix-claude-code-validator`) starts inside its container | both |
| `PLUMBIX_REPOSITORY` | the repository's `url` exactly as `GET /api/v1/repos` returns it for the registration, byte for byte, never normalised, so a reader applies its own normalisation and aliases to one value | review loop |
| `PLUMBIX_REPO_ID` | the registration's id (`r_…`), the `repo_id` on the stream's events | review loop |
| `PLUMBIX_CHANGE` | the change's id (`c_…`), the `change_id` on the stream's events | both |
| `PLUMBIX_RUN` | the run's id (`run_…`), the `run_id` on the stream's events | review loop |
| `PLUMBIX_REVIEW` | the review's id (`rv_…`), the `review_id` of `review.requested`, `review.claimed`, `review.released` and `review.expired` | review loop |

The validator image's session carries only `PLUMBIX_LAUNCHER` and `PLUMBIX_CHANGE`: its input (`validator-input.schema.json`) names the change, not the run or the repository. A variable is either set with its full value or absent; none is ever set empty.

## Where the session runs

The review loop runs the session in a detached checkout of the validated head under its work directory (`<data-dir>/review/worktrees/<review id>` by default), removed when the review ends; the directory is not part of this contract. The validator image runs in the step's workspace inside the container.

## Stability

The names and the meanings above are stable from WU-069 on; a new launcher value or a new variable may be added, an existing one is never renamed or given another meaning. The values are Plumbix's ids and the registration's url as the API returns them: a reader needs no knowledge of their form beyond `openapi.yaml`.
