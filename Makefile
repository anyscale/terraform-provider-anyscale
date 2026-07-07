# Terraform Provider Anyscale - Makefile
# ============================================================================

BINARY_NAME := terraform-provider-anyscale
BUILD_DIR := ./build
INSTALL_DIR := $(HOME)/.terraform.d/plugins/registry.terraform.io/anyscale/anyscale/0.0.1/darwin_arm64
GO := go
GOFLAGS := -v
GOLANGCI_LINT := golangci-lint
TFPLUGINDOCS := tfplugindocs

# Per-run suffix used by example apply/destroy targets to isolate state and
# cloud names across parallel runs. Defaults to a timestamp.
# Override with: make apply-aws-vm-basic SUFFIX=mytest
SUFFIX ?= $(shell date +%s)

# Acceptance-test parallelism. Conservative default; override with PARALLEL=8.
PARALLEL ?= 4

# Get version info
# Auto-detect version from git tags (e.g., v0.1.0 -> 0.1.0)
# Falls back to VERSION env var, then to 0.0.1
GIT_TAG := $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//')
GIT_VERSION := $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
VERSION ?= $(if $(GIT_TAG),$(GIT_TAG),0.0.1)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
# Set version in both main.version and provider.Version for compatibility
LDFLAGS := -ldflags "-X github.com/anyscale/terraform-provider-anyscale/internal/provider.Version=$(VERSION)"

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
	TF_ACC=1 $(GO) test ./internal/acctest/ -v -timeout 120m -parallel $(PARALLEL)

.PHONY: testacc-cover
testacc-cover: ## Run acceptance tests with coverage
	@echo "==> Running acceptance tests with coverage..."
	TF_ACC=1 $(GO) test ./... -v -timeout 120m -parallel $(PARALLEL) -coverprofile=coverage.out -covermode=atomic
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "==> Coverage report: coverage.html"

.PHONY: sweep
sweep: ## Run sweepers to clean up leaked test resources
	@echo "==> Running sweepers..."
	TF_ACC=1 $(GO) test ./internal/acctest/ -v -timeout 60m -sweep=anyscale -sweep-run=

.PHONY: sweep-dry-run
sweep-dry-run: ## List what sweepers would delete without actually deleting
	@echo "==> Running sweepers in dry-run mode..."
	TF_ACC=1 ANYSCALE_SWEEP_DRY_RUN=1 $(GO) test ./internal/acctest/ -v -timeout 60m -sweep=anyscale -sweep-run=

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
		$(TFPLUGINDOCS) generate --provider-name $(BINARY_NAME); \
	else \
		echo "tfplugindocs not installed. Install with: go install github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@latest"; \
		exit 1; \
	fi

.PHONY: docs-validate
docs-validate: ## Validate provider documentation
	@echo "==> Validating documentation..."
	@if command -v $(TFPLUGINDOCS) >/dev/null 2>&1; then \
		$(TFPLUGINDOCS) validate --provider-name $(BINARY_NAME); \
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

.PHONY: clean-tests
clean-tests: ## Remove stale local test artifacts (binaries, example tfstate)
	@echo "==> Cleaning local test artifacts..."
	rm -f acctest.test provider.test
	find examples -maxdepth 2 -name 'terraform.tfstate*' -delete
	find examples -maxdepth 2 -name '.terraform.lock.hcl' -delete
	find examples -maxdepth 2 -type d -name '.terraform' -exec rm -rf {} +
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
	@mkdir -p $(BUILD_DIR)
	@bash -c 'set -u; \
	  SUFFIX=$${GITHUB_RUN_ID:-$$(date +%s)-$$$$}; \
	  STATE=$(CURDIR)/$(BUILD_DIR)/aws-vm-basic-$$SUFFIX.tfstate; \
	  CLOUD=tfacc-aws-vm-basic-$$SUFFIX; \
	  cd examples/aws-vm-basic; \
	  trap "terraform destroy -auto-approve -state=$$STATE -var=cloud_name=$$CLOUD || true" EXIT; \
	  terraform apply -auto-approve -state=$$STATE -var=cloud_name=$$CLOUD'

