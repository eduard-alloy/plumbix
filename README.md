# Plumbix

Self-hosted, open-source pipelines and change review for the age of coding agents.

Plumbix runs pipelines in containers on your own machine with free parallelism. It validates each change against its intent and acceptance criteria instead of line-by-line review. It works with plain git repositories on your machine or over SSH, and with GitHub and Azure DevOps.

**Status**: early development. Nothing is usable yet. See [PROJECT.md](PROJECT.md) for the plan and [ARCHITECTURE.md](ARCHITECTURE.md) for the design.

## Build

Requires Go 1.27 or later. The web interface needs Node 20 or later and npm; `make build` builds it first and embeds it, and a Go-only build still works without it.

```sh
make check          # build, test and lint
./bin/plumbix serve # starts on 127.0.0.1:6400
curl http://127.0.0.1:6400/api/v1/health
```

Plumbix listens only on loopback addresses. On first start it writes a random API token to `~/.plumbix/api-token`, readable only by you. Every API call except the health check needs it:

```sh
curl -H "Authorization: Bearer $(cat ~/.plumbix/api-token)" \
     --data-binary @.plumbix/pipeline.yaml \
     http://127.0.0.1:6400/api/v1/pipelines/check
```

To rotate the token, delete the file and restart the server. Plumbix refuses to start if the file is readable by other users.

## Try it locally, no account needed

Plumbix works with a plain git repository on your machine. Use a bare repository as the shared remote; you and your agents push branches to it.

```sh
git init --bare ~/git/myapp.git           # or point at an existing bare repo
./bin/plumbix serve &                      # needs Docker for pipeline steps
./bin/plumbix repo add --host git --url ~/git/myapp.git
./bin/plumbix submit -f change.json        # intent, criteria, source and target branch
./bin/plumbix change runs <change-id>
./bin/plumbix change merge <change-id>   # once approved: lands the validated tree on main
./bin/plumbix alert list                 # commits that reached main without a verdict
./bin/plumbix events                     # follow everything Plumbix does, one JSON event per line
./bin/plumbix review --watch             # answer external review steps with the Claude Code on this machine
./bin/plumbix login                      # one-time link that signs your browser in at http://127.0.0.1:6400
# in the browser: Settings holds the git host tokens, the AI key and model, and review guidance for validators
# then open http://127.0.0.1:6400: changes that need you first, verdict and evidence on each, merge with one click
```

An AI review can run outside the pipeline too: a validator step with `external: true` waits for a verdict, and `plumbix review --watch` on any machine with Claude Code installed pulls the review brief, reads the change in a checkout, and posts the verdict, using whatever Claude login that machine already has.

Which model judges is a reviewer profile, defined under AI reviewers in Settings. API profiles (Anthropic, or any OpenAI-compatible endpoint: OpenAI, Gemini, Grok, Kimi, DeepSeek, Groq, a local server) judge from inside the pipeline with their own key, billed per token; a Claude Code profile is the loop above, on that machine's Claude subscription, with its model and whether it may run commands; a Claude Code in-the-pipeline profile runs the same Claude Code inside a `plumbix-claude-code-validator` step on a token you mint with `claude setup-token` (or an API key), for a server where no loop is signed in. A validator step names its profile with `reviewer: <name>`; steps that name none get their family's default. Two validator steps with two profiles run side by side, so models can be compared on the same change; mark the second one `advisory: true` and its verdict is recorded and shown but never counted. Reasoning models think before they answer, sometimes for minutes: set the profile's effort, or a step's `effort: low`, and the log shows how many reasoning tokens a verdict cost.

The Repositories page imports a repository for you: point it at a local folder that already has a remote, give it a remote URL to register and clone into a folder in your home directory, or publish a folder that has no remote to a new GitHub or Azure DevOps repository, and choose the host token or one for that repository alone. Plumbix also accepts `file://` paths and SSH remotes (`ssh://host/path` or `user@host:path`), which use your own SSH keys. Your repository needs a `.plumbix/pipeline.yaml`; see [ARCHITECTURE.md](ARCHITECTURE.md) section 4.2. A validator step has no `run`: its image is the validator, started with the change's inputs at `/plumbix/in`, and it writes `/plumbix/out/verdict.json`. Plumbix ships three such images, `plumbix-reference-validator` (deterministic checks), `plumbix-ai-validator` (asks a reviewer profile's model) and `plumbix-claude-code-validator` (Claude Code with tools, on your token), and builds them for you from the Images page in the browser, which also lists every other image your pipelines run and whether it is present. You can define your own images there too: write a Dockerfile, build it on the server, and name it in a pipeline step. The pipeline editor in the browser saves by editing your file's text, so its comments and formatting survive and a one-field change is a one-line diff. A step can keep its dependency caches between runs with `cache: [/go/pkg/mod, /root/.cache/go-build]` (or `/root/.npm`, `/root/.cache/pip`): each path is a per-repository directory mounted into the container.

## Use Plumbix from an agent session

Plumbix is also an MCP server. In Claude Code:

```sh
claude mcp add plumbix -- plumbix mcp
```

