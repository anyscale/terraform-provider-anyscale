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
# Auto-detect version from git tags (e.g., v0.1.0 -> 0.1.0)
# Falls back to VERSION env var, then to 0.0.1
GIT_TAG := $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//')
GIT_VERSION := $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
VERSION ?= $(if $(GIT_TAG),$(GIT_TAG),0.0.1)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
# Set version in both main.version and provider.Version for compatibility
LDFLAGS := -ldflags "-X main.version=$(VERSION) -X github.com/brent/terraform-provider-anyscale/internal/provider.Version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)"

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
	@echo "==> Version: $(VERSION) (from $(if $(GIT_TAG),git tag: $(GIT_TAG),default/override))"
	$(GO) build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_NAME)
	@echo "==> Build complete: $(BINARY_NAME)"

.PHONY: build-dir
build-dir: ## Build the provider binary to build directory
	@echo "==> Building $(BINARY_NAME) to $(BUILD_DIR)..."
	@echo "==> Version: $(VERSION) (from $(if $(GIT_TAG),git tag: $(GIT_TAG),default/override))"
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
# PRIMARY TERRAFORM TESTS - Efficient coverage of all provider variations
# ============================================================================
# These 6 tests efficiently cover all major variations:
#   - Cloud Providers: AWS, GCP
#   - Compute Stacks: VM, K8S
#   - Patterns: All-in-one (basic), Split (full)
#   - Features: EFS/MemoryDB (AWS), Filestore/Memorystore (GCP)
#
# Test Matrix:
#   aws-vm-basic  : AWS + VM + All-in-one pattern (simplest AWS)
#   aws-vm-full   : AWS + VM + Split pattern + EFS + MemoryDB
#   aws-eks-basic : AWS + K8S + kubernetes_config
#   gcp-vm-basic  : GCP + VM + All-in-one pattern (simplest GCP)
#   gcp-vm-full   : GCP + VM + Split pattern + Filestore + Memorystore
#   gcp-gke-basic : GCP + K8S + kubernetes_config
# ============================================================================

.PHONY: test-primary
test-primary: build ## Run primary test suite (6 tests covering all variations)
	@echo "==> Running primary test suite..."
	@echo "==> This covers: AWS/GCP, VM/K8S, All-in-one/Split patterns, all features"
	$(MAKE) test-aws-vm-basic
	$(MAKE) test-aws-vm-full
	$(MAKE) test-aws-eks-basic
	$(MAKE) test-gcp-vm-basic
	$(MAKE) test-gcp-vm-full
	$(MAKE) test-gcp-gke-basic
	@echo "==> Primary test suite completed"

.PHONY: test-primary-vm
test-primary-vm: build ## Run primary VM tests only (4 tests)
	@echo "==> Running primary VM tests..."
	$(MAKE) test-aws-vm-basic
	$(MAKE) test-aws-vm-full
	$(MAKE) test-gcp-vm-basic
	$(MAKE) test-gcp-vm-full
	@echo "==> Primary VM tests completed"

.PHONY: test-primary-k8s
test-primary-k8s: build ## Run primary K8S tests only (2 tests)
	@echo "==> Running primary K8S tests..."
	$(MAKE) test-aws-eks-basic
	$(MAKE) test-gcp-gke-basic
	@echo "==> Primary K8S tests completed"

.PHONY: test-primary-aws
test-primary-aws: build ## Run primary AWS tests only (3 tests)
	@echo "==> Running primary AWS tests..."
	$(MAKE) test-aws-vm-basic
	$(MAKE) test-aws-vm-full
	$(MAKE) test-aws-eks-basic
	@echo "==> Primary AWS tests completed"

.PHONY: test-primary-gcp
test-primary-gcp: build ## Run primary GCP tests only (3 tests)
	@echo "==> Running primary GCP tests..."
	$(MAKE) test-gcp-vm-basic
	$(MAKE) test-gcp-vm-full
	$(MAKE) test-gcp-gke-basic
	@echo "==> Primary GCP tests completed"

