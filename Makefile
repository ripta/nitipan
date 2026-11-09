SHELL := bash -o pipefail
.DEFAULT_GOAL := help
.DELETE_ON_ERROR:
.SUFFIXES:
MAKEFLAGS += --warn-undefined-variables

# Version embedded in build artifacts. Override with make build VERSION=1.2.3.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Directory that receives built binaries.
BUILD_DIR ?= build

# Go toolchain entry point.
GO ?= go

# Architecture for the Linux cross-compile.
LINUX_ARCH ?= amd64

# Package import path that carries the injected version string.
VERSION_PKG ?= github.com/space-armada/nitipan/internal/version

# Packages and extra flags for the test target, for example make test TEST_PKGS=./internal/broker TEST_ARGS='-run Mail'.
TEST_PKGS ?= ./...
TEST_ARGS ?=

# Extra flags for the opencode live harness, for example --dry-run, --probe, or --only tabs.
LIVE_OPENCODE_ARGS ?=

# Extra flags for the Codex live harness, for example --dry-run, --probe, --selftest, or --only exec.
LIVE_CODEX_ARGS ?=

.PHONY: help
help: ## Show this help
	@awk 'BEGIN { FS = ":.*## " } \
		/^##@ / { printf "\n%s\n", substr($$0, 5) } \
		/^[a-zA-Z0-9_.\/-]+:([^=]|$$).*## / { printf "  %-24s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

##@ Build

# Linux builds must use cgo and never the osusergo tag. A static build reads only /etc/passwd, and
# the state directory would then resolve differently for LDAP or SSSD users.
.PHONY: build
build: ## Build the nitipan binary into BUILD_DIR
	$(GO) build -ldflags="-X '$(VERSION_PKG).Version=$(VERSION)'" -o $(BUILD_DIR)/nitipan ./cmd/nitipan

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BUILD_DIR)

##@ Test

.PHONY: test
test: ## Run the tests with the race detector
	$(GO) test -race $(TEST_ARGS) $(TEST_PKGS)

##@ Lint

.PHONY: lint
lint: ## Run golangci-lint
	bin/golangci-lint run ./...

.PHONY: fmt
fmt: ## Format the Go source in place
	bin/golangci-lint fmt ./...

.PHONY: fmt-check
fmt-check: ## Verify formatting without modifying files
	bin/golangci-lint fmt --diff ./...

# The Linux code paths are checked only by fixture tests and this cross-compile. The vet runs with
# cgo off because no Linux C toolchain is assumed on the machine that runs it.
.PHONY: vet-linux
vet-linux: ## Vet the Linux build, for example make vet-linux LINUX_ARCH=arm64
	GOOS=linux GOARCH=$(LINUX_ARCH) CGO_ENABLED=0 $(GO) vet ./...

##@ Live checks

# This target drives the real opencode binary on this machine, but against a private server, private
# XDG directories, and a fake model provider. Your opencode service, database, and config are not
# touched, and nothing needs closing. It costs no model usage. It does share nitipan's real state
# directory, and removes only what it created there. Never part of ci. Use LIVE_OPENCODE_ARGS=--probe
# first on a new machine, or --dry-run to see the plan.
.PHONY: live-opencode
live-opencode: ## Run the opencode live checks on this machine, e.g. make live-opencode LIVE_OPENCODE_ARGS=--probe
	python3 scripts/live-checks/opencode.py $(LIVE_OPENCODE_ARGS)

# This target drives the real codex binary on this machine, but against a private CODEX_HOME per
# test config, its own app-server, exec, and TUI processes, and a fake Responses API. Your Codex
# daemon, ~/.codex, and sessions are not touched, and nothing needs closing. It costs no model
# usage. It does share nitipan's real state directory, and removes only what it created there.
# Never part of ci. Use LIVE_CODEX_ARGS=--probe first on a new machine, or --dry-run to see the plan.
.PHONY: live-codex
live-codex: ## Run the Codex live checks on this machine, e.g. make live-codex LIVE_CODEX_ARGS=--probe
	python3 scripts/live-checks/codex.py $(LIVE_CODEX_ARGS)

##@ CI

.PHONY: ci
ci: fmt-check lint vet-linux test build ## Run exactly what CI runs
