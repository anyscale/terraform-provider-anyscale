# Claude Instructions – Terraform Provider Dev

## Project Overview

Building a Go based Terraform Provider for managing Anyscale resources. Uses Anyscale APIs.

## Project Role

You are assisting with developing a Terraform provider written in Go using the official Terraform Plugin Framework.
Goals:

- Implement a clean, idiomatic provider with resources and data sources following HashiCorp best practices.
- Maintain high-quality docs compatible with the Terraform Registry format.
- Keep the provider easy to test, extend, and review.

## Tech Stack & Conventions

- Language: Go (latest stable), standard tooling (`go test`, `golangci-lint` if present).
- SDK: `github.com/hashicorp/terraform-plugin-framework` and `providerserver`.
- Target API: Anyscale API v2, as exposed via the Anyscale console OpenAPI/Swagger docs at https://console.anyscale.com/api/v2/docs#.
  - Local source code for this API can be found in the product repository at
- Auth: Bearer token for Anyscale API requests, resolved using the priority rules in **Provider-Specific Notes**.
- Docs: Generated via `tfplugindocs` where possible; hand-written markdown must follow Terraform Registry structure.
- Layout (preferred):
  - `main.go` – provider entrypoint using `providerserver.Serve`.
  - `internal/provider/provider.go` – `Metadata`, `Schema`, `Configure`, `Resources`, `DataSources`.
  - `internal/provider/resource_*.go` – each resource in its own file.
  - `internal/provider/data_source_*.go` – each data source in its own file.
  - `docs/` – generated docs for provider, resources, and data sources.
  - `examples/` – small, runnable Terraform configurations.

## How To Work On This Repo

When responding:

1. **Scaffold first, then refine**
   - Propose or update:
     - `main.go` with a `providerserver.Serve` entrypoint.
     - `internal/provider/provider.go` with `Metadata`, `Schema`, `Configure`, `Resources`, and `DataSources`. [web:7][web:8]
     - Per-resource and per-datasource files with clear `Schema`, `Create`, `Read`, `Update`, and `Delete` (as applicable).
   - Start with minimal working code, then iterate to add validation, error handling, and tests.

2. **Follow Terraform provider patterns**
   - Keep provider configuration focused on auth, endpoints, and common client options.
   - Ensure resources:
     - Use strong types (`schema.StringAttribute`, `schema.Int64Attribute`, etc.) with `Required`, `Optional`, `Computed` set correctly. [web:7]
     - Handle plan modifiers, defaults, and `RequiresReplace` where appropriate.
     - Correctly map state <-> API models.
   - Ensure provider `Configure` builds and shares a client object via `ResourceData` / `DataSourceData`. [web:7]

3. **Documentation expectations**
   - For each resource/data source, maintain a markdown doc under `docs/` with:
     - Front-matter and headings compatible with the Terraform Registry docs guidelines. [web:6]
     - A minimal working example under an `## Example Usage` section.
   - Prefer suggesting `tfplugindocs` usage for regeneration of docs after schema changes. [web:6]

4. **Testing guidance**
   - Add or extend Go tests in `internal/provider/*_test.go`.
   - Prefer:
     - Unit tests for schema and model conversions.
     - Acceptance-style tests using the plugin framework patterns if the repo already uses them.
   - When writing tests, show how to run them, e.g. `go test ./...`.

5. **Development workflow**
   - Assume local dev flow:
     - Build: `go build ./...`
     - Test: `go test ./...`
     - (Optional) `tfplugindocs` to regenerate docs.
     - Terraform dev overrides via `.terraformrc` or `CLI config` so Terraform loads the local provider binary. [web:7]
   - When asked, generate example `.terraformrc` or example Terraform configuration using the provider.

## How To Interact With Me

When you need to modify or add files:

- Show **only the relevant file(s)** with full contents if reasonably small, or focused diffs/patch-style edits if large.
- Avoid generic explanations unless requested; focus on concrete code, schemas, and examples.
- When the user asks for help:
  - If the request is vague, ask one or two clarifying questions instead of guessing.
  - Prefer small, incremental steps so changes are easy to review and revert.

## Provider-Specific Notes

- Authentication priority:
  1. `token` argument on the Terraform provider block.
  2. `ANYSCALE_CLI_TOKEN` environment variable.
  3. Credentials file at `~/.anyscale/credentials.json` (same format as the Anyscale CLI).