# ============================================================================
# TERRAFORM TESTING - AWS VM
# ============================================================================

.PHONY: test-aws-vm-basic
test-aws-vm-basic: build ## Test AWS VM basic (all-in-one pattern)
	@echo "==> Testing AWS VM basic scenario..."
	cd examples/aws-vm-basic && terraform apply -auto-approve
	cd examples/aws-vm-basic && terraform destroy -auto-approve

.PHONY: test-aws-vm-full
test-aws-vm-full: build ## Test AWS VM full (split pattern + EFS + MemoryDB)
	@echo "==> Testing AWS VM full scenario..."
	cd examples/aws-vm && terraform apply -auto-approve -var="enable_efs=true" -var="enable_memorydb=true" -var="cloud_name=tfprovider-aws-vm-full" -var="common_prefix=aws-vm-full-" -var="vpc_cidr_block=172.27.0.0/16" -var='vpc_public_subnets=["172.27.21.0/24", "172.27.22.0/24", "172.27.23.0/24"]'
	cd examples/aws-vm && terraform destroy -auto-approve -var="enable_efs=true" -var="enable_memorydb=true" -var="cloud_name=tfprovider-aws-vm-full" -var="common_prefix=aws-vm-full-" -var="vpc_cidr_block=172.27.0.0/16" -var='vpc_public_subnets=["172.27.21.0/24", "172.27.22.0/24", "172.27.23.0/24"]'

.PHONY: apply-aws-vm-basic
apply-aws-vm-basic: build ## Apply AWS VM basic only
	cd examples/aws-vm-basic && terraform apply -auto-approve

.PHONY: apply-aws-vm-full
apply-aws-vm-full: build ## Apply AWS VM full only
	cd examples/aws-vm && terraform apply -auto-approve -var="enable_efs=true" -var="enable_memorydb=true" -var="cloud_name=tfprovider-aws-vm-full" -var="common_prefix=aws-vm-full-" -var="vpc_cidr_block=172.27.0.0/16" -var='vpc_public_subnets=["172.27.21.0/24", "172.27.22.0/24", "172.27.23.0/24"]'

.PHONY: destroy-aws-vm-basic
destroy-aws-vm-basic: ## Destroy AWS VM basic
	cd examples/aws-vm-basic && terraform destroy -auto-approve

.PHONY: destroy-aws-vm-full
destroy-aws-vm-full: ## Destroy AWS VM full
	cd examples/aws-vm && terraform destroy -auto-approve -var="enable_efs=true" -var="enable_memorydb=true" -var="cloud_name=tfprovider-aws-vm-full" -var="common_prefix=aws-vm-full-" -var="vpc_cidr_block=172.27.0.0/16" -var='vpc_public_subnets=["172.27.21.0/24", "172.27.22.0/24", "172.27.23.0/24"]'

# ============================================================================
# TERRAFORM TESTING - AWS EKS
# ============================================================================

.PHONY: test-aws-eks-basic
test-aws-eks-basic: build ## Test AWS EKS basic (K8S)
	@echo "==> Testing AWS EKS basic scenario..."
	cd examples/aws-eks-basic && terraform apply -auto-approve
	cd examples/aws-eks-basic && terraform destroy -auto-approve

.PHONY: apply-aws-eks-basic
apply-aws-eks-basic: build ## Apply AWS EKS basic only
	cd examples/aws-eks-basic && terraform apply -auto-approve

.PHONY: destroy-aws-eks-basic
destroy-aws-eks-basic: ## Destroy AWS EKS basic
	cd examples/aws-eks-basic && terraform destroy -auto-approve

# ============================================================================
# TERRAFORM TESTING - GCP VM
# ============================================================================

.PHONY: test-gcp-vm-basic
test-gcp-vm-basic: build ## Test GCP VM basic (all-in-one pattern)
	@echo "==> Testing GCP VM basic scenario..."
	cd examples/gcp-vm-basic && terraform apply -auto-approve
	cd examples/gcp-vm-basic && terraform destroy -auto-approve

