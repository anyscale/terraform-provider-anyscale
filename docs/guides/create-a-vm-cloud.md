---
page_title: "Create a VM Cloud"
subcategory: "Getting Started"
description: |-
  A first-time walkthrough of registering an AWS VM cloud with Anyscale from scratch, building up to the runnable aws-vm-basic example.
---

# Create a VM cloud

This walks through registering a new AWS **VM cloud** with Anyscale, end to end: providing the AWS
infrastructure Anyscale needs, then creating the [`anyscale_cloud`](../resources/cloud.md) resource
that ties it together. By the end you'll have a cloud you can launch Ray clusters against, and
you'll understand what each block in the configuration is for rather than just having copy-pasted
it.

This is the **all-in-one pattern**: one `anyscale_cloud` resource with an embedded `aws_config`
block, the simplest of the cloud shapes this provider supports. It's also VM, not Kubernetes -
Anyscale schedules Ray clusters directly onto EC2 instances it manages, rather than onto a
Kubernetes cluster you provide. See the [Cloud Resources
guide](./cloud-resources.md) for how the other shapes (Kubernetes, multi-resource) differ and when
you'd reach for them instead.

Everything here is also available as a complete, runnable configuration at
[`examples/aws-vm-basic`](https://github.com/anyscale/terraform-provider-anyscale/tree/main/examples/aws-vm-basic) -
copy that directory if you just want the working example; keep reading if you want to understand
it first.

## What you'll need

- Terraform >= 1.9
- AWS credentials with permission to create VPCs, IAM roles, and S3 buckets (this guide provisions
  all three - it doesn't attach to infrastructure you already have)
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
    aws = {
      source  = "hashicorp/aws"
      version = ">= 6.37.0, < 7.0.0"
    }
  }
}

provider "aws" {
  region = "us-west-2"
}

provider "anyscale" {}
```

The `anyscale` provider block can be empty: with no `token` argument, it falls back to
`ANYSCALE_CLI_TOKEN` and then `~/.anyscale/credentials.json`, in that order. There's no need to put
a token in configuration at all unless you specifically want per-resource token overrides.

## Step 2: Provision the AWS infrastructure

Anyscale doesn't require you to hand-build a VPC, IAM roles, and a bucket yourself - Anyscale
publishes a [Terraform module](https://github.com/anyscale/terraform-aws-anyscale-cloudfoundation-modules)
that creates exactly what a VM cloud needs:

```terraform
module "aws_anyscale_v2" {
  source = "anyscale/anyscale-cloudfoundation-modules/aws"

  anyscale_org_id      = var.anyscale_org_id
  anyscale_external_id = var.anyscale_external_id

  anyscale_vpc_cidr_block     = "172.24.0.0/16"
  anyscale_vpc_public_subnets = ["172.24.21.0/24", "172.24.22.0/24", "172.24.23.0/24"]

  security_group_ingress_allow_access_from_cidr_range = var.customer_ingress_cidr_ranges

  # This example skips EFS and MemoryDB - the module can create those too, but a basic
  # VM cloud doesn't need either.
  create_efs_resources = false
}
```

A few things worth knowing before you run this:

- `anyscale_external_id` is not issued by Anyscale - it's a string you make up, used as the
  external ID on the IAM trust policy this module creates. Anyscale's control plane presents this
  same value back when it assumes the role, which is what prevents the
  [confused deputy problem](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_create_for-user_externalid.html)
  for cross-account role assumption. Pick any value and reuse it consistently for this cloud; it
  doesn't need to be a secret.
- `security_group_ingress_allow_access_from_cidr_range` takes a single CIDR block (a string), not a
  list - if you need more than one range, look at the module's own variables for the multi-range
  equivalent rather than assuming you can pass a list here.
- The module creates **two** IAM roles: one your Anyscale org's control plane assumes cross-account
  to manage clusters, and a separate one attached to the actual Ray cluster nodes. Both feed into
  the `anyscale_cloud` resource in the next step.

## Step 3: Register the cloud

```terraform
resource "anyscale_cloud" "primary" {
  name           = "my-first-vm-cloud"
  cloud_provider = "AWS"
  region         = "us-west-2"
  compute_stack  = "VM"

  aws_config {
    vpc_id           = module.aws_anyscale_v2.anyscale_vpc_id
    subnet_ids_to_az = module.aws_anyscale_v2.anyscale_vpc_public_subnet_ids_az_map

    security_group_ids = [module.aws_anyscale_v2.anyscale_security_group_id]

    controlplane_iam_role_arn = module.aws_anyscale_v2.anyscale_iam_role_arn
    dataplane_iam_role_arn    = module.aws_anyscale_v2.anyscale_iam_role_cluster_node_arn
    external_id               = module.aws_anyscale_v2.anyscale_iam_role_external_id
  }

  object_storage {
    bucket_name = module.aws_anyscale_v2.anyscale_s3_bucket_id
    region      = "us-west-2"
  }
}
```

This is the resource that actually calls the Anyscale API. Everything in `aws_config` and
`object_storage` is a direct reference to an output from the module in Step 2 - by this point
you're just wiring the AWS infrastructure you already provisioned into Anyscale's view of it, not
creating anything new on the AWS side.

## Step 4: Apply

```bash
terraform init
terraform plan
terraform apply
```

`terraform plan` should show the module's AWS resources plus the `anyscale_cloud` resource, nothing
else. If `apply` fails validating an IAM trust relationship, double-check that
`anyscale_external_id` is the exact same string in both the module block and anything else that
references it - a mismatch here is the most common cause.

Once `apply` succeeds, your cloud is ready to launch Ray clusters against.

## Next: add a compute config and a project

A cloud on its own doesn't define what a cluster launched against it looks like - that's what
[`anyscale_compute_config`](../resources/compute_config.md) is for:

```terraform
resource "anyscale_compute_config" "default" {
  name     = "my-first-compute-config"
  cloud_id = anyscale_cloud.primary.id

  head_node = {
    instance_type = "m5.xlarge"
  }

  worker_nodes = [
    {
      instance_type = "m5.2xlarge"
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

This tears down the `anyscale_cloud` resource and every AWS resource the module created, including
the S3 bucket - the [full example](https://github.com/anyscale/terraform-provider-anyscale/tree/main/examples/aws-vm-basic)
sets `anyscale_s3_force_destroy = true` specifically so `destroy` doesn't get blocked by a
non-empty bucket during iteration. Leave that unset (or `false`) once you're managing a cloud you
intend to keep, so an accidental `destroy` can't silently drop stored data.

## See also

- [`examples/aws-vm-basic`](https://github.com/anyscale/terraform-provider-anyscale/tree/main/examples/aws-vm-basic) -
  the complete, runnable version of this walkthrough, including additional compute config and
  project variations
- [Create a Kubernetes cloud](./create-a-kubernetes-cloud.md) - the Kubernetes/EKS equivalent of
  this same walkthrough, a bigger lift since it also installs the Anyscale Operator
- [Kitchen Sink tour](./kitchen-sink-tour.md) - once the basics feel comfortable, a tour of every
  resource and data source this provider registers, composed together
- [Cloud Resources guide](./cloud-resources.md) - provider support matrix, naming differences
  between resources and data sources, and known limitations
- [Compute Config guide](./compute-config.md) - versioning model and write-only fields
- [`anyscale_cloud` resource reference](../resources/cloud.md)
