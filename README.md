# Terraform Provider for Anyscale

A Terraform provider for managing Anyscale resources via the Anyscale API v2, built with the [Terraform Plugin Framework](https://developer.hashicorp.com/terraform/plugin/framework).

## Features

- **WIP Anyscale Platform Support**:
  - Anyscale Clouds (self contained deployment pattern)
  - Cloud resource deployment (split deployment pattern)
  - Compute configurations (cluster templates)
  - Projects

- **Automatic Detection**: Cloud provider and region auto-detected from configuration blocks

- **Flexible Authentication**: Environment variable, credentials file, or provider configuration

## Framework

**Version 0.1.0+** uses the [Terraform Plugin Framework](https://developer.hashicorp.com/terraform/plugin/framework) instead of SDK v2. Key improvements:

- **Native HCL Support**: Top-level fields now support native Terraform syntax:
  ```hcl
  # Before (SDK v2) - required jsonencode
  flags = jsonencode({
    "ray-cluster-ray-version" = "2.9.0"
  })

  # After (Framework) - native HCL syntax!
  flags = {
    "ray-cluster-ray-version" = "2.9.0"
  }
  ```

- **Better Type Safety**: Strongly-typed schema with compile-time validation
- **Improved Plan Modifiers**: Auto-population of computed fields
- **State Compatibility**: Existing resources continue working without migration

## Authentication

The provider supports three methods of authentication (checked in order):

1. **Provider Configuration**: Set `token` in the provider block
2. **Environment Variable**: Set `ANYSCALE_CLI_TOKEN` with your API token
3. **Credentials File**: Store your token in `~/.anyscale/credentials.json`

### Credentials File Format

```json
{
  "cli_token": "your-api-token-here"
}
```

or

```json
{
  "token": "your-api-token-here"
}
```

## Installation

### Development Build

```bash
go mod download
go build -o terraform-provider-anyscale
```

For local development, configure Terraform to use your local build via `~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "terraform-providers/anyscale" = "/path/to/terraform-provider-anyscale"
  }
  direct {}
}
```

**Note**: When using `dev_overrides`, skip `terraform init` and go directly to `terraform plan`/`apply`.

## Usage

### Provider Configuration

```hcl
terraform {
  required_providers {
    anyscale = {
      source = "github.com/brent/anyscale"
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

## Resources

### anyscale_cloud

Creates and manages an Anyscale Cloud with all-in-one or empty cloud patterns. Supports AWS VM, GCP VM, AWS EKS (K8S), and GCP GKE (K8S) deployments.

**Examples**: See [`examples/aws-vm-basic/`](examples/aws-vm-basic/), [`examples/gcp-vm-basic/`](examples/gcp-vm-basic/), [`examples/aws-eks-basic/`](examples/aws-eks-basic/), and [`examples/gcp-gke-basic/`](examples/gcp-gke-basic/) for complete examples.

### anyscale_cloud_resource

Manages cloud resource deployments separately from the cloud itself (split deployment pattern). Use this when you need to add multiple resource deployments to a single cloud.

**Import**: Use composite ID format `cloud_id:resource_name`
```bash
terraform import anyscale_cloud_resource.aws_vm "cld_abc123:vm-aws-us-east-2"
```

**Examples**: See [`examples/aws-vm-basic-resource/`](examples/aws-vm-basic-resource/) for split deployment pattern examples.

### anyscale_compute_config

Creates cluster templates (compute configurations) with native HCL syntax for flags and advanced configurations.

**Examples**: See [`examples/aws-vm-basic/`](examples/aws-vm-basic/) and [`examples/gcp-vm-basic/`](examples/gcp-vm-basic/) for compute config examples.

For more examples, see the [`examples/`](examples/) directory.

## Examples

See the [`examples/`](examples/) directory for complete, working examples:

- **AWS VM**: [`examples/aws-vm-basic/`](examples/aws-vm-basic/) - Basic AWS VM cloud
- **GCP VM**: [`examples/gcp-vm-basic/`](examples/gcp-vm-basic/) - Basic GCP VM cloud
- **AWS EKS**: [`examples/aws-eks-basic/`](examples/aws-eks-basic/) - AWS EKS (K8S) cloud
- **GCP GKE**: [`examples/gcp-gke-basic/`](examples/gcp-gke-basic/) - GCP GKE (K8S) cloud
- **Split Deployment**: [`examples/aws-vm-basic-resource/`](examples/aws-vm-basic-resource/) - Empty cloud with separate resource deployment

## Cloud Lifecycle

The provider handles the full cloud lifecycle automatically:
- Creates minimal cloud via API
- Adds resource deployment (for all-in-one patterns)
- Polls until cloud is ready (up to 30 minutes)
- Manages updates and deletions

## Development

### Building

```bash
make build
```

### Project Structure

```
├── main.go                    # Provider entry point
├── internal/provider/         # Provider implementation
├── examples/                  # Example configurations
└── docs/                      # Generated documentation
```

## Migration from SDK v2 (v0.0.x → v0.1.0+)

### State Compatibility

Existing resources continue working - no state migration needed! The framework provider can read SDK v2 state.

### Syntax Changes

#### Removed: timeouts Blocks

SDK v2 `timeouts` blocks are no longer supported (framework uses internal timeouts):

```hcl
# Remove this:
timeouts {
  create = "30m"
  update = "10m"
  delete = "10m"
}
```

#### Improved: Native HCL Syntax

Top-level `flags` and `advanced_configurations_json` now support native HCL:

```hcl
# Before (SDK v2):
flags = jsonencode({
  "ray-cluster-ray-version" = "2.9.0"
})

# After (Framework):
flags = {
  "ray-cluster-ray-version" = "2.9.0"
}
```

### Schema Changes

#### anyscale_cloud

- Renamed fields (backward compatible in state):
  - `anyscale_iam_role_id` → `controlplane_iam_role_arn`
  - `instance_iam_role_id` → `dataplane_iam_role_arn`
  - `s3_bucket_id` → `object_storage.bucket_name`

- New computed fields:
  - `is_empty_cloud` - Indicates if cloud has embedded config
  - `cloud_deployment_id` - Deployment ID (may be null for empty clouds)

#### anyscale_compute_config

- `flags` and `advanced_configurations_json` support native HCL
- Same state structure, improved type safety

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

1. Follow [Terraform Plugin Framework](https://developer.hashicorp.com/terraform/plugin/framework) best practices
2. Add unit tests for helper functions
3. Add acceptance tests for resources
4. Update documentation
5. Run `make lint` and `make test` before submitting

## License

[Your License Here]
