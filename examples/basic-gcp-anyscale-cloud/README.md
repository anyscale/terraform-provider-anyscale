# Basic GCP Anyscale Cloud Example

This example demonstrates how to create an Anyscale Cloud on Google Cloud Platform (GCP) using the VM compute stack.

## Prerequisites

- GCP account with appropriate permissions
- Anyscale account with API token
- Terraform >= 1.9
- GCP billing account ID
- GCP folder number for project creation

## Overview

This example creates:

1. **GCP Infrastructure** (via the Anyscale GCP Cloud Foundation Module):
   - GCP Project
   - VPC and Subnet
   - Cloud Storage bucket
   - Service Accounts (controlplane and dataplane)
   - Workload Identity Federation
   - Firewall policies

2. **Anyscale Cloud Resource**:
   - Registers the GCP infrastructure with Anyscale

## Usage

1. Copy the example `terraform.tfvars` and customize:

```hcl
# Required variables
gcp_region                   = "us-central1"
gcp_zone                     = "us-central1-a"
billing_account_id           = "012345-ABCDEF-GHIJKL"
root_folder_number           = "123456789012"
customer_ingress_cidr_ranges = "YOUR_IP/32"
anyscale_org_id              = "org_your_org_id"
cloud_name                   = "my-gcp-cloud"

# Optional variables
anyscale_deploy_env = "production"
common_prefix       = "anyscale-"
is_private_cloud    = false
auto_add_user       = false
```

2. Build the provider (from the repository root):

```bash
go build -o terraform-provider-anyscale
```

3. Apply the configuration (no `terraform init` needed with dev_overrides):

```bash
terraform plan
terraform apply
```

## Inputs

| Name | Description | Type | Required |
|------|-------------|------|----------|
| gcp_region | GCP region for resources | string | yes |
| gcp_zone | GCP zone for zonal resources | string | yes |
| billing_account_id | GCP billing account ID | string | yes |
| root_folder_number | GCP folder number for project | string | yes |
| customer_ingress_cidr_ranges | CIDR ranges allowed to access clusters (comma-separated) | string | yes |
| anyscale_org_id | Anyscale Organization ID | string | yes |
| cloud_name | Name for the Anyscale cloud | string | yes |
| cloud_provider | Cloud provider (default: GCP) | string | no |
| compute_stack | Compute stack type (default: VM) | string | no |
| is_private_cloud | Whether this is a private cloud | bool | no |
| auto_add_user | Auto-add users to cloud | bool | no |

## Outputs

| Name | Description |
|------|-------------|
| cloud_id | The ID of the created Anyscale cloud |
| cloud_name | The name of the created Anyscale cloud |
| cloud_state | Current state of the Anyscale cloud |
| cloud_status | Current status of the Anyscale cloud |
| anyscale_register_command | CLI command to register the cloud |

## Module Outputs Mapping

The GCP Cloud Foundation Module outputs are mapped to the Anyscale Cloud resource as follows:

| Module Output | Terraform Field |
|---------------|-----------------|
| `project_id` | `gcp_config.project_id` |
| `iam_workload_identity_provider_name` | `gcp_config.provider_name` |
| `vpc_name` | `gcp_config.vpc_name` |
| `public_subnet_name` | `gcp_config.subnet_names[0]` |
| `iam_anyscale_access_service_acct_email` | `gcp_config.controlplane_service_account_email` |
| `iam_anyscale_cluster_node_service_acct_email` | `gcp_config.dataplane_service_account_email` |
| `vpc_firewall_policy_name` | `gcp_config.firewall_policy_names[0]` |
| `cloudstorage_bucket_name` | `object_storage.bucket_name` |

## Cleanup

To destroy all resources:

```bash
terraform destroy
```
