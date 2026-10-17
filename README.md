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

An AI review can run outside the pipeline too: a validator step with `external: true` waits for a verdict, and `plumbix review --watch` on any machine with Claude Code installed pulls the review brief, reads the change in a checkout, and posts the verdict, using whatever Claude login that machine already has. Settings decide whether such a reviewer may also run commands.

Plumbix also accepts `file://` paths and SSH remotes (`ssh://host/path` or `user@host:path`), which use your own SSH keys. Your repository needs a `.plumbix/pipeline.yaml`; see [ARCHITECTURE.md](ARCHITECTURE.md) section 4.2.

## Use Plumbix from an agent session

Plumbix is also an MCP server. In Claude Code:

```sh
claude mcp add plumbix -- plumbix mcp
```

The session then has tools to submit a change, wait for its verdict, read what the validators found, approve, merge or refresh it, and to act as an external reviewer (`next_review`, `post_verdict`). Each session acts under its own identity, so it never reviews a change it submitted.

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
