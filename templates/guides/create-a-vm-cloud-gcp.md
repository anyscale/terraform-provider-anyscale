---
page_title: "Create a GCP VM Cloud"
subcategory: "Getting Started"
description: |-
  A first-time walkthrough of registering a GCP VM cloud with Anyscale from scratch, including provisioning a brand-new GCP project, building up to the runnable gcp-vm-basic example.
---

# Create a GCP VM cloud

This walks through registering a new GCP **VM cloud** with Anyscale, end to end: providing the GCP
infrastructure Anyscale needs - including a brand-new GCP project - then creating the
[`anyscale_cloud`](../resources/cloud.md) resource that ties it together. By the end you'll have a
cloud you can launch Ray clusters against, and you'll understand what each block in the
configuration is for rather than just having copy-pasted it.

This is the **all-in-one pattern**: one `anyscale_cloud` resource with an embedded `gcp_config`
block, the simplest of the cloud shapes this provider supports - the same shape the [Create a VM
Cloud](./create-a-vm-cloud.md) guide walks through for AWS. If you've already read that one, the
overall structure here will feel familiar; what's different is GCP-specific: a brand-new project
created from scratch, and workload identity federation in place of cross-account IAM role
assumption. It's also VM, not Kubernetes - Anyscale schedules Ray clusters directly onto Compute
Engine instances it manages, rather than onto a Kubernetes cluster you provide. See the [Cloud
Resources guide](./cloud-resources.md) for how the other shapes (Kubernetes, multi-resource) differ
and when you'd reach for them instead.

