# Claude Instructions – Terraform Provider Dev

## Purpose

You are assisting with development of a Go-based Terraform Provider for managing Anyscale resources via the Anyscale API v2.

### Goals

- Clean, idiomatic provider code using the Terraform Plugin Framework.
- Stable, reviewable schemas and state <-> API mappings.
- Docs compatible with Terraform Registry format (generated where possible).
- Easy to test, extend, and troubleshoot.

---

## How to Respond (Agent Behavior)

### Default approach
1. **Scaffold first, then refine**
   - Start with minimal working code (provider + one resource/data source).
   - Iterate to add validation, plan modifiers, error handling, and tests.

2. **Show only what’s needed**
   - If files are small: show full file contents.
   - If files are large: show focused diffs/patches.
   - Avoid generic explanations unless asked; prefer concrete changes.

3. **Ask only when truly blocked**
   - If the request is vague, ask 1–2 clarifying questions.
   - Otherwise, make reasonable assumptions and state them.

### Security / hygiene
- **Never print or commit tokens** (including snippets from credentials files).
- If you need to show examples: use `$ANYSCALE_CLI_TOKEN` and placeholders.

---

## Tech Stack & Conventions

- Language: Go (as defined by `go.mod`).
- Framework: `github.com/hashicorp/terraform-plugin-framework` + `providerserver`.
- API: Anyscale Managed Ray API v2 (see console OpenAPI/Swagger docs).
- Docs: `tfplugindocs` preferred; do not hand-edit generated docs under `docs/` unless the repo explicitly requires it.
- Layout (preferred):
  - `main.go` — provider entrypoint using `providerserver.Serve`
  - `internal/provider/provider.go` — `Metadata`, `Schema`, `Configure`, `Resources`, `DataSources`
  - `internal/provider/resource_*.go` — each resource in its own file
  - `internal/provider/data_source_*.go` — each data source in its own file
  - `docs/` — generated docs
  - `examples/` — runnable Terraform configs

---

## Provider-Specific Notes

### Authentication priority
1. `token` argument on the provider block
2. `ANYSCALE_CLI_TOKEN` environment variable
3. `~/.anyscale/credentials.json` (same format as Anyscale CLI)

### Configure behavior
- Centralize token resolution in a helper (e.g. `resolveToken(ctx, config)`).
- Initialize a shared Anyscale API client once.
- Attach it to both:
  - `resp.ResourceData`
  - `resp.DataSourceData`


### Error handling
- Use `resp.Diagnostics.AddError` for configuration/auth issues.
- Avoid panics / fatal logs.

### Compatibility targets
- Terraform >= 1.6
- Current `terraform-plugin-framework` version used by the repo

## Local Dev Workflow (Canonical: Makefile)

### Build
```bash
make build
```

### Unit tests
```bash
make test
```

### Lint / format (optional but encouraged)
```bash
make fmt
make lint
```

### Docs
```bash
make docs
# Do not manually edit generated docs under docs/
```

## Terraform Local Testing (dev_overrides)

This repo uses Terraform dev_overrides in ~/.terraformrc to load the local provider binary.

### Key rules

- **Do not run terraform init** when dev_overrides is active (provider is not in the public registry).
- Rebuild after changes (`make build`) before running terraform plan/apply.
- `make install` is a convenience wrapper that builds and prints the expected local binary location.

### Example flow

```bash
# Build provider binary where dev_overrides expects it
make build

# Test with example configs (no init)
cd examples/aws-vm-basic/
terraform plan
terraform apply
```

---

## Testing guidance

- Prefer:
	- Unit tests for schema validation and model conversions.
	- Acceptance tests using resource.
	- Test.

### Acceptance Tests

Acceptance tests run real API calls against Anyscale and require credentials.
They are found in `internal/acctest`

```bash
make testacc
```

### Acceptance tests with coverage

```bash
make testacc-cover
```

### Credentials
Acceptance tests must authenticate using the same resolution order as the provider:
1.	ANYSCALE_CLI_TOKEN
2.	`~/.anyscale/credentials.json` from `anyscale login`

**Never print or log raw tokens.**

### Test cloud selection - preferred behavior: auto-discover

