BINARY     := docker-orbit
BUILD_DIR  := ./bin
CMD        := ./cmd/docker-orbit
IMAGE      := orbit/proxy
TAG        := latest
# System-wide plugin dir: visible to all users without sudo on path.
# Override with: make PLUGIN_DIR=~/.docker/cli-plugins install-plugin
PLUGIN_DIR := /usr/local/lib/docker/cli-plugins

# /usr/bin/go is a trimmed distro stub with no GOROOT; use the real toolchain.
# Override with: make GO=/path/to/go build
GO         ?= /usr/local/go/bin/go

# VERSION: derived from the nearest git tag; falls back to "dev" outside a
# git checkout or before the first tag exists. See docs/governance/RELEASES.md.
VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOFLAGS    := -trimpath -ldflags="-s -w -X main.version=$(VERSION)"

# Stable, deterministic packages — the blocking local/CI test gate.
# Mirrors .github/workflows/ci.yml's "Test (stable)" step.
# internal/stack and internal/state were excluded here as "known
# flaky/failing" (see CHANGELOG.md); both are now -race-clean and promoted
# back into the blocking gate.
STABLE_PKGS := ./internal/api/... ./internal/cli/... ./internal/compose/... ./internal/config/... \
  ./internal/history/... ./internal/metrics/... ./internal/plugin/... ./internal/proxy/... \
  ./internal/rollout/... ./internal/stack/... ./internal/state/... ./internal/volumes/... \
  ./internal/testing/concurrency/... ./internal/testing/profile/... ./cmd/...

.PHONY: build test test-soak test-integration install-plugin lint docs docker-build docker-image docker-push dist dist-check install-local clean-dist clean help

## build: Compile the docker-orbit binary to ./bin/docker-orbit
build:
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY) $(CMD)
	@echo "Built: $(BUILD_DIR)/$(BINARY) ($(VERSION))"

## test: Run the stable, deterministic test suite with the race detector (fast — mirrors CI's blocking gate)
test:
	$(GO) test -race -count=1 $(STABLE_PKGS)

## test-soak: Run chaos + extended-load suites (slow — minutes, not seconds; see .github/workflows/soak.yml)
test-soak:
	$(GO) test -race -timeout 300s -count=1 ./internal/testing/chaos/...
	$(GO) test -timeout 600s -count=1 ./internal/testing/benchmark/...

## test-integration: Run integration tests (requires Docker)
test-integration:
	DOCKER_INTEGRATION=true $(GO) test -race -timeout 120s ./tests/integration/...

## install-plugin: Copy the binary to Docker CLI plugins directory
install-plugin: build
	@mkdir -p $(PLUGIN_DIR)
	cp $(BUILD_DIR)/$(BINARY) $(PLUGIN_DIR)/$(BINARY)
	@echo "Plugin installed: $(PLUGIN_DIR)/$(BINARY)"
	@echo "Run: docker orbit --help"

## lint: Run golangci-lint
lint:
	golangci-lint run ./...

## docs: Regenerate docs/cli-reference/ from the live Cobra command tree
docs: build
	@$(BUILD_DIR)/$(BINARY) docs --out docs/cli-reference

## docker-build: Build the proxy container image
docker-build:
	docker build -t $(IMAGE):$(TAG) -f docker/proxy/Dockerfile .
	@echo "Built: $(IMAGE):$(TAG)"

## docker-image: Build the Orbit Docker image (orbit/orbit)
docker-image:
	@./docker-build.sh $(TAG)
	@echo "Built: orbit/orbit:$(TAG)"

## docker-push: Build and push Orbit Docker image to registry
docker-push:
	@./docker-build.sh $(TAG) push
	@echo "Pushed: orbit/orbit:$(TAG)"

## dist: Build release artifacts locally with GoReleaser (archives, checksums, deb, rpm) into ./dist — no publish
dist:
	goreleaser release --snapshot --clean

## dist-check: Validate the GoReleaser config
dist-check:
	goreleaser check

## install-local: Build a snapshot and install it as a Docker CLI plugin via the native installer (no download)
install-local: dist
	ORBIT_DIST_DIR=./dist ./install.sh

## clean-dist: Remove GoReleaser release artifacts
clean-dist:
	rm -rf ./dist

## clean: Remove built binaries and release artifacts
clean: clean-dist
	rm -rf $(BUILD_DIR)

## help: Show this help message
help:
	@grep -E '^##' $(MAKEFILE_LIST) | sed 's/## //'
