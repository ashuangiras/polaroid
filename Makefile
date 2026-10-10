# Polaroid development commands. `make help` lists them.
# CI (.github/workflows/ci.yml, manual dispatch only) runs `make ci`, so local
# and CI checks match. Which commands a change needs is decided by the
# development procedures and AGENTS.md, not here.

GO                    ?= go
GOLANGCI_LINT         ?= golangci-lint
BIN                   := bin
# Keep in sync with .github/workflows/ci.yml.
GOLANGCI_LINT_VERSION := 2.14.0
GOVULNCHECK_VERSION   := v1.8.0
# 0 skips e2e-mcp's independent-client checks (npm downloads, a local VS Code).
E2E_INTEROP           ?= 1
# Packages for test, race and focused.
PKGS                  ?= ./...
export GO BIN

.DEFAULT_GOAL := help
.PHONY: help fmt fmt-check vet lint build binaries test race deps-check docs-check records-check loader-check check focused vuln demo run ci e2e e2e-mcp lifecycle clean

help: ## List targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z0-9-]+:.*## / {printf "  %-13s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

fmt: ## Format Go sources in place
	gofmt -w .

fmt-check: ## Fail if any Go source is not gofmt-formatted
	@files="$$(gofmt -l .)"; if [ -n "$$files" ]; then echo "gofmt needed:"; echo "$$files"; exit 1; fi

vet: ## Run go vet
	$(GO) vet ./...

lint: ## Run golangci-lint (pinned version; override the binary with GOLANGCI_LINT=path)
	@command -v $(GOLANGCI_LINT) >/dev/null 2>&1 || { echo "golangci-lint $(GOLANGCI_LINT_VERSION) is required: https://golangci-lint.run/docs/welcome/install/"; exit 1; }
	@v="$$($(GOLANGCI_LINT) version --short)"; [ "$$v" = "$(GOLANGCI_LINT_VERSION)" ] || { echo "golangci-lint $(GOLANGCI_LINT_VERSION) is required, found $$v"; exit 1; }
	$(GOLANGCI_LINT) run ./...

build: ## Build polaroidd and polaroid into bin/, and record the source state they were built from
	@./scripts/build.sh

binaries: ## Build into bin/ only if it was not built from the current source state
	@./scripts/build.sh -if-stale

test: ## Run the tests of PKGS (default ./...); the test cache applies except to packages that must run fresh
	@./scripts/go-test.sh -- $(PKGS)

race: ## Run the tests of PKGS with the race detector, cached the same way
	@./scripts/go-test.sh -race -- $(PKGS)

deps-check: ## Verify the dependency inventory and licenses
	./scripts/check-deps.sh

docs-check: ## Check the relative links and anchors of every tracked Markdown file
	@./scripts/check-doc-links.sh

records-check: binaries ## Load the fixture records into an isolated catalog twice and resolve their bindings (RECORDS_FROM=BACKUP starts from a copy)
	@./scripts/check-records.sh

loader-check: binaries ## Check how scripts/load-fixtures.sh loads binding revisions, in an isolated catalog
	@./scripts/check-loader.sh

check: fmt-check vet lint binaries test race deps-check docs-check records-check loader-check ## Run every offline check

focused: ## Offline checks with the tests of PKGS only, for a bounded change (PKGS is required)
	@[ "$(origin PKGS)" = "command line" ] || { echo "focused: set PKGS to the changed packages and every package that imports them"; exit 2; }
	@$(MAKE) --no-print-directory fmt-check vet lint binaries test race deps-check PKGS='$(PKGS)'

vuln: ## Scan for known vulnerabilities (needs network)
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

demo: binaries ## Demonstrate storage and versioning against a real daemon
	./scripts/demo.sh

run: binaries ## Run the daemon; pass flags with ARGS="-addr ... -db ..."
	./$(BIN)/polaroidd $(ARGS)

ci: E2E_INTEROP := 0
ci: check vuln demo e2e e2e-mcp ## Everything CI runs (end-to-end interop checks off)

e2e: binaries ## End-to-end report of every feature against a real daemon (bin/e2e/REPORT.md)
	./scripts/e2e.sh

e2e-mcp: binaries ## End-to-end report of /mcp; E2E_INTEROP=0 skips the independent clients (bin/e2e/MCP-REPORT.md)
	E2E_INTEROP=$(E2E_INTEROP) ./scripts/e2e-mcp.sh

lifecycle: ## Install, run and uninstall an isolated managed service with the REAL launchd or systemd user manager (exit 77: none reachable)
	./scripts/lifecycle-check.sh

clean: ## Remove build output
	rm -rf $(BIN)
