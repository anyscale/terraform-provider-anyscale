# Terraform Provider for Anyscale - Examples

This directory contains example configurations demonstrating how to use the Anyscale Terraform Provider.

## Available Examples

### Cloud Resources

#### [aws-vm-basic](./aws-vm-basic/)

Basic AWS VM cloud example using the all-in-one deployment pattern. Creates an Anyscale Cloud with AWS VM compute stack using existing AWS infrastructure.

**Use this when**: You want to register an AWS VM cloud with Anyscale using existing VPC, subnets, security groups, and IAM roles.

**What it demonstrates**:
- Creating an `anyscale_cloud` resource with AWS VM configuration
- Using existing AWS infrastructure (VPC, subnets, security groups, IAM roles)
- Object storage configuration
- Compute config creation

#### [aws-vm](./aws-vm/)

Full AWS VM example with AWS Cloud Foundation modules. Creates both AWS infrastructure and Anyscale Cloud resources.

**Use this when**: You want a complete end-to-end example that creates AWS infrastructure and registers it with Anyscale.

**What it demonstrates**:
- Creating AWS infrastructure using Anyscale Cloud Foundation modules
- Registering the cloud with Anyscale
- Full integration between AWS and Anyscale resources

#### [aws-vm-basic-resource](./aws-vm-basic-resource/)

AWS VM cloud using the split deployment pattern. Demonstrates creating an empty cloud first, then adding a resource deployment separately.

**Use this when**: You want to use the split deployment pattern where the cloud and resource deployment are managed separately.

**What it demonstrates**:
- Creating an empty `anyscale_cloud` resource
- Adding a resource deployment via `anyscale_cloud_resource`
- Split deployment pattern workflow

#### [gcp-vm-basic](./gcp-vm-basic/)

Basic GCP VM cloud example. Creates an Anyscale Cloud with GCP VM compute stack.

**Use this when**: You want to register a GCP VM cloud with Anyscale using existing GCP infrastructure.

**What it demonstrates**:
- Creating an `anyscale_cloud` resource with GCP VM configuration
- GCP-specific configuration (project, VPC, subnets, service accounts)
- Object storage configuration with GCS
- Compute config creation

#### [gcp-vm](./gcp-vm/)

Full GCP VM example with GCP Cloud Foundation modules. Creates both GCP infrastructure and Anyscale Cloud resources.

**Use this when**: You want a complete end-to-end example that creates GCP infrastructure and registers it with Anyscale.

**What it demonstrates**:
- Creating GCP infrastructure using Anyscale Cloud Foundation modules
- Registering the cloud with Anyscale
- Full integration between GCP and Anyscale resources

### Kubernetes Examples

#### [aws-eks-basic](./aws-eks-basic/)

AWS EKS (Kubernetes) cloud example, using the all-in-one deployment pattern. Creates a brand new
VPC, S3 bucket, IAM roles, and EKS cluster, then registers the result with Anyscale as a K8S
cloud in a single `anyscale_cloud` resource. Despite the "basic" name, this creates
infrastructure via modules rather than assuming you already have a cluster to point at — see
the [example README](./aws-eks-basic/README.md) for the full breakdown.

**Use this when**: You want Terraform to stand up a new EKS cluster and register it with
Anyscale in one apply. If you already have a cluster and want to register it without creating a
second one, this isn't that example yet.

**What it demonstrates**:
- Creating a VPC, S3 bucket, IAM roles, and an EKS cluster (`terraform-aws-modules/eks` v21) via modules
- Creating an `anyscale_cloud` resource with K8S compute stack and an embedded `kubernetes_config`
- Object storage configuration

#### [gcp-gke-basic](./gcp-gke-basic/)

GCP GKE (Kubernetes) cloud example, using the split deployment pattern. Creates an empty Anyscale
Cloud, then attaches a GCP GKE compute stack via a separate `anyscale_cloud_resource`.

**Use this when**: You want to register a GCP GKE cluster with Anyscale, managing the cloud and its
resource deployment separately.

**What it demonstrates**:
- Creating an empty `anyscale_cloud` resource
- Attaching a K8S `anyscale_cloud_resource` with an embedded `kubernetes_config`
- Object storage configuration

### Advanced Examples

#### [multi-resource-cloud-basic](./multi-resource-cloud-basic/)

Example demonstrating multiple resource deployments in a single cloud. Attaches two separate AWS VM
foundations (distinct VPCs and `common_prefix`es, same region) to one Anyscale Cloud as two
`anyscale_cloud_resource` blocks.

**Use this when**: You need multiple resource deployments (e.g., multiple regions or compute stacks) in a single Anyscale Cloud.

