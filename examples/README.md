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

AWS VM cloud using the multi-resource cloud pattern. Demonstrates creating an empty cloud first, then adding a resource deployment separately.

**Use this when**: You want to use the multi-resource cloud pattern where the cloud and resource deployment are managed separately.

**What it demonstrates**:
- Creating an empty `anyscale_cloud` resource
- Adding a resource deployment via `anyscale_cloud_resource`
- Multi-resource cloud pattern workflow

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

GCP GKE (Kubernetes) cloud example, using the multi-resource cloud pattern. Creates an empty Anyscale
Cloud, then attaches a GCP GKE compute stack via a separate `anyscale_cloud_resource`.

**Use this when**: You want to register a GCP GKE cluster with Anyscale, managing the cloud and its
resource deployment separately.

**What it demonstrates**:
- Creating an empty `anyscale_cloud` resource
- Attaching a K8S `anyscale_cloud_resource` with an embedded `kubernetes_config`
- Object storage configuration

#### [azure-aks-basic](./azure-aks-basic/)

Azure AKS (Kubernetes) cloud example, using the all-in-one deployment pattern (the same pattern
`aws-eks-basic` uses). Creates a resource group, network, AKS cluster, ADLS Gen2 storage account,
and the Anyscale Operator's managed identity, then registers the result in one `anyscale_cloud`
resource.

**Use this when**: You want Terraform to stand up a new AKS cluster and register it with Anyscale
in one apply.

**What it demonstrates**:
- Creating an AKS cluster and its supporting Azure infrastructure via modules
- Creating an `anyscale_cloud` resource with K8S compute stack and an embedded `kubernetes_config`
- ADLS Gen2 object storage configuration