.PHONY: test-aws-vm-full
test-aws-vm-full: build ## Test AWS VM full (split pattern + EFS + MemoryDB)
	@echo "==> Testing AWS VM full scenario..."
	@mkdir -p $(BUILD_DIR)
	@bash -c 'set -u; \
	  SUFFIX=$${GITHUB_RUN_ID:-$$(date +%s)-$$$$}; \
	  STATE=$(CURDIR)/$(BUILD_DIR)/aws-vm-full-$$SUFFIX.tfstate; \
	  CLOUD=tfacc-aws-vm-full-$$SUFFIX; \
	  PREFIX=aws-vm-full-$$SUFFIX-; \
	  VARS="-var=cloud_name=$$CLOUD -var=common_prefix=$$PREFIX -var=enable_efs=true -var=enable_memorydb=true -var=vpc_cidr_block=172.27.0.0/16 -var=vpc_public_subnets=[\"172.27.21.0/24\",\"172.27.22.0/24\",\"172.27.23.0/24\"]"; \
	  cd examples/aws-vm; \
	  trap "terraform destroy -auto-approve -state=$$STATE $$VARS || true" EXIT; \
	  terraform apply -auto-approve -state=$$STATE $$VARS'

.PHONY: apply-aws-vm-basic
apply-aws-vm-basic: build ## Apply AWS VM basic only (override SUFFIX=<id> to pair with destroy)
	@mkdir -p $(BUILD_DIR)
	cd examples/aws-vm-basic && terraform apply -auto-approve \
	  -state=$(CURDIR)/$(BUILD_DIR)/aws-vm-basic-$(SUFFIX).tfstate \
	  -var=cloud_name=tfacc-aws-vm-basic-$(SUFFIX)

.PHONY: apply-aws-vm-full
apply-aws-vm-full: build ## Apply AWS VM full only (override SUFFIX=<id> to pair with destroy)
	@mkdir -p $(BUILD_DIR)
	cd examples/aws-vm && terraform apply -auto-approve \
	  -state=$(CURDIR)/$(BUILD_DIR)/aws-vm-full-$(SUFFIX).tfstate \
	  -var="cloud_name=tfacc-aws-vm-full-$(SUFFIX)" \
	  -var="common_prefix=aws-vm-full-$(SUFFIX)-" \
	  -var="enable_efs=true" -var="enable_memorydb=true" \
	  -var="vpc_cidr_block=172.27.0.0/16" \
	  -var='vpc_public_subnets=["172.27.21.0/24", "172.27.22.0/24", "172.27.23.0/24"]'

.PHONY: destroy-aws-vm-basic
destroy-aws-vm-basic: ## Destroy AWS VM basic (must match SUFFIX used by apply)
	cd examples/aws-vm-basic && terraform destroy -auto-approve \
	  -state=$(CURDIR)/$(BUILD_DIR)/aws-vm-basic-$(SUFFIX).tfstate \
	  -var=cloud_name=tfacc-aws-vm-basic-$(SUFFIX)

.PHONY: destroy-aws-vm-full
destroy-aws-vm-full: ## Destroy AWS VM full (must match SUFFIX used by apply)
	cd examples/aws-vm && terraform destroy -auto-approve \
	  -state=$(CURDIR)/$(BUILD_DIR)/aws-vm-full-$(SUFFIX).tfstate \
	  -var="cloud_name=tfacc-aws-vm-full-$(SUFFIX)" \
	  -var="common_prefix=aws-vm-full-$(SUFFIX)-" \
	  -var="enable_efs=true" -var="enable_memorydb=true" \
	  -var="vpc_cidr_block=172.27.0.0/16" \
	  -var='vpc_public_subnets=["172.27.21.0/24", "172.27.22.0/24", "172.27.23.0/24"]'

# ============================================================================
# TERRAFORM TESTING - AWS EKS
# ============================================================================

