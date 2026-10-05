MODULE  := github.com/nevzatcirak/review-mcp
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "")
LDFLAGS := -X $(MODULE)/internal/version.Version=$(VERSION) -X $(MODULE)/internal/version.Commit=$(COMMIT)

.PHONY: build test lint fmt

build:
	go build -ldflags "$(LDFLAGS)" -o review-mcp ./cmd/review-mcp

test:
	go test -race ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .
