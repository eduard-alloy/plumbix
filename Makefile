SHELL := bash

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build web test lint check integration images live docs-check release

build: web
	go build -ldflags "$(LDFLAGS)" -o bin/plumbix ./cmd/plumbix

# The browser app. Its build output in web/dist is embedded into the binary.
web/node_modules: web/package.json web/package-lock.json
	cd web && npm ci
	touch web/node_modules

web: web/node_modules
	cd web && npm run build

test: web/node_modules
	go test ./...
	cd web && npm test -- --reporter=dot


lint:
	go vet ./...
	@unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi

check: build test lint docs-check

# Docker-backed tests. Needs a running Docker engine.
integration:
	set -o pipefail; go test -tags integration -count=1 -v ./... 2>&1 | grep -E "^(=== RUN|--- (PASS|FAIL|SKIP)|ok|FAIL|PASS)|OPEN SECURITY FINDING|route "

# Validator container images, built from the repository root, labelled
# with the version the way the server labels the ones it builds itself.
images:
	docker build -f validators/reference/Dockerfile --label org.plumbix.version=$(VERSION) -t plumbix-reference-validator:dev .
	docker build -f validators/ai/Dockerfile --label org.plumbix.version=$(VERSION) -t plumbix-ai-validator:dev .

# Release binaries: the web interface built once and embedded, then one
# static binary per platform under dist/, and their SHA-256 checksums. No
# C toolchain is needed (the SQLite driver is pure Go). The release
# workflow runs this on a tag with VERSION set to the tag's name.
PLATFORMS := darwin/arm64 darwin/amd64 linux/arm64 linux/amd64
release: web
	rm -rf dist && mkdir -p dist
	@for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; \
	  echo "building dist/plumbix-$$os-$$arch ($(VERSION))"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/plumbix-$$os-$$arch ./cmd/plumbix || exit 1; \
	done
	cd dist && (shasum -a 256 plumbix-* 2>/dev/null || sha256sum plumbix-*) > checksums.txt
	@cat dist/checksums.txt

# Tests that call real model APIs. They cost a few cents and need
# ANTHROPIC_API_KEY (Claude) and, for the openai provider, OPENAI_API_KEY
# plus optionally PLUMBIX_AI_BASE_URL and PLUMBIX_AI_MODEL, in the
# environment or in .env; each part skips without its key.
live:
	go test -tags live -count=1 -timeout 20m -v ./validators/ai/ -run Live

# Documentation checks: the README names no work unit, decision or
# discovery question (those ids belong in the design documents), and every
# relative link or image in README.md and docs/ points at a file that exists.
docs-check:
	@if grep -nE 'WU-[0-9]{3}|(^|[^A-Za-z0-9_])[AQ][0-9]{2}([^A-Za-z0-9_]|$$)' README.md; then echo "README names a work unit, decision or question"; exit 1; fi
	@bad=0; for f in README.md docs/*.md; do \
	  for l in $$(grep -oE '\]\([^)#: ]+[)#]|src(set)?="[^": ]+"' "$$f" | sed -E 's/^\]\(//; s/[)#]$$//; s/^src(set)?="//; s/"$$//' | sort -u); do \
	    [ -e "$$(dirname "$$f")/$$l" ] || { echo "$$f: broken link $$l"; bad=1; }; \
	  done; \
	done; [ $$bad -eq 0 ]
