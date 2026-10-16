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
./bin/plumbix login                      # one-time link that signs your browser in at http://127.0.0.1:6400
```

Plumbix also accepts `file://` paths and SSH remotes (`ssh://host/path` or `user@host:path`), which use your own SSH keys. Your repository needs a `.plumbix/pipeline.yaml`; see [ARCHITECTURE.md](ARCHITECTURE.md) section 4.2.

## License

Apache 2.0. See [LICENSE](LICENSE).
