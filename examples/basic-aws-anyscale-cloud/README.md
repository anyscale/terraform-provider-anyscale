# Basic Anyscale Cloud Example

This is a minimal example showing how to register an Anyscale Cloud when you already have the AWS infrastructure in place.

## What This Example Does

Registers an existing AWS infrastructure as an Anyscale Cloud using the `anyscale_cloud` resource.

**Note**: This example assumes you already have:
- VPC with subnets
- Security groups
- IAM roles configured
- S3 bucket

If you need to create the AWS infrastructure from scratch, see the [basic-commonname](../basic-commonname/) example instead.

## Prerequisites

- Terraform >= 1.9
- Existing AWS infrastructure (VPC, subnets, security groups, IAM roles, S3 bucket)
- Anyscale authentication via one of:
  - `ANYSCALE_CLI_TOKEN` environment variable
  - `~/.anyscale/credentials.json` file

## Usage

1. **Update the `main.tf` file** with your existing AWS resource IDs:

```hcl
resource "anyscale_cloud" "example" {
  name            = "my-terraform-cloud"
  provider        = "AWS"
  region          = "us-east-2"
  compute_stack   = "VM"
  networking_mode = "PUBLIC"

  deployment_name = "vm-aws-us-east-2"

  aws_config {
    vpc_id                = "vpc-0343edeee0eab27c3"  # Your VPC ID
    subnet_ids            = [
      "subnet-086ac7bba68e3c1c3",  # Your subnet IDs
      "subnet-08a309019a027ec72",
      "subnet-06a825a292bd4d476",
      "subnet-084f2adab2e2aff10"
    ]
    security_group_ids    = ["sg-064dac0ed5cffc779"]  # Your security group ID
    s3_bucket_id          = "my-anyscale-bucket"  # Your S3 bucket
    anyscale_iam_role_id  = "arn:aws:iam::367974485317:role/anyscale-crossacct-role"  # Your IAM role
    instance_iam_role_id  = "arn:aws:iam::367974485317:role/anyscale-cluster-node-role"  # Your cluster role
    external_id           = "org_abc123-external-id"  # Your external ID
  }
}
```

2. **Initialize Terraform**:

```bash
terraform init
```

3. **Apply the configuration**:

```bash
terraform apply
```

4. **View the cloud ID**:

```bash
terraform output cloud_id
```

## What Gets Created

- **Anyscale Cloud Registration**: Creates the cloud in Anyscale's control plane
- **No AWS Resources**: This example doesn't create any AWS resources (assumes they already exist)

## Cleaning Up

To unregister the cloud:

```bash
terraform destroy
```

This will only remove the Anyscale cloud registration. It will **not** delete any AWS resources.

## Next Steps

- See the [basic-commonname](../basic-commonname/) example for a complete end-to-end setup that creates both AWS infrastructure and registers the cloud
- Refer to the [main README](../../README.md) for full provider documentation
