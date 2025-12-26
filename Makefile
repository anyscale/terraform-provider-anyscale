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
# TERRAFORM TESTING - AWS VM SCENARIOS
# ============================================================================

.PHONY: test-aws-vm-basic
test-aws-vm-basic: build ## Test AWS VM basic scenario (no EFS, no MemoryDB)
	@echo "==> Testing AWS VM basic scenario..."
	cd examples/aws-vm-basic && terraform apply -auto-approve
	cd examples/aws-vm-basic && terraform destroy -auto-approve

.PHONY: test-aws-vm-efs
test-aws-vm-efs: build ## Test AWS VM with EFS scenario
	@echo "==> Testing AWS VM with EFS scenario..."
	cd examples/aws-vm-with-efs && terraform apply -auto-approve
	cd examples/aws-vm-with-efs && terraform destroy -auto-approve

.PHONY: test-aws-vm-memorydb
test-aws-vm-memorydb: build ## Test AWS VM with MemoryDB scenario
	@echo "==> Testing AWS VM with MemoryDB scenario..."
	cd examples/aws-vm-with-memorydb && terraform apply -auto-approve
	cd examples/aws-vm-with-memorydb && terraform destroy -auto-approve

.PHONY: test-aws-vm-full
test-aws-vm-full: build ## Test AWS VM full scenario (EFS + MemoryDB)
	@echo "==> Testing AWS VM full scenario..."
	cd examples/aws-vm-full && terraform apply -auto-approve
	cd examples/aws-vm-full && terraform destroy -auto-approve

.PHONY: test-aws-vm-basic-resource
test-aws-vm-basic-resource: build ## Test AWS VM basic with separate cloud_resource
	@echo "==> Testing AWS VM basic resource scenario..."
	cd examples/aws-vm-basic-resource && terraform apply -auto-approve
	cd examples/aws-vm-basic-resource && terraform destroy -auto-approve

# ============================================================================
# TERRAFORM TESTING - GCP VM SCENARIOS
# ============================================================================

.PHONY: test-gcp-vm-basic
test-gcp-vm-basic: build ## Test GCP VM basic scenario (no Filestore, no Memorystore)
	@echo "==> Testing GCP VM basic scenario..."
	cd examples/gcp-vm-basic && terraform apply -auto-approve
	cd examples/gcp-vm-basic && terraform destroy -auto-approve

.PHONY: test-gcp-vm-filestore
test-gcp-vm-filestore: build ## Test GCP VM with Filestore scenario
	@echo "==> Testing GCP VM with Filestore scenario..."
	cd examples/gcp-vm-with-filestore && terraform apply -auto-approve
	cd examples/gcp-vm-with-filestore && terraform destroy -auto-approve

.PHONY: test-gcp-vm-memorystore
test-gcp-vm-memorystore: build ## Test GCP VM with Memorystore scenario
	@echo "==> Testing GCP VM with Memorystore scenario..."
	cd examples/gcp-vm-with-memorystore && terraform apply -auto-approve
	cd examples/gcp-vm-with-memorystore && terraform destroy -auto-approve

.PHONY: test-gcp-vm-full
test-gcp-vm-full: build ## Test GCP VM full scenario (Filestore + Memorystore)
	@echo "==> Testing GCP VM full scenario..."
	cd examples/gcp-vm-full && terraform apply -auto-approve
	cd examples/gcp-vm-full && terraform destroy -auto-approve

# ============================================================================
# TERRAFORM TESTING - MULTI-RESOURCE SCENARIOS
# ============================================================================

.PHONY: test-multi-resource-basic
test-multi-resource-basic: build ## Test multi-resource cloud basic scenario
	@echo "==> Testing multi-resource cloud basic scenario..."
	cd examples/multi-resource-cloud-basic && terraform apply -auto-approve
	cd examples/multi-resource-cloud-basic && terraform destroy -auto-approve

# ============================================================================
# TERRAFORM TESTING - BATCH SCENARIOS
# ============================================================================

.PHONY: test-all-aws-vm
test-all-aws-vm: build ## Run all AWS VM test scenarios sequentially
	@echo "==> Running all AWS VM test scenarios..."
	$(MAKE) test-aws-vm-basic
	$(MAKE) test-aws-vm-efs
	$(MAKE) test-aws-vm-memorydb
	$(MAKE) test-aws-vm-full
	@echo "==> All AWS VM scenarios completed"

.PHONY: test-all-gcp-vm
test-all-gcp-vm: build ## Run all GCP VM test scenarios sequentially
	@echo "==> Running all GCP VM test scenarios..."
	$(MAKE) test-gcp-vm-basic
	$(MAKE) test-gcp-vm-filestore
	$(MAKE) test-gcp-vm-memorystore
	$(MAKE) test-gcp-vm-full
	@echo "==> All GCP VM scenarios completed"

.PHONY: test-all-vm
test-all-vm: build ## Run all VM test scenarios (AWS + GCP)
	@echo "==> Running all VM test scenarios..."
	$(MAKE) test-all-aws-vm
	$(MAKE) test-all-gcp-vm
	@echo "==> All VM scenarios completed"

