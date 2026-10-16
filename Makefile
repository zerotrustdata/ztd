# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.

# ztd Platform Root Makefile
# Industry standard orchestration for multi-component proto generation and builds.

SHELL := /bin/bash
# `go install` writes to GOBIN, else GOPATH/bin. Put it (and ~/.local/bin) on PATH so the dev tools installed by
# `make dev-tools` resolve for every recipe even when the invoking shell has not
# sourced the profile the setup scripts updated.
# On Windows, USERPROFILE is the home directory and paths must be converted to
# POSIX style via cygpath. Also ensure /usr/bin is placed ahead of the inherited
# Windows system PATH so that tools using `#!/usr/bin/env bash` (such as npm and
# npx on Windows) resolve to Git/MSYS bash rather than C:\WINDOWS\system32\bash.exe
# (the WSL entry point, which fails with "No such file or directory").
USER_HOME := $(or $(HOME),$(USERPROFILE))
USER_HOME_POSIX := $(shell cygpath -u "$(USER_HOME)" 2>/dev/null || echo "$(USER_HOME)")
GO_BIN_DIR := $(or $(shell go env GOBIN 2>/dev/null),$(if $(shell go env GOPATH 2>/dev/null),$(shell go env GOPATH)/bin,$(USER_HOME_POSIX)/go/bin))
GO_BIN_DIR := $(shell cygpath -u "$(GO_BIN_DIR)" 2>/dev/null || echo "$(GO_BIN_DIR)")
HOST_OS := $(shell go env GOOS 2>/dev/null)
ifeq ($(filter Windows_NT,$(OS))$(filter windows,$(HOST_OS)),)
export PATH := $(GO_BIN_DIR):$(USER_HOME_POSIX)/.local/bin:$(PATH)
else
export PATH := $(GO_BIN_DIR):$(USER_HOME_POSIX)/.local/bin:/usr/bin:$(PATH)
endif
GOTOOLCHAIN ?= auto
export GOTOOLCHAIN
.DEFAULT_GOAL := help
comma := ,

# =============================================================================
# BUILD VARIABLES
# =============================================================================
VERSION := $(shell cat VERSION | tr -d '\n')
BUILD_TIME ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
ifeq ($(strip $(BUILD_TIME)),)
BUILD_TIME := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
endif
ifeq ($(strip $(BUILD_TIME)),unknown)
BUILD_TIME := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
endif
BIN_DIR := bin
# Sidecar checksums must name the file by basename only, so `sha256sum -c <name>.sha256`
# works from the directory holding the binary. $$ZTD_BINARY is expanded by the shell.
WRITE_SHA256 = (cd "$$(dirname "$$ZTD_BINARY")" && sha256sum "$$(basename "$$ZTD_BINARY")") > "$$ZTD_BINARY.sha256"
MAIN_PKG := ./cmd/ztd

# Source provenance stamps use explicit non-Git values and an explicit source
# manifest. Callers may override all three values with reviewed candidate data.
PROVENANCE_SOURCE_PATHS := cmd internal protocol scripts test vendor Makefile VERSION go.mod go.sum buf.gen.yaml
PROVENANCE_EXCLUDES := .git,.env,.ztd*,.local.dev,.venv,node_modules,__pycache__,*.egg-info,.pytest_cache,.ruff_cache,.mypy_cache,bin,build,dist,site,coverage,reports,test-results,auditor-out,*.out,*.test
SOURCE_TREE_HASH ?= $(shell go run ./internal/tools/treehash -mode manifest -base . -exclude '$(PROVENANCE_EXCLUDES)' $(wildcard $(PROVENANCE_SOURCE_PATHS)) 2>/dev/null || echo "unknown")
BUILD_ID ?= $(SOURCE_TREE_HASH)
SOURCE_REVISION ?= unknown
LDFLAGS = -X main.version=$(VERSION) -X main.buildID=$(BUILD_ID) -X main.buildTime=$(BUILD_TIME) -X main.sourceRevision=$(SOURCE_REVISION) -X main.sourceTreeHash=$(SOURCE_TREE_HASH)
HOST_ARCH := $(shell go env GOARCH 2>/dev/null)

# Platform and architecture lists are emitted by the typed ztd-binary catalog.
PLATFORMS := $(shell go run ./internal/tools/ztdbinaries -list-targets 2>/dev/null)

# Build flags
CGO_ENABLED := 0
STRIP_FLAGS := -s -w
TRIMPATH := -trimpath
BUILD_TAGS := netgo,osusergo

# FIPS 140-3 build configuration.
# GOFIPS140 is a BUILD-TIME setting consumed by the go toolchain (not the
# running binary). Pinning to v1.0.0 links the Go Cryptographic Module
# (CMVP Cert #5247, CAVP A6650) and enables FIPS 140-3 approved mode by
# default — the binary enters approved mode on startup and runs its
# integrity/CAST self-tests at init, with no runtime env var required.
# v1.26.0 (frozen from Go 1.26, CAVP A8028) is also available in Go 1.26.5
# but is still Pending Review on the CMVP Modules-In-Process list and must
# NOT be used for a compliance claim.
# The FIPS compliance claim is restricted to linux/amd64 (the tested OE).
GOFIPS140_VERSION := v1.0.0
FIPS_GOOS := linux
FIPS_GOARCH := amd64

# Test configuration
# Integration tests: the gateway package alone has well over a thousand tests that
# exercise real SQLite (modernc, pure-Go) via setupTestHTTPHandler. Under `-race`
# on a 2-vCPU CI runner (more so with GOFIPS140), race-detector background threads
# compete with test goroutines for CPU, producing a >3x slowdown versus multicore
# local machines. 360s timed out in the FIPS job with the victim test only just
# started (0s elapsed) because the package had simply outgrown the budget, not hung.
# Keep in sync with integrationTestTimeout in internal/cli/cmd/test/test.go.
TEST_TIMEOUT := 600s
# Per-package deadlock backstop for unit tests.
TEST_SHORT_TIMEOUT := 180s
# Race detector for Tier 2 integration tests (non-Windows). Tier 1 unit tests
# omit -race for speed; use CI=1 make test-unit or integration tests when
# hunting data races.
TEST_RACE := $(if $(filter windows,$(HOST_OS)),,-race)
# Integration and coverage always disable caching. Unit tests use the Go test
# cache locally; CI sets CI=true and passes -count=1.
TEST_COUNT := -count=1
TEST_UNIT_COUNT := $(if $(CI),-count=1,)
COVERAGE_THRESHOLD := 75

# =============================================================================
# TEST & COVERAGE EXCLUSIONS — single source of truth
# =============================================================================
# Every Go package is tested, locally and in CI, so `make test` and `make ci`
# run the same package set. Exclusions apply only to the coverage profile.
#
# Packages whose statements do not count toward the coverage threshold
# (test harnesses, constants, generated code, example programs). They are
# still tested. Each pattern is matched against Go import paths.
COVERAGE_EXCLUDE_PKGS := \
	ztd/test \
	/cmd/ztd \
	/internal/constants \
	/internal/httpclient \
	/internal/models \
	/internal/testutil \
	/internal/tools/chaos \
	/internal/services/gateway/docs \
	/internal/services/gateway/scripts \
	/internal/services/storage/storagetest \
	ztd/protocol/proto \
	ztd/examples \
	/node_modules

# Files excluded from coverage only (belong to otherwise-tested packages).
EXCLUDE_FILES :=

