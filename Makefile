SHELL := bash

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build test lint check integration images

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