> [!NOTE]
> Schema- and `terraform validate`-checked, but not yet applied against a real AKS cluster (no
> Azure subscription in this provider's test environment) — see the
> [example README](./azure-aks-basic/README.md) before relying on it.

### Advanced Examples

#### [kitchen-sink](./kitchen-sink/)

Comprehensive, multi-cloud build exercising every resource and data source this provider
registers — including the multiple-resources-on-one-cloud and mixed VM+K8s coverage that used to
live in a separate `multi-resource-cloud-basic` example (now superseded and folded in here). Two
Anyscale Clouds share one AWS VPC and one EKS cluster, built fresh via modules: Cloud A is a
BYOC/multi-resource cloud carrying both a VM `anyscale_cloud_resource` and a K8S (EKS) `anyscale_cloud_resource`;
Cloud B is a simpler all-in-one VM cloud. Compute configs (including one that targets the EKS
deployment specifically via `cloud_resource`), container images, and two projects sit on top, and
all 13 registered data sources — `anyscale_cloud`, `anyscale_clouds`, `anyscale_compute_config`,
`anyscale_container_image`, `anyscale_container_images`, `anyscale_project`, `anyscale_projects`,
`anyscale_user`, `anyscale_organization`, `anyscale_organization_user`, `anyscale_organization_users`,
`anyscale_services`, `anyscale_service` — read back what those resources just created.

> [!WARNING]
> Building fresh via modules means this apply creates a real VPC and a real EKS cluster, not just
> Anyscale-side resources — expect real AWS cost and a noticeably longer apply than the other
> examples in this directory. See the [kitchen-sink README](./kitchen-sink/README.md) before
> running it.

**Use this when**: You want to see the whole provider surface working together, mixing a VM and a
K8s compute stack on one cloud, or a create-then-read-back pattern for a resource/data-source pair
you haven't used yet.

**What it demonstrates**:
- Every resource and data source type in one coherent, applyable configuration
- Multiple `anyscale_cloud_resource` deployments on a single BYOC cloud, mixing VM and K8S compute
  stacks side by side — see the [Cloud Resources guide](../docs/guides/cloud-resources.md#multiple-resource-deployments-on-one-cloud)
  for the cardinality rules this relies on
- `anyscale_compute_config`'s `cloud_resource` attribute targeting a specific deployment within a
  multi-resource cloud, instead of always landing on the primary
- The attribute-reference dependency ordering a data source needs to safely read back a resource
  created earlier in the same apply, instead of 404ing on a first apply
- Why `anyscale_organization_collaborator` (import-only) and `anyscale_service` (no matching
  resource — services aren't Terraform-created) are called out explicitly rather than silently
  applied, and why the invitation email and existing-service lookup are both opt-in variables
  rather than forced on every apply

See the [kitchen-sink README](./kitchen-sink/README.md) for the full breakdown, including real
AWS cost, apply time, and what it creates in your account and org before you apply it.

### Container Images

#### [container-image-compute-config](./container-image-compute-config/)

Builds a container image and defines a compute config side by side, then surfaces the
`name_version` handles you'd hand to a job or service submission command.

**Use this when**: You want to see how a container image (built from a Containerfile, or
registered from an existing registry) and a compute config fit together for running Ray
workloads — including why they're independent resources rather than one referencing the other.

**What it demonstrates**:
- Creating an `anyscale_container_image_build` resource (with a commented-out
  `anyscale_container_image_registry` alternative)
- Creating an `anyscale_compute_config` resource
- Using `name_version` as the identifier for job/service submission, instead of `image_uri` or `id`

See the [Container Images guide](../docs/guides/container-images.md) for the full explanation.

### Data Sources

#### [data-sources](./data-sources/)

Examples demonstrating how to use Anyscale data sources to look up existing resources.

**Use this when**: You need to reference existing Anyscale resources (clouds, compute configs, projects) in your Terraform configuration.

**What it demonstrates**:
- Using `anyscale_cloud` and `anyscale_clouds` data sources
- Using `anyscale_compute_config` data source
- Using `anyscale_container_image` and `anyscale_container_images` data sources
- Using `anyscale_project` and `anyscale_projects` data sources
- Using `anyscale_organization`, `anyscale_organization_user`, and `anyscale_organization_users` data sources
- Using `anyscale_user` data source

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

#### [resources/anyscale_container_image_build](./resources/anyscale_container_image_build/)

Minimal example showing just the `anyscale_container_image_build` resource configuration.

**Use this when**: You want a simple, focused example of building a container image from a Containerfile.

#### [resources/anyscale_container_image_registry](./resources/anyscale_container_image_registry/)

Minimal example showing just the `anyscale_container_image_registry` resource configuration.

**Use this when**: You want a simple, focused example of registering an existing container image.

#### [resources/anyscale_project](./resources/anyscale_project/)

Minimal example showing just the `anyscale_project` resource configuration.

**Use this when**: You want a simple, focused example of the `anyscale_project` resource.

#### [resources/anyscale_organization_collaborator](./resources/anyscale_organization_collaborator/)

Minimal example showing the `anyscale_organization_collaborator` resource — import-only, since
collaborators can't be created directly through the API.

**Use this when**: You want to manage an existing org member's role through Terraform.

#### [resources/anyscale_organization_invitation](./resources/anyscale_organization_invitation/)

Minimal example showing just the `anyscale_organization_invitation` resource configuration.

**Use this when**: You want to invite a new member to your Anyscale organization via Terraform.

#### [resources/organization_user_workflow](./resources/organization_user_workflow/)

A walkthrough (not a single-shot apply) chaining invite → wait for acceptance → import → manage,
since `anyscale_organization_collaborator` only supports import and the wait between invite and
acceptance happens outside Terraform entirely.

**Use this when**: You want to see how the invitation and collaborator resources fit together
across the full member-onboarding lifecycle, not just in isolation.

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
| `aws-vm-basic` | AWS | VM | All-in-one | Creates via modules |
| `aws-vm` | AWS | VM | All-in-one | Creates via modules |
| `aws-vm-basic-resource` | AWS | VM | Multi-Resource | Creates via modules |
| `gcp-vm-basic` | GCP | VM | All-in-one | Creates via modules |
| `gcp-vm` | GCP | VM | All-in-one | Creates via modules |
| `aws-eks-basic` | AWS | K8S | All-in-one | Creates via modules |
| `gcp-gke-basic` | GCP | K8S | Multi-Resource | Creates via modules |
| `azure-aks-basic` | Azure | K8S | All-in-one | Creates via modules |
| `kitchen-sink` | AWS | Mixed (VM + K8S) | Mixed (Multi-Resource + All-in-one) | Creates via modules |

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
