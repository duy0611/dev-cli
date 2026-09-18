# dev - a CLI for devcontainers
#
#   make build      build dist/dev
#   make test       go test ./...
#   make lint       gofmt, go vet, golangci-lint
#   make smoke      end-to-end test against a real container engine
#   make install    build, then copy the binary into ~/.local/bin
#   make clean      remove build output

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

BIN     := dist/dev
PREFIX  ?= $(HOME)/.local
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# CGO off on purpose: the SQLite driver is modernc.org/sqlite, which is pure Go,
# so the build needs no C toolchain and the result is one static binary.
GO      ?= go
GOBUILD := CGO_ENABLED=0 $(GO) build -ldflags "-X main.version=$(VERSION)"

.DEFAULT_GOAL := help
.PHONY: help build test lint smoke install clean

help:
	@sed -n '3,9p' $(MAKEFILE_LIST) | sed 's/^# \{0,1\}//'

build:
	@mkdir -p dist
	$(GOBUILD) -o $(BIN) ./cmd/dev

# -timeout 120s, not go's 10m default: nothing here is slow, so a package that
# stops finishing has deadlocked, and the stack should print in a minute rather
# than after ten. The timeout is per package, so it is not a budget for the run.
test:
	$(GO) test -timeout 120s ./...

# gofmt -l prints the files it would change and exits 0 either way, so the
# output is what has to be checked.
lint:
	@echo "==> gofmt"
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "$$out"; echo "run: gofmt -w ."; exit 1; fi
	@echo "==> go vet"
	@$(GO) vet ./...
	@echo "==> golangci-lint"
	@if command -v golangci-lint >/dev/null 2>&1; then \
	  golangci-lint run ./...; \
	else echo "    golangci-lint absent, skipped"; fi

# Behind a build tag so `make test` never tries to reach a container engine.
smoke:
	$(GO) test -tags smoke -count=1 -v ./test/smoke/...

install: build
	@mkdir -p $(PREFIX)/bin
	install -m 0755 $(BIN) $(PREFIX)/bin/dev
	@echo "installed $(PREFIX)/bin/dev"

clean:
	rm -rf dist