.PHONY: test-aws-eks-basic
test-aws-eks-basic: build ## Test AWS EKS basic (K8S)
	@echo "==> Testing AWS EKS basic scenario..."
	@mkdir -p $(BUILD_DIR)
	@bash -c 'set -u; \
	  SUFFIX=$${GITHUB_RUN_ID:-$$(date +%s)-$$$$}; \
	  STATE=$(CURDIR)/$(BUILD_DIR)/aws-eks-basic-$$SUFFIX.tfstate; \
	  CLOUD=tfacc-aws-eks-basic-$$SUFFIX; \
	  cd examples/aws-eks-basic; \
	  trap "terraform destroy -auto-approve -state=$$STATE -var=cloud_name=$$CLOUD || true" EXIT; \
	  terraform apply -auto-approve -state=$$STATE -var=cloud_name=$$CLOUD'

.PHONY: apply-aws-eks-basic
apply-aws-eks-basic: build ## Apply AWS EKS basic only (override SUFFIX=<id> to pair with destroy)
	@mkdir -p $(BUILD_DIR)
	cd examples/aws-eks-basic && terraform apply -auto-approve \
	  -state=$(CURDIR)/$(BUILD_DIR)/aws-eks-basic-$(SUFFIX).tfstate \
	  -var=cloud_name=tfacc-aws-eks-basic-$(SUFFIX)

.PHONY: destroy-aws-eks-basic
destroy-aws-eks-basic: ## Destroy AWS EKS basic (must match SUFFIX used by apply)
	cd examples/aws-eks-basic && terraform destroy -auto-approve \
	  -state=$(CURDIR)/$(BUILD_DIR)/aws-eks-basic-$(SUFFIX).tfstate \
	  -var=cloud_name=tfacc-aws-eks-basic-$(SUFFIX)

# ============================================================================
# TERRAFORM TESTING - GCP VM
# ============================================================================

.PHONY: test-gcp-vm-basic
test-gcp-vm-basic: build ## Test GCP VM basic (all-in-one pattern)
	@echo "==> Testing GCP VM basic scenario..."
	@mkdir -p $(BUILD_DIR)
	@bash -c 'set -u; \
	  SUFFIX=$${GITHUB_RUN_ID:-$$(date +%s)-$$$$}; \
	  STATE=$(CURDIR)/$(BUILD_DIR)/gcp-vm-basic-$$SUFFIX.tfstate; \
	  CLOUD=tfacc-gcp-vm-basic-$$SUFFIX; \
	  cd examples/gcp-vm-basic; \
	  trap "terraform destroy -auto-approve -state=$$STATE -var=cloud_name=$$CLOUD || true" EXIT; \
	  terraform apply -auto-approve -state=$$STATE -var=cloud_name=$$CLOUD'

.PHONY: test-gcp-vm-full
test-gcp-vm-full: build ## Test GCP VM full (split pattern + Filestore + Memorystore)
	@echo "==> Testing GCP VM full scenario..."
	@mkdir -p $(BUILD_DIR)
	@bash -c 'set -u; \
	  SUFFIX=$${GITHUB_RUN_ID:-$$(date +%s)-$$$$}; \
	  STATE=$(CURDIR)/$(BUILD_DIR)/gcp-vm-full-$$SUFFIX.tfstate; \
	  CLOUD=tfacc-gcp-vm-full-$$SUFFIX; \
	  PREFIX=gcp-vm-full-$$SUFFIX-; \
	  VARS="-var=cloud_name=$$CLOUD -var=common_prefix=$$PREFIX -var=enable_filestore=true -var=enable_memorystore=true -var=vpc_public_subnet_cidr=10.103.0.0/16"; \
	  cd examples/gcp-vm; \
	  trap "terraform destroy -auto-approve -state=$$STATE $$VARS || true" EXIT; \
	  terraform apply -auto-approve -state=$$STATE $$VARS'

