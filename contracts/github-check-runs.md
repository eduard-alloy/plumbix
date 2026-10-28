# Plumbix check runs on GitHub

When a GitHub repository is registered with `pull_requests: true`, Plumbix gates its pull requests beside GitHub's own flow (WU-054): every pull request becomes a change, every run of that change ends in check runs on the pull request's head commit, and GitHub merges. This file is the contract a reader of those check runs can rely on without the Plumbix API. The Plumbix API (`openapi.yaml`) and the event stream (`events.schema.json`) stay the source of truth.

## Names

| Check run | One per | Conclusion |
|---|---|---|
| `plumbix/<validator>` | validator step of the run, by its pipeline step name | `success` for `approve`; `failure` for `fix` or `reject`; `neutral` for an advisory validator, whatever it said; `action_required` when the validator produced no valid verdict |
| `plumbix/gate` | run | `in_progress` while the run goes; then `success` for an approved change, `failure` for `fix_requested` or `rejected`, `action_required` for `needs_human` or `escalated` and for a run that errored; `cancelled` for a cancelled run. A person's approval posts a new `plumbix/gate` with `success` on the approved head |

`external_id` is the run id (`run_…`), or the change id on the approval check. `started_at` and `completed_at` are the run's times.

The gate is the hold: mark `plumbix/gate` a required status check in branch protection or a ruleset, and GitHub refuses the merge until it is `success`. If it is not required, Plumbix can hold nothing and raises a `gate_bypassed` alert when the pull request merges anyway.

## Output

`output.title` is a short phrase in words (`Approved`, `Fixes requested`, `Rejected`, `Needs a person`, `No verdict`, `Advisory: …`). `output.summary` is Markdown for people: the validator's summary, a table of criteria with status and reason, the findings and risks, and the model usage exactly as the validator reported it. When the pull request stated no acceptance criteria, the gate's summary says so: `No acceptance criteria stated in the pull request: validators judged the intent and the diff only.` Findings with a file and line are also posted as annotations (`failure` for blocker and major, `warning` for minor), at most fifty per check run.

`output.text` carries the structured document, so a consumer parses it instead of the summary:

```
<!-- plumbix-verdict/v1 -->
```json
{ ...a verdict.schema.json object... }
```
<!-- /plumbix-verdict -->
```

| Check run | Marker | Document |
|---|---|---|
| `plumbix/<validator>` | `plumbix-verdict/v1` … `/plumbix-verdict` | the validator's verdict: `plumbix`, `outcome`, `summary`, `criteria[]` (id, status, reason, evidence), `findings[]`, `risks[]` (area, message, human_required), `usage` (model, input_tokens, output_tokens, and whatever else the validator reported: turns, cost_usd, reasoning_tokens, cache_read_tokens, cache_write_tokens). Field names are those of `verdict.schema.json`, and the document validates against it |
| `plumbix/gate` | `plumbix-result/v1` … `/plumbix-result` | the combined run result, the `ValidationResult` schema of `openapi.yaml`: `state`, `criteria[]` with every validator's assessment, `findings[]` and `risks[]` tagged with their validator, `reasons[]`, `validators[]` with each one's outcome, summary, `advisory` flag and `usage` |

Rules for a reader:

- Parse with the regular expression `<!-- (plumbix-[a-z]+/v[0-9]+) -->\s*```json\n(.*?)\n```\s*<!-- /plumbix-[a-z]+ -->`, dot matching newlines. The marker's version changes only when the document's shape does.
- A missing `usage` means the validator reported none. A missing `cost_usd` means unknown cost, never zero: Plumbix copies what the validator reported and computes, converts or estimates nothing.
- A missing block (`output.text` empty, or a comment saying the document was omitted because it exceeded GitHub's 65535-character limit) means the document must be read from the Plumbix API: `GET /api/v1/runs/{external_id}`.
- Nothing in a check run is synthetic: no criteria the pull request did not state, no cost nobody reported.

## Who may approve through a review

An approving review counts as a person's approval of the change only when GitHub's `author_association` on the review is `OWNER`, `MEMBER` or `COLLABORATOR` and the review is on the validated commit. Anyone can submit an approving review on a public repository; those are logged and ignored.

## What Plumbix posts nothing for

Pull requests from forks (their head is not a branch of the repository) and review comments on diff lines. Draft pull requests are gated like any other.
