MODULE  := github.com/nevzatcirak/review-mcp
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "")
LDFLAGS := -X $(MODULE)/internal/version.Version=$(VERSION) -X $(MODULE)/internal/version.Commit=$(COMMIT)

.PHONY: build test lint fmt licenses licenses-check

build:
	go build -ldflags "$(LDFLAGS)" -o review-mcp ./cmd/review-mcp

test:
	go test -race ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .

# Regenerate the third-party license bundle from the modules linked into
# ./cmd/review-mcp (see tools/licensebundle).
licenses:
	go run ./tools/licensebundle

# Regenerate the bundle into a temporary file and fail if it differs from the
# committed THIRD_PARTY_LICENSES. Also fails on a missing, stale or
# out-of-policy allowlist entry.
licenses-check:
	@tmp=$$(mktemp) && trap 'rm -f "$$tmp"' EXIT && \
	go run ./tools/licensebundle -out "$$tmp" && \
	diff -u THIRD_PARTY_LICENSES "$$tmp" && echo "THIRD_PARTY_LICENSES is up to date"