.PHONY: apply-gcp-vm-basic
apply-gcp-vm-basic: build ## Apply GCP VM basic only (override SUFFIX=<id> to pair with destroy)
	@mkdir -p $(BUILD_DIR)
	cd examples/gcp-vm-basic && terraform apply -auto-approve \
	  -state=$(CURDIR)/$(BUILD_DIR)/gcp-vm-basic-$(SUFFIX).tfstate \
	  -var=cloud_name=tfacc-gcp-vm-basic-$(SUFFIX)

.PHONY: apply-gcp-vm-full
apply-gcp-vm-full: build ## Apply GCP VM full only (override SUFFIX=<id> to pair with destroy)
	@mkdir -p $(BUILD_DIR)
	cd examples/gcp-vm && terraform apply -auto-approve \
	  -state=$(CURDIR)/$(BUILD_DIR)/gcp-vm-full-$(SUFFIX).tfstate \
	  -var="cloud_name=tfacc-gcp-vm-full-$(SUFFIX)" \
	  -var="common_prefix=gcp-vm-full-$(SUFFIX)-" \
	  -var="enable_filestore=true" -var="enable_memorystore=true" \
	  -var="vpc_public_subnet_cidr=10.103.0.0/16"

.PHONY: destroy-gcp-vm-basic
destroy-gcp-vm-basic: ## Destroy GCP VM basic (must match SUFFIX used by apply)
	cd examples/gcp-vm-basic && terraform destroy -auto-approve \
	  -state=$(CURDIR)/$(BUILD_DIR)/gcp-vm-basic-$(SUFFIX).tfstate \
	  -var=cloud_name=tfacc-gcp-vm-basic-$(SUFFIX)

.PHONY: destroy-gcp-vm-full
destroy-gcp-vm-full: ## Destroy GCP VM full (must match SUFFIX used by apply)
	cd examples/gcp-vm && terraform destroy -auto-approve \
	  -state=$(CURDIR)/$(BUILD_DIR)/gcp-vm-full-$(SUFFIX).tfstate \
	  -var="cloud_name=tfacc-gcp-vm-full-$(SUFFIX)" \
	  -var="common_prefix=gcp-vm-full-$(SUFFIX)-" \
	  -var="enable_filestore=true" -var="enable_memorystore=true" \
	  -var="vpc_public_subnet_cidr=10.103.0.0/16"

# ============================================================================
# TERRAFORM TESTING - GCP GKE
# ============================================================================

.PHONY: test-gcp-gke-basic
test-gcp-gke-basic: build ## Test GCP GKE basic (K8S)
	@echo "==> Testing GCP GKE basic scenario..."
	@mkdir -p $(BUILD_DIR)
	@bash -c 'set -u; \
	  SUFFIX=$${GITHUB_RUN_ID:-$$(date +%s)-$$$$}; \
	  STATE=$(CURDIR)/$(BUILD_DIR)/gcp-gke-basic-$$SUFFIX.tfstate; \
	  CLOUD=tfacc-gcp-gke-basic-$$SUFFIX; \
	  cd examples/gcp-gke-basic; \
	  trap "terraform destroy -auto-approve -state=$$STATE -var=cloud_name=$$CLOUD || true" EXIT; \
	  terraform apply -auto-approve -state=$$STATE -var=cloud_name=$$CLOUD'

.PHONY: apply-gcp-gke-basic
apply-gcp-gke-basic: build ## Apply GCP GKE basic only (override SUFFIX=<id> to pair with destroy)
	@mkdir -p $(BUILD_DIR)
	cd examples/gcp-gke-basic && terraform apply -auto-approve \
	  -state=$(CURDIR)/$(BUILD_DIR)/gcp-gke-basic-$(SUFFIX).tfstate \
	  -var=cloud_name=tfacc-gcp-gke-basic-$(SUFFIX)

.PHONY: destroy-gcp-gke-basic
destroy-gcp-gke-basic: ## Destroy GCP GKE basic (must match SUFFIX used by apply)
	cd examples/gcp-gke-basic && terraform destroy -auto-approve \
	  -state=$(CURDIR)/$(BUILD_DIR)/gcp-gke-basic-$(SUFFIX).tfstate \
	  -var=cloud_name=tfacc-gcp-gke-basic-$(SUFFIX)

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
		echo "Consider cutting a release: make changelog-release VERSION=0.1.0"; \
		exit 1; \
	fi
	@echo "Version check passed: $(VERSION)"

