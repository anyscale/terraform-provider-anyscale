# AWS EKS Basic Example

Creates a new Amazon EKS cluster and registers it with Anyscale as a `K8S` cloud, all in a
single `terraform apply`. Unlike `aws-vm-basic`, this example does **not** assume you already
have infrastructure to point at — it provisions the VPC, S3 bucket, IAM roles, and the EKS
cluster itself, then wires the result into one `anyscale_cloud` resource with an embedded
`kubernetes_config`.

If you already have a running EKS cluster and just want to register it with Anyscale, this
example will create a second, redundant cluster rather than adopt yours. Neither Kubernetes
example in this repo supports that today — [gcp-gke-basic](../gcp-gke-basic/) also provisions a
brand new cluster via modules; it only differs from this example in using the multi-resource
cloud pattern instead of all-in-one. Open an issue if a bring-your-own-cluster
example would help.

## What this creates

- A VPC with public and private subnets, via Anyscale's [cloud foundation
  modules](https://github.com/anyscale/terraform-aws-anyscale-cloudfoundation-modules)
- An S3 bucket for Anyscale object storage, named from `eks_cluster_name`, `aws_region`, and your
  AWS account ID — S3 bucket names are globally unique across every AWS account, not just within
  yours, so the account ID is baked in to guarantee that by construction rather than relying on
  you (or a test harness) to pick a globally-unique `eks_cluster_name` yourself
- An optional EFS filesystem for shared node storage (`enable_efs`)
- IAM roles/policies for the cluster autoscaler and the AWS Load Balancer Controller
- An EKS cluster ([`terraform-aws-modules/eks`](https://registry.terraform.io/modules/terraform-aws-modules/eks/aws/latest) v21) with:
  - Four explicitly-managed cluster addons — `vpc-cni` and `eks-pod-identity-agent` (both
    `before_compute = true`, so they exist before any node joins), plus `coredns` and
    `kube-proxy`. This example must list `vpc-cni` itself: v21 hardcodes
    `bootstrap_self_managed_addons = false`, so EKS no longer installs it for you the way it did
    implicitly under v20. See "Upgrading from the v20 module" below if you're merging this from
    an older copy.
  - Five kinds of managed node groups, all running [Bottlerocket](https://bottlerocket.dev/)
    (`BOTTLEROCKET_x86_64` for CPU groups, `BOTTLEROCKET_x86_64_NVIDIA` for GPU groups — see
    "Bottlerocket node groups" below if you haven't used it before): `default` (small on-demand
    nodes for cluster components — CoreDNS, autoscaler, ingress, the Anyscale Operator),
    `ondemand_cpu` / `spot_cpu` (general-purpose CPU capacity for Ray workloads), and
    `ondemand_gpu_<type>` / `spot_gpu_<type>` (one pair of node groups per entry in
    `gpu_instance_types`, generated automatically)
- An `anyscale_cloud` resource with `compute_stack = "K8S"`, pointing its `kubernetes_config` at the EKS cluster's operator IAM identity and availability zones
- [Envoy Gateway](https://gateway.envoyproxy.io/) and the Anyscale Operator itself, installed via
  Terraform (`helm_release`/`kubernetes_manifest`) rather than left as a manual post-apply step —
  see "Running the example" below for the two-apply flow this requires and "Known limitation"
  for a required one-time Helm chart prerequisite

For capacity-reserved GPU capacity (ODCR), see the [targeted ODCR
pattern](https://aws-ia.github.io/terraform-aws-eks-blueprints/patterns/machine-learning/targeted-odcr/)
referenced at the top of `aws_eks.tf` — it's a complement to this example, not something this
example sets up for you.

### Bottlerocket node groups

The `ondemand_cpu`, `spot_cpu`, and GPU node groups run
[Bottlerocket](https://bottlerocket.dev/) rather than a general-purpose Linux AMI. Bottlerocket is
only the *node host OS* — the operating system EC2 runs to join the EC2 instance to the cluster
and run `containerd`. It has no bearing on what your Ray workloads themselves run: a Ray
container (Ubuntu-based or otherwise, including whatever image you pass via `image_uri`) runs as
a normal Kubernetes pod scheduled onto the node, the same way it would on any other EKS-optimized
AMI. Bottlerocket's node-level restrictions don't reach into your workload containers.

Two things are worth knowing about the node host OS itself if this is your first time with it:

- **It's container-only, on purpose.** There's no shell, package manager, or SSH access the way
  you'd get on Amazon Linux — Bottlerocket boots straight into a container runtime with a small,
  locked-down host OS. If you need to inspect a node directly, use its [admin
  container](https://github.com/bottlerocket-os/bottlerocket#admin-container) or SSM Session
  Manager, not `ssh`.
- **Storage is two volumes, not one.** Bottlerocket splits the OS (small, fixed, read-only-ish)
  from workload data (container images, ephemeral storage) onto separate EBS volumes. See
  `node_group_disk_size` in the variables table below — it sizes the *data* volume, not the OS
  volume, which is what you actually want for ML workloads with large container images.

The `default` (management) node group also runs Bottlerocket now but keeps its unsized, small
default data volume — it only hosts lightweight cluster components, not Ray workloads, so it
doesn't need the explicit sizing the workload node groups get.

Only x86_64 GPU instance types are covered today (`BOTTLEROCKET_x86_64_NVIDIA`). If you add an
ARM-based GPU instance type (e.g. `g5g`), you'll need `BOTTLEROCKET_ARM_64_NVIDIA` instead — this
example doesn't handle that automatically.

## Prerequisites

- Terraform >= 1.9
- AWS credentials with permission to create VPCs, EKS clusters, IAM roles, and S3 buckets
- Anyscale credentials — either:
  - `export ANYSCALE_CLI_TOKEN="your-token"`, or
  - `~/.anyscale/credentials.json` (same format `anyscale login` produces)

Unlike the AWS VM examples, you do **not** need `anyscale_org_id` or `anyscale_external_id`
here. Those exist to build an AWS IAM trust policy for VM clouds; K8S clouds authenticate
through the Anyscale Operator's IAM identity instead, which this example wires up for you via
`kubernetes_config.anyscale_operator_iam_identity`.

**Known limitation: Envoy Gateway chart pull.** This example installs [Envoy
Gateway](https://gateway.envoyproxy.io/) via Terraform's `helm_release` resource, but the
`terraform-provider-helm` v2.17.0 cannot pull Envoy Gateway's chart directly from its OCI
registry — it fails with `insufficient_scope: authorization failed`, even though the chart is
public and the standalone `helm` CLI pulls it fine on the same host. This is a real bug in the
provider's own OCI client (see
[hashicorp/terraform-provider-helm#1397](https://github.com/hashicorp/terraform-provider-helm/issues/1397)
for the same class of issue against a different registry), not something this example's
configuration can route around — there's no credential to configure for an anonymous public
pull. Work around it with one manual pre-step, once per chart version:

```bash
helm pull oci://docker.io/envoyproxy/gateway-helm --version 1.8.2 -d examples/aws-eks-basic/.charts
```

If you skip this, `terraform plan`/`apply` fails fast with a clear message telling you to run
the command above, rather than a cryptic OCI error partway through applying.

(Pinned to `1.8.2`, the latest stable at time of writing — newer than the `v1.7.0` [Anyscale's own
Gateway setup guide](https://docs.anyscale.com/clouds/kubernetes/gateway-envoy) references, since
that guide predates this chart version. Re-verify against Envoy Gateway's own [release
notes](https://github.com/envoyproxy/gateway/releases) before bumping it here.)

## Running the example

Bringing this example up to a fully workload-capable cloud is **two `terraform apply`s**, not
one. This is a deliberate, loudly-documented two-phase design, not a silent "just run it twice"
gotcha: `kubernetes_manifest` resolves the target Kubernetes CRD against the *live cluster* at
**plan time**, so a single apply can't both install Envoy Gateway's CRDs and create a Gateway
object that depends on them — the CRD isn't registered yet when the same apply's plan step needs
to resolve it. This is a documented limitation of the Kubernetes provider itself (see
`gateway_operator.tf`), not a bug in this example.

```bash
cd examples/aws-eks-basic

# One-time prerequisite - see "Known limitation" above.
helm pull oci://docker.io/envoyproxy/gateway-helm --version 1.8.2 -d .charts

terraform init

# First apply: creates the EKS cluster, registers the Anyscale cloud, and
# installs Envoy Gateway's CRDs + controller. install_gateway_resources stays
# at its default (false).
terraform plan
terraform apply

# Second apply: flip install_gateway_resources to true (in terraform.tfvars,
# or -var install_gateway_resources=true) to create the Gateway objects and
# install the Anyscale Operator, now that the CRDs exist.
terraform apply -var install_gateway_resources=true
```

Or use the repo's Makefile wrapper, which runs apply and destroy with a unique `cloud_name`
suffix and a cleanup trap so a failed apply doesn't leak resources:

```bash
make test-aws-eks-basic
```

Expect the full first apply (cluster + all node groups + Envoy Gateway) to take **15-25
minutes**, and plan on roughly **112 resources** being created; the second apply (Gateway objects
+ Operator) is fast, typically under a minute. Real AWS costs apply for as long as resources stay
up: the EKS control plane bills hourly, and the `default` node group runs 2x `t3.medium` on
demand out of the box. The CPU and GPU node groups all default to `desired_size = 0`, so you
won't pay for that capacity until something scales them up.

If you only need one half of the cycle (for example, debugging a failed apply without
re-destroying working resources), use the paired targets directly with a shared `SUFFIX`:

```bash
make apply-aws-eks-basic SUFFIX=dev1
# ... inspect, iterate ...
make destroy-aws-eks-basic SUFFIX=dev1
```

## Key variables

| Variable | Description | Default |
|----------|-------------|---------|
| `aws_region` | AWS region for all resources | `us-east-2` |
| `eks_cluster_name` | Name for the EKS cluster (and prefix for related resource names) | `anyscale-eks-public` |
| `eks_cluster_version` | Kubernetes version | `1.36` |
| `gpu_instance_types` | Map of GPU node group definitions to generate; see [`gpu_instances.tfvars.example`](./gpu_instances.tfvars.example) for more types (L4, L40S, A100, H100) | `{ T4 = ... }` |
| `node_group_disk_size` | Size in GB of the Bottlerocket *data* volume (`/dev/xvdb`) on `ondemand_cpu`, `spot_cpu`, and GPU node groups — not the OS volume. See "Bottlerocket node groups" above | `500` |
| `enable_efs` | Create a shared EFS filesystem | `false` |
| `cloud_name` | Name of the Anyscale cloud to create | `tf-aws-eks-test` |
| `anyscale_s3_force_destroy` | Allow `terraform destroy` to delete a non-empty S3 bucket | `true` |
| `install_gateway_resources` | Set `true` on a **second** `terraform apply` to install the Gateway + Anyscale Operator. See "Running the example" below | `false` |

`anyscale_s3_force_destroy` defaults to `true` here for test convenience — flip it to `false`
before pointing this example at a bucket that will hold real data you care about.

## Outputs

| Output | Description |
|--------|-------------|
| `cloud_id` | ID of the created Anyscale cloud |
| `cloud_name` | Name of the created Anyscale cloud |
| `cloud_resource_id` | The registered cloud's resource ID (wired automatically into the Operator's helm values — see "Running the example") |
| `eks_cluster_name` | Name of the EKS cluster |
| `gateway_address` | The Envoy Gateway's external load balancer address. Null until the second apply (`install_gateway_resources = true`) has run |

## Upgrading from the v20 module (existing forks/copies of this example)

This example now pins `terraform-aws-modules/eks/aws` to `v21.24.0` (previously `v20.33.1`).
This was a compatibility fix, not a style choice: v20's node-group submodules reference
`elastic_gpu_specifications`/`elastic_inference_accelerator` launch-template blocks that AWS
provider v6 removed from the `aws_launch_template` resource schema entirely, so `terraform
validate` (and therefore `terraform plan`, which runs validation first) fails hard on v20 with
any AWS provider version this example already required. If you copied this example before this
change, `terraform plan` most likely already fails for you today — upgrading is the fix, not
optional hardening.

If you maintain your own fork, carry over these changes. **Read the `addons` one first** — it's
the one change here that a `terraform plan` cannot catch for you:

- **`addons` must explicitly include `vpc-cni`, or the cluster comes up with no working
  networking.** v21 hardcodes `bootstrap_self_managed_addons = false` in the module, so EKS no
  longer self-installs the default networking addons the way it did under v20 (where the API's
  own default of `true` gave you `vpc-cni` for free). Renaming `cluster_addons` to `addons`
  without also adding `vpc-cni` produces a cluster where every node sits in `NotReady` forever
  and the Anyscale Operator never gets scheduled — and `terraform plan` will not warn you, because
  this is a runtime/API-level effect, not a schema-level one. The addons block needs to be:
  ```hcl
  addons = {
    coredns                = {}
    kube-proxy             = {}
    eks-pod-identity-agent = { before_compute = true }
    vpc-cni                = { before_compute = true }
  }
  ```
  `vpc-cni` and `eks-pod-identity-agent` need `before_compute = true` so they're installed before
  any node tries to join — `coredns` and `kube-proxy` stay at their default (after-compute) timing,
  since `coredns` needs a schedulable node to land on, which the `default` node group provides.
- **Module version**: `20.33.1` → `21.24.0`
- **Renamed arguments** (the addons rename is covered above; the other three changed for this example's usage):
  - `cluster_name` → `name`
  - `cluster_version` → `kubernetes_version`
  - `cluster_addons` → `addons` (see the `vpc-cni` note above — the rename alone isn't sufficient)
  - `cluster_endpoint_public_access` → `endpoint_public_access`
- **Taints are now a map, not a list** — v21 changed the type constraint from `list(object)` to
  `map(object)` on both `eks_managed_node_groups[*].taints` and the self-managed equivalent.
  Give each taint a map key instead of a list position; if you were building taint lists with
  `concat()`, switch to `merge()`.
- **`disk_size` and `use_custom_launch_template = false` are unchanged by the module upgrade
  itself** — both are still live in v21 and behave exactly as they did in v20. An earlier draft of
  this example experimented with replacing them with `block_device_mappings` as a v21 "upgrade";
  that was reverted as unnecessary — the v21 module bump alone never required it. This example
  *does* use `block_device_mappings` today, but for an unrelated, later reason: the switch to
  Bottlerocket node groups. See "Migrating from AL2023 to Bottlerocket" below — don't confuse the
  two changes, they happened for different reasons at different times.
- Everything else this example touches is unchanged, including the operator IAM identity
  wiring (`module.eks.eks_managed_node_groups["default"].iam_role_arn` in `main.tf`) and every
  output — no changes needed there even though several were renamed elsewhere in v21.

Two v21 default changes are worth knowing about even though this example doesn't set either
variable explicitly:

- `encryption_config` (formerly `cluster_encryption_config`) replaces `{}` with `null` as the
  syntax for accepting the module's default. The actual outcome is unchanged: verified against
  real plan output on both versions, the module creates its own dedicated KMS key for cluster
  secrets encryption by default either way — this is a syntax-only change for this example.
- Node IMDS hop limit default drops from `2` to `1` on every node group. This is intentional
  upstream hardening (hop limit `1` blocks pods from reaching the node's instance-role
  credentials via IMDS, which is the safer default), but it interacts with how this example
  wires up IAM: the cluster-autoscaler and AWS Load Balancer Controller policies
  (`aws_iam_policy.autoscaler_policy`, `aws_iam_policy.elb_policy`) are attached directly to the
  `default` node group's role, the older pattern that expects a pod to inherit node-role
  credentials through IMDS — which stops working at hop limit `1`. This example doesn't deploy
  either controller itself, so it doesn't hit the gap directly, but if you deploy
  cluster-autoscaler or the AWS Load Balancer Controller onto this cluster afterwards, give it
  an EKS Pod Identity association instead of relying on IMDS-inherited node-role credentials —
  otherwise the pod won't be able to reach the policy at all. The Anyscale Operator itself is
  unaffected either way, since it already uses Pod Identity.

## Migrating from AL2023 to Bottlerocket

Independent of the v21 module upgrade above, this example's node groups moved from Amazon Linux
2023 to [Bottlerocket](https://bottlerocket.dev/). If you forked this example before that change,
carry over these two edits:

- **`ami_type`**: `AL2023_x86_64_STANDARD` → `BOTTLEROCKET_x86_64` on CPU node groups;
  `AL2023_x86_64_NVIDIA` → `BOTTLEROCKET_x86_64_NVIDIA` on GPU node groups.
- **`block_device_mappings` is back — for a different, Bottlerocket-specific reason this time.**
  Don't confuse this with the block-device experiment reverted earlier in this document: that one
  was an optional style choice for AL2023 with no real benefit, and staying with `disk_size` was
  correct for AL2023. Bottlerocket is different: it boots from a small, fixed OS volume
  (`/dev/xvda`) and keeps container images and all workload storage on a *separate* data volume
  (`/dev/xvdb`). `disk_size` only ever sizes the OS volume, and that volume isn't allowed to grow
  under a custom launch template — so on Bottlerocket, `disk_size` cannot size the volume you
  actually care about, at all. Getting a usably-sized data volume requires
  `block_device_mappings` targeting `/dev/xvdb` explicitly. Remove `disk_size` and
  `use_custom_launch_template = false` from `ondemand_cpu`, `spot_cpu`, and the
  `gpu_node_group_base` local, and add both volumes. This example defines the mapping once as a
  shared local (the same dedup pattern already used for `local.anyscale_iam`) and references it
  from all three node groups, rather than repeating the block:
  ```hcl
  locals {
    bottlerocket_block_device_mappings = {
      # Bottlerocket OS volume (immutable, small) -- do not grow this
      xvda = {
        device_name = "/dev/xvda"
        ebs = {
          volume_size           = 4
          volume_type           = "gp3"
          delete_on_termination = true
        }
      }
      # Bottlerocket DATA volume (container images, ephemeral) -- the one that must be large
      xvdb = {
        device_name = "/dev/xvdb"
        ebs = {
          volume_size           = var.node_group_disk_size
          volume_type           = "gp3"
          encrypted             = true
          delete_on_termination = true
        }
      }
    }
  }

  # then, on ondemand_cpu / spot_cpu / gpu_node_group_base:
  block_device_mappings = local.bottlerocket_block_device_mappings
  ```
  The `default` node group is the one exception: it switches its `ami_type` to
  `BOTTLEROCKET_x86_64` too, but keeps the AMI's small default data volume rather than
  referencing `local.bottlerocket_block_device_mappings` — it only runs lightweight cluster
  components, not Ray workloads, so it doesn't need the larger sized volume the other groups get.

If you skip this and keep `disk_size` on a Bottlerocket `ami_type`, the node still boots — this
isn't a `terraform plan`-time error — but the data volume stays at its ~20 GiB AMI default no
matter what `node_group_disk_size` says, and container images or Ray object spill can fill it
without warning. See the matching entry under Troubleshooting.

## Troubleshooting

**Nodes stuck in `NotReady`, Anyscale Operator never schedules, apply otherwise "succeeded"** —
the `vpc-cni` addon is missing or not ordered `before_compute`. This example ships it correctly;
if you're seeing this, you likely merged an older copy of this example's `addons` block. See the
`vpc-cni` note in "Upgrading from the v20 module" above. `terraform plan`/`apply` will not warn
you about this — check `kubectl get nodes` and `kubectl get pods -n kube-system` directly.

**`Unsupported block type` during `terraform validate`** — you're on the old `v20.x` module
against a current AWS provider. See "Upgrading from the v20 module" above.

**`kubernetes_config.anyscale_operator_iam_identity` is null or empty in the plan** — this reads
`module.eks.eks_managed_node_groups["default"].iam_role_arn`, which only resolves once the
`default` node group's IAM role exists in state. If you're applying with `-target` or the
apply partially failed, make sure the `default` node group applied successfully before this
value is expected to be populated.

**`InsufficientInstanceCapacity` or vCPU limit errors on GPU node groups** — most AWS accounts
start with a very low (often zero) service quota for `g4dn`/`g5`/`p4d`/`p5` instance families.
Request a quota increase for the relevant "Running On-Demand P/G instances" quota in the AWS
console before scaling a GPU node group above `desired_size = 0`.

**Nodes fail to join, or Ray/container images silently run out of disk space** — on a
Bottlerocket node group, check that `block_device_mappings` targets `/dev/xvdb` (the data
volume) with `volume_size = var.node_group_disk_size`, not `/dev/xvda` (the small, fixed OS
volume) and not a plain `disk_size` argument, which only ever resizes the OS volume on
Bottlerocket. See "Migrating from AL2023 to Bottlerocket" above.

**Destroy leaves an EFS or S3 bucket behind** — check `enable_efs` and
`anyscale_s3_force_destroy`; a non-empty bucket with `force_destroy = false` will fail
`terraform destroy` rather than silently delete your data.

**Second apply fails on a Helm/OCI error instead of the chart-missing message** — you already ran
`helm pull` but pointed it at the wrong directory, or pulled a different chart version than
`1.8.2`. `gateway_operator.tf`'s `local.envoy_gateway_chart_path` expects exactly
`.charts/gateway-helm-v1.8.2.tgz` under this example's directory.

**Gateway never reaches `Programmed`, or the load balancer stays `<pending>`** — confirm you
didn't change the EnvoyProxy's Service annotations away from
`service.beta.kubernetes.io/aws-load-balancer-type: nlb`. The AWS Load Balancer Controller value,
`external`, is deliberately not used here — this example doesn't install that controller, so
`external` leaves the Service with no address, forever, with no error.

**HTTPS listener stuck at `ResolvedRefs: False` after the Operator installs** — the Operator
creates a TLS Secret named after the cloud's resource ID with underscores swapped for dashes
(`anyscale-<dashed-id>-certificate`); the Gateway's `certificateRefs` must reference that same
dashed form, not the raw underscored `cloud_resource_id`. Both forms are load-bearing in
different places — see the comments in `gateway_operator.tf`.

## See also

- [gcp-gke-basic](../gcp-gke-basic/) — the multi-resource cloud pattern equivalent for GCP/GKE
- [Cloud resource documentation](../../docs/resources/cloud.md)
- [terraform-aws-modules/eks](https://registry.terraform.io/modules/terraform-aws-modules/eks/aws/latest) — upstream module docs
- [Anyscale Gateway (Envoy) setup](https://docs.anyscale.com/clouds/kubernetes/gateway-envoy) — the manual/CLI equivalent of what this example automates
- [Anyscale documentation](https://docs.anyscale.com/)
