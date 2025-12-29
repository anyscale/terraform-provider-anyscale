# Terraform Provider for Anyscale

A Terraform provider for managing Anyscale resources via the Anyscale API v2, built with the [Terraform Plugin Framework](https://developer.hashicorp.com/terraform/plugin/framework).

## Features

- **Full Anyscale Platform Support**:
  - Cloud infrastructure management (AWS, GCP, Azure VM and Kubernetes stacks)
  - Compute configurations (cluster templates)
  - Cloud resource deployment (split deployment pattern)

- **Native HCL Syntax**: No more `jsonencode()` for complex fields like `flags` and `advanced_configurations_json`

- **Automatic Detection**: Cloud provider and region auto-detected from configuration blocks

- **Flexible Authentication**: Environment variable, credentials file, or provider configuration

- **Production-Ready**: Comprehensive test coverage with unit and acceptance tests

## Framework Migration

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

Creates and manages an Anyscale Cloud with all-in-one or empty cloud patterns.

#### Example: AWS VM Cloud (All-in-One Pattern)

```hcl
resource "anyscale_cloud" "aws_example" {
  name           = "my-terraform-cloud"
  cloud_provider = "AWS"
  region         = "us-east-2"
  compute_stack  = "VM"

  # Cloud-level settings
  auto_add_user           = false
  enable_lineage_tracking = false
  enable_log_ingestion    = false
  is_private_cloud        = false

  # AWS-specific configuration
  aws_config {
    vpc_id                    = "vpc-0343edeee0eab27c3"
    subnet_ids_to_az = {
      "subnet-086ac7bba68e3c1c3" = "us-east-2a"
      "subnet-08a309019a027ec72" = "us-east-2b"
      "subnet-06a825a292bd4d476" = "us-east-2c"
    }
    security_group_ids       = ["sg-064dac0ed5cffc779"]
    controlplane_iam_role_arn = "arn:aws:iam::367974485317:role/anyscale-crossacct-role"
    dataplane_iam_role_arn    = "arn:aws:iam::367974485317:role/anyscale-cluster-node-role"
    external_id               = "org_abc123-external-id"
  }

  # Object storage configuration
  object_storage {
    bucket_name = "my-anyscale-bucket"  # s3:// prefix added automatically
    region      = "us-east-2"
  }
}

output "cloud_id" {
  value = anyscale_cloud.aws_example.id
}
```

#### Example: GCP VM Cloud

```hcl
resource "anyscale_cloud" "gcp_example" {
  name           = "my-gcp-cloud"
  cloud_provider = "GCP"
  region         = "us-central1"
  compute_stack  = "VM"

  gcp_config {
    project_id                  = "my-project-123"
    provider_name               = "projects/123/locations/global/workloadIdentityPools/anyscale/providers/anyscale"
    vpc_name                    = "anyscale-vpc"
    subnet_names                = ["anyscale-subnet-us-central1"]
    anyscale_service_account_email = "anyscale@my-project.iam.gserviceaccount.com"
    cluster_service_account_email  = "cluster@my-project.iam.gserviceaccount.com"
  }

  object_storage {
    bucket_name = "my-gcs-bucket"  # gs:// prefix added automatically
  }
}
```

#### Example: AWS EKS (Kubernetes)

```hcl
resource "anyscale_cloud" "eks_example" {
  name           = "my-eks-cloud"
  cloud_provider = "AWS"
  region         = "us-west-2"
  compute_stack  = "K8S"

  kubernetes_config {
    anyscale_operator_iam_identity = "arn:aws:iam::367974485317:role/anyscale-eks-operator-role"
    zones                          = ["us-west-2a", "us-west-2b"]
  }

  object_storage {
    bucket_name = "my-eks-bucket"
    region      = "us-west-2"
  }
}
```

#### Example: Empty Cloud (Split Deployment Pattern)

```hcl
# Step 1: Create empty cloud
resource "anyscale_cloud" "empty" {
  name           = "my-empty-cloud"
  cloud_provider = "AWS"
  region         = "us-east-2"

  # No config blocks = empty cloud
  # Resources added via anyscale_cloud_resource
}

# Step 2: Add resource deployment
resource "anyscale_cloud_resource" "deployment" {
  cloud_id      = anyscale_cloud.empty.id
  resource_name = "vm-aws-us-east-2"
  compute_stack = "VM"

  aws_config {
    # ... same as anyscale_cloud aws_config
  }

  object_storage {
    # ... same as anyscale_cloud object_storage
  }
}
```

### anyscale_cloud_resource

Manages cloud resource deployments separately from the cloud itself (split pattern).

#### Example: AWS VM Resource

```hcl
resource "anyscale_cloud_resource" "aws_vm" {
  cloud_id      = "cld_abc123"
  resource_name = "vm-aws-us-east-2"
  compute_stack = "VM"

  aws_config {
    vpc_id                    = "vpc-0343edeee0eab27c3"
    subnet_ids_to_az = {
      "subnet-086ac" = "us-east-2a"
      "subnet-08a30" = "us-east-2b"
    }
    security_group_ids       = ["sg-064dac0ed5cffc779"]
    controlplane_iam_role_arn = "arn:aws:iam::xxx:role/crossacct"
    dataplane_iam_role_arn    = "arn:aws:iam::xxx:role/cluster-node"
  }

  object_storage {
    bucket_name = "my-bucket"
  }
}
```

**Import**: Use composite ID format `cloud_id:resource_name`

```bash
terraform import anyscale_cloud_resource.aws_vm "cld_abc123:vm-aws-us-east-2"
```

### anyscale_compute_config

Creates cluster templates (compute configurations).

#### Example: Compute Config with Native HCL Flags

```hcl
resource "anyscale_compute_config" "example" {
  name        = "my-compute-config"
  cloud_id    = anyscale_cloud.aws_example.id

  # Native HCL syntax - no jsonencode needed!
  flags = {
    "ray-cluster-ray-version"           = "2.9.0"
    "ray-cluster-kubernetes-namespace"  = "anyscale"
  }

  # Native HCL for advanced configurations
  advanced_configurations_json = {
    ray_head_node = {
      instance_type = "m5.large"
      min_instances = 1
      max_instances = 1
    }
    ray_worker_nodes = [
      {
        instance_type = "m5.xlarge"
        min_instances = 0
        max_instances = 10
      }
    ]
  }
}
```

## API Operations

### Cloud Lifecycle

1. **Create Cloud**: `POST /api/v2/clouds` - Creates minimal cloud
2. **Add Resource** (optional): `PUT /api/v2/clouds/{id}/add_resource` - Deploys infrastructure
3. **Poll Ready**: Waits for cloud state=ACTIVE and status=ready (up to 30min)
4. **Read Cloud**: `GET /api/v2/clouds/{id}` - Retrieves cloud details
5. **Delete Cloud**: `DELETE /api/v2/clouds/{id}` - Deletes cloud

### Two-Phase Create Pattern

For all-in-one clouds, the provider automatically:
1. Creates a minimal cloud (Step 1)
2. Adds the resource deployment (Step 2)
3. Polls until ready (Step 3)
4. Reads final state

## Development

### Building

```bash
make build
```

### Testing

```bash
# Unit tests (88 tests)
make test

# Acceptance tests (requires AWS credentials)
make testacc

# Specific scenario test
make test-aws-vm-basic
```

### Project Structure

```
├── main.go                           # Provider entry point
├── internal/provider/
│   ├── provider.go                   # Provider schema and config
│   ├── client.go                     # Anyscale API client
│   ├── models.go                     # API models
│   ├── resource_cloud.go             # Cloud resource (1,275 lines)
│   ├── resource_cloud_resource.go    # Cloud resource deployment (1,432 lines)
│   ├── resource_compute_config.go    # Compute config resource
│   ├── resource_cloud_test.go        # Unit tests (43 tests)
│   ├── resource_cloud_resource_test.go # Unit tests (27 tests)
│   ├── resource_cloud_acc_test.go    # Acceptance tests
│   └── resource_cloud_resource_acc_test.go
├── examples/
│   ├── aws-vm-basic/                 # Basic AWS VM cloud
│   ├── aws-vm/                       # Full AWS VM with modules
│   ├── aws-eks-basic/                # AWS EKS (K8S) cloud
│   ├── gcp-vm-basic/                 # Basic GCP VM cloud
│   ├── gcp-gke-basic/                # GCP GKE (K8S) cloud
│   └── aws-vm-basic-resource/        # Split deployment example
└── docs/                             # Generated documentation
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

## Comparison with CLI

Provider mirrors `anyscale cloud register` CLI functionality:

```bash
# CLI command
anyscale cloud register --provider aws \
  --name my-cloud \
  --region us-east-2 \
  --vpc-id vpc-xxx \
  --subnet-ids subnet-a,subnet-b \
  --security-group-ids sg-xxx

# Equivalent Terraform
resource "anyscale_cloud" "my_cloud" {
  name           = "my-cloud"
  cloud_provider = "AWS"
  region         = "us-east-2"

  aws_config {
    vpc_id             = "vpc-xxx"
    subnet_ids_to_az   = {
      "subnet-a" = "us-east-2a"
      "subnet-b" = "us-east-2b"
    }
    security_group_ids = ["sg-xxx"]
    # ... other required fields
  }
}
```

## Testing Matrix

### Unit Tests (88 total)

- Cloud helper functions (43 tests)
- Cloud resource expand helpers (27 tests)
- Compute config helpers (18 tests)

### Acceptance Tests

- AWS VM basic (all-in-one)
- AWS VM empty cloud
- GCP VM basic
- AWS K8S basic
- Cloud resource AWS VM
- Cloud resource with file storage

### Integration Tests

- `make test-aws-vm-basic` - Full end-to-end AWS provisioning

## Contributing

1. Follow [Terraform Plugin Framework](https://developer.hashicorp.com/terraform/plugin/framework) best practices
2. Add unit tests for helper functions
3. Add acceptance tests for resources
4. Update documentation
5. Run `make lint` and `make test` before submitting

## License

[Your License Here]