# Grep chains derived from the lists above — do not edit directly.
_COV_PKG_GREP  := $(foreach p,$(COVERAGE_EXCLUDE_PKGS),| grep -v "$(p)")
_FILE_GREP     := $(foreach f,$(EXCLUDE_FILES),| grep -v "$(f)")
_COV_GREP      := $(_COV_PKG_GREP) $(_FILE_GREP)

# Packages passed to go test.
TEST_PKGS = $$(go list ./... | grep -v /node_modules)

# Filter coverage.out (the raw profile) to remove excluded paths, then report %.
# We operate on the profile data — not on the formatted output of go tool cover.
FILTER_PROFILE = { head -1 coverage.out; tail -n +2 coverage.out $(_COV_GREP); } > coverage_filtered.out
COVERAGE_PCT   = go tool cover -func=coverage_filtered.out | tail -1 | awk '{print $$3}' | sed 's/%//'

# =============================================================================
# TOOLS
# =============================================================================
# Protocol buffer plugin versions - update these when upgrading tools
PROTOC_GEN_GO_VERSION := v1.36.12
PROTOC_GEN_DOC_VERSION := v1.5.1

# Developer toolchain pins. These are the only copy: CI installs them with
# `make ci-tools` and reads GOLANGCI_LINT_VERSION for golangci-lint-action;
# `make dev-tools` and `make dev-check` read them locally.
BUF_VERSION := v1.73.0
GOLANGCI_LINT_VERSION := v2.14.0
GOVULNCHECK_VERSION := v1.9.0
SWAG_VERSION := v1.16.6

# buf runs these as local plugins from PATH (see buf.gen.yaml); the committed
# generated code is produced by exactly these versions.
PROTO_PLUGIN_PKGS := \
	google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION) \
	github.com/pseudomuto/protoc-gen-doc/cmd/protoc-gen-doc@$(PROTOC_GEN_DOC_VERSION)
# CI gets golangci-lint from golangci-lint-action, so ci-tools omits it.
GO_CI_TOOL_PKGS := \
	github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION) \
	$(PROTO_PLUGIN_PKGS) \
	golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) \
	github.com/swaggo/swag/cmd/swag@$(SWAG_VERSION)
GO_DEV_TOOL_PKGS := \
	$(GO_CI_TOOL_PKGS) \
	github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

# Recursive (=) so the lookup runs when a recipe uses it, after buf-install has
# had the chance to put buf on PATH. $(shell) does not see the exported PATH on
# GNU make < 4.4, so the Go bin dir is added explicitly.
BUF = $(shell export PATH="$(GO_BIN_DIR):$$PATH"; command -v buf 2>/dev/null || echo "$(GO_BIN_DIR)/buf")

# =============================================================================
# HELP
# =============================================================================
.PHONY: help
help:
	@printf '%s\n' \
		'ztd developer commands' \
		'' \
		'Usage: make <target> [VARIABLE=value]' \
		'Windows: GNU Make and Git for Windows Bash are required.' \
		'' \
		'Setup' \
		'  dev-setup                 Install everything required by make ci' \
		'  dev-check                 Verify the local development toolchain' \
		'  dev-tools                 Install pinned Go development tools' \
		'  dev-node                  Install Node dependencies for all workspaces' \
		'' \
		'CI' \
		'  ci                        Run the complete local CI pipeline' \
		'  ci-platform               Run platform, protocol, and documentation CI' \
		'  ci-console                Run console and adapter checks; refresh embeds' \
		'  ci-explorer               Run evaluation explorer checks and verify its embed' \
		'  ci-protocol               Run Node protocol tests and Go conformance vectors' \
		'  ci-website                Run website tests and build' \
		'' \
		'Build and release' \
		'  build                     Build ztd for the host platform' \
		'  build-all                 Build ztd for every supported platform' \
		'  build-target              Build one target (requires GOOS and GOARCH)' \
		'  build-linux               Build every supported Linux architecture' \
		'  build-darwin              Build every supported macOS architecture' \
		'  build-windows             Build every supported Windows architecture' \
		'  build-compressed          Build for the host and compress with UPX' \
		'  fips-build                Build the Linux AMD64 FIPS variant' \
		'  fips-verify               Build and verify the FIPS variant' \
		'  verify-static             Check a built Linux binary is fully static (STATIC_BINARY=...)' \
		'  version-sync              Sync version declarations to VERSION' \
		'  release-rehearse          Run the release workflow on main without publishing' \
		'  release                   Tag and push the release in VERSION (needs a passing rehearsal)' \
		'' \
		'Test and quality' \
		'  test                      Run unit and in-process integration tests' \
		'  test-unit                 Run Tier 1 unit tests' \
		'  test-integration          Run Tier 2 in-process integration tests' \
		'  test-enrollment-burst     Run the enrollment timing contract without instrumentation' \
		'  test-operator-burst       Run the 2000-Operator transport burst without instrumentation' \
		'  test-coverage             Run tests and enforce $(COVERAGE_THRESHOLD)% coverage (PKG=..., VERBOSE=true)' \
		'  test-cross-enrollment     Run Tier 3 cross-enrollment E2E tests' \
		'  test-scale                Operator fleet scale qualification (SCALE_COUNT=100, SCALE_ARGS=...)' \
		'  test-airgap               Verify the vendored air-gap build' \
		'  test-go-import            Build a fresh module that imports ztd via a replace directive' \
		'  test-onboarding           Hermetic tests for the setup and quickstart scripts' \
		'  test-onboarding-docker    Fresh-host onboarding in clean Linux containers (DISTROS=..., SCENARIOS=...)' \
		'  lint                      Run all lint and quality checks' \
		'  fmt                       Format all Go source files' \
		'  vulncheck                 Check Go dependencies for vulnerabilities' \
		'  bsl-headers-check         Verify first-party BSL 1.1 headers' \
		'  version-sync-check        Check version declarations against VERSION' \
		'  doctrines-validate        Validate doctrine JSON syntax' \
		'' \
		'Generated artifacts' \
		'  proto-generate            Generate Go and Node protobuf artifacts' \
		'  constants-generate        Generate constants from the event registry' \
		'  constants-check           Check generated constants for drift' \
		'  explorer-catalog-generate Generate the explorer scenario catalog and wire enums from Go' \
		'  explorer-catalog-check    Check the explorer scenario catalog and wire enums for drift' \
		'  third-party-notices       Regenerate THIRD_PARTY_NOTICES.md' \
		'  third-party-notices-check Check THIRD_PARTY_NOTICES.md for drift' \
		'  swagger-generate          Generate the Gateway OpenAPI specification' \
		'  website-build             Build zerotrustdata.io: homepage, journal, llms-full.txt' \
		'  website-test              Test the website generator and Worker' \
		'  website-bundle-check      Validate the Cloudflare Worker bundle (wrangler dry run)' \
		'' \
		'Components' \
		'  console-build             Build the console SPA' \
		'  console-embed             Build and embed the console SPA' \
		'  console-embed-check       Check the committed console embed for drift' \
		'  console-test              Run console tests' \
		'  console-lint              Typecheck and lint the console' \
		'  explorer-embed            Build and embed the evaluation explorer' \
		'  explorer-embed-check      Check the committed explorer embed for drift' \
		'  explorer-test             Run evaluation explorer tests' \
		'  explorer-lint             Typecheck and lint the evaluation explorer' \
		'  claude-plugin-test        Test, shellcheck, and validate the Claude Code plugin' \
		'' \
		'Run locally' \
		'  quickstart                Start a Gateway, sign in, and run a first governed action (QUICKSTART_ARGS)' \
		'  up                        Build and start the Gateway on this host (GW_ARGS)' \
		'  up-public                 Like up, listening on all interfaces for Operators on other hosts' \
		'  gw                        Start the Gateway with every embedded Operator capability (GW_CAPABILITIES, MODEL_STORAGE_ROOT, GW_ARGS)' \
		'  gw-public                 Like gw, listening on all interfaces for Operators on other hosts' \
		'  operator                  Run an outbound Operator in the foreground (OPERATOR_ARGS)' \
		'  status                    Show Gateway status, Operators, and pending enrollments' \
		'  down                      Stop local Operators, then the host Gateway' \
		'  stop                      Alias for down' \
		'  ZTD_ARGS                  Global CLI flags shared by up, gw, operator, status, and down' \
		'' \
		'Maintenance' \
		'  clean                     Remove build artifacts and Go caches' \
		'  harness-clean             Remove stale test harness directories' \
		'  buf-install               Install Buf when it is unavailable'

