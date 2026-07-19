---
page_title: "Kitchen Sink: A Tour of the Full Provider Surface"
subcategory: "Getting Started"
description: |-
  A curated tour of the kitchen-sink example - every resource and data source this provider registers, wired together, with the non-obvious Terraform patterns it exists to demonstrate.
---

# Kitchen sink: a tour of the full provider surface

[`examples/kitchen-sink`](https://github.com/anyscale/terraform-provider-anyscale/tree/main/examples/kitchen-sink)
is every resource and data source this provider registers, wired together into one configuration.
It's not a getting-started example - if you haven't yet, read [Create a VM
cloud](./create-a-vm-cloud.md) or [Create a Kubernetes cloud](./create-a-kubernetes-cloud.md)
first. This guide is a tour of what kitchen-sink demonstrates and why, for when you're ready to see
how the pieces compose - not a step-by-step walkthrough of its own.

**Read this before applying it, not after:** unlike every `-basic` example, kitchen-sink builds a
real VPC, S3 bucket, and EKS cluster from scratch - the same shared infrastructure pattern as
[aws-eks-basic](./create-a-kubernetes-cloud.md), just with two Anyscale Clouds and three cloud
resource deployments layered on top of it. Expect a 15-20+ minute apply, real ongoing AWS spend for
as long as it stays up, and note that this example - unlike the `-basic` examples - has no
`make test-kitchen-sink` wrapper or automatic destroy-on-failure trap. You track and tear down your
own state here.

## The core pattern: one cloud, multiple resource deployments

Kitchen-sink's Cloud A is the BYOC/multi-resource pattern: an empty `anyscale_cloud` with two
separate `anyscale_cloud_resource` deployments attached to it - a VM leg and an EKS (K8S) leg, both
on the same shared VPC. This is the one way to mix compute stacks under a single cloud:

```terraform
resource "anyscale_cloud" "a" {
  name           = "${var.name_prefix}-cloud-a"
  cloud_provider = "AWS"
  region         = var.aws_region
  # compute_stack intentionally omitted - a multi-resource cloud derives its stack from
  # whichever resource(s) end up attached to it.
}

resource "anyscale_cloud_resource" "a_vm" {
  cloud_id      = anyscale_cloud.a.id
  name          = "${var.name_prefix}-cloud-a-vm"
  compute_stack = "VM"
  # ...aws_config, object_storage
}

resource "anyscale_cloud_resource" "a_eks" {
  depends_on    = [anyscale_cloud_resource.a_vm] # see below
  cloud_id      = anyscale_cloud.a.id
  name          = "${var.name_prefix}-cloud-a-eks"
  compute_stack = "K8S"
  # ...kubernetes_config, object_storage
}
```

Only a multi-resource cloud accepts a second `anyscale_cloud_resource` at all - an all-in-one cloud
(like the ones the VM/Kubernetes guides build) rejects one outright. The `depends_on` on `a_eks`
isn't decorative: which resource is "primary" for the cloud is assigned by the backend to whichever
one was created first, not a field you set, so this ordering is what keeps the VM leg primary. See
the [Cloud Resources guide](./cloud-resources.md#multiple-resource-deployments-on-one-cloud) for the
full cardinality rules this relies on.

## Targeting a specific deployment from compute config

With two resource deployments on one cloud, a compute config needs a way to say which one it means:

```terraform
resource "anyscale_compute_config" "cc_a_default" {
  cloud_id = anyscale_cloud.a.id
  # No cloud_resource set - resolves to Cloud A's primary (VM) deployment.

  head_node    = { instance_type = var.head_node_instance_type }
  worker_nodes = [{ instance_type = var.worker_instance_type, min_nodes = 0, max_nodes = 5 }]
}

resource "anyscale_compute_config" "cc_a_eks" {
  cloud_id       = anyscale_cloud.a.id
  cloud_resource = anyscale_cloud_resource.a_eks.name # targets the EKS leg specifically

  head_node    = { instance_type = var.head_node_instance_type }
  worker_nodes = [{ instance_type = var.worker_instance_type, min_nodes = 0, max_nodes = 5 }]
}
```

`cloud_resource` takes the deployment's *name*, and it's an attribute reference here
(`anyscale_cloud_resource.a_eks.name`), not a hardcoded string - which matters for the next section.

## The most important lesson: data source dependency edges

The example's own README calls this out as the one Terraform gotcha it exists to demonstrate, and
it's worth internalizing beyond just this example. Every data source lookup here references the
resource's own attribute:

```terraform
data "anyscale_cloud" "lookup_a" {
  name = anyscale_cloud.a.name # not a literal "kitchen-sink-cloud-a" string
}
```

Referencing `anyscale_cloud.a.name` (rather than repeating the same literal name in both blocks)
gives Terraform an explicit dependency edge: it defers reading the data source until after the
resource exists. Hardcode the identical string in both places instead, and there's no such edge -
Terraform is free to run the data source read *before* the resource is created on a first apply,
and the lookup 404s. Where there's no natural attribute to borrow (a list-lookup like
`anyscale_clouds`, filtering rather than looking up by name), the example adds an explicit
`depends_on` instead to get the same guarantee. This single pattern - prefer a real attribute
reference, fall back to `depends_on` only when there's nothing to reference - is the difference
between a config that works reliably on a first apply and one that's one lucky scheduling order
away from a 404.

## The rest of the tour

- **Container images** - `anyscale_container_image_build` (built from an inline Containerfile) and
  `anyscale_container_image_registry` (registered from a public registry), the two ways to get an
  image into Anyscale. See the [Container Images guide](./container-images.md).
- **Projects and org resources** - one project per cloud, plus
  `anyscale_organization_collaborator` shown (commented out, not applied - it's import-only, no
  `Create`, and manages an *existing* org member) alongside the invite/import lifecycle it needs;
  see [`organization_user_workflow`](https://github.com/anyscale/terraform-provider-anyscale/blob/main/examples/resources/organization_user_workflow/main.tf)
  for that full flow.
- **All 13 data sources** the provider registers, in one file - including the two zero-argument
  connection-level singletons, `anyscale_user` and `anyscale_organization`, which have no
  dependency on anything else in the config and are always safe to read.
- **Two resources are opt-in, off by default**: `anyscale_organization_invitation` (set
  `invite_email` to actually send one) and the singular `anyscale_service` data source lookup (set
  `existing_service_name` to a real service). A fresh `terraform apply` with neither set creates
  zero instances of either - safe by default, not a placeholder you need to delete.

## Known limitations worth knowing before you extend this

- A first apply can hit a transient `403` on Cloud A's project or default compute config - a
  backend permission-propagation lag on a freshly-created cloud, not a config problem. Re-applying
  with no changes resolves it.
- This proves attachment and configuration for the EKS leg, not a running workload - its node group
  is sized for cluster components, not Ray jobs.

See the full [README](https://github.com/anyscale/terraform-provider-anyscale/blob/main/examples/kitchen-sink/README.md)
for the complete list, including a backend `500` known issue on replacing the EKS resource.

## Running it

```bash
cd examples/kitchen-sink
cp terraform.tfvars.example terraform.tfvars
# edit terraform.tfvars with your own AWS account details
terraform plan
terraform apply
```

## See also

- The full [`examples/kitchen-sink`](https://github.com/anyscale/terraform-provider-anyscale/tree/main/examples/kitchen-sink)
  README - complete variable reference, every known limitation, and the opt-in resource details
  this tour condensed
- [Create a VM cloud](./create-a-vm-cloud.md) and [Create a Kubernetes cloud](./create-a-kubernetes-cloud.md) -
  read these first if you haven't
- [Cloud Resources guide](./cloud-resources.md) - the multi-resource cardinality rules Cloud A relies on
- [Compute Config guide](./compute-config.md) and [Container Images guide](./container-images.md)
