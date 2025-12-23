# Anyscale Cloud - AWS Common Name Example

This example demonstrates how to use the Anyscale Terraform Provider to create AWS infrastructure and register it as an Anyscale Cloud in a single Terraform configuration.

## What This Example Does

1. **Creates AWS Infrastructure** using the official [Anyscale Cloud Foundation Modules](https://registry.terraform.io/modules/anyscale/anyscale-cloudfoundation-modules/aws/latest):
   - VPC with public subnets
   - Security groups
   - IAM roles (cross-account and cluster node roles)
   - S3 bucket for Anyscale storage

2. **Registers the Cloud with Anyscale** using the `anyscale_cloud` resource:
   - Automatically uses the outputs from the AWS module
   - Creates the cloud in Anyscale's control plane
   - Waits for the cloud to be ready

## Prerequisites

- Terraform >= 1.9
- AWS credentials configured
- Anyscale authentication via one of:
  - Setting `anyscale_token` in `terraform.tfvars` (recommended for automation)
  - `ANYSCALE_CLI_TOKEN` environment variable
  - `~/.anyscale/credentials.json` file

## Usage

1. **Create a `terraform.tfvars` file** with your values:

   Copy the example file and customize it:
   ```bash
   cp terraform.tfvars.example terraform.tfvars
   # Edit terraform.tfvars with your values
   ```

   Or create it manually:

```hcl
# Required variables
aws_region                     = "us-east-2"
customer_ingress_cidr_ranges   = "0.0.0.0/0"  # Restrict this in production
anyscale_external_id           = "my-external-id-12345"
anyscale_org_id                = "org_abc123xyz"

# Cloud configuration
cloud_name                     = "my-terraform-cloud"

# Anyscale authentication (optional - can also use ANYSCALE_CLI_TOKEN env var or credentials file)
anyscale_token                 = "your-anyscale-token-here"

# Optional variables
anyscale_deploy_env            = "production"
common_prefix                  = "my-company-"
anyscale_s3_force_destroy      = true  # Set to false in production
```

2. **Initialize Terraform**:

```bash
terraform init
```

3. **Review the plan**:

```bash
terraform plan
```

4. **Apply the configuration**:

```bash
terraform apply
```

The apply will:
- Create all AWS resources (VPC, subnets, security groups, IAM roles, S3 bucket)
- Register the cloud with Anyscale
- Wait for the cloud to be ready (may take several minutes)

5. **View outputs**:

```bash
terraform output
```

You'll see:
- `cloud_id` - The Anyscale cloud ID (e.g., `cld_abc123`)
- `cloud_name` - The name of your cloud
- `cloud_status` - Current status (should be `ready`)
- `cloud_state` - Current state (should be `ACTIVE`)
- `anyscale_register_command` - The CLI equivalent command (for reference)

## Variables

### Required Variables

| Name | Description | Type |
|------|-------------|------|
| `aws_region` | AWS region to deploy resources | string |
| `customer_ingress_cidr_ranges` | CIDR ranges allowed to access clusters | string |
| `anyscale_external_id` | External ID for IAM trust policy | string |
| `anyscale_org_id` | Your Anyscale organization ID | string |
| `cloud_name` | Name for the Anyscale cloud | string |

### Optional Variables

| Name | Description | Type | Default |
|------|-------------|------|---------|
| `anyscale_token` | Anyscale API token (can also use env var or credentials file) | string | `null` |
| `anyscale_deploy_env` | Deployment environment | string | `"production"` |
| `cloud_provider` | Cloud provider | string | `"AWS"` |
| `compute_stack` | Compute stack type | string | `"VM"` |
| `common_prefix` | Prefix for resource names | string | `"anyscale-pfx-test-"` |
| `is_private_cloud` | Whether this is a private cloud | bool | `false` |
| `anyscale_s3_force_destroy` | Force destroy S3 bucket on delete | bool | `false` |

## Outputs

| Name | Description |
|------|-------------|
| `cloud_id` | The unique ID of the Anyscale cloud |
| `cloud_name` | The name of the cloud |
| `cloud_status` | Cloud status (e.g., `ready`, `pending`) |
| `cloud_state` | Cloud state (e.g., `ACTIVE`, `CREATING`) |
| `anyscale_register_command` | CLI command equivalent (for reference) |

## Architecture

This example creates a public networking setup with:
- 1 VPC with 3 public subnets across availability zones
- Security groups configured for cluster access
- Cross-account IAM role for Anyscale control plane
- Cluster node IAM role for worker nodes
- S3 bucket for Anyscale storage

## Cleaning Up

To destroy all resources:

```bash
terraform destroy
```

This will:
1. Delete the Anyscale cloud registration
2. Remove all AWS resources (VPC, subnets, security groups, IAM roles, S3 bucket)

**Note**: If `anyscale_s3_force_destroy` is `false`, you may need to manually empty the S3 bucket before destroying.

## Comparison to CLI

This Terraform configuration replaces the need for the manual `anyscale cloud register` command. The outputs include the equivalent CLI command for reference.

**Before (CLI)**:
```bash
# First create AWS resources manually or with separate Terraform
# Then register with CLI
anyscale cloud register --provider aws \
  --name my-cloud \
  --region us-east-2 \
  --vpc-id vpc-xxx \
  --subnet-ids subnet-a,subnet-b,subnet-c \
  --security-group-ids sg-xxx \
  --s3-bucket-id my-bucket \
  --anyscale-iam-role-id arn:aws:iam::xxx:role/role1 \
  --instance-iam-role-id arn:aws:iam::xxx:role/role2 \
  --external-id external-id
```

**After (Terraform)**:
```hcl
module "aws_anyscale_v2_common_name" { ... }

resource "anyscale_cloud" "example" {
  # All configuration in one place
  # Automatic dependency management
  # Infrastructure as code
}
```

## Troubleshooting

### Cloud stuck in "CREATING" state

The provider waits up to 10 minutes for the cloud to become ready. If it takes longer:
- Check the Anyscale console for error messages
- Verify IAM roles have correct trust policies
- Ensure security groups allow required traffic

### Authentication errors

Make sure you have valid credentials:
```bash
# Check environment variable
echo $ANYSCALE_CLI_TOKEN

# Or check credentials file
cat ~/.anyscale/credentials.json
```

### Provider not found

For local development, you may need to build and install the provider:
```bash
cd ../..
go build -o terraform-provider-anyscale
mkdir -p ~/.terraform.d/plugins/github.com/brent/anyscale/1.0.0/darwin_arm64/
cp terraform-provider-anyscale ~/.terraform.d/plugins/github.com/brent/anyscale/1.0.0/darwin_arm64/
```

(Adjust the path based on your OS and architecture)