**What it demonstrates**:
- Creating a cloud with multiple resource deployments
- Managing multiple `anyscale_cloud_resource` resources, each with its own explicit, distinct `name`
- Multi-resource cardinality rules — see the [Cloud Resources guide](../docs/guides/cloud-resources.md#multiple-resource-deployments-on-one-cloud)

### Data Sources

#### [data-sources](./data-sources/)

Examples demonstrating how to use Anyscale data sources to look up existing resources.

**Use this when**: You need to reference existing Anyscale resources (clouds, compute configs, projects) in your Terraform configuration.

**What it demonstrates**:
- Using `anyscale_cloud` data source
- Using `anyscale_clouds` data source
- Using `anyscale_compute_config` data source
- Using `anyscale_project` and `anyscale_projects` data sources

See the [data-sources README](./data-sources/README.md) for detailed documentation.

### Resource Examples

#### [resources/anyscale_cloud](./resources/anyscale_cloud/)

Minimal example showing just the `anyscale_cloud` resource configuration.

**Use this when**: You want a simple, focused example of the `anyscale_cloud` resource without additional infrastructure.

#### [resources/anyscale_cloud_resource](./resources/anyscale_cloud_resource/)

Minimal example showing just the `anyscale_cloud_resource` resource configuration.

**Use this when**: You want a simple, focused example of the `anyscale_cloud_resource` resource.

#### [resources/anyscale_compute_config](./resources/anyscale_compute_config/)

Minimal example showing just the `anyscale_compute_config` resource configuration.

**Use this when**: You want a simple, focused example of the `anyscale_compute_config` resource.

### Provider Configuration

#### [provider](./provider/)

Example showing basic provider configuration.

**Use this when**: You want to see how to configure the Anyscale provider.

## Getting Started

1. **Choose an example** based on your needs (see above)

2. **Navigate to the example directory**:
   ```bash
   cd aws-vm-basic/  # or your chosen example
   ```

3. **Set up authentication**:

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

   Or use:
   ```json
   {
     "token": "your-token-here"
   }
   ```

4. **Configure your variables**:
   Create a `terraform.tfvars` file with your values (see example directory for required variables)

5. **Run Terraform**:
   ```bash
   terraform init
   terraform plan
   terraform apply
   ```

## Prerequisites

All examples require:

- **Terraform**: >= 1.9
- **Cloud Provider Credentials**: Configured for Terraform (AWS, GCP, etc.)
- **Anyscale Authentication**: Via `ANYSCALE_CLI_TOKEN` env var or credentials file
- **Anyscale Organization ID**: Required for IAM trust policies (AWS/GCP examples)
- **Anyscale External ID**: Required for IAM trust policies (AWS/GCP examples)

## Example Comparison

| Example | Cloud Provider | Compute Stack | Pattern | Infrastructure Creation |
|---------|---------------|---------------|---------|------------------------|
| `aws-vm-basic` | AWS | VM | All-in-one | Uses existing |
| `aws-vm` | AWS | VM | All-in-one | Creates via modules |
| `aws-vm-basic-resource` | AWS | VM | Split | Uses existing |
| `gcp-vm-basic` | GCP | VM | All-in-one | Uses existing |
| `gcp-vm` | GCP | VM | All-in-one | Creates via modules |
| `aws-eks-basic` | AWS | K8S | All-in-one | Creates via modules |
| `gcp-gke-basic` | GCP | K8S | Split | Uses existing GKE |
| `multi-resource-cloud-basic` | AWS | VM | Split | Uses existing |

## Common Variables

Most examples use these common variables:

| Variable | Description | Example |
|----------|-------------|---------|
| `cloud_name` | Name for the Anyscale Cloud | `"my-terraform-cloud"` |
| `anyscale_org_id` | Your Anyscale org ID | `"org_abc123xyz"` |
| `anyscale_external_id` | External ID for IAM | `"my-external-id-12345"` |
| `region` | Cloud provider region | `"us-east-2"` (AWS) or `"us-central1"` (GCP) |

## Support

For issues or questions:
- Check the example's directory for specific README files
- Refer to the [main provider README](../README.md)
- Review the [Anyscale documentation](https://docs.anyscale.com/)

## Contributing

To add a new example:

1. Create a new subdirectory under `examples/`
2. Include at minimum:
   - `main.tf` or resource-specific `.tf` files - Main configuration
   - `variables.tf` - Variable definitions
   - `outputs.tf` - Output definitions
   - `versions.tf` - Provider requirements
   - `README.md` - Usage instructions (recommended)
3. Follow the naming convention: `*-basic` for simple examples, descriptive names for specific use cases