.PHONY: constants-generate constants-check
constants-generate:
	@echo "Regenerating protocol constants from protocol/constants/events.json..."
	@go run ./internal/tools/constgen -write

constants-check:
	@echo "Checking protocol/constants/events.json registry and generated constants..."
	@go run ./internal/tools/constgen -check

# The evaluation explorer's scenario catalog module is generated from the Go
# scenario catalog, and its campaign wire enum module from the eval.proto
# descriptors. Never hand-edit scenario-catalog.generated.ts or
# proto-enums.generated.ts.
.PHONY: explorer-catalog-generate explorer-catalog-check
explorer-catalog-generate:
	@echo "Regenerating the evaluation explorer scenario catalog and wire enums from Go..."
	@go run ./internal/tools/explorercatalog -write

explorer-catalog-check:
	@echo "Checking the evaluation explorer scenario catalog and wire enums against Go..."
	@go run ./internal/tools/explorercatalog -check

# THIRD_PARTY_NOTICES.md is generated from vendor/modules.txt and the production
# npm locks of console/ and evaluation-explorer/. Never hand-edit it. The check
# regenerates into a temp file and fails if the committed file is stale.
.PHONY: third-party-notices third-party-notices-check
third-party-notices:
	@echo "Regenerating THIRD_PARTY_NOTICES.md..."
	@go run ./internal/tools/thirdpartynotices -repo . -out THIRD_PARTY_NOTICES.md

third-party-notices-check:
	@echo "Checking THIRD_PARTY_NOTICES.md against vendor/ and the npm locks..."
	@scratch=$$(. scripts/lib/scratch.sh && ztd_scratch_dir "$(CURDIR)" ztd-notices) && trap 'rm -rf "$$scratch"' EXIT && \
		go run ./internal/tools/thirdpartynotices -repo . -out "$$scratch/THIRD_PARTY_NOTICES.md" && \
		diff -u --strip-trailing-cr THIRD_PARTY_NOTICES.md "$$scratch/THIRD_PARTY_NOTICES.md" || { echo "THIRD_PARTY_NOTICES.md is stale; run 'make third-party-notices'"; exit 1; }

.PHONY: website-build
website-build:
	@echo "Building zerotrustdata.io from website/src, website/content and README.md..."
	@cd website && npm run build

.PHONY: website-test
website-test:
	@echo "Testing the zerotrustdata.io generator and Worker..."
	@cd website && npm run check

.PHONY: website-bundle-check
website-bundle-check:
	@echo "Validating the Cloudflare Worker bundle..."
	@cd website && npx wrangler deploy --dry-run

# =============================================================================
# PROTOCOL GENERATION
# =============================================================================
.PHONY: proto-generate
proto-generate: proto-go proto-node
	@echo "Protobuf generation complete."

.PHONY: proto-go
proto-go: buf-install proto-tools-install
	@echo "Generating Go Protobuf code with Buf..."
	@$(BUF) generate protocol/proto
	@echo "Go Protobuf generation complete."

.PHONY: proto-node-install
proto-node-install:
	@if [ ! -f "protocol/node/node_modules/.bin/protoc-gen-es" ] && [ ! -f "protocol/node/node_modules/.bin/protoc-gen-es.cmd" ]; then \
		echo "Installing Node Protobuf generator..."; \
		npm ci --prefix protocol/node; \
	fi

.PHONY: proto-node
proto-node: buf-install proto-node-install
	@echo "Generating Node TypeScript Protobuf code with Buf..."
	@cd protocol/node && PATH="$$(pwd)/node_modules/.bin:$$PATH" $(BUF) generate ../proto --template buf.gen.yaml
	@echo "Node TypeScript Protobuf generation complete."

# =============================================================================
# TOOL INSTALLATION
#
# buf ships its own compiler and invokes the protoc-gen-* plugins directly, so
# the standalone protoc binary is not required.
# =============================================================================
.PHONY: buf-install
buf-install:
	@if ! command -v buf &> /dev/null; then \
		echo "Installing Buf $(BUF_VERSION)..."; \
		go install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION); \
	fi

