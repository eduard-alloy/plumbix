# Plumbix

Self-hosted, open-source pipelines and change review for the age of coding agents.

Plumbix runs pipelines in containers on your own machine with free parallelism. It validates each change against its intent and acceptance criteria instead of line-by-line review. It works with your existing git host, starting with GitHub and Azure DevOps.

**Status**: early development. Nothing is usable yet. See [PROJECT.md](PROJECT.md) for the plan and [ARCHITECTURE.md](ARCHITECTURE.md) for the design.

## Build

Requires Go 1.27 or later.

```sh
make check          # build, test and lint
./bin/plumbix serve # starts on 127.0.0.1:6400
curl http://127.0.0.1:6400/api/v1/health
```

Plumbix has no authentication yet, so it refuses to listen on anything but a loopback address.

## License

Apache 2.0. See [LICENSE](LICENSE).