# ============================================================================
# RELEASE
# ============================================================================

GORELEASER := goreleaser

.PHONY: release-check
release-check: ## Validate goreleaser configuration
	@echo "==> Validating goreleaser configuration..."
	@if command -v $(GORELEASER) >/dev/null 2>&1; then \
		$(GORELEASER) check; \
	else \
		echo "goreleaser not installed. Install with: brew install goreleaser"; \
		exit 1; \
	fi

.PHONY: release-snapshot
release-snapshot: ## Create a snapshot release (no publish, no tag required)
	@echo "==> Creating snapshot release..."
	@if command -v $(GORELEASER) >/dev/null 2>&1; then \
		$(GORELEASER) release --snapshot --clean; \
	else \
		echo "goreleaser not installed. Install with: brew install goreleaser"; \
		exit 1; \
	fi

.PHONY: release-dry-run
release-dry-run: ## Dry run release (validate everything without publishing)
	@echo "==> Running release dry-run..."
	@if command -v $(GORELEASER) >/dev/null 2>&1; then \
		$(GORELEASER) release --skip=publish --clean; \
	else \
		echo "goreleaser not installed. Install with: brew install goreleaser"; \
		exit 1; \
	fi

.PHONY: release
release: ## Create and publish a release (requires GPG_FINGERPRINT env var)
	@echo "==> Creating release..."
	@if [ -z "$(GPG_FINGERPRINT)" ]; then \
		echo "ERROR: GPG_FINGERPRINT environment variable is required"; \
		echo "Set it with: export GPG_FINGERPRINT=<your-gpg-key-fingerprint>"; \
		echo "Find your fingerprint with: gpg --list-secret-keys --keyid-format=long"; \
		exit 1; \
	fi
	@if command -v $(GORELEASER) >/dev/null 2>&1; then \
		$(GORELEASER) release --clean; \
	else \
		echo "goreleaser not installed. Install with: brew install goreleaser"; \
		exit 1; \
	fi

# ============================================================================
# CHANGELOG
# ============================================================================
# Fragment-based changelog automation. See .crystl/quest/changelog-release-contract.md
# and tools/changelog-build/.

.PHONY: changelog-build
changelog-build: ## Fold .changelog/ fragments into Unreleased, or finalize a release with VERSION=x.y.z
	@if [ "$(origin VERSION)" = "command line" ] && [ "$(VERSION)" != "0.0.1" ]; then \
		go run ./tools/changelog-build -finalize $(VERSION); \
	else \
		go run ./tools/changelog-build; \
	fi

.PHONY: changelog-check
changelog-check: ## Validate .changelog/ fragments parse cleanly, without writing anything
	@go run ./tools/changelog-build -check

define require_semver_version
	@if [ "$(origin VERSION)" != "command line" ] || [ "$(VERSION)" = "0.0.1" ]; then \
		echo "ERROR: VERSION is required. Usage: make $(1) VERSION=0.1.0"; \
		exit 1; \
	fi
	@case "$(VERSION)" in \
		[0-9]*.[0-9]*.[0-9]*) ;; \
		*) echo "ERROR: VERSION must look like a semantic version, e.g. 0.2.0 (got: $(VERSION))"; exit 1 ;; \
	esac
endef

# ============================================================================
# TAGGING HELPERS
# ============================================================================
# `main` requires 1 approval + code-owner review + passing checks (branch
# protection; enforce_admins is off, so an admin token COULD push straight to
# main, but this deliberately doesn't rely on that). So finalizing the
# changelog and cutting the tag are two steps with a normal PR in between,
# same as every other change to main -- not one command that only works for
# whoever happens to have an admin bypass.

