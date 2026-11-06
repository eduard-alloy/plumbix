# Contributing to Plumbix

Plumbix has one maintainer and is in early development. This page says what you can expect, so nobody wastes an evening.

## Code

Outside code contributions are not taken yet. Every change to this repository lands through Plumbix itself, with its intent and acceptance criteria written before the code, and the path for a second contributor to do that (push a branch to this repository, reach the maintainer's Plumbix server) does not exist yet. When it does, this page will say how. If you want to build something, open an issue first and say what and why; a change that arrives unannounced will be closed with a pointer here.

## Bugs

Open an issue with the bug report template. The useful facts are what you did, what you expected, what happened, the Plumbix version (`plumbix version`), your operating system and the git host involved, and the relevant lines of `~/.plumbix/serve.log` or the output of `plumbix change show <id>` and `plumbix change runs <id>`. Remove tokens and secrets before pasting; the server is built not to log them ([ARCHITECTURE.md](ARCHITECTURE.md), the security section), but a shell session might.

## Questions and ideas

Open an issue without a template. The design documents answer most questions about why something is the way it is: [PROJECT.md](PROJECT.md) for purpose, features and roadmap, [ARCHITECTURE.md](ARCHITECTURE.md) for components and every design decision, [CONSTRAINTS.md](CONSTRAINTS.md) for what implementation taught, and `work_units/` for how each piece was specified and built.

## Security

Do not put the details of a security problem in a public issue. Use GitHub's private vulnerability reporting instead: the [Report a vulnerability](https://github.com/eduard-alloy/plumbix/security/advisories/new) form on the repository's Security tab reaches the maintainer and nobody else. The security model Plumbix promises is written down in [CONSTRAINTS.md](CONSTRAINTS.md): the server listens on loopback only, every API call needs the local token, steps have no network path to the API, secrets reach only runs on protected branches, repository content is untrusted, and an AI approval alone is never enough on Plumbix's own pipeline. A report that shows one of those broken is the most valuable issue this project can receive.

## What to expect

Issues are read by one person, usually within a few days, with no guarantee. What becomes work is planned as a work unit and appears in the roadmap in [PROJECT.md](PROJECT.md); the issue gets a comment when that happens and another when the change lands.