# Installs any missing protoc plugin that buf.gen.yaml invokes. Existing binaries
# are left alone; `make dev-tools` reinstalls them at the pinned versions.
.PHONY: proto-tools-install
proto-tools-install:
	@for pkg in $(PROTO_PLUGIN_PKGS); do \
		bin=$${pkg%@*}; bin=$${bin##*/}; \
		if ! command -v $$bin &> /dev/null; then \
			echo "Installing $$bin ($$pkg)..."; \
			go install $$pkg || exit 1; \
		fi; \
	done

# =============================================================================
# DEVELOPER TOOLCHAIN
#
# OS-level prerequisites (git, make, go, node, rg, bc, a C compiler)
# are installed by scripts/linux-setup.sh, scripts/macos-setup.sh, or
# scripts/windows-setup.ps1. Everything those need on top is pinned above and
# installed here, so the setup scripts and a manual install share one definition.
# =============================================================================
.PHONY: dev-setup
dev-setup:
	@$(MAKE) dev-tools-if-needed
	@$(MAKE) dev-node
	@$(MAKE) hooks-install
	@echo "Developer toolchain installed. Verify with: make dev-check"

.PHONY: test-release-scripts
test-release-scripts:
	@bash scripts/test-release-scripts.sh

.PHONY: test-commit-hygiene
test-commit-hygiene:
	@bash scripts/test-commit-hygiene.sh

# Onboarding: hermetic script tests (seconds, no network) and the clean-room
# Docker matrix that replays a new user's first run on fresh Linux hosts.
.PHONY: test-onboarding test-onboarding-docker
test-onboarding:
	@bash scripts/test-onboarding.sh

DISTROS ?= ubuntu debian fedora arch
SCENARIOS ?= build-only rerun quickstart
test-onboarding-docker:
	@bash scripts/onboarding/run-matrix.sh --distros "$(DISTROS)" --scenarios "$(SCENARIOS)"

# Point git at the tracked hooks: pre-commit and pre-push run
# scripts/check-commit-hygiene.sh (ignored paths, >5 MiB blobs, executables,
# local home paths, executable-bit/shebang mismatches, gitleaks). CI runs the
# same script over pushed commits.
.PHONY: hooks-install
hooks-install:
	@git config core.hooksPath scripts/git-hooks
	@echo "Git hooks installed (core.hooksPath=scripts/git-hooks)."

# Go-based tools (buf, protoc plugins, golangci-lint, govulncheck, swag).
# Explicit `make dev-tools` refreshes every tool to the pinned versions;
# `make ci-tools` installs the CI subset.
dev-tools: TOOL_PKGS = $(GO_DEV_TOOL_PKGS)
ci-tools: TOOL_PKGS = $(GO_CI_TOOL_PKGS)
.PHONY: dev-tools ci-tools
dev-tools ci-tools:
	@echo "Installing Go tools into $(GO_BIN_DIR)..."
	@for pkg in $(TOOL_PKGS); do \
		echo "  go install $$pkg"; \
		go install $$pkg || exit 1; \
	done

# Setup scripts reuse binaries that are already installed. Keep the explicit
# `dev-tools` target above for contributors who want to refresh pinned tools.
.PHONY: dev-tools-if-needed
dev-tools-if-needed:
	@set -e; source scripts/lib/dev-setup-common.sh; for pkg in $(GO_DEV_TOOL_PKGS); do \
		ztd_dev_tool_if_needed "$$pkg" || exit 1; \
	done

.PHONY: dev-node
dev-node:
	@echo "Installing Node dependencies (protocol/node, console, ztd-adapter, website)..."
	@set -e; source scripts/lib/dev-setup-common.sh; for dir in protocol/node console ztd-adapter website; do \
		if ! ztd_node_deps_current "$$dir"; then \
			echo "  installing dependencies in $$dir..."; \
			npm ci --prefix "$$dir"; \
		else \
			echo "  $$dir dependencies: current — skipping"; \
		fi; \
	done
	@npm run build --prefix ztd-adapter

# Preflight for `make ci`: reports every missing or mismatched tool at once.
.PHONY: dev-check
dev-check:
	@bash scripts/dev-check.sh

# =============================================================================
# BUILD
# =============================================================================

# bin/ holds only catalogued ztd-<os>-<arch> artifacts (what the Gateway serves to
# other hosts); ./ztd is the host convenience copy. It is a copy, not a symlink:
# os.Executable resolves symlinks, which would move the Gateway's adjacent bin/
# lookup to bin/bin.
# Install a built binary over an existing path without stopping a running copy.
# Direct cp fails with ETXTBSY when the target is executing; rename replaces the
# directory entry while the old inode stays mapped for the running process.
INSTALL_EXECUTABLE = \
	if [ "$(HOST_OS)" = "windows" ]; then \
		cp "$$INSTALL_SRC" "$$INSTALL_DST"; \
	else \
		cp "$$INSTALL_SRC" "$$INSTALL_DST.new" && chmod +x "$$INSTALL_DST.new" && mv -f "$$INSTALL_DST.new" "$$INSTALL_DST"; \
	fi

# Builds one catalogued target from the shell variables GOOS and GOARCH. Linux
# targets link the Go Cryptographic Module (GOFIPS140), as releases do. Previous
# outputs are removed first so a failed build never leaves a stale binary.
BUILD_ONE = \
	ZTD_BINARY=$(BIN_DIR)/ztd-$$GOOS-$$GOARCH; \
	if [ "$$GOOS" = "windows" ]; then ZTD_BINARY=$$ZTD_BINARY.exe; fi; \
	rm -f "$$ZTD_BINARY" "$$ZTD_BINARY.sha256"; \
	if [ "$$GOOS" = "linux" ]; then FIPS_ENV="GOFIPS140=$(GOFIPS140_VERSION)"; else FIPS_ENV="-u GOFIPS140"; fi; \
	echo "Building $$GOOS/$$GOARCH -> $$ZTD_BINARY..."; \
	env $$FIPS_ENV CGO_ENABLED=$(CGO_ENABLED) GOOS=$$GOOS GOARCH=$$GOARCH go build $(TRIMPATH) -tags $(BUILD_TAGS) -ldflags "$(LDFLAGS) $(STRIP_FLAGS) -X main.platform=$$GOOS/$$GOARCH" -o "$$ZTD_BINARY" $(MAIN_PKG) || { echo "ERROR: build failed for $$GOOS/$$GOARCH"; exit 1; }; \
	test -s "$$ZTD_BINARY" || { echo "ERROR: $$ZTD_BINARY missing or empty after build"; exit 1; }; \
	$(WRITE_SHA256)

EXPLORER_DIST := evaluation-explorer/dist
EXPLORER_EMBED := internal/services/gateway/explorer/static

# Frontend builds are incremental: each dist rebuilds only when its sources
# change, and each embed refreshes only when its dist is newer. The explorer
# bundle also compiles in VERSION and imports ztd-adapter/src. The Go source
# lister avoids platform-specific find commands during Makefile parsing. It is
# built once to a fixed path (go build skips the link when current) instead of
# three `go run` temp builds per make invocation. Goals that only drive an
# already-built ./ztd, or only run the website's npm scripts, need no Go
# toolchain, so they skip the lister entirely.
# The `||` keeps a failed build non-empty: shells that exec a lone command
# report "command not found" outside the 2>&1 capture.
TOOLCHAIN_FREE_GOALS := help status down stop website-test website-build website-bundle-check
SOURCE_FILES_TOOL := $(BIN_DIR)/.tools/sourcefiles$(if $(filter windows,$(HOST_OS)),.exe,)
ifneq ($(filter-out $(TOOLCHAIN_FREE_GOALS),$(or $(MAKECMDGOALS),$(.DEFAULT_GOAL))),)
SOURCE_FILES_BUILD := $(shell go build -o $(SOURCE_FILES_TOOL) ./internal/tools/sourcefiles 2>&1 || echo "go build exited $$?")
ifneq ($(SOURCE_FILES_BUILD),)
$(error building $(SOURCE_FILES_TOOL) failed: $(SOURCE_FILES_BUILD))
endif
source_files = $(shell $(SOURCE_FILES_TOOL) -base . $(1))
else
source_files =
endif

EXPLORER_SOURCES := $(call source_files,-exclude node_modules$(comma)dist$(comma)tests$(comma)e2e evaluation-explorer) $(call source_files,ztd-adapter/src) VERSION

evaluation-explorer/node_modules/.package-lock.json: evaluation-explorer/package-lock.json
	@npm ci --prefix evaluation-explorer

$(EXPLORER_DIST)/index.html: evaluation-explorer/node_modules/.package-lock.json $(EXPLORER_SOURCES)
	@echo "Building the evaluation explorer..."
	@npm run build --prefix evaluation-explorer
	@touch $@

$(EXPLORER_EMBED)/index.html: $(EXPLORER_DIST)/index.html
	@rm -rf $(EXPLORER_EMBED) && cp -r $(EXPLORER_DIST) $(EXPLORER_EMBED)
	@echo "Embedded explorer updated from fresh explorer build."

.PHONY: explorer-embed
explorer-embed: $(EXPLORER_EMBED)/index.html

# Always rebuild: CI must compare the committed embed, never refresh it first.
.PHONY: explorer-embed-check
explorer-embed-check: evaluation-explorer/node_modules/.package-lock.json
	@npm run build --prefix evaluation-explorer
	@diff -r $(EXPLORER_DIST) $(EXPLORER_EMBED) >/dev/null || { echo "ERROR: $(EXPLORER_EMBED) is stale; run 'make explorer-embed' and commit the result"; exit 1; }
	@echo "Embedded explorer is current."

CONSOLE_DIST := console/dist
CONSOLE_EMBED := internal/services/gateway/console/static
CONSOLE_SOURCES := $(call source_files,-exclude node_modules$(comma)dist$(comma)tests console)

$(CONSOLE_EMBED)/index.html: $(CONSOLE_DIST)/index.html
	@rm -rf $(CONSOLE_EMBED) && cp -r $(CONSOLE_DIST) $(CONSOLE_EMBED)
	@echo "Embedded console updated from fresh console build."

.PHONY: console-embed
console-embed: $(CONSOLE_EMBED)/index.html

# Source builds use committed frontend assets and require only Go. Refresh
# changed frontend sources explicitly with console-embed / explorer-embed.
.PHONY: build
build:
	@echo "Building ztd for the host platform..."
	@mkdir -p $(BIN_DIR)
	@rm -f $(BIN_DIR)/ztd-binaries.json
	@set -e; \
	ZTD_BINARY=$(BIN_DIR)/ztd-$(HOST_OS)-$(HOST_ARCH); \
	if [ "$(HOST_OS)" = "windows" ]; then \
		ZTD_BINARY=$$ZTD_BINARY.exe; \
		ROOT_COPY=ztd.exe; \
	else \
		ROOT_COPY=ztd; \
	fi; \
	echo "Building $(HOST_OS)/$(HOST_ARCH) -> $$ZTD_BINARY..."; \
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(HOST_OS) GOARCH=$(HOST_ARCH) go build $(TRIMPATH) -tags $(BUILD_TAGS) -ldflags "$(LDFLAGS) $(STRIP_FLAGS) -X main.platform=$(HOST_OS)/$(HOST_ARCH)" -o $$ZTD_BINARY $(MAIN_PKG); \
	$(WRITE_SHA256); \
	INSTALL_SRC=$$ZTD_BINARY INSTALL_DST=$$ROOT_COPY; $(INSTALL_EXECUTABLE)
	@echo "Build complete. Binary: $(BIN_DIR)/ztd-$(HOST_OS)-$(HOST_ARCH)$(if $(filter windows,$(HOST_OS)),.exe,)"

.PHONY: build-compressed
build-compressed: build
	@echo "Compressing binary with UPX..."
	@if ! command -v upx &> /dev/null; then \
		echo "Error: UPX is not installed. Install it with:"; \
		echo "  Debian/Ubuntu: sudo apt-get install upx-ucl"; \
		echo "  macOS:         brew install upx"; \
		echo "  Arch:          sudo pacman -S upx"; \
		exit 1; \
	fi
	@BINARY=$(BIN_DIR)/ztd-$(HOST_OS)-$(HOST_ARCH); \
	if [ "$(HOST_OS)" = "windows" ]; then \
		BINARY=$$BINARY.exe; \
	fi; \
	upx --best --lzma $$BINARY; \
	echo "Compressed binary: $$BINARY"

.PHONY: build-target
build-target:
	@test -n "$(GOOS)" || { echo "ERROR: GOOS is required for build-target"; exit 1; }
	@test -n "$(GOARCH)" || { echo "ERROR: GOARCH is required for build-target"; exit 1; }
	@mkdir -p $(BIN_DIR)
	@rm -f $(BIN_DIR)/ztd-binaries.json
	@set -e; GOOS=$(GOOS); GOARCH=$(GOARCH); $(BUILD_ONE)

.PHONY: build-linux build-darwin build-windows
build-linux build-darwin build-windows: build-%:
	@mkdir -p $(BIN_DIR)
	@rm -f $(BIN_DIR)/ztd-binaries.json
	@set -e; GOOS=$*; for GOARCH in $(patsubst $*/%,%,$(filter $*/%,$(PLATFORMS))); do $(BUILD_ONE); done

.PHONY: build-all
build-all:
	@echo "Building ztd for all platforms (FIPS 140-3 for linux)..."
	@test -n "$(strip $(PLATFORMS))" || { echo "ERROR: ztdbinaries catalog lists no build targets"; exit 1; }
	@mkdir -p $(BIN_DIR)
	@# Remove previous outputs first so a failed build can never leave a stale binary
	@# (or checksum or manifest) that a later completeness check would accept.
	@rm -f $(BIN_DIR)/ztd-binaries.json $(BIN_DIR)/checksums-sha256.txt
	@set -e; \
	for platform in $(PLATFORMS); do \
		GOOS=$${platform%/*}; GOARCH=$${platform#*/}; $(BUILD_ONE); \
	done
	@set -e; \
	HOST_ZTD_BINARY=$(BIN_DIR)/ztd-$(HOST_OS)-$(HOST_ARCH); \
	if [ "$(HOST_OS)" = "windows" ]; then \
		HOST_ZTD_BINARY=$$HOST_ZTD_BINARY.exe; \
		ROOT_COPY=ztd.exe; \
	else \
		ROOT_COPY=ztd; \
	fi; \
	INSTALL_SRC=$$HOST_ZTD_BINARY INSTALL_DST=$$ROOT_COPY; $(INSTALL_EXECUTABLE)
	@go run ./internal/tools/ztdbinaries --root $(BIN_DIR) --version "$(VERSION)" --build-id "$(BUILD_ID)" --build-time "$(BUILD_TIME)" --source-revision "$(SOURCE_REVISION)" --source-tree-hash "$(SOURCE_TREE_HASH)"
	@echo "Multi-platform build complete. Manifest and checksums: $(BIN_DIR)/ztd-binaries.json"
	@echo "Host binary copied: ./ztd ($(HOST_OS)/$(HOST_ARCH))"

# FIPS 140-3 build variant.
# Produces a linux/amd64 binary linked against the Go Cryptographic Module
# v1.0.0 (CMVP Cert #5247) with FIPS 140-3 approved mode enabled by default.
# GOFIPS140 is set at BUILD TIME ONLY — the resulting binary enters approved
# mode on startup without any runtime env var. See:
#   https://go.dev/doc/security/fips140
# Verify the deployed binary with: ./ztd version --fips
.PHONY: fips-build
fips-build:
	@echo "Building ztd with FIPS 140-3 approved mode (GOFIPS140=$(GOFIPS140_VERSION), $(FIPS_GOOS)/$(FIPS_GOARCH))..."
	@mkdir -p $(BIN_DIR)
	@ZTD_BINARY=$(BIN_DIR)/ztd-fips-$(FIPS_GOOS)-$(FIPS_GOARCH); \
	echo "Building $(FIPS_GOOS)/$(FIPS_GOARCH) -> $$ZTD_BINARY..."; \
	GOFIPS140=$(GOFIPS140_VERSION) CGO_ENABLED=$(CGO_ENABLED) GOOS=$(FIPS_GOOS) GOARCH=$(FIPS_GOARCH) \
		go build $(TRIMPATH) -tags $(BUILD_TAGS) \
		-ldflags "$(LDFLAGS) $(STRIP_FLAGS) -X main.platform=$(FIPS_GOOS)/$(FIPS_GOARCH)" \
		-o $$ZTD_BINARY $(MAIN_PKG); \
	$(WRITE_SHA256); \
	INSTALL_SRC=$$ZTD_BINARY INSTALL_DST=ztd-fips; $(INSTALL_EXECUTABLE)
	@echo "FIPS build complete. Binary: $(BIN_DIR)/ztd-fips-$(FIPS_GOOS)-$(FIPS_GOARCH)"
	@echo "Verify with: ./ztd-fips version --fips"

# Quick FIPS self-check: build the FIPS variant and confirm the binary reports
# FIPS 140-3 approved mode AND enforcement are active via the native
# crypto/fips140 module API. GODEBUG=fips140=only is set at runtime to switch
# the module into enforcement mode (rejecting non-approved primitives);
# GOFIPS140 alone only enables approved mode (GODEBUG defaults to fips140=on).
# Exits non-zero if the self-check fails. Intended for CI and release gates.
.PHONY: fips-verify
fips-verify: fips-build
	@echo "Verifying FIPS 140-3 approved mode and enforcement in the built binary..."
	@GODEBUG=fips140=only ./ztd-fips version --fips
	@echo "FIPS 140-3 self-check passed."

# Shipped Linux binaries carry no libc dependency. CI checks both the standard
# and the FIPS binary through this one target.
STATIC_BINARY ?= $(BIN_DIR)/ztd-linux-amd64
.PHONY: verify-static
verify-static:
	@file "$(STATIC_BINARY)" | grep -q "statically linked" || \
		{ echo "ERROR: $(STATIC_BINARY) is not statically linked"; file "$(STATIC_BINARY)"; exit 1; }
	@ldd "$(STATIC_BINARY)" 2>&1 | grep -q "not a dynamic executable" || \
		{ echo "ERROR: $(STATIC_BINARY) has dynamic dependencies"; ldd "$(STATIC_BINARY)"; exit 1; }
	@echo "$(STATIC_BINARY) is statically linked."

# Format all Go source files. Build targets no longer format the working tree;
# run this explicitly or wire it into a pre-commit hook.
.PHONY: fmt
fmt:
	@gofmt -w .
	@echo "Formatting complete."

# =============================================================================
# TEST
# =============================================================================
# Core test targets
.PHONY: test
test: test-unit test-integration
	@echo "All tests completed successfully."

# Unit Tests: no build tags, so integration and e2e files are excluded.
.PHONY: test-unit
test-unit: constants-check
	@echo "Running Tier 1 (Unit) tests..."
	@go test $(TEST_UNIT_COUNT) -timeout $(TEST_SHORT_TIMEOUT) $(TEST_PKGS)


# Tier 2: In-Process Integration Tests - no external dependencies
.PHONY: test-integration
test-integration:
	@echo "Running Tier 2 (In-Process Integration) tests..."
	@go test $(if $(TEST_P),-p=$(TEST_P),) -tags=integration $(TEST_RACE) $(TEST_COUNT) -timeout $(TEST_TIMEOUT) $(TEST_PKGS)

# Timing contracts run separately from race and coverage instrumentation.
.PHONY: test-enrollment-burst
test-enrollment-burst:
	@echo "Running the uninstrumented enrollment burst timing contract..."
	@go test -tags=integration $(TEST_COUNT) -timeout $(TEST_TIMEOUT) \
		-run '^TestPlatformEnrollmentBurst$$' ./internal/services/gateway

# The Operator transport burst enrolls 2000 in-process Operators, a size only
# an uninstrumented build carries; the race build runs the same contract small.
.PHONY: test-operator-burst
test-operator-burst:
	@echo "Running the uninstrumented 2000-Operator transport burst..."
	@go test -tags=integration $(TEST_COUNT) -timeout 15m \
		-run '^TestOperatorTransportBoundary_BurstDeliversEveryCommittedIdentity$$' ./test

# Tier 3: Cross-Enrollment E2E Tests - a gateway enrolling as an operator of
# another gateway. Requires a running primary Gateway and a secondary Gateway
# started in operator mode against it, with the owner bootstrapped.
.PHONY: test-cross-enrollment
test-cross-enrollment:
	@echo "Running Tier 3 cross-enrollment E2E tests..."
	@./ztd test e2e --run 'TestCrossEnrollment'

# Operator fleet scale qualification: starts an isolated Gateway in a fresh
# scratch root, deploys SCALE_COUNT real Operators, runs the fleet soak/fan-out
# and Gateway restart scenarios, and always tears down its own processes.
# Never part of `make test`. Extra flags: SCALE_ARGS='--soak 10m --root DIR'.
SCALE_COUNT ?= 100
.PHONY: test-scale
test-scale: build
	@./ztd test scale --count $(SCALE_COUNT) $(SCALE_ARGS)

# Air-Gap Verification: verify vendored build works without network access
.PHONY: test-airgap
test-airgap:
	@echo "Running air-gap verification..."
	@echo "  1. Verifying vendor directories exist..."
	@test -d vendor/ || { echo "ERROR: vendor/ directory missing — run 'go mod vendor'"; exit 1; }
	@echo "  2. Building with vendored modules (-mod=vendor)..."
	@go build -mod=vendor ./... || { echo "ERROR: vendored build failed"; exit 1; }
	@echo "Air-gap verification PASSED."

.PHONY: test-go-import
test-go-import:
	@bash scripts/smoke-test-go.sh

# =============================================================================
# CONSOLE — Gateway-embedded browser frontend (console/, served at /console/)
# =============================================================================
# Requires node_modules installed first:
#   cd console && npm ci

console/node_modules/.package-lock.json: console/package-lock.json
	@npm ci --prefix console

$(CONSOLE_DIST)/index.html: console/node_modules/.package-lock.json $(CONSOLE_SOURCES)
	@echo "Building the console SPA..."
	@npm run build --prefix console
	@touch $@

.PHONY: console-build
console-build: $(CONSOLE_DIST)/index.html

.PHONY: console-lint
console-lint:
	@echo "Typechecking and linting the console..."
	@cd console && npm run typecheck && npm run lint

.PHONY: console-test
console-test:
	@echo "Running the console vitest suite..."
	@cd console && npm test

.PHONY: explorer-lint
explorer-lint:
	@echo "Typechecking and linting the evaluation explorer..."
	@cd evaluation-explorer && npm run typecheck && npm run lint

.PHONY: explorer-test
explorer-test:
	@echo "Running the evaluation explorer vitest suite..."
	@cd evaluation-explorer && npm test

# The Claude Code plugin (claude-plugin/) and its marketplace (.claude-plugin/).
# Manifest validation needs the claude CLI and is skipped without it.
.PHONY: claude-plugin-test
claude-plugin-test:
	@echo "Testing the Claude Code plugin..."
	@sh claude-plugin/test/run.sh
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck -s sh claude-plugin/hooks/*.sh claude-plugin/scripts/*.sh claude-plugin/test/*.sh; \
	else \
		echo "shellcheck not found; skipping shell lint (install via 'choco install shellcheck' on Windows or 'apt install shellcheck' on Debian/Ubuntu)"; \
	fi
	@if command -v claude >/dev/null 2>&1; then \
		claude plugin validate --strict claude-plugin && claude plugin validate --strict .; \
	else \
		echo "claude CLI not found; skipping manifest validation"; \
	fi

# Rebuilds the console and fails if the committed embed is stale.
.PHONY: console-embed-check
console-embed-check: console-build
	@diff -r $(CONSOLE_DIST) $(CONSOLE_EMBED) >/dev/null || { echo "ERROR: $(CONSOLE_EMBED) is stale; run 'make console-embed' and commit the result"; exit 1; }
	@echo "Embedded console is current."

# Coverage tests
.PHONY: test-coverage
test-coverage:
	@echo "Running tests with coverage (threshold: $(COVERAGE_THRESHOLD)%)..."
	@go test -tags=integration $(TEST_RACE) -timeout $(TEST_TIMEOUT) \
		-coverprofile=coverage.out -covermode=atomic \
		$(if $(VERBOSE),-v,) \
		$(if $(PKG),$(PKG),$(TEST_PKGS))
	@$(FILTER_PROFILE)
	@COVERAGE=$$($(COVERAGE_PCT)); \
	BELOW=$$(if command -v bc >/dev/null 2>&1; then echo "$$COVERAGE < $(COVERAGE_THRESHOLD)" | bc -l; else awk -v cov="$$COVERAGE" -v th="$(COVERAGE_THRESHOLD)" 'BEGIN { print (cov < th) ? 1 : 0 }'; fi); \
	if [ "$$BELOW" -eq 1 ]; then \
		echo "Coverage $$COVERAGE% is below $(COVERAGE_THRESHOLD)% threshold"; \
		exit 1; \
	fi; \
	echo "Coverage $$COVERAGE% meets $(COVERAGE_THRESHOLD)% threshold"

# =============================================================================
# LINT & QUALITY
# =============================================================================
.PHONY: lint
lint: vulncheck doctrines-validate
	@if grep -rnE --include='*.go' 'os\.(MkdirTemp|CreateTemp)\(""' cmd internal test; then \
		echo "error: empty-dir os.MkdirTemp/os.CreateTemp falls back to the OS temp dir; use testutil.TempDir or fs.RuntimeFileService (INV-FS-11)"; exit 1; fi
	@exec golangci-lint run
	@echo "All linting and quality checks complete."

.PHONY: vulncheck
vulncheck:
	@exec govulncheck ./...

.PHONY: doctrines-validate
doctrines-validate:
	@echo "Validating doctrine JSON syntax..."
	@go run ./internal/tools/jsoncheck protocol/constants/doctrine/*.json
	@echo "All doctrine files are valid JSON."


.PHONY: swagger-generate
swagger-generate:
	@echo "Generating Swagger/OpenAPI documentation..."
	@command -v swag &> /dev/null || go install github.com/swaggo/swag/cmd/swag@$(SWAG_VERSION)
	@swag init --dir cmd/ztd,internal/services/gateway,internal/models,internal/constants --output internal/services/gateway/docs --outputTypes json,yaml --parseInternal --parseDependencyLevel 1 --packagePrefix github.com/zerotrustdata/ztd,github.com/go-webauthn,encoding/json --quiet
	@echo "Swagger documentation generated successfully."


# =============================================================================
# CLEANUP
# =============================================================================
.PHONY: clean
# Build cleanup must preserve the Gateway CA, databases, and workload trust. The
# shared Go module cache is left alone; builds use vendor/.
# Use ./ztd gw clean explicitly to archive and reset the Gateway runtime.
clean: harness-clean
	@echo "Cleaning up build artifacts..."
	@rm -rf .ztd-test-tmp/ bin/
	@rm -f *.sha256 *.test coverage.out coverage_filtered.out
	@GOTOOLCHAIN=local go clean -cache
	@echo "Clean complete."

.PHONY: harness-clean
harness-clean:
	@echo "Cleaning up stale harness directories..."
	@rm -rf .ztd-harness-*/

# =============================================================================
# HOST PLATFORM LIFECYCLE
# =============================================================================
# These targets run the platform directly on the development host by calling
# ./ztd, which loads only registry secret and user_endpoint keys from .env
# (.env.example); pass every other ./ztd flag through the *_ARGS variables
# (confirm flags with `./ztd gw start --help` and `./ztd operator start --help`).
# up-public and gw-public run up and gw with the Gateway listeners bound to
# every interface so Operators on other hosts can enroll; up and gw stay
# loopback-only.
GW_LISTEN_ARGS :=
# One-command first run: build if needed, start or reuse the Gateway, sign in
# headless, and dispatch a first governed action. Safe to rerun. QUICKSTART_ARGS
# carries quickstart options: gw start flags go after `--`, for example
#   make quickstart QUICKSTART_ARGS='-- --log info'
.PHONY: quickstart
quickstart:
	@bash scripts/quickstart.sh $(QUICKSTART_ARGS)

.PHONY: up
up: build
	@echo "Starting the ztd Gateway on this host..."
	@./ztd gw start $(ZTD_ARGS) $(GW_LISTEN_ARGS) $(GW_ARGS)

# Gateway whose embedded Operator carries every Operator capability.
# Capabilities are immutable in a running Gateway, so run `make down` first when
# one is already running. Provenance attests model weights on this host and is
# added only when MODEL_STORAGE_ROOT names the local model store. GW_ARGS
# carries extra flags, for example
#   make gw MODEL_STORAGE_ROOT=~/.ollama/models GW_ARGS='--inference-ollama-endpoint http://host:11434'
GW_CAPABILITIES ?= data,inference,observer
MODEL_STORAGE_ROOT ?=
.PHONY: gw
gw: build
	@./ztd gw start $(ZTD_ARGS) --capabilities $(GW_CAPABILITIES)$(if $(MODEL_STORAGE_ROOT),$(comma)provenance --model-storage-root $(MODEL_STORAGE_ROOT)) $(GW_LISTEN_ARGS) $(GW_ARGS)
	@./ztd gw status $(ZTD_ARGS)

.PHONY: up-public gw-public
up-public gw-public: GW_LISTEN_ARGS := --listen-host 0.0.0.0
up-public: up
gw-public: gw

# Outbound Operator in the foreground (Ctrl+C stops it). OPERATOR_ARGS carries
# the capability and endpoint flags, for example
#   make operator OPERATOR_ARGS='--endpoint localhost --capabilities inference --working-dir ~/.ztd-inference'
.PHONY: operator
operator: build
	@./ztd operator start $(ZTD_ARGS) $(OPERATOR_ARGS)

.PHONY: status
status:
	@./ztd gw status $(ZTD_ARGS)

# Operators first (governed shutdown needs the Gateway), then the Gateway even if
# no Operator was running. Runtime state in .ztd/ is preserved.
.PHONY: down stop
down:
	-@./ztd operator stop $(ZTD_ARGS)
	@./ztd gw stop $(ZTD_ARGS)

stop: down

# =============================================================================
# CI/CD (LOCAL)
# =============================================================================
# Recipes deliberately sequence stages, including under make -j. Generation
# refreshes local artifacts; GitHub Actions separately enforces committed freshness.
.PHONY: ci
ci: dev-check
	@$(MAKE) ci-console
	@$(MAKE) ci-explorer
	@$(MAKE) ci-platform
	@$(MAKE) ci-protocol
	@$(MAKE) ci-website
	@echo "CI complete. Generated artifacts have been refreshed."

.PHONY: ci-platform
ci-platform: dev-check
	@$(MAKE) _ci-verify-proto
	@$(MAKE) _ci-swagger
	@$(MAKE) constants-check
	@$(MAKE) explorer-catalog-check
	@$(MAKE) lint
	@$(MAKE) _ci-test
	@$(MAKE) _ci-binaries
	@$(MAKE) test-airgap
	@$(MAKE) test-go-import
	@$(MAKE) bsl-headers-check
	@$(MAKE) third-party-notices-check
	@$(MAKE) test-commit-hygiene
	@$(MAKE) test-release-scripts
	@$(MAKE) test-onboarding
	@$(MAKE) version-sync-check
	@echo "Platform CI complete."

.PHONY: ci-console
ci-console: dev-check
	@$(MAKE) console-lint
	@$(MAKE) console-test
	@$(MAKE) console-embed
	@npm run lint --prefix ztd-adapter
	@npm test --prefix ztd-adapter
	@npm run gen:contract-pack:check --prefix ztd-adapter
	@echo "Console and adapter CI complete."

.PHONY: ci-explorer
ci-explorer: dev-check
	@$(MAKE) explorer-lint
	@$(MAKE) explorer-test
	@$(MAKE) explorer-embed-check
	@echo "Evaluation explorer CI complete."

.PHONY: ci-protocol
ci-protocol: dev-check
	@go test $(TEST_UNIT_COUNT) -timeout $(TEST_SHORT_TIMEOUT) -run TestHashParity ./internal/governance
	@npm run typecheck --prefix protocol/node
	@npm test --prefix protocol/node
	@echo "Protocol CI complete."

.PHONY: ci-website
ci-website: dev-check
	@$(MAKE) website-test
	@$(MAKE) website-build
	@$(MAKE) website-bundle-check
	@echo "Website CI complete."

.PHONY: bsl-headers-check
bsl-headers-check:
	@go run ./internal/tools/bslheaders

# VERSION is the single source of truth for protocol/docs Version: headers and
# first-party npm package versions. CI runs version-sync-check; make release
# runs version-sync.
.PHONY: version-sync version-sync-check
version-sync:
	@go run ./internal/tools/versionsync -write

version-sync-check:
	@go run ./internal/tools/versionsync -check

.PHONY: _ci-verify-proto
_ci-verify-proto:
	@echo "=== generate-proto ==="
	@$(MAKE) proto-generate
	@$(MAKE) doctrines-validate

.PHONY: _ci-swagger
_ci-swagger:
	@echo "=== swagger ==="
	@$(MAKE) swagger-generate
	@go test -count=1 ./internal/services/gateway/docs

.PHONY: _ci-test
_ci-test: export CI := 1
_ci-test: export ZTD_STRICT_CONSTANTS_LINT := 1
_ci-test:
	@echo "=== unit tests ==="
	@$(MAKE) test-unit
	@$(MAKE) test-enrollment-burst
	@echo "=== integration tests and coverage ==="
	@$(MAKE) test-coverage

# Release-shape checks GitHub Actions runs on the built binaries: the Windows
# cross-compile, then (on a Linux host, the FIPS tested OE) the FIPS self-check
# and fully static standard and FIPS binaries.
.PHONY: _ci-binaries
_ci-binaries:
	@$(MAKE) build-windows
ifeq ($(HOST_OS),linux)
	@$(MAKE) build-linux
	@$(MAKE) verify-static
	@$(MAKE) fips-verify
	@$(MAKE) verify-static STATIC_BINARY=$(BIN_DIR)/ztd-fips-$(FIPS_GOOS)-$(FIPS_GOARCH)
endif

# =============================================================================
# RELEASE
# =============================================================================
# VERSION is the single source of truth. `make release` syncs the maintained
# protocol specification Version: headers and first-party npm package versions
# from VERSION (if needed; see version-sync), tags the current commit as
# v<VERSION>, and pushes the tag. The GitHub Actions
# workflows create the GitHub release and upload binary assets.
#
# Workflow:
#   1. Merge PRs (CI enforces version sync, proto/swagger generation, tests)
#   2. git pull origin main
#   3. make release        (tags, pushes — workflows create the release)
#
# The Go module is part of the root module (github.com/zerotrustdata/ztd)
# and is versioned by the v* tag. External consumers use:
#   go get github.com/zerotrustdata/ztd@vX.Y.Z

.PHONY: release
release:
	@VERSION=$$(sed 's/^v//' VERSION | tr -d '\n\r'); \
	TAG="v$$VERSION"; \
	MAJOR_MINOR=$$(echo $$VERSION | cut -d. -f1-2); \
	NOTES_FILE="docs/release_notes/v$$MAJOR_MINOR.x/v$$VERSION.md"; \
	echo "=== release: $$TAG ==="; \
	\
	if [ "$$(git rev-parse --abbrev-ref HEAD)" != "main" ]; then \
		echo "Error: make release runs on main only."; exit 1; \
	fi; \
	git fetch -q origin main --tags || exit 1; \
	if [ "$$(git rev-parse HEAD)" != "$$(git rev-parse origin/main)" ]; then \
		echo "Error: HEAD is not origin/main. Push or pull so the release commit is on origin/main, then retry."; exit 1; \
	fi; \
	REHEARSALS=$$(gh run list --workflow release-binary.yml --event workflow_dispatch --commit "$$(git rev-parse HEAD)" \
		--status success --json databaseId --jq length) || exit 1; \
	if [ "$$REHEARSALS" = "0" ]; then \
		echo "Error: no successful release rehearsal for $$(git rev-parse --short HEAD). Run make release-rehearse first."; exit 1; \
	fi; \
	\
	go run ./internal/tools/versionsync -write || exit 1; \
	\
	if [ -n "$$(git status --porcelain)" ]; then \
		echo "Error: working tree is dirty after version sync."; \
		echo "Version declarations were out of sync with VERSION. Commit synced files, push, merge PR, then retry."; exit 1; \
	fi; \
	if [ ! -f "$$NOTES_FILE" ]; then \
		echo "Error: release notes file $$NOTES_FILE not found."; exit 1; \
	fi; \
	if git rev-parse -q --verify "refs/tags/$$TAG" >/dev/null; then \
		echo "Error: tag $$TAG already exists."; exit 1; \
	fi; \
	echo "Tagging $$TAG..."; \
	git tag "$$TAG" && git push origin "$$TAG" || exit 1; \
	\
	echo ""; \
	echo "Tag $$TAG pushed."; \
	echo "The GitHub Actions workflows will create the release and upload assets."; \
	echo "Monitor: gh run list --workflow release-binary.yml --branch $$TAG, then gh run watch <id> --exit-status"

# Rehearse the release workflow on origin/main without publishing: preflight,
# build-all, checksums, cosign signing, staging, and re-verification of the
# downloaded artifact set. make release requires a successful rehearsal of HEAD.
.PHONY: release-rehearse
release-rehearse:
	@if [ "$$(git rev-parse --abbrev-ref HEAD)" != "main" ] || [ -n "$$(git status --porcelain)" ]; then \
		echo "Error: make release-rehearse runs on a clean main checkout."; exit 1; \
	fi; \
	git fetch -q origin main || exit 1; \
	if [ "$$(git rev-parse HEAD)" != "$$(git rev-parse origin/main)" ]; then \
		echo "Error: HEAD is not origin/main. Push or pull, then retry."; exit 1; \
	fi; \
	URL=$$(gh workflow run release-binary.yml --ref main -f publish=false 2>&1 | grep -o 'https://[^ ]*/actions/runs/[0-9]*') \
		|| { echo "Error: could not dispatch release-binary.yml"; exit 1; }; \
	echo "Rehearsal: $$URL"; \
	gh run watch "$${URL##*/}" --exit-status
