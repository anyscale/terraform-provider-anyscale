# Terraform provider for Anyscale (Beta)

> [!WARNING]
> ## Beta / Use at Your Own Risk
>
> This Terraform provider is currently in **beta** and is intended for
> **evaluation, experimentation, and early feedback**.
>
> **Do not rely on this provider for production infrastructure.**
>
> Expect:
>
> - Breaking changes between releases
> - Incomplete API coverage
> - Missing features and documentation
> - Bugs that may result in failed or partial infrastructure changes
> - State compatibility changes without notice
>
> Use this provider **at your own risk**.

A Terraform provider for managing Anyscale resources using the Anyscale API v2.

This provider is currently in **beta**. APIs, resource schemas, behavior,
and Terraform state compatibility may change before the first stable release.

We welcome bug reports and feedback as the provider evolves toward a stable v1.0 release.

This beta provider requires Terraform v1.8 or newer.

```hcl
terraform {
  required_providers {
    anyscale = {
      source = "anyscale/anyscale"
    }
  }
}

provider "anyscale" {
  # Optional: defaults to https://console.anyscale.com
  # api_url = "https://console.anyscale.com"

  # Optional: token can be set here, via ANYSCALE_CLI_TOKEN env var,
  # or read from ~/.anyscale/credentials.json
  # token = "your-token-here"
}
```

## Current Beta Capabilities

- Currently supported resources:
  - Anyscale Clouds with the all-in-one deployment pattern (embedded resource configuration)
  - Cloud resource deployments with the multi-resource cloud pattern
  - Compute configurations
  - Container images (build from a Containerfile, or register existing images from a registry)
  - Services (deploy Ray Serve applications and roll out new versions)
  - Projects
  - Organization invitations
  - Organization collaborators (import-only; manages permissions for existing members)
- Currently supported data sources:
  - Clouds (single lookup and list/filter)
  - Projects (single lookup and list/filter)
  - Compute configurations
  - Container images (single lookup and list/filter)
  - Services (single lookup and list/filter)
  - The current authenticated user and their connected organization
  - Organization users (single lookup and list/filter)
- **Automatic Detection**: Cloud provider and region auto-detected from configuration blocks
- **Flexible Authentication**: Environment variable, credentials file, or provider configuration

## Current Limitations

This provider is under active development. Not all Anyscale APIs and
resources are currently supported.

Current limitations include:

- Incomplete API coverage
- Resource schemas may change
- Import support may be incomplete
- Documentation is still being expanded
- Breaking changes may occur between releases

## Authentication

The provider supports three methods of authentication checked in the following order:

1. **Provider Configuration**: Set `token` in the provider block
2. **Environment Variable**: Set `ANYSCALE_CLI_TOKEN` with your API token
3. **Credentials File**: Store your token in `~/.anyscale/credentials.json`


## Installation

### Development build

```bash
go mod download
make build
```

For local development, configure Terraform to use your local build with `~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "anyscale/anyscale" = "/path/to/terraform-provider-anyscale"
  }
  direct {}
}
```

**Note**: When using `dev_overrides`, Terraform uses your local binary instead of downloading from registry.

## Usage

### Provider Configuration

```hcl
terraform {
  required_providers {
    anyscale = {
      source  = "anyscale/anyscale"
      version = "~> 0.1"
    }
  }
}

provider "anyscale" {
  # Optional: defaults to https://console.anyscale.com
  # api_url = "https://console.anyscale.com"

  # Optional: token can be set here, via ANYSCALE_CLI_TOKEN env var,
  # or read from ~/.anyscale/credentials.json
  # token = "your-token-here"
}
```

## Examples

> [!NOTE]
> These examples demonstrate provider functionality during beta. They are
> not production reference architectures.

See the [`examples/`](examples/) directory for complete, working examples:

- **AWS VM**: [`examples/aws-vm-basic/`](examples/aws-vm-basic/) - Basic AWS VM cloud with compute config examples
- **GCP VM**: [`examples/gcp-vm-basic/`](examples/gcp-vm-basic/) - Basic GCP VM cloud with compute config examples
- **AWS EKS**: [`examples/aws-eks-basic/`](examples/aws-eks-basic/) - AWS EKS Kubernetes cloud
- **GCP GKE**: [`examples/gcp-gke-basic/`](examples/gcp-gke-basic/) - GCP GKE Kubernetes cloud
- **Azure AKS**: [`examples/azure-aks-basic/`](examples/azure-aks-basic/) - Azure AKS Kubernetes cloud (schema-validated only - see the example's README for status)
- **Multi-Resource Cloud**: [`examples/aws-vm-basic-resource/`](examples/aws-vm-basic-resource/) - Empty cloud with separate resource deployment
- **Kitchen Sink**: [`examples/kitchen-sink/`](examples/kitchen-sink/) - Comprehensive multi-cloud build mixing VM and EKS resources on one cloud, plus every resource and data source this provider registers

## Versioning

Until the provider reaches a 1.0 release:

- Any release prior to v1.0 may include breaking changes without a major version bump.
- Resource schemas may change.
- Terraform state migrations may be required.

### Minor vs. patch releases

Within the 0.x line, the version bump follows mechanically from the changelog fragment types a release
contains (see [`.changelog/`](.changelog/)), not a case-by-case severity judgment:

- A release with at least one `breaking-change` fragment is a **minor** bump. `breaking-change` covers
  anything that forces an existing configuration to be edited, produces a plan diff that did not exist
  before, or replaces a resource that previously updated in place — regardless of how safe, small, or
  clearly-deserved the underlying fix is.
- A release with no `breaking-change` fragments (only `added`, `fixed`, `deprecated`, or similar) is a
  **patch** bump.

For example, renaming a broken attribute that never worked, or adding `RequiresReplace` to stop a
silent orphaned-resource bug, both still count as `breaking-change` and therefore ship as minor: the
fragment type tracks the effect on existing configurations, not whether the change was a bug fix.

## Feedback

Bug reports, feature requests, and pull requests are welcome.

Please include:

- Terraform version
- Provider version
- Cloud provider
- Example configuration
- Relevant logs

## Development

### Building

```bash
make build
```

### Project structure

```
├── main.go                    # Provider entry point
├── internal/provider/         # Provider implementation
├── examples/                  # Example configurations
└── docs/                      # Generated documentation
```

## Testing

```bash
# Unit tests
make test

# Acceptance tests (requires AWS credentials)
make testacc

# Specific scenario test
make test-aws-vm-basic
```

## Contributing

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for the PR workflow, including the changelog fragment
each PR needs.

## License

[Mozilla Public License 2.0](LICENSE)