.PHONY: test-gcp-vm-full
test-gcp-vm-full: build ## Test GCP VM full (split pattern + Filestore + Memorystore)
	@echo "==> Testing GCP VM full scenario..."
	cd examples/gcp-vm && terraform apply -auto-approve -var="enable_filestore=true" -var="enable_memorystore=true" -var="cloud_name=tfprovider-gcp-vm-full" -var="common_prefix=gcp-vm-full-" -var="vpc_public_subnet_cidr=10.103.0.0/16"
	cd examples/gcp-vm && terraform destroy -auto-approve -var="enable_filestore=true" -var="enable_memorystore=true" -var="cloud_name=tfprovider-gcp-vm-full" -var="common_prefix=gcp-vm-full-" -var="vpc_public_subnet_cidr=10.103.0.0/16"

.PHONY: apply-gcp-vm-basic
apply-gcp-vm-basic: build ## Apply GCP VM basic only
	cd examples/gcp-vm-basic && terraform apply -auto-approve

.PHONY: apply-gcp-vm-full
apply-gcp-vm-full: build ## Apply GCP VM full only
	cd examples/gcp-vm && terraform apply -auto-approve -var="enable_filestore=true" -var="enable_memorystore=true" -var="cloud_name=tfprovider-gcp-vm-full" -var="common_prefix=gcp-vm-full-" -var="vpc_public_subnet_cidr=10.103.0.0/16"

.PHONY: destroy-gcp-vm-basic
destroy-gcp-vm-basic: ## Destroy GCP VM basic
	cd examples/gcp-vm-basic && terraform destroy -auto-approve

.PHONY: destroy-gcp-vm-full
destroy-gcp-vm-full: ## Destroy GCP VM full
	cd examples/gcp-vm && terraform destroy -auto-approve -var="enable_filestore=true" -var="enable_memorystore=true" -var="cloud_name=tfprovider-gcp-vm-full" -var="common_prefix=gcp-vm-full-" -var="vpc_public_subnet_cidr=10.103.0.0/16"

# ============================================================================
# TERRAFORM TESTING - GCP GKE
# ============================================================================

.PHONY: test-gcp-gke-basic
test-gcp-gke-basic: build ## Test GCP GKE basic (K8S)
	@echo "==> Testing GCP GKE basic scenario..."
	cd examples/gcp-gke-basic && terraform apply -auto-approve
	cd examples/gcp-gke-basic && terraform destroy -auto-approve

.PHONY: apply-gcp-gke-basic
apply-gcp-gke-basic: build ## Apply GCP GKE basic only
	cd examples/gcp-gke-basic && terraform apply -auto-approve

.PHONY: destroy-gcp-gke-basic
destroy-gcp-gke-basic: ## Destroy GCP GKE basic
	cd examples/gcp-gke-basic && terraform destroy -auto-approve

# ============================================================================
# VERSION HELPERS
# ============================================================================

.PHONY: version
version: ## Show current version information
	@echo "Current Version Information:"
	@echo "  VERSION: $(VERSION)"
	@echo "  Git Tag: $(if $(GIT_TAG),$(GIT_TAG),none found)"
	@echo "  Git Version: $(if $(GIT_VERSION),$(GIT_VERSION),unknown)"
	@echo "  Commit: $(COMMIT)"
	@echo "  Build Date: $(BUILD_DATE)"

.PHONY: version-check
version-check: ## Check if version is set correctly (useful for CI)
	@if [ "$(VERSION)" = "0.0.1" ] && [ -z "$(GIT_TAG)" ]; then \
		echo "WARNING: No git tag found and VERSION is default (0.0.1)"; \
		echo "Consider creating a git tag: git tag -a v0.1.0 -m 'Release 0.1.0'"; \
		exit 1; \
	fi
	@echo "Version check passed: $(VERSION)"

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
