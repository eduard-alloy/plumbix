# Running the Plumbix server

Build, the API token, agent sessions, and how Plumbix validates its own changes. Back to the [README](../README.md).

## Build

Requires Go 1.27 or later. The web interface needs Node 20 or later and npm; `make build` builds it first and embeds it, and a Go-only build still works without it.

```sh
make check          # build, test and lint
./bin/plumbix serve # starts on 127.0.0.1:6400
curl http://127.0.0.1:6400/api/v1/health
```

## The API token

Plumbix listens only on loopback addresses. On first start it writes a random API token to `~/.plumbix/api-token`, readable only by you. Every API call except the health check needs it:

```sh
curl -H "Authorization: Bearer $(cat ~/.plumbix/api-token)" \
     --data-binary @.plumbix/pipeline.yaml \
     http://127.0.0.1:6400/api/v1/pipelines/check
```

To rotate the token, delete the file and restart the server. Plumbix refuses to start if the file is readable by other users.

## Agent sessions

Plumbix is also an MCP server. In Claude Code:

```sh
claude mcp add plumbix -- plumbix mcp
```

The session then has tools to register a repository (without a token), submit a change, wait for its verdict, read what the validators found, merge or refresh it, and to act as an external reviewer (`next_review`, `post_verdict`). Each session acts under its own identity, so it never reviews a change it submitted. Approving a `needs_human` change stays a person's act: the tool for it appears only when you start the server with `--allow-approve`, for a session you drive yourself.

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
