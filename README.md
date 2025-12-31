# Terraform provider for Anyscale

A Terraform provider for managing Anyscale resources with Anyscale API v2, built with the [Terraform Plugin Framework](https://developer.hashicorp.com/terraform/plugin/framework).

This is a work in progress.

The Anyscale Terraform Provider works with Terraform v1.8+.

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

## Features

- **WIP Anyscale Platform Support**:
  - Anyscale Clouds with self contained deployment pattern
  - Cloud resource deployments with split deployment pattern
  - Compute configurations
  - Projects
- **Automatic Detection**: Cloud provider and region auto-detected from configuration blocks
- **Flexible Authentication**: Environment variable, credentials file, or provider configuration

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
    "terraform-providers/anyscale" = "/path/to/terraform-provider-anyscale"
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
      source = "github.com/anyscale/terraform-provider-anyscale"
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

See the [`examples/`](examples/) directory for complete, working examples:

- **AWS VM**: [`examples/aws-vm-basic/`](examples/aws-vm-basic/) - Basic AWS VM cloud with compute config examples
- **GCP VM**: [`examples/gcp-vm-basic/`](examples/gcp-vm-basic/) - Basic GCP VM cloud with compute config examples
- **AWS EKS**: [`examples/aws-eks-basic/`](examples/aws-eks-basic/) - AWS EKS Kubernetes cloud
- **GCP GKE**: [`examples/gcp-gke-basic/`](examples/gcp-gke-basic/) - GCP GKE Kubernetes cloud
- **Split Deployment**: [`examples/aws-vm-basic-resource/`](examples/aws-vm-basic-resource/) - Empty cloud with separate resource deployment

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

1. Follow [Terraform Plugin Framework](https://developer.hashicorp.com/terraform/plugin/framework) best practices
2. Add unit tests for helper functions
3. Add acceptance tests for resources
4. Update documentation
5. Run `make lint` and `make test` before submitting

## License

[Your License Here]
