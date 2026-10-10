# Polaroid development commands. `make help` lists them.
# CI (.github/workflows/ci.yml) runs `make ci`, so local and CI checks match.

GO                    ?= go
GOLANGCI_LINT         ?= golangci-lint
BIN                   := bin
# Keep in sync with .github/workflows/ci.yml.
GOLANGCI_LINT_VERSION := 2.14.0
GOVULNCHECK_VERSION   := v1.8.0
# 0 skips e2e-mcp's independent-client checks (npm downloads, a local VS Code).
E2E_INTEROP           ?= 1

.DEFAULT_GOAL := help
.PHONY: help fmt fmt-check vet lint build test race deps-check check vuln demo run ci e2e e2e-mcp lifecycle clean

help: ## List targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z0-9-]+:.*## / {printf "  %-11s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

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

build: ## Build polaroidd and polaroid into bin/
	$(GO) build -trimpath -o $(BIN)/ ./cmd/polaroidd ./cmd/polaroid

test: ## Run all tests
	$(GO) test ./...

race: ## Run all tests with the race detector
	$(GO) test -race ./...

deps-check: ## Verify the dependency inventory and licenses
	./scripts/check-deps.sh

check: fmt-check vet lint build test race deps-check ## Run every offline check

vuln: ## Scan for known vulnerabilities (needs network)
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

demo: build ## Demonstrate storage and versioning against a real daemon
	./scripts/demo.sh

run: build ## Run the daemon; pass flags with ARGS="-addr ... -db ..."
	./$(BIN)/polaroidd $(ARGS)

ci: E2E_INTEROP := 0
ci: check vuln demo e2e e2e-mcp ## Everything CI runs (end-to-end interop checks off)

e2e: build ## End-to-end report of every feature against a real daemon (bin/e2e/REPORT.md)
	./scripts/e2e.sh

e2e-mcp: build ## End-to-end report of /mcp; E2E_INTEROP=0 skips the independent clients (bin/e2e/MCP-REPORT.md)
	E2E_INTEROP=$(E2E_INTEROP) ./scripts/e2e-mcp.sh

lifecycle: ## Install, run and uninstall an isolated managed service with the REAL launchd or systemd user manager (exit 77: none reachable)
	./scripts/lifecycle-check.sh

clean: ## Remove build output
	rm -rf $(BIN)
