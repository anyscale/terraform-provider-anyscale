---
page_title: "Create a Kubernetes Cloud"
subcategory: "Getting Started"
description: |-
  A first-time walkthrough of registering an AWS EKS cloud with Anyscale, including the Anyscale Operator and Gateway install, building up to the runnable aws-eks-basic example.
---

# Create a Kubernetes cloud

This walks through registering a new AWS **Kubernetes (EKS) cloud** with Anyscale: provisioning an
EKS cluster, registering it as an `anyscale_cloud`, then installing the Anyscale Operator so the
cloud can actually run workloads. If you haven't read [Create a VM
cloud](./create-a-vm-cloud.md) yet, start there first - a Kubernetes cloud is a meaningfully bigger
lift than a VM cloud, and this guide assumes you're already comfortable with the basic
provider/`anyscale_cloud` shape from that one.

**This is more involved than the VM walkthrough, on purpose - set expectations up front:**

- It's still the **all-in-one pattern** (one `anyscale_cloud` resource, `compute_stack = "K8S"`),
  but a K8S cloud needs a real, running Kubernetes control plane to point at, and a real workload
  - the [Anyscale Operator](https://docs.anyscale.com/clouds/kubernetes/gateway-envoy) - actually
  running inside that cluster before the cloud can do anything.
- Getting the Operator installed and reachable takes **two separate `terraform apply` runs**, not
  one. This isn't a workaround for a bug in this provider - it's a real, documented limitation of
  how Terraform's Kubernetes provider resolves custom resources (explained in Step 4 below).
- The full apply takes 15-25 minutes and creates on the order of 100 AWS resources (an EKS cluster
  and its node groups are not fast or small).

Everything here is also available as a complete, runnable configuration at
[`examples/aws-eks-basic`](https://github.com/anyscale/terraform-provider-anyscale/tree/main/examples/aws-eks-basic) -
its [README](https://github.com/anyscale/terraform-provider-anyscale/blob/main/examples/aws-eks-basic/README.md)
has the exact command sequence, the full variable and output reference, and a troubleshooting
section for the landmines called out below. Treat this guide as the orientation tour and that
README as the manual you keep open while running it.

## What you'll need

- Terraform >= 1.10
- The AWS CLI, installed and authenticated - not just AWS credentials in the usual Terraform
  sense. This example's `kubernetes`/`helm` provider blocks fetch a short-lived cluster auth token
  by shelling out to `aws eks get-token` at apply time (an "exec plugin"), rather than minting one
  token up front, because a single apply here runs long enough to outlive a token grabbed early
- AWS credentials with permission to create VPCs, EKS clusters, IAM roles, and S3 buckets
- Anyscale credentials (`ANYSCALE_CLI_TOKEN` or `~/.anyscale/credentials.json`)
- `helm` installed locally, to run a required one-time chart download (Step 4 explains why)

Unlike the VM cloud guide, you do **not** need an Anyscale org ID or an external ID here - those
exist to build an AWS IAM trust policy for VM clouds specifically. A K8S cloud authenticates
through the Anyscale Operator's own IAM identity instead (Step 3 below).

## Step 1: Configure the providers

A Kubernetes cloud needs two more providers than a VM cloud: `kubernetes` and `helm`, to install
the Operator and its Gateway into the cluster you're about to create.

```terraform
terraform {
  required_providers {
    anyscale = {
      source = "anyscale/anyscale"
    }
    aws = {
      source  = "hashicorp/aws"
      version = ">= 6.52.0, < 7.0.0"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.17"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.35"
    }
  }
}

provider "aws" {
  region = "us-east-2"
}

provider "anyscale" {}
```

The `kubernetes` and `helm` provider blocks themselves need to authenticate against a cluster that
doesn't exist until partway through this same apply - see the full example's `versions.tf` for the
exec-plugin idiom that makes that work, rather than repeating the wiring here.

## Step 2: Provision the EKS cluster and supporting AWS infrastructure

Same shape as the VM cloud guide's module usage, scaled up: a VPC via Anyscale's [cloud foundation
modules](https://github.com/anyscale/terraform-aws-anyscale-cloudfoundation-modules), an S3 bucket
for object storage, and an EKS cluster via
[`terraform-aws-modules/eks`](https://registry.terraform.io/modules/terraform-aws-modules/eks/aws/latest).
The full example's `aws_eks.tf` configures node groups for cluster components, general CPU
workloads, and (optionally) GPU workloads - there's real depth there (Bottlerocket node images,
`before_compute` addon ordering, per-GPU-type node group generation) that's worth reading in the
full README rather than summarizing again here.

## Step 3: Give the Operator its own IAM identity

This is the first genuinely K8S-specific gotcha, and it's worth understanding rather than just
copying: the Anyscale Operator runs as a **pod**, not an EC2 instance, so it can't reuse your node
group's IAM role - that role's trust policy only allows `ec2.amazonaws.com` to assume it (instance
profile assumption), not `pods.eks.amazonaws.com`. Without a role that trusts the latter, the
Operator pod fails to start with an IMDS/credentials error that has nothing obviously to do with
IAM trust policies.

The fix is [EKS Pod
Identity](https://docs.aws.amazon.com/eks/latest/userguide/pod-identities.html): a dedicated role,
trusted by `pods.eks.amazonaws.com`, associated with the Operator's specific namespace and service
account:

```terraform
resource "aws_iam_role" "anyscale_operator" {
  name = "anyscale-operator"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "pods.eks.amazonaws.com" }
      Action    = ["sts:AssumeRole", "sts:TagSession"]
    }]
  })
}

resource "aws_eks_pod_identity_association" "anyscale_operator" {
  cluster_name    = module.eks.cluster_name
  namespace       = "anyscale-operator"
  service_account = "anyscale-operator"
  role_arn        = aws_iam_role.anyscale_operator.arn

  depends_on = [module.eks] # requires the eks-pod-identity-agent addon
}
```

That role also needs permission to read/write the object storage bucket the cloud is about to
register (the Operator checks it as part of its own startup verification) - see
`anyscale_operator_iam.tf` in the full example for the exact policy.

## Step 4: Register the cloud

```terraform
resource "anyscale_cloud" "primary" {
  name           = "my-first-k8s-cloud"
  cloud_provider = "AWS"
  region         = "us-east-2"
  compute_stack  = "K8S"

  kubernetes_config {
    anyscale_operator_iam_identity = aws_iam_role.anyscale_operator.arn
    zones                          = module.anyscale_vpc.availability_zones
  }

  object_storage {
    bucket_name = module.anyscale_s3.s3_bucket_id
    region      = "us-east-2"
  }
}
```

`anyscale_operator_iam_identity` points at the role from Step 3, **not** the node group's role -
that's the whole point of giving the Operator its own identity. This resource is Computed with a
`cloud_resource_id` attribute once it applies successfully; that's the real, populated ID you pass
to the Operator during installation, in the next step.

## Step 5: Install the Gateway and the Anyscale Operator

The cloud you just registered doesn't do anything until the Anyscale Operator is actually running
in the cluster and reachable from outside it. This is where the two-apply requirement comes in:

**Why two applies, specifically:** installing the Operator's ingress path means creating Gateway
API objects (a `Gateway`, a `GatewayClass`) that depend on [Envoy
Gateway](https://gateway.envoyproxy.io/)'s CRDs already being registered in the cluster. Terraform
resolves a `kubernetes_manifest` resource's schema against the **live cluster, at plan time** - not
apply time - so a single `terraform apply` can't both install those CRDs (via `helm_release`) and
plan a `kubernetes_manifest` that depends on them; the CRDs simply aren't there yet when that
plan step runs. This is a documented limitation of the Kubernetes provider itself, not a bug in
this design.

The practical split, gated by a boolean variable:

- **First apply** (`install_gateway_resources = false`, the default): creates the EKS cluster,
  registers the Anyscale cloud, and installs Envoy Gateway's CRDs and controller via `helm_release`
  - which has no plan-time dependency on the CRDs it's about to create.
- **Second apply** (`install_gateway_resources = true`): now that the CRDs exist, creates the
  `Gateway`/`GatewayClass` objects and installs the Anyscale Operator itself via `helm_release`,
  pointing it at the cloud you registered in Step 4:

  ```terraform
  resource "helm_release" "anyscale_operator" {
    name             = "anyscale-operator"
    repository       = "https://anyscale.github.io/helm-charts"
    chart            = "anyscale-operator"
    namespace        = "anyscale-operator"
    create_namespace = false

    values = [
      yamlencode({
        global = {
          # The helm key is literally "cloudDeploymentId" but the value it wants
          # is the cloud RESOURCE id from Step 4.
          cloudDeploymentId = anyscale_cloud.primary.cloud_resource_id
          cloudProvider     = "aws"
          aws               = { region = "us-east-2" }
        }
        networking = {
          gateway = {
            enabled   = true
            name      = "anyscale-gateway"
            namespace = "anyscale-operator"
            hostname  = "<the Gateway's load balancer address>"
          }
        }
      })
    ]
  }
  ```

The real example resolves that `hostname` value automatically (reading the Gateway's address back
via a `kubernetes_resource` data source once it reaches `Programmed`), and handles one more
non-obvious detail: the Operator's helm values want `cloud_resource_id` in its raw underscored
form, but the TLS certificate it creates uses the same ID with underscores swapped for dashes. Both
forms matter in different places - see `gateway_operator.tf` and the full README's troubleshooting
section rather than re-deriving this by hand.

**One more one-time prerequisite before either apply**: at the time of writing, the Terraform Helm
provider can't pull the Envoy Gateway chart directly from its OCI registry (a real upstream
provider limitation, not a config issue), so you pull it once yourself:

```bash
helm pull oci://docker.io/envoyproxy/gateway-helm --version 1.8.2 -d examples/aws-eks-basic/.charts
```

Skipping this fails the first apply immediately with a clear message telling you to run that
command, rather than a confusing partial failure.

## Running it

```bash
cd examples/aws-eks-basic
helm pull oci://docker.io/envoyproxy/gateway-helm --version 1.8.2 -d .charts
terraform init

# First apply: cluster + cloud + Gateway CRDs/controller.
terraform apply

# Second apply, once the first has finished: Gateway objects + Operator.
terraform apply -var install_gateway_resources=true
```

## Cleaning up

```bash
terraform destroy
```

## See also

- The full [`examples/aws-eks-basic`](https://github.com/anyscale/terraform-provider-anyscale/tree/main/examples/aws-eks-basic)
  README covers everything this guide intentionally skipped: the complete variable/output
  reference, Bottlerocket node group details, GPU node group configuration, and a troubleshooting
  section for each landmine mentioned above.
- [Cloud Resources guide](./cloud-resources.md) - provider support matrix and known limitations
  across every cloud shape this provider supports, not just this one.
- [Create a VM cloud](./create-a-vm-cloud.md) - the simpler sibling to this guide, if you landed
  here first.
- [Kitchen Sink tour](./kitchen-sink-tour.md) - a tour of every resource and data source this
  provider registers, including a second cloud resource sharing this same EKS cluster's VPC.
- [Anyscale's own Gateway (Envoy) setup documentation](https://docs.anyscale.com/clouds/kubernetes/gateway-envoy) -
  the manual/CLI equivalent of what Step 5 automates.