The session then has tools to register a repository (without a token), submit a change, wait for its verdict, read what the validators found, merge or refresh it, and to act as an external reviewer (`next_review`, `post_verdict`). Each session acts under its own identity, so it never reviews a change it submitted. Approving a `needs_human` change stays a person's act: the tool for it appears only when you start the server with `--allow-approve`, for a session you drive yourself.

## Gate GitHub pull requests

Plumbix can run beside a GitHub pull request instead of replacing it: validators run on the pull request, every verdict lands as a check run on its head commit, the hold is a required check, a review approval by a person counts as Plumbix's approval, and GitHub merges. Nothing about the enterprise's workflow changes; Plumbix adds the structured verdict, the per-criterion judgment and the validator's model usage to GitHub's own surfaces (`contracts/github-check-runs.md`).

1. **Identity.** Create a GitHub App with these permissions: checks read and write, pull requests read, contents read, metadata read. Add contents write only if Plumbix is to push merges for this repository (`--merger plumbix`), and administration read if Plumbix should be able to tell whether `plumbix/gate` is required (it warns when it cannot). Install the App on the repositories and put its id and private key in the secrets file as `PLUMBIX_GITHUB_APP_ID` and `PLUMBIX_GITHUB_APP_KEY` (the PEM with `\n` for its line breaks, or base64-encoded: `base64 < key.pem | tr -d '\n'`). The pull-request calls (pull requests, reviews, check runs, required checks) go out as the App whenever it is installed on the repository, since only an App may create check runs; a personal token in `PLUMBIX_GITHUB_TOKEN` or a repository token serves them only where the App is not installed, and keeps doing the git fetches and pushes. Plumbix mints and renews installation tokens itself; the Check button beside the App's id or key in Settings verifies them.
2. **Register the repository** with pull requests on: `plumbix repo add --host github --url https://github.com/you/repo --pull-requests`, or turn them on for a registered one: `plumbix repo set <id> --pull-requests` (`PUT /api/v1/repos/{id}/settings`). GitHub merges by default (`merger: github`); `--merger plumbix` keeps Plumbix pushing the validated tree, for a repository where the token or App has contents write.
3. **The hold.** In branch protection or a ruleset of the target branch, require the status check `plumbix/gate`. Without it Plumbix can hold nothing, and says so: a pull request merged while the gate is not required, or while its change was not approved, raises a `gate_bypassed` alert. Who overrode a required check is recorded in GitHub's audit log, which Plumbix does not read.
4. **Intake.** The poller lists the repository's open pull requests on every poll; each one becomes a change with the pull request's branches, title, description as the intent, the pull request author as `submitted_by` (login, or `app/<slug>` for an App) and the acceptance criteria read from the description. State them as a list under a heading named `Acceptance criteria`, one criterion per item (`- [ ] Uploads retry three times`). A pull request that states none gets a change without criteria: the gate's check says so and validators judge the intent and the diff. For faster reaction, open a webhook on your side: set `PLUMBIX_GITHUB_WEBHOOK_SECRET`, point GitHub at `POST /api/v1/hooks/github` with that secret, and subscribe it to pull requests, pull request reviews and check suites. The webhook needs no API token; every delivery is checked against the secret. Polling still runs, so a missed delivery costs only time.
5. **Verdicts.** Every run posts one check run per validator (`plumbix/<step name>`) and the gate (`plumbix/gate`), each with the verdict in words and, in `output.text`, the verdict as JSON between stable markers. Pushes to the pull request's branch start a new run as any push does; a re-requested check suite on GitHub does too.
6. **Approval and merge.** A `needs_human` or escalated change takes an approving review on the validated commit by an owner, organisation member or collaborator (GitHub's `author_association`) as its approval, with the reviewer's login and review text as the reason; the Approve button and `plumbix change approve` still work. The gate turns green, GitHub merges, and Plumbix records the merge (commit, who merged) on the change and in the event stream as `change.merged` with strategy `github`. Pull requests from forks are not gated, since their head is not a branch of the repository; a closed pull request closes its change.

## Plumbix validates itself

Every change to this repository goes through Plumbix. `.plumbix/pipeline.yaml` builds, tests and lints the Go code and the web interface in parallel containers, then the reference validator judges the change against its intent, and an external `ai-review` step waits for a verdict from `plumbix review --watch` running on the maintainer's machine with Claude Code. Nothing lands on `main` without a verdict; a commit that gets there any other way raises an alert.

To contribute a change: push a branch, then submit it with its intent and acceptance criteria.

```sh
cat > change.json <<'EOF'
{"repo_id": "<id from plumbix repo list>", "source_branch": "my-branch", "target_branch": "main",
 "title": "What the change does", "intent": "Why, in a few sentences.",
 "acceptance_criteria": [{"text": "All Go tests pass", "check": {"steps": ["test"]}}]}
EOF
./bin/plumbix submit -f change.json
./bin/plumbix events --change <change-id>   # watch the run; or open the web interface
./bin/plumbix change merge <change-id>      # once approved
```

The first pipeline commit landed on `main` directly, since nothing could validate it yet; Plumbix flagged it, and the alert is recorded in `work_units/WU-012.md`.

## License

Apache 2.0. See [LICENSE](LICENSE).