Everything here is also available as a complete, runnable configuration at
[`examples/gcp-vm-basic`](https://github.com/anyscale/terraform-provider-anyscale/tree/main/examples/gcp-vm-basic) -
copy that directory if you just want the working example; keep reading if you want to understand
it first.

## What you'll need

- Terraform >= 1.9
- GCP credentials with permission to create a new project under the folder you specify, associate
  it with the billing account you specify, and create VPCs, service accounts, and a GCS bucket
  within it once it exists (e.g. `gcloud auth application-default login`) - this guide provisions
  all of it fresh, it doesn't attach to a GCP project you already have
- A GCP billing account ID and the folder number of the GCP folder the new project should be
  created under - real, pre-existing GCP identifiers you look up yourself (for example, `gcloud
  billing accounts list` and `gcloud resource-manager folders list`), not something you invent the
  way you would an AWS external ID. Step 2 explains why this guide needs both.
- An Anyscale API token - set `ANYSCALE_CLI_TOKEN`, or run `anyscale login` to populate
  `~/.anyscale/credentials.json`, either of which the provider picks up with no configuration. See
  the [Anyscale API keys documentation](https://docs.anyscale.com/auth/api-keys) for how to
  generate one.
- Your Anyscale organization ID (starts with `org_`). If you don't have it handy, look it up with a
  zero-argument data source instead of hunting for it:

  ```terraform
  data "anyscale_organization" "current" {}

  output "my_org_id" {
    value = data.anyscale_organization.current.id
  }
  ```

## Step 1: Configure the providers

```terraform
terraform {
  required_providers {
    anyscale = {
      source = "anyscale/anyscale"
    }
    google = {
      source  = "hashicorp/google"
      version = "~> 5.0"
    }
  }
}

provider "google" {
  region = "us-central1"
}

provider "anyscale" {}
```

The `anyscale` provider block can be empty, for the same reason as the AWS guide: with no `token`
argument, it falls back to `ANYSCALE_CLI_TOKEN` and then `~/.anyscale/credentials.json`, in that
order. The `google` provider block is minimal for a different reason - it takes a default `region`
but no `project`, because there isn't a project yet for it to default to. The project this scenario
runs against doesn't exist until Step 2 creates it; every downstream resource that needs to
reference a project gets it explicitly from that module's own `project_id` output, not from the
provider's implicit default. Authentication for the `google` provider itself comes from Application
Default Credentials (`gcloud auth application-default login`) rather than anything in this
configuration.

## Step 2: Provision the GCP infrastructure

Anyscale doesn't require you to hand-build a project, VPC, and service accounts yourself - Anyscale
publishes a [Terraform module](https://github.com/anyscale/terraform-google-anyscale-cloudfoundation-modules)
that creates exactly what a VM cloud needs, including the GCP project itself:

```terraform
module "google_anyscale_v2" {
  source = "anyscale/anyscale-cloudfoundation-modules/google"

  anyscale_organization_id = var.anyscale_org_id

  anyscale_project_billing_account = var.billing_account_id
  anyscale_project_folder_id       = var.root_folder_number

  anyscale_vpc_public_subnet_cidr               = "10.100.0.0/16"
  anyscale_vpc_firewall_allow_access_from_cidrs = var.customer_ingress_cidr_ranges

  # This example skips Filestore and Memorystore - the module can create those too, but a basic
  # VM cloud doesn't need either.
  enable_anyscale_filestore   = false
  enable_anyscale_memorystore = false
}
```

A few things worth knowing before you run this:

- `billing_account_id` and `root_folder_number` have no default and can't be skipped, because this
  module creates a **brand-new GCP project** rather than attaching to one you already have - the
  new project is created under `root_folder_number` and associated with `billing_account_id` in the
  same step. This is the biggest structural difference from the AWS guide's module, which only ever
  creates a VPC inside an AWS account that already exists.
- `anyscale_vpc_firewall_allow_access_from_cidrs` (fed from `customer_ingress_cidr_ranges`) takes a
  single CIDR block, a string, not a list - the same shape as the AWS guide's equivalent variable,
  despite the plural-sounding name.
- There's no `anyscale_external_id` to make up here, unlike the AWS guide. GCP's workload identity
  federation setup (wired up in Step 3) doesn't need the cross-account IAM trust relationship that
  concept exists for on AWS - a workload identity pool provider establishes trust directly, without
  either side holding a long-lived, exportable service-account key. See [Google's own Workload
  Identity Federation documentation](https://cloud.google.com/iam/docs/workload-identity-federation)
  for the general mechanism this module sets up for you.
- The module creates **two** service accounts: one Anyscale's control plane assumes via workload
  identity federation to manage clusters, and a separate one attached to the actual Ray cluster
  nodes. Both feed into the `anyscale_cloud` resource in the next step - the same two-identity split
  as the AWS guide's two IAM roles.

## Step 3: Register the cloud

```terraform
resource "anyscale_cloud" "primary" {
  name           = "my-first-gcp-vm-cloud"
  cloud_provider = "GCP"
  region         = "us-central1"
  compute_stack  = "VM"

  is_private_cloud = false
  auto_add_user    = false

  gcp_config {
    project_id    = module.google_anyscale_v2.project_id
    provider_name = module.google_anyscale_v2.iam_workload_identity_provider_name
    vpc_name      = module.google_anyscale_v2.vpc_name
    subnet_names  = [module.google_anyscale_v2.public_subnet_name]

    controlplane_service_account_email = module.google_anyscale_v2.iam_anyscale_access_service_acct_email
    dataplane_service_account_email    = module.google_anyscale_v2.iam_anyscale_cluster_node_service_acct_email

    firewall_policy_names = [module.google_anyscale_v2.vpc_firewall_policy_name]
  }

  object_storage {
    bucket_name = module.google_anyscale_v2.cloudstorage_bucket_name
    region      = "us-central1"
  }
}
```

This is the resource that actually calls the Anyscale API. Everything in `gcp_config` and
`object_storage` is a direct reference to an output from the module in Step 2 - by this point
you're just wiring the GCP infrastructure you already provisioned into Anyscale's view of it, not
creating anything new on the GCP side.

`is_private_cloud` and `auto_add_user` both default to `false` on their own - they're `Optional`
with a `Computed` default - so setting them explicitly here doesn't change the outcome; they're
shown because the full example wires them from variables. `subnet_names` and `firewall_policy_names`
are both lists even though this scenario only ever produces one of each - the module's public
subnet and firewall policy outputs are single values wrapped in a one-element list here. VM compute
genuinely supports spreading across more than one subnet if you configure more than one; this
scenario just doesn't need to.

## Step 4: Apply

```bash
terraform init
terraform plan
terraform apply
```

`terraform plan` should show the module's GCP resources - including the new project itself - plus
the `anyscale_cloud` resource, nothing else. If `apply` fails partway through with a permission or
billing error, double-check that the credentials Terraform is running as have **both**
project-creation permission on `root_folder_number` **and** the right to associate a new project
with `billing_account_id` - these are two separate IAM grants on GCP, and either alone will fail
this apply partway through.

Once `apply` succeeds, your cloud is ready to launch Ray clusters against.

## Next: add a compute config and a project

A cloud on its own doesn't define what a cluster launched against it looks like - that's what
[`anyscale_compute_config`](../resources/compute_config.md) is for:

```terraform
resource "anyscale_compute_config" "default" {
  name     = "my-first-compute-config"
  cloud_id = anyscale_cloud.primary.id

  head_node = {
    instance_type = "n2-standard-8"
  }

  worker_nodes = [
    {
      instance_type = "n2-standard-16"
      min_nodes     = 0
      max_nodes     = 3
    }
  ]
}
```

See the [Compute Config guide](./compute-config.md) for the versioning model and the less obvious
attributes (advanced instance configs, per-node flags). And a cloud needs at least one
[`anyscale_project`](../resources/project.md) for anything to actually run in:

```terraform
resource "anyscale_project" "default" {
  name     = "my-first-project"
  cloud_id = anyscale_cloud.primary.id
}
```

Once you're sharing a project with a team rather than working solo, see the [Project
guide](./project.md) for the collaborator access model and permission levels.

## Cleaning up

```bash
terraform destroy
```

This tears down the `anyscale_cloud` resource - deregistering the cloud from Anyscale - and every
GCP resource the module created: the VPC, the GCS bucket, both service accounts, and the project
itself. That last one is worth pausing on: this scenario creates a brand-new GCP project rather
than attaching to one you already had, so destroying it destroys that whole project, not just a
bucket inside one you'll keep using either way.

Google Cloud doesn't purge a deleted project instantly - it enters a 30-day pending-deletion state
first, recoverable with `gcloud projects undelete <project-id>` until that window closes (see
[Google's project deletion and restoration documentation](https://cloud.google.com/resource-manager/docs/delete-restore-projects)).
Don't treat that window as a safety net for everything, though: some services, Cloud Storage
included, can purge their contents in as little as 7 days, well before the project itself is gone.
There's also no equivalent to the AWS guide's `anyscale_s3_force_destroy` flag here - this example
doesn't expose one for the GCS bucket - so if you left it non-empty (for example, by running a
workload against this cloud before tearing it down), `terraform destroy` can fail on that specific
resource; empty the bucket yourself first rather than expecting a force-destroy escape hatch.

## See also

- [Create a VM cloud](./create-a-vm-cloud.md) - the AWS equivalent of this same walkthrough, the
  same all-in-one pattern applied to a different cloud provider
- [`examples/gcp-vm-basic`](https://github.com/anyscale/terraform-provider-anyscale/tree/main/examples/gcp-vm-basic) -
  the complete, runnable version of this walkthrough, including an additional compute config
  variation
- [Cloud Resources guide](./cloud-resources.md) - provider support matrix, naming differences
  between resources and data sources, and known limitations
- [Compute Config guide](./compute-config.md) - versioning model and write-only fields
- [Project guide](./project.md) - collaborator access model and permission levels
