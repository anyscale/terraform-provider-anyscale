# Terraform Provider Anyscale - Makefile
# ============================================================================

BINARY_NAME := terraform-provider-anyscale
BUILD_DIR := ./build
INSTALL_DIR := $(HOME)/.terraform.d/plugins/registry.terraform.io/terraform-providers/anyscale/0.0.1/darwin_arm64
GO := go
GOFLAGS := -v
GOLANGCI_LINT := golangci-lint
TFPLUGINDOCS := tfplugindocs

# Get version info
VERSION ?= 0.0.1
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)"

# Default target
.DEFAULT_GOAL := help

# ============================================================================
# HELP
# ============================================================================

.PHONY: help
help: ## Show this help message
	@echo "Terraform Provider Anyscale"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@awk 'BEGIN {FS = ":.*##"; printf ""} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

# ============================================================================
# BUILD
# ============================================================================

.PHONY: build
build: ## Build the provider binary
	@echo "==> Building $(BINARY_NAME)..."
	$(GO) build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_NAME)
	@echo "==> Build complete: $(BINARY_NAME)"

.PHONY: build-dir
build-dir: ## Build the provider binary to build directory
	@echo "==> Building $(BINARY_NAME) to $(BUILD_DIR)..."
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)
	@echo "==> Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

.PHONY: install
install: build ## Install the provider locally for dev_overrides
	@echo "==> Provider built. Using dev_overrides in ~/.terraformrc"
	@echo "==> Provider location: $(shell pwd)/$(BINARY_NAME)"

# ============================================================================
# TEST
# ============================================================================

.PHONY: test
test: ## Run unit tests
	@echo "==> Running unit tests..."
	$(GO) test ./... -v -timeout 120s

.PHONY: testacc
testacc: ## Run acceptance tests (requires TF_ACC=1)
	@echo "==> Running acceptance tests..."
	TF_ACC=1 $(GO) test ./... -v -timeout 120m

.PHONY: testacc-cover
testacc-cover: ## Run acceptance tests with coverage
	@echo "==> Running acceptance tests with coverage..."
	TF_ACC=1 $(GO) test ./... -v -timeout 120m -coverprofile=coverage.out -covermode=atomic
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "==> Coverage report: coverage.html"

.PHONY: test-compile
test-compile: ## Verify tests compile without running
	@echo "==> Verifying test compilation..."
	$(GO) test -c ./... -o /dev/null

# ============================================================================
# LINT & FORMAT
# ============================================================================

.PHONY: lint
lint: ## Run golangci-lint
	@echo "==> Running linter..."
	@if command -v $(GOLANGCI_LINT) >/dev/null 2>&1; then \
		$(GOLANGCI_LINT) run ./...; \
	else \
		echo "golangci-lint not installed. Install with: brew install golangci-lint"; \
		exit 1; \
	fi

.PHONY: lint-fix
lint-fix: ## Run golangci-lint with auto-fix
	@echo "==> Running linter with auto-fix..."
	@if command -v $(GOLANGCI_LINT) >/dev/null 2>&1; then \
		$(GOLANGCI_LINT) run --fix ./...; \
	else \
		echo "golangci-lint not installed. Install with: brew install golangci-lint"; \
		exit 1; \
	fi

.PHONY: fmt
fmt: ## Format Go code
	@echo "==> Formatting code..."
	$(GO) fmt ./...
	@echo "==> Format complete"

.PHONY: vet
vet: ## Run go vet
	@echo "==> Running go vet..."
	$(GO) vet ./...

# ============================================================================
# DEPENDENCIES
# ============================================================================

.PHONY: tidy
tidy: ## Run go mod tidy
	@echo "==> Tidying dependencies..."
	$(GO) mod tidy
	@echo "==> Tidy complete"

.PHONY: deps
deps: ## Download dependencies
	@echo "==> Downloading dependencies..."
	$(GO) mod download
	@echo "==> Dependencies downloaded"

.PHONY: deps-update
deps-update: ## Update all dependencies
	@echo "==> Updating dependencies..."
	$(GO) get -u ./...
	$(GO) mod tidy
	@echo "==> Dependencies updated"

# ============================================================================
# DOCUMENTATION
# ============================================================================

.PHONY: docs
docs: ## Generate provider documentation
	@echo "==> Generating documentation..."
	@if command -v $(TFPLUGINDOCS) >/dev/null 2>&1; then \
		$(TFPLUGINDOCS) generate; \
	else \
		echo "tfplugindocs not installed. Install with: go install github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@latest"; \
		exit 1; \
	fi

.PHONY: docs-validate
docs-validate: ## Validate provider documentation
	@echo "==> Validating documentation..."
	@if command -v $(TFPLUGINDOCS) >/dev/null 2>&1; then \
		$(TFPLUGINDOCS) validate; \
	else \
		echo "tfplugindocs not installed. Install with: go install github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@latest"; \
		exit 1; \
	fi

# ============================================================================
# CLEAN
# ============================================================================

.PHONY: clean
clean: ## Clean build artifacts
	@echo "==> Cleaning build artifacts..."
	rm -f $(BINARY_NAME)
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html
	@echo "==> Clean complete"

# ============================================================================
# DEVELOPMENT HELPERS
# ============================================================================

.PHONY: dev
dev: clean build ## Clean and build for development
	@echo "==> Development build complete"

.PHONY: check
check: fmt vet lint test-compile ## Run all checks (fmt, vet, lint, test-compile)
	@echo "==> All checks passed"

.PHONY: ci
ci: deps check test ## Run CI pipeline (deps, check, test)
	@echo "==> CI pipeline complete"

# ============================================================================
# TERRAFORM TESTING
# ============================================================================

.PHONY: tf-plan
tf-plan: build ## Build and run terraform plan in examples/basic-anyscale-cloud
	@echo "==> Running terraform plan..."
	cd examples/basic-anyscale-cloud && terraform plan

.PHONY: tf-apply
tf-apply: build ## Build and run terraform apply in examples/basic-anyscale-cloud
	@echo "==> Running terraform apply..."
	cd examples/basic-anyscale-cloud && terraform apply

.PHONY: tf-destroy
tf-destroy: ## Run terraform destroy in examples/basic-anyscale-cloud
	@echo "==> Running terraform destroy..."
	cd examples/basic-anyscale-cloud && terraform destroy

.PHONY: tf-plan-debug
tf-plan-debug: build ## Build and run terraform plan with DEBUG logging
	@echo "==> Running terraform plan with DEBUG logging..."
	cd examples/basic-anyscale-cloud && TF_LOG=DEBUG terraform plan

# ============================================================================
# RELEASE (placeholder for future CI/CD)
# ============================================================================

.PHONY: release-snapshot
release-snapshot: ## Create a snapshot release (requires goreleaser)
	@echo "==> Creating snapshot release..."
	@if command -v goreleaser >/dev/null 2>&1; then \
		goreleaser release --snapshot --clean; \
	else \
		echo "goreleaser not installed. Install with: brew install goreleaser"; \
		exit 1; \
	fi
