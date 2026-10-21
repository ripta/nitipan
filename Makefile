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
	$(GO) test -race ./...

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

##@ CI

.PHONY: ci
ci: fmt-check lint vet-linux test build ## Run exactly what CI runs