# Parallel testing (each scenario has unique IAM roles)
.PHONY: test-all-aws-vm-parallel
test-all-aws-vm-parallel: build ## Run all AWS VM scenarios in parallel
	@echo "==> Running all AWS VM scenarios in parallel..."
	$(MAKE) -j4 test-aws-vm-basic test-aws-vm-efs test-aws-vm-memorydb test-aws-vm-full

.PHONY: test-all-gcp-vm-parallel
test-all-gcp-vm-parallel: build ## Run all GCP VM scenarios in parallel
	@echo "==> Running all GCP VM scenarios in parallel..."
	$(MAKE) -j4 test-gcp-vm-basic test-gcp-vm-filestore test-gcp-vm-memorystore test-gcp-vm-full

# ============================================================================
# TERRAFORM TESTING - APPLY/DESTROY ONLY (for manual testing)
# ============================================================================

# AWS VM Apply-only targets
.PHONY: apply-aws-vm-basic
apply-aws-vm-basic: build ## Apply AWS VM basic scenario only
	cd examples/aws-vm-basic && terraform apply -auto-approve

.PHONY: apply-aws-vm-efs
apply-aws-vm-efs: build ## Apply AWS VM with EFS scenario only
	cd examples/aws-vm-with-efs && terraform apply -auto-approve

.PHONY: apply-aws-vm-memorydb
apply-aws-vm-memorydb: build ## Apply AWS VM with MemoryDB scenario only
	cd examples/aws-vm-with-memorydb && terraform apply -auto-approve

.PHONY: apply-aws-vm-full
apply-aws-vm-full: build ## Apply AWS VM full scenario only
	cd examples/aws-vm-full && terraform apply -auto-approve

.PHONY: apply-aws-vm-basic-resource
apply-aws-vm-basic-resource: build ## Apply AWS VM basic resource scenario only
	cd examples/aws-vm-basic-resource && terraform apply -auto-approve

# GCP VM Apply-only targets
.PHONY: apply-gcp-vm-basic
apply-gcp-vm-basic: build ## Apply GCP VM basic scenario only
	cd examples/gcp-vm-basic && terraform apply -auto-approve

.PHONY: apply-gcp-vm-filestore
apply-gcp-vm-filestore: build ## Apply GCP VM with Filestore scenario only
	cd examples/gcp-vm-with-filestore && terraform apply -auto-approve

.PHONY: apply-gcp-vm-memorystore
apply-gcp-vm-memorystore: build ## Apply GCP VM with Memorystore scenario only
	cd examples/gcp-vm-with-memorystore && terraform apply -auto-approve

.PHONY: apply-gcp-vm-full
apply-gcp-vm-full: build ## Apply GCP VM full scenario only
	cd examples/gcp-vm-full && terraform apply -auto-approve

# Multi-resource Apply-only targets
.PHONY: apply-multi-resource-basic
apply-multi-resource-basic: build ## Apply multi-resource cloud basic scenario only
	cd examples/multi-resource-cloud-basic && terraform apply -auto-approve

# AWS VM Destroy-only targets
.PHONY: destroy-aws-vm-basic
destroy-aws-vm-basic: ## Destroy AWS VM basic scenario
	cd examples/aws-vm-basic && terraform destroy -auto-approve

.PHONY: destroy-aws-vm-efs
destroy-aws-vm-efs: ## Destroy AWS VM with EFS scenario
	cd examples/aws-vm-with-efs && terraform destroy -auto-approve

.PHONY: destroy-aws-vm-memorydb
destroy-aws-vm-memorydb: ## Destroy AWS VM with MemoryDB scenario
	cd examples/aws-vm-with-memorydb && terraform destroy -auto-approve

.PHONY: destroy-aws-vm-full
destroy-aws-vm-full: ## Destroy AWS VM full scenario
	cd examples/aws-vm-full && terraform destroy -auto-approve

.PHONY: destroy-aws-vm-basic-resource
destroy-aws-vm-basic-resource: ## Destroy AWS VM basic resource scenario
	cd examples/aws-vm-basic-resource && terraform destroy -auto-approve

# GCP VM Destroy-only targets
.PHONY: destroy-gcp-vm-basic
destroy-gcp-vm-basic: ## Destroy GCP VM basic scenario
	cd examples/gcp-vm-basic && terraform destroy -auto-approve

.PHONY: destroy-gcp-vm-filestore
destroy-gcp-vm-filestore: ## Destroy GCP VM with Filestore scenario
	cd examples/gcp-vm-with-filestore && terraform destroy -auto-approve

.PHONY: destroy-gcp-vm-memorystore
destroy-gcp-vm-memorystore: ## Destroy GCP VM with Memorystore scenario
	cd examples/gcp-vm-with-memorystore && terraform destroy -auto-approve

.PHONY: destroy-gcp-vm-full
destroy-gcp-vm-full: ## Destroy GCP VM full scenario
	cd examples/gcp-vm-full && terraform destroy -auto-approve

# Multi-resource Destroy-only targets
.PHONY: destroy-multi-resource-basic
destroy-multi-resource-basic: ## Destroy multi-resource cloud basic scenario
	cd examples/multi-resource-cloud-basic && terraform destroy -auto-approve

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