Acceptance tests should be able to run without manually setting cloud IDs. Using the credentials, access the Anyscale APIs to list all clouds at:
https://console.anyscale.com/api/v2/docs#/default/list_clouds_api_v2_clouds__get

Optional overrides:
- `ANYSCALE_TEST_CLOUD_ID` — pin tests to an existing cloud ID (validated to exist).
- `ANYSCALE_TEST_CLOUD_NAME` — pin tests by cloud name (must resolve uniquely).

If neither are set, tests should:
1.	Discover an existing test cloud (e.g., by name prefix/tag such as tf-acc-*), or
2.	Create an ephemeral test cloud, then reuse it during the test run.

Cleanup:
- By default, destroy any ephemeral cloud created by tests.
- If `ANYSCALE_TEST_KEEP=1`, keep the created cloud for debugging and print the cloud ID/name (but never tokens).

---

## Quick reference

```bash
# Build & Install
make build
make install

# Testing
make test
make testacc

# Code Quality
make fmt
make lint
pre-commit run --all-files

# Documentation
make docs
make docs-validate
```

---

## Repo-Level Terraform Scenario Tests (Examples)

There are Makefile targets that run end-to-end Terraform applies/destroys using the examples/ configs.

### Primary matrix (efficient coverage)

```bash
make test-primary
# or narrowed:
make test-primary-aws
make test-primary-gcp
make test-primary-vm
make test-primary-k8s
```

### Individual scenarios
```bash
make test-aws-vm-basic
make test-aws-vm-full
make test-aws-eks-basic
make test-gcp-vm-basic
make test-gcp-vm-full
make test-gcp-gke-basic
```

These targets run terraform apply and terraform destroy. Ensure your credentials and cloud quotas are in a safe state before running.

---

## Repository Context & Boundaries

- This Terraform provider repo is the only place where files may be created/edited.
- External product monorepo is **read-only reference**:
  - Location: ~/projects/anyscale/product
  - You may read files there to understand API surface/models.
  - **Do not** run build/test/tooling commands inside it.
  - **Do not** suggest changes to that repository.
  - API code reference: ~/projects/anyscale/product/backend/server/api and subfolders

---

## Using the Anyscale API Docs

- Treat console OpenAPI/Swagger docs as the primary reference for endpoints and schemas.
- The OpenAPI/Swagger docs can be found at https://console.anyscale.com/api/v2/docs
- When showing example requests:
  - Use Authorization: Bearer $ANYSCALE_CLI_TOKEN
  - Do not print real tokens
  - The $ANYSCALE_CLI_TOKEN may be read from an environment variable, or read from ~/.anyscale/credentials.json

## Test Resource Naming and Sweeping

All test-created resources MUST use the `acctest.UniqueName(t, slug)` helper
which produces names of the form `tfacc-<slug>-<rand>`. Do not hardcode
literal names — concurrent CI runs will collide. Do not use legacy prefixes
`tf-test-` or `tfprovider-` for new tests; sweepers still match those for
backward compatibility but new code should standardize on `tfacc-`.

### Sweepers

Sweepers in `internal/acctest/sweeper_*.go` automatically clean leaked test
resources whose names match a sweepable prefix AND that are older than
`ANYSCALE_SWEEP_MIN_AGE` (default 2h). The age guard prevents racing live
tests. Run manually:

    make sweep            # actually deletes
    make sweep-dry-run    # logs what would be deleted

A daily GitHub Actions job at `.github/workflows/sweep.yml` runs `make sweep`
at 03:00 UTC against the test org.

### When a test crashes or is interrupted

The example-based test targets (`make test-aws-vm-basic`, etc.) wrap apply
and destroy in a bash EXIT trap so destroy fires even on apply failure or
ctrl-C. If you still suspect a leak, run `make sweep-dry-run` to inspect or
`make sweep` to clean.

### Adding a new resource type

If you add a new resource type to the provider that creates real backend
state, add a sweeper file `internal/acctest/sweeper_<type>_test.go` following
the pattern in `sweeper_project_test.go`. The cloud sweeper's `Dependencies`
list determines order — if your new resource lives under a cloud, add it to
the cloud sweeper's `Dependencies` so it sweeps first.

<!-- crystl-cli:begin -->
@AGENTS.md
<!-- crystl-cli:end -->
