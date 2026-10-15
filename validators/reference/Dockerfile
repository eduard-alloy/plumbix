# Plumbix deterministic reference validator.
# Build from the repository root:
#   docker build -f validators/reference/Dockerfile -t plumbix-reference-validator .
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY internal ./internal
COPY validators ./validators
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/plumbix-reference-validator ./validators/reference

FROM alpine:3.20
RUN adduser -D -u 10001 validator
COPY --from=build /out/plumbix-reference-validator /usr/local/bin/plumbix-reference-validator
USER validator
ENTRYPOINT ["/usr/local/bin/plumbix-reference-validator"]
