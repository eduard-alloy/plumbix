SHELL := bash

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build test lint check integration images live

build:
	go build -ldflags "$(LDFLAGS)" -o bin/plumbix ./cmd/plumbix

test:
	go test ./...

lint:
	go vet ./...
	@unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi

check: build test lint

# Docker-backed tests. Needs a running Docker engine.
integration:
	set -o pipefail; go test -tags integration -count=1 -v ./... 2>&1 | grep -E "^(=== RUN|--- (PASS|FAIL|SKIP)|ok|FAIL|PASS)|OPEN SECURITY FINDING|route "

# Validator container images, built from the repository root.
images:
	docker build -f validators/reference/Dockerfile -t plumbix-reference-validator:dev .
	docker build -f validators/ai/Dockerfile -t plumbix-ai-validator:dev .

# Tests that call the real Claude API. They cost a few cents and need
# ANTHROPIC_API_KEY in the environment or in .env.
live:
	go test -tags live -count=1 -timeout 20m -v ./validators/ai/ -run Live