.PHONY: changelog-release
# Target-specific variable, not a plain shell `branch=...` capture: each
# recipe line below runs in its OWN subshell (Make's default, no .ONESHELL),
# so a shell variable set on one line is gone by the next. $(START_BRANCH)
# is evaluated once by Make itself and textually substituted into every
# line that references it, surviving across all of them - confirmed live
# during the v0.1.2 release, where the plain-shell-variable version failed
# the final `git checkout "$$branch"` with an empty pathspec (cosmetic only:
# it ran after the finalize/branch/push/PR steps, which had already
# succeeded, so it never blocked an actual release).
changelog-release: START_BRANCH := $(shell git rev-parse --abbrev-ref HEAD)
changelog-release: ## Open a PR that finalizes CHANGELOG.md for a release (usage: make changelog-release VERSION=0.1.0)
	$(call require_semver_version,changelog-release)
	@if [ "$(START_BRANCH)" != "main" ]; then \
		echo "ERROR: run 'make changelog-release' from main (currently on $(START_BRANCH))"; \
		exit 1; \
	fi
	@if ! git diff --quiet || ! git diff --quiet --cached; then \
		echo "ERROR: working tree has uncommitted changes; commit or stash first"; \
		exit 1; \
	fi
	@echo "==> Finalizing CHANGELOG.md for v$(VERSION)..."
	go run ./tools/changelog-build -finalize $(VERSION)
	git checkout -b "changelog/v$(VERSION)"
	git add CHANGELOG.md .changelog
	git commit -m "chore: finalize CHANGELOG.md for v$(VERSION)"
	git push origin "changelog/v$(VERSION)"
	gh pr create --title "chore: finalize CHANGELOG.md for v$(VERSION)" \
		--body "Mechanical: renames Unreleased to v$(VERSION) and dates it. Every entry in it already went through review as its own .changelog/ fragment PR; this just reorganizes them. Merge before running 'make tag VERSION=$(VERSION)'." \
		--base main
	git checkout "$(START_BRANCH)"
	@echo "==> Get that PR reviewed and merged, THEN run: make tag VERSION=$(VERSION)"

.PHONY: tag
tag: ## Create and push a release tag once CHANGELOG.md is finalized on main (usage: make tag VERSION=0.1.0)
	$(call require_semver_version,tag)
	@branch="$$(git rev-parse --abbrev-ref HEAD)"; \
	if [ "$$branch" != "main" ]; then \
		echo "ERROR: run 'make tag' from main (currently on $$branch)"; \
		exit 1; \
	fi
	@if ! git diff --quiet || ! git diff --quiet --cached; then \
		echo "ERROR: working tree has uncommitted changes; commit or stash first"; \
		exit 1; \
	fi
	@if ! grep "^## \[" CHANGELOG.md | grep -qF "[$(VERSION)]"; then \
		echo "ERROR: CHANGELOG.md has no '## [$(VERSION)]' section on this branch."; \
		echo "Run 'make changelog-release VERSION=$(VERSION)' and merge that PR first."; \
		exit 1; \
	fi
	@echo "==> Creating tag v$(VERSION)..."
	git tag -a "v$(VERSION)" -m "Release v$(VERSION)"
	@echo "==> Pushing tag v$(VERSION)..."
	git push origin "v$(VERSION)"
	@echo "==> Tag v$(VERSION) created and pushed"

.PHONY: tag-delete
tag-delete: ## Delete a release tag locally and remotely (usage: make tag-delete VERSION=0.1.0)
	@if [ -z "$(VERSION)" ] || [ "$(VERSION)" = "0.0.1" ]; then \
		echo "ERROR: VERSION is required. Usage: make tag-delete VERSION=0.1.0"; \
		exit 1; \
	fi
	@echo "==> Deleting tag v$(VERSION) locally..."
	-git tag -d "v$(VERSION)"
	@echo "==> Deleting tag v$(VERSION) remotely..."
	-git push origin --delete "v$(VERSION)"
	@echo "==> Tag v$(VERSION) deleted"

.PHONY: tag-list
tag-list: ## List all version tags
	@echo "==> Version tags:"
	@git tag -l "v*" --sort=-version:refname