- Authentication behavior:
  - Use the first non-empty source in the priority list to build the Anyscale API client.
  - If no token is found, return a clear diagnostic explaining how to create an API key in the Anyscale console and configure it via the provider block, `ANYSCALE_CLI_TOKEN`, or the credentials file.
- Provider `Configure`:
  - Centralize token resolution in a helper (e.g. `resolveToken(ctx, providerConfig)`).
  - Initialize a shared Anyscale API client using the resolved token and attach it to `resp.DataSourceData` and `resp.ResourceData` for reuse by all resources and data sources.
- Error handling:
  - Use `resp.Diagnostics.AddError` for authentication and configuration issues instead of panics or fatal logs.
- Compatibility:
  - Assume target Terraform >= 1.5 and a current `terraform-plugin-framework` version consistent with HashiCorp docs.

## When Unsure

- If a Terraform behavior, plugin framework API, or docs requirement is unclear, mention the uncertainty and propose one or two reasonable implementation options.
- Ask the user what target platform or API they are wrapping (e.g., internal HTTP API, cloud service) before generating large sets of resources or models.

## Essential Commands

```bash
# Build & Test
make build                   # Build to ./build/atmos
make testacc                 # Run tests
make testacc-cover           # Tests with coverage
make lint                    # golangci-lint on changed files
```

## Repository Context & Boundaries

- Working directory:
  - This Terraform provider lives in the current repository and is the **only** place where you should create, edit, or delete files.
- External monorepo (read-only reference):
  - The Anyscale product monorepo is located at `~/projects/anyscale/product`.
  - Treat this monorepo as **read-only**: you may open and read files there to understand the Anyscale Managed Ray API v2 surface and models, but must not modify or create files in that repository.
  - No changes should be suggested on the product monorepo.
  - The APIs that will be called from this provider can be found in `~/projects/anyscale/product/backend/server/api` and subfolders
- Scope rules:
  - Do not run build, test, or tooling commands from inside `~/projects/anyscale/product` as part of this workflow.
  - Keep all automated refactors, generated code, and edits constrained to the Terraform provider repository and its subdirectories.

## Anyscale API Docs Access

- API documentation:
  - The REST surface for this provider is documented in the Anyscale Managed Ray API v2 docs at the Anyscale console.
  - Treat these API docs as the primary reference for available endpoints, fields, and expected request/response shapes.
- Authentication for docs and ad-hoc calls:
  - The current environment is already authenticated to Anyscale via `~/.anyscale/credentials.json`, which is managed by `anyscale login`.
  - When you need to inspect or experiment with an endpoint beyond the Terraform provider:
    - Read the CLI token from `~/.anyscale/credentials.json`.
    - Use it as a bearer token in `curl` or small scripts when calling the Managed Ray API v2.
  - Do **not** commit or print the raw token; keep it in memory or use environment variables like `ANYSCALE_CLI_TOKEN`.
- Using the docs with code:
  - Prefer copying endpoint paths, query parameters, and JSON schemas from the Managed Ray API v2 docs into the provider implementation and tests.
  - When suggesting example requests, show how to pass the bearer token obtained from `ANYSCALE_CLI_TOKEN` or `credentials.json` in the `Authorization: Bearer …` header.

## Local Development & Testing Workflow

- Provider installation for local dev:
  - This provider uses Terraform's `dev_overrides` in `~/.terraformrc` to load the local binary instead of fetching from a registry.
  - The override points at the provider directory: `/Users/brent/Projects/terraform/terraform-provider-anyscale`.
- Build the provider:
  - From the provider repo root, run:
    ```
    go build -o terraform-provider-anyscale
    ```
  - This drops the compiled binary in the repo root where the dev_override expects it.
- Testing with Terraform:
  - **Skip `terraform init`** when the dev override is active; it will fail because the provider is not in the public registry yet.
  - Go directly to `terraform plan` or `terraform apply` to test resources and data sources.
  - Terraform will automatically load the local provider binary via the dev_override.
  - Example workflow:
    ```
    # Build provider
    go build -o terraform-provider-anyscale

    # Test with example config (no init needed)
    cd examples/
    terraform plan
    terraform apply
    ```
- When suggesting test workflows or examples:
  - Do not include `terraform init` in the steps.
  - Show `go build` followed directly by `terraform plan` or `terraform apply`.
  - Remind the user that changes to the provider require a rebuild (`go build`) before the next Terraform command.
- Acceptance tests:
  - Use Go's acceptance test framework for the plugin if the repo has `*_test.go` files with `resource.Test`.
  - Run with `TF_ACC=1 go test ./...` to enable acceptance tests that actually invoke
