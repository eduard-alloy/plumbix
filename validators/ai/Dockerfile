# Plumbix AI validator. Needs ANTHROPIC_API_KEY as a step secret.
# Build from the repository root:
#   docker build -f validators/ai/Dockerfile -t plumbix-ai-validator .
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY internal ./internal
COPY validators ./validators
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/plumbix-ai-validator ./validators/ai

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 validator
COPY --from=build /out/plumbix-ai-validator /usr/local/bin/plumbix-ai-validator
USER validator
ENTRYPOINT ["/usr/local/bin/plumbix-ai-validator"]
