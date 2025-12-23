# Terraform Provider for Anyscale

A Terraform provider for managing Anyscale resources via the Anyscale API.

## Features

- Create, read, update, and delete Anyscale Clouds
- Support for AWS VM-based deployments
- Automatic authentication via environment variable or credentials file

## Authentication

The provider supports two methods of authentication:

1. **Environment Variable**: Set `ANYSCALE_CLI_TOKEN` with your API token
2. **Credentials File**: Store your token in `~/.anyscale/credentials.json`

The provider checks the environment variable first, then falls back to the credentials file.

### Credentials File Format

The credentials file supports two formats:

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

```bash
go mod download
go build -o terraform-provider-anyscale
```

## Usage

### Provider Configuration

```hcl
terraform {
  required_providers {
    anyscale = {
      source = "github.com/brent/anyscale"
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

### Resource: anyscale_cloud

Creates and manages an Anyscale Cloud.

#### Example: AWS Cloud with VM Compute Stack

```hcl
resource "anyscale_cloud" "example" {
  name            = "my-terraform-cloud"
  cloud_provider  = "AWS"
  region          = "us-east-2"
  compute_stack   = "VM"
  networking_mode = "PUBLIC"

  deployment_name = "vm-aws-us-east-2"

  aws_config {
    vpc_id                = "vpc-0343edeee0eab27c3"
    subnet_ids            = [
      "subnet-086ac7bba68e3c1c3",
      "subnet-08a309019a027ec72",
      "subnet-06a825a292bd4d476",
      "subnet-084f2adab2e2aff10"
    ]
    security_group_ids    = ["sg-064dac0ed5cffc779"]
    s3_bucket_id          = "my-anyscale-bucket"
    anyscale_iam_role_id  = "arn:aws:iam::367974485317:role/anyscale-crossacct-role"
    instance_iam_role_id  = "arn:aws:iam::367974485317:role/anyscale-cluster-node-role"
    external_id           = "org_abc123-external-id"
  }
}

output "cloud_id" {
  value = anyscale_cloud.example.cloud_id
}

output "cloud_status" {
  value = anyscale_cloud.example.status
}
```

#### Argument Reference

The following arguments are supported:

- `name` - (Required) The name of the cloud.
- `cloud_provider` - (Required) Cloud provider. Must be `AWS`, `GCP`, or `AZURE`.
- `region` - (Required) The region where the cloud is deployed.
- `compute_stack` - (Optional) Compute stack type. Must be `VM` or `K8S`. Defaults to `VM`.
- `networking_mode` - (Optional) Networking mode. Must be `PUBLIC` or `PRIVATE`. Defaults to `PUBLIC`.
- `deployment_name` - (Optional) Name for the cloud deployment resource. Auto-generated if not provided.
- `aws_config` - (Optional) AWS-specific configuration block.

##### AWS Config Block

The `aws_config` block supports:

- `vpc_id` - (Required) VPC ID.
- `subnet_ids` - (Required) List of subnet IDs.
- `security_group_ids` - (Required) List of security group IDs.
- `s3_bucket_id` - (Required) S3 bucket name (with or without `s3://` prefix).
- `anyscale_iam_role_id` - (Required) Anyscale cross-account IAM role ARN.
- `instance_iam_role_id` - (Required) Instance/cluster IAM role ARN.
- `external_id` - (Optional) External ID for IAM role assumption.

#### Attribute Reference

In addition to all arguments above, the following attributes are exported:

- `cloud_id` - The unique identifier for the cloud.
- `status` - Status of the cloud (e.g., `ready`, `pending`, `failed`).
- `state` - State of the cloud (e.g., `ACTIVE`, `CREATING`, `FAILED`).

## API Operations

The provider implements the following Anyscale API operations:

1. **Create Cloud**: `POST /api/v2/clouds/` - Creates a placeholder cloud
2. **Add Cloud Resource**: `PUT /api/v2/clouds/{cloud_id}/add_resource` - Adds deployment configuration
3. **Read Cloud**: `GET /api/v2/clouds/{cloud_id}` - Retrieves cloud details
4. **Delete Cloud**: `DELETE /api/v2/clouds/{cloud_id}` - Deletes a cloud

## Development

This provider is built using the Terraform Plugin SDK v2.

### Building

```bash
go build -o terraform-provider-anyscale
```

### Testing

```bash
go test ./...
```

## Project Structure

- `main.go` - Entry point for the provider
- `provider.go` - Provider schema and configuration
- `client.go` - Anyscale API client with authentication logic
- `models.go` - API request/response models
- `resource_cloud.go` - Cloud resource implementation
- `examples/` - Example Terraform configurations

## Comparison with CLI

This provider mirrors the functionality of the `anyscale cloud register` CLI command:

```bash
# CLI command
anyscale cloud register --provider aws \
  --name my-cloud \
  --region us-east-2 \
  --vpc-id vpc-xxx \
  --subnet-ids subnet-a,subnet-b \
  --security-group-ids sg-xxx \
  --s3-bucket-id my-bucket \
  --anyscale-iam-role-id arn:aws:iam::xxx:role/anyscale-role \
  --instance-iam-role-id arn:aws:iam::xxx:role/cluster-role \
  --external-id external-id-value
```

Becomes:

```hcl
resource "anyscale_cloud" "my_cloud" {
  name           = "my-cloud"
  cloud_provider = "AWS"
  region         = "us-east-2"

  aws_config {
    vpc_id                = "vpc-xxx"
    subnet_ids            = ["subnet-a", "subnet-b"]
    security_group_ids    = ["sg-xxx"]
    s3_bucket_id          = "my-bucket"
    anyscale_iam_role_id  = "arn:aws:iam::xxx:role/anyscale-role"
    instance_iam_role_id  = "arn:aws:iam::xxx:role/cluster-role"
    external_id           = "external-id-value"
  }
}
```
