# AWS VM Example

Creates a new AWS VPC and registers it with Anyscale as a `VM` cloud, using the **multi-resource
cloud pattern**: an empty `anyscale_cloud` shell resource (`anyscale_cloud.primary`), then a
separate `anyscale_cloud_resource` (`anyscale_cloud_resource.primary`) attached to it that carries
all of the actual AWS wiring - `aws_config`, `object_storage`, and optionally `file_storage`. This
is the "full" / consolidated AWS VM scenario: on top of the split-resource pattern itself, it adds
two feature toggles its simpler siblings don't exercise - EFS (shared file storage) and MemoryDB
(Ray GCS fault tolerance) - both off by default.

Like its siblings, this example always creates fresh AWS infrastructure via Anyscale's [cloud
foundation modules](https://github.com/anyscale/terraform-aws-anyscale-cloudfoundation-modules)
rather than attaching to a VPC or IAM roles you already have. For the same multi-resource pattern
without the EFS/MemoryDB toggles, see [aws-vm-basic-resource](../aws-vm-basic-resource/); for the
simplest possible AWS VM path (a single `anyscale_cloud` resource, no split pattern, no toggles),
see [aws-vm-basic](../aws-vm-basic/); for the same pattern and feature set on GCP instead of AWS,
see [gcp-vm](../gcp-vm/).

**A naming heads-up before you run anything:** this directory is `examples/aws-vm`, but the repo's
Makefile and test matrix call this scenario **`full`**, not `aws-vm` - there is no `make
test-aws-vm` target. See [Running the example](#running-the-example) below for the real target
names.

## What this creates

- A VPC, public subnets (`172.24.0.0/16` across three `/24`s by default), and a security group
  allowing HTTPS (443) from `customer_ingress_cidr_ranges`, via Anyscale's
  `anyscale-cloudfoundation-modules/aws` module (the `aws_anyscale_v2` module block in
  `aws_anyscale.tf`)
- An S3 bucket for Anyscale object storage (`anyscale_s3_force_destroy = true` by default, so
  `terraform destroy` isn't blocked by a non-empty bucket in this example)
- Two IAM roles: a control-plane role Anyscale's control plane assumes cross-account (trust policy
  scoped to `anyscale_org_id` + `anyscale_external_id`), and a data-plane role attached to the Ray
  cluster nodes themselves
- An empty `anyscale_cloud.primary` shell - just `name` (`var.cloud_name`), `is_private_cloud`, and
  `auto_add_user`, plus `enable_lineage_tracking` and `enable_log_ingestion` hardcoded to `true`
  (fixed in this example, not exposed as variables). It deliberately has no `aws_config`,
  `object_storage`, or `file_storage` block of its own - that's what makes it "empty," and it's why
  the `is_empty_cloud` output below reads `true`
- A separate `anyscale_cloud_resource.primary`, attached via `cloud_id = anyscale_cloud.primary.id`
  and sharing the same `name` value (`var.cloud_name` again - safe, since a resource's `name` only
  has to be unique per cloud, not globally), carrying `region`, `compute_stack = "VM"`, `is_private`
  (from the same `var.is_private_cloud`), the `aws_config` block (VPC/subnets/security group/IAM
  roles from the module, plus MemoryDB fields populated only when `enable_memorydb = true`), and
  the `object_storage` block
- A `dynamic "file_storage"` block on that same resource, present only when `enable_efs = true`
  (`for_each = var.enable_efs ? [1] : []`) - when enabled it wires up the module's EFS file system
  and its first mount target IP
- Optionally, when the respective toggle is on: an EFS file system (`enable_efs`) and a MemoryDB
  cluster for Ray GCS fault tolerance (`enable_memorydb`) - both `false` by default, so a plain
  `terraform apply` creates neither

## Prerequisites

- Terraform >= 1.9
- AWS credentials with permission to create VPCs, IAM roles, and S3 buckets - and, if you turn on
  the feature toggles, EFS file systems and MemoryDB clusters too
- Anyscale credentials - either:
  - `export ANYSCALE_CLI_TOKEN="your-token"`, or
  - `~/.anyscale/credentials.json` (same format `anyscale login` produces)
- Your Anyscale organization ID (`anyscale_org_id`, starts with `org_`) - either look it up
  yourself, or add a lookup to this config instead of hardcoding it:

  ```terraform
  data "anyscale_organization" "current" {}
  # then reference data.anyscale_organization.current.id as anyscale_org_id
  ```

`aws_region`, `customer_ingress_cidr_ranges`, `anyscale_external_id`, and `anyscale_org_id` all
have no default and must be supplied - there's no `terraform.tfvars.example` in this directory, so
create your own `terraform.tfvars` or pass them with `-var`. `anyscale_external_id` is your own
choice of string; it just has to match between the IAM trust policy this example creates and
whatever Anyscale stores for the cloud, which this example handles for you automatically since
both come from the same variable.

## Running the example

```bash
cd examples/aws-vm
terraform init
terraform plan
terraform apply
```

A plain `terraform apply` above uses this example's own defaults - EFS and MemoryDB stay off
(`enable_efs = false`, `enable_memorydb = false`) unless you set them yourself.

**The Makefile calls this scenario `full`, not `aws-vm`.** The real wrapper target is:

```bash
make test-aws-vm-full
```

Unlike the `-basic` wrapper (which only overrides `cloud_name`), the `test-aws-vm-full` wrapper
also switches both feature toggles on and moves to an alternate VPC CIDR, so a default run of this
target exercises the full EFS + MemoryDB path, not just the minimal split-pattern shape:
`enable_efs=true`, `enable_memorydb=true`, `vpc_cidr_block=172.27.0.0/16`,
`vpc_public_subnets=["172.27.21.0/24","172.27.22.0/24","172.27.23.0/24"]`, on top of the unique
`cloud_name`/`common_prefix` every wrapper applies.

If you only need one half of the cycle (for example, debugging a failed apply without
re-destroying working resources), use the paired targets directly with a shared `SUFFIX` - these
carry the same EFS/MemoryDB/VPC overrides as `test-aws-vm-full` above:

```bash
make apply-aws-vm-full SUFFIX=dev1
# ... inspect, iterate ...
make destroy-aws-vm-full SUFFIX=dev1
```

Real AWS costs apply for as long as resources stay up. With the toggles on (the Makefile default),
that includes the MemoryDB cluster and EFS file system themselves, not just the VPC/NAT/S3
infrastructure a plain `terraform apply` creates.

## Key variables

| Variable | Description | Default |
|----------|-------------|---------|
| `aws_region` | AWS region for all resources | *(required, no default)* |
| `customer_ingress_cidr_ranges` | CIDR block allowed through the security group on 443 | *(required, no default)* |
| `anyscale_org_id` | Your Anyscale organization ID (`org_...`) | *(required, no default)* |
| `anyscale_external_id` | External ID for the IAM trust policy - your own choice of string | *(required, no default)* |
| `cloud_name` | Name of the Anyscale cloud shell (and its attached cloud resource) | `tf-aws-vm-test` |
| `is_private_cloud` | Whether the cloud shell and its attached resource are private | `false` |
| `auto_add_user` | Whether to automatically add org users to the cloud | `true` |
| `anyscale_deploy_env` | Deployment environment tag (`production`, `test`, or `development`) | `test` |
| `common_prefix` | Prefix for AWS resource names; must be unique per scenario/run, max 30 chars | `as-aws-vm-` |
| `anyscale_s3_force_destroy` | Allow `terraform destroy` to remove a non-empty S3 bucket | `true` |
| `tags` | A map of tags applied to every taggable resource | `{test = "true", environment = "test"}` |
| `enable_efs` | Attach an EFS file system via a `dynamic "file_storage"` block | `false` |
| `enable_memorydb` | Wire a MemoryDB cluster into `aws_config` for Ray GCS fault tolerance | `false` |
| `vpc_cidr_block` | CIDR block for the VPC the cloud foundation module creates | `172.24.0.0/16` |
| `vpc_public_subnets` | Public subnet CIDR blocks within the VPC | `["172.24.21.0/24", "172.24.22.0/24", "172.24.23.0/24"]` |

## Outputs

| Output | Description |
|--------|-------------|
| `cloud_id` | ID of the created (empty-shell) Anyscale cloud |
| `cloud_name` | Name of the created Anyscale cloud |
| `is_empty_cloud` | Whether the cloud was created as an empty shell - `true` here, since all AWS wiring lives on the attached `anyscale_cloud_resource` instead of on the cloud itself |
| `cloud_resource_id` | ID of the attached `anyscale_cloud_resource` |
| `cloud_resource_name` | Name of the attached `anyscale_cloud_resource` |
| `efs_id` | The EFS file system ID - `null` unless `enable_efs = true` |
| `memorydb_cluster_id` | The MemoryDB cluster ID - `null` unless `enable_memorydb = true` |

## Troubleshooting

**`terraform apply` fails validating the AWS provider version, or a module argument it doesn't
recognize** - the `module.aws_anyscale_v2` block in `aws_anyscale.tf` has no version constraint, so
it intentionally floats to the cloud foundation module's latest release rather than pinning it, and
a new module release could occasionally introduce a breaking argument change before this example
catches up. Run `terraform init -upgrade` if you suspect a stale cached version, and open an issue
if the module's current release has genuinely diverged from what this example expects.

**`anyscale_org_id` validation error** - the value must start with `org_`. Use the
`anyscale_organization` data source shown under Prerequisites above instead of guessing or
copy-pasting a truncated value.

**IAM trust policy errors on `terraform apply`** - double check `anyscale_external_id` hasn't
changed between applies. It's baked into the IAM trust policy the module creates, and a value that
drifts from what Anyscale already has on record for this cloud will fail the trust relationship.

**`efs_id` or `memorydb_cluster_id` output as `null`** - expected unless you explicitly set
`enable_efs = true` / `enable_memorydb = true`. Both default to `false`, so a plain `terraform
apply` (as opposed to `make test-aws-vm-full`, which turns both on) creates neither resource, and
both outputs stay `null` by design, not by error.

**`make test-aws-vm`, `make apply-aws-vm`, or `make destroy-aws-vm` - "No rule to make target"** -
those targets don't exist. This scenario is named `full` in the Makefile, not `aws-vm` - use `make
test-aws-vm-full`, `make apply-aws-vm-full SUFFIX=<id>`, and `make destroy-aws-vm-full SUFFIX=<id>`
instead (see [Running the example](#running-the-example)).

**Don't set `compute_stack` on `anyscale_cloud.primary` too** - an empty cloud in the
multi-resource pattern derives its compute stack from whichever `anyscale_cloud_resource` ends up
attached to it, so this example deliberately leaves `compute_stack` unset on the parent
`anyscale_cloud` and sets it only on `anyscale_cloud_resource.primary`. Setting it on both risks
the two disagreeing with each other for no benefit.

## See also

- [Cloud Resources guide](../../docs/guides/cloud-resources.md) - the multi-resource cloud pattern
  in depth: supported providers, renaming, multiple attached resources, and known limitations
- [aws-vm-basic](../aws-vm-basic/) - the simplest all-in-one AWS VM path (single resource, no
  toggles)
- [aws-vm-basic-resource](../aws-vm-basic-resource/) - the same multi-resource pattern without the
  EFS/MemoryDB toggles
- [gcp-vm](../gcp-vm/) - the same pattern and feature set on GCP
- [`anyscale_cloud` resource documentation](../../docs/resources/cloud.md)
- [`anyscale_cloud_resource` resource documentation](../../docs/resources/cloud_resource.md)
