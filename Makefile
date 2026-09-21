# dev - a CLI for devcontainers
#
#   make build      build dist/dev
#   make relay      build the in-container agent relay, both architectures
#   make test       go test ./...
#   make lint       gofmt, go vet, golangci-lint
#   make smoke      end-to-end test against a real container engine
#   make install    build, then copy the binary into ~/.local/bin
#   make hooks      enable the repository's git hooks (once per clone)
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
.PHONY: help build relay test lint smoke install hooks clean

help:
	@sed -n '3,11p' $(MAKEFILE_LIST) | sed 's/^# \{0,1\}//'

build: relay
	@mkdir -p dist
	$(GOBUILD) -o $(BIN) ./cmd/dev

# The in-container SSH agent relay, one build per architecture a container might
# run on, embedded into dev by internal/relay/embed.go.
#
# Both architectures every time, not just this host's: the k8s provider builds
# for linux/amd64 by default whatever the operator is sitting at, so an arm64
# laptop still has to carry an amd64 relay.
#
# -s -w strips the symbol table and DWARF. This is copied into a container on
# every session, so its size is a cost paid repeatedly rather than once.
#
# Not committed: they are build output, two megabytes each, and would churn on
# every rebuild. internal/relaybin embeds the directory rather than the files so
# that an absent binary is a runtime error it can explain, not a compile error
# on a fresh checkout.
#
# Building the relay here is only possible because it lives in internal/relaybin
# and nothing on the path to building cmd/dev-relay imports that package.
RELAY_DIR := internal/relaybin/bin
relay:
	@mkdir -p $(RELAY_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "-s -w" \
	  -o $(RELAY_DIR)/.amd64.tmp ./cmd/dev-relay
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "-s -w" \
	  -o $(RELAY_DIR)/.arm64.tmp ./cmd/dev-relay
	@mv $(RELAY_DIR)/.amd64.tmp $(RELAY_DIR)/relay-linux-amd64
	@mv $(RELAY_DIR)/.arm64.tmp $(RELAY_DIR)/relay-linux-arm64

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
	else echo "    golangci-lint absent, skipped — install it, see below"; fi

# golangci-lint is not vendored, and `make lint` skips rather than fails without
# it — so a machine that never installed it lints a third less than it appears
# to. Install with the same version the tree was last cleaned against:
#
#   V=2.13.2; A=$$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/'); \
#   curl -sSL https://github.com/golangci/golangci-lint/releases/download/v$$V/golangci-lint-$$V-linux-$$A.tar.gz \
#     | tar xz -C /tmp && install -m0755 /tmp/golangci-lint-$$V-linux-$$A/golangci-lint ~/.local/bin/
#
# No config file: the default linters are what the tree is clean against, and
# errcheck is the one that has actually caught things here. An error deliberately
# ignored is written `_ = f()`, which says it was considered.

# Behind a build tag so `make test` never tries to reach a container engine.
smoke:
	$(GO) test -tags smoke -count=1 -v ./test/smoke/...

install: build
	@mkdir -p $(PREFIX)/bin
	install -m 0755 $(BIN) $(PREFIX)/bin/dev
	@echo "installed $(PREFIX)/bin/dev"

# Point git at the hooks this repository tracks. core.hooksPath lives in
# .git/config, which no clone inherits, so a fresh checkout has to run this or
# the hooks are simply not there — and a hook that is quietly absent is worse
# than none, since the rule it enforces looks handled.
hooks:
	git config core.hooksPath .githooks
	@echo "git hooks enabled from .githooks"

# The relay binaries go too — they are build output, and gitignored. The README
# beside them stays: the embed names the directory, and one that matches nothing
# does not compile.
clean:
	rm -rf dist
	rm -f $(RELAY_DIR)/relay-linux-amd64 $(RELAY_DIR)/relay-linux-arm64
