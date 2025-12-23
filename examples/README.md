# Terraform Provider for Anyscale - Examples

This directory contains example configurations demonstrating how to use the Anyscale Terraform Provider.

## Available Examples

### [basic-anyscale-cloud](./basic-anyscale-cloud/)

A minimal example showing how to register an existing AWS infrastructure as an Anyscale Cloud.

**Use this when**: You already have AWS resources (VPC, subnets, security groups, IAM roles, S3 bucket) and just want to register them with Anyscale.

**What it does**:
- Registers an Anyscale Cloud using existing AWS resources
- No AWS infrastructure creation

**Time to complete**: ~5-10 minutes

### [basic-commonname](./basic-commonname/)

A complete end-to-end example that creates AWS infrastructure and registers it as an Anyscale Cloud.

**Use this when**: You want to create everything from scratch in a single Terraform configuration.

**What it does**:
- Creates VPC with public subnets
- Creates security groups
- Creates IAM roles (cross-account and cluster node)
- Creates S3 bucket
- Registers the cloud with Anyscale

**Time to complete**: ~10-15 minutes

## Getting Started

1. **Choose an example** based on your needs (see above)

2. **Navigate to the example directory**:
   ```bash
   cd basic-commonname/  # or basic-anyscale-cloud/
   ```

3. **Read the example's README**:
   Each example has detailed instructions in its own README.md

4. **Set up authentication**:

   Either set the environment variable:
   ```bash
   export ANYSCALE_CLI_TOKEN="your-token-here"
   ```

   Or ensure you have `~/.anyscale/credentials.json` with:
   ```json
   {
     "cli_token": "your-token-here"
   }
   ```

5. **Configure your variables**:
   Create a `terraform.tfvars` file with your values (see example README for required variables)

6. **Run Terraform**:
   ```bash
   terraform init
   terraform plan
   terraform apply
   ```

## Example Comparison

| Feature | basic-anyscale-cloud | basic-commonname |
|---------|---------------------|------------------|
| Creates AWS VPC | ❌ No | ✅ Yes |
| Creates AWS Subnets | ❌ No | ✅ Yes |
| Creates Security Groups | ❌ No | ✅ Yes |
| Creates IAM Roles | ❌ No | ✅ Yes |
| Creates S3 Bucket | ❌ No | ✅ Yes |
| Registers Anyscale Cloud | ✅ Yes | ✅ Yes |
| Best for | Existing infrastructure | New deployments |
| Complexity | Simple | Moderate |

## Prerequisites

All examples require:

- **Terraform**: >= 1.9
- **AWS Credentials**: Configured for Terraform
- **Anyscale Authentication**: Via `ANYSCALE_CLI_TOKEN` env var or credentials file
- **Anyscale Organization ID**: Required for IAM trust policies
- **Anyscale External ID**: Required for IAM trust policies

## Common Variables

Most examples use these common variables:

| Variable | Description | Example |
|----------|-------------|---------|
| `aws_region` | AWS region | `"us-east-2"` |
| `cloud_name` | Name for the cloud | `"my-terraform-cloud"` |
| `anyscale_org_id` | Your Anyscale org ID | `"org_abc123xyz"` |
| `anyscale_external_id` | External ID for IAM | `"my-external-id-12345"` |

## Support

For issues or questions:
- Check the example's README for troubleshooting tips
- Refer to the [main provider README](../README.md)
- Review the [Anyscale documentation](https://docs.anyscale.com/)

## Contributing

To add a new example:

1. Create a new subdirectory under `examples/`
2. Include at minimum:
   - `main.tf` - Main configuration
   - `variables.tf` - Variable definitions
   - `outputs.tf` - Output definitions
   - `versions.tf` - Provider requirements
   - `README.md` - Usage instructions
3. Follow the naming convention: `basic-*` for simple examples, `advanced-*` for complex ones
