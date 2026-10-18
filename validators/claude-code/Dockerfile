# Plumbix Claude Code validator (WU-035, A33): runs `claude -p` inside the
# pipeline on the reviewer profile's token. PLUMBIX_AI_API_KEY holds a token
# from `claude setup-token` (a Claude subscription) or an Anthropic API key;
# PLUMBIX_AI_MODEL and PLUMBIX_AI_TOOLS come from the profile. No login is
# baked in. Build from the repository root:
#   docker build -f validators/claude-code/Dockerfile -t plumbix-claude-code-validator .
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY internal ./internal
COPY validators ./validators
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/plumbix-claude-code-validator ./validators/claude-code

FROM node:24-bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g @anthropic-ai/claude-code@2.1.302 \
 && npm cache clean --force \
 && useradd --create-home --uid 10001 validator
COPY --from=build /out/plumbix-claude-code-validator /usr/local/bin/plumbix-claude-code-validator
# Claude Code keeps its state under HOME; the workspace is mounted read-only.
ENV HOME=/home/validator DISABLE_AUTOUPDATER=1 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
USER validator
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/plumbix-claude-code-validator"]
