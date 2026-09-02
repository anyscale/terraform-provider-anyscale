# AWS VM Basic Resource Example

Creates a new AWS VPC and registers it with Anyscale as a `VM` cloud, using the **multi-resource
cloud pattern**: an empty `anyscale_cloud` resource created first, with a separate
`anyscale_cloud_resource` attached to it afterward. This is the minimal form of that pattern - no
EFS, no MemoryDB, exactly one resource deployment attached - so it's the place to see the
empty-cloud-plus-resource wiring on its own, before reaching for a configuration that builds more
on top of it.

Its all-in-one sibling with the identical minimal scope is [aws-vm-basic](../aws-vm-basic/) - a
single `anyscale_cloud` resource with an embedded `aws_config` block, instead of two separate
resources. The AWS infrastructure the two provision (VPC, security group, S3 bucket, IAM roles) is
the same either way; what differs is only how the Anyscale-side cloud is expressed in Terraform -
read that example's README alongside this one if you're deciding which pattern fits. Its own fuller
sibling, [aws-vm](../aws-vm/), keeps this same two-resource pattern but layers EFS and MemoryDB
toggles on top - if you outgrow this example, `aws-vm` is the next step up in the same pattern, not
a switch to the other one.

The multi-resource pattern exists because a cloud can carry more than one `anyscale_cloud_resource`
- a second region, or a second compute stack, attached to the same cloud (see
[kitchen-sink](../kitchen-sink/) for a working two-resource configuration, and the [Cloud Resources
guide](../../docs/guides/cloud-resources.md#multiple-resource-deployments-on-one-cloud) for the
cardinality rules that makes possible). This example only ever attaches one resource, but it's built
on the same empty-cloud foundation, so the wiring here is exactly what you'd extend to attach a
second `anyscale_cloud_resource` alongside `primary`.

## What this creates

- A VPC, public subnets, and a security group allowing HTTPS (443) from
  `customer_ingress_cidr_ranges`, via Anyscale's `anyscale-cloudfoundation-modules/aws` module
- An S3 bucket for Anyscale object storage (`anyscale_s3_force_destroy = true` by default, so
  `terraform destroy` doesn't get blocked by a non-empty bucket in this example)
- Two IAM roles: a control-plane role Anyscale's control plane assumes cross-account (trust policy
  scoped to `anyscale_org_id` + `anyscale_external_id`), and a data-plane role attached to the Ray
  cluster nodes themselves
- An `anyscale_cloud` resource with nothing but `name` and `auto_add_user` set - no `compute_stack`,
  no `aws_config`, not even `is_private_cloud` - deliberately empty, since a multi-resource cloud
  derives its compute stack from whichever resource ends up attached to it rather than declaring one
  on the parent
- A separate `anyscale_cloud_resource` (`compute_stack = "VM"`) attached to that cloud via
  `cloud_id = anyscale_cloud.primary.id`, carrying `is_private` plus the `aws_config` block
  (`vpc_id`, `subnet_ids_to_az`, `security_group_ids`, `controlplane_iam_role_arn`,
  `dataplane_iam_role_arn`, `external_id`, all sourced from the module above) and an
  `object_storage` block for the S3 bucket - no `file_storage` block, since EFS isn't wired up
  anywhere in this scenario, not even as an optional toggle
- On top of the cloud: three `anyscale_compute_config` variants (`basic`, `advanced` - showing
  custom resources, labels, and advanced instance config, and `simple`), all targeting
  `anyscale_cloud.primary.id` with no `cloud_resource` set, so each resolves to the cloud's one
  (and only) attached deployment

Unlike [aws-vm-basic](../aws-vm-basic/), this example creates no `anyscale_project` resources or
project data source lookups - just the cloud, its one resource deployment, and the three compute
configs above.

## Prerequisites

- Terraform >= 1.10
- AWS credentials with permission to create VPCs, IAM roles, and S3 buckets
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
have no default and must be supplied - there's no `terraform.tfvars.example` for this one, so
create your own `terraform.tfvars` or pass them with `-var`. `anyscale_external_id` is your own
choice of string; it just has to match between the IAM trust policy this example creates and
whatever Anyscale stores for the cloud, which this example handles for you automatically since
both come from the same variable.

## Running the example

```bash
cd examples/aws-vm-basic-resource
terraform init
terraform plan
terraform apply
```

When you're done:

```bash
terraform destroy
```

Unlike most other scenario examples in this repo - including its own
[aws-vm-basic](../aws-vm-basic/) sibling - there is no `make test-aws-vm-basic-resource` target, no
paired `make apply-aws-vm-basic-resource` / `make destroy-aws-vm-basic-resource`, and it isn't part
of the `make test-primary` matrix either. This directory has no Makefile automation at all: no
unique-suffix `cloud_name`, no exit-trap destroy on a failed apply, nothing beyond the plain
`terraform` commands above. Track your own state and run `terraform destroy` yourself when you're
finished - this repo's sweeper only ever reaches Anyscale-side resources, never the underlying AWS
VPC/IAM/S3 infrastructure, so an abandoned apply here leaks real AWS spend with nothing automated
to catch it.

Real AWS costs apply for as long as resources stay up - the compute configs created here define
*what Ray clusters would look like* if launched against this cloud, but don't launch anything
themselves, so the ongoing cost is the VPC/NAT/S3 infrastructure itself, not compute.

## Key variables

| Variable | Description | Default |
|----------|-------------|---------|
| `aws_region` | AWS region for all resources | *(required, no default)* |
| `customer_ingress_cidr_ranges` | CIDR block allowed through the security group on 443 | *(required, no default)* |
| `anyscale_org_id` | Your Anyscale organization ID (`org_...`) | *(required, no default)* |
| `anyscale_external_id` | External ID for the IAM trust policy - your own choice of string | *(required, no default)* |
| `cloud_name` | Name of both the Anyscale cloud and its attached cloud resource | `tf-aws-basic-test` |
| `is_private_cloud` | Whether the cloud resource is private | `false` |
| `auto_add_user` | Whether to automatically add users to the cloud | `false` (`aws-vm`'s equivalent default is `true` - a deliberate per-scenario difference, not a copy error) |
| `anyscale_deploy_env` | Deployment environment tag (`production`, `test`, or `development`) | `test` |
| `common_prefix` | Prefix for AWS resource names; must be unique per scenario/run | `as-aws-basic-` |
| `anyscale_s3_force_destroy` | Allow `terraform destroy` to remove a non-empty S3 bucket | `true` |
| `tags` | A map of tags applied to every taggable resource | `{test = "true", environment = "test", scenario = "aws-basic"}` |

## Outputs

| Output | Description |
|--------|-------------|
| `cloud_id` | ID of the created Anyscale cloud |
| `cloud_name` | Name of the created Anyscale cloud |
| `cloud_resource_id` | ID of the separate `anyscale_cloud_resource` attached to the cloud |

That's the full output set - deliberately smaller than [aws-vm-basic](../aws-vm-basic/)'s. There
are no outputs for the three compute configs this example creates (`aws-vm-basic` exposes
`basic_compute_config_id` and friends); if you need to reference one of them outside this
configuration, add your own output or read it back with the `anyscale_compute_config` data source.

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

**`terraform apply` fails on `anyscale_cloud_resource.primary` with a `409` ("A cloud deployment
with the name ... already exists")** - this means a resource with that name is already attached to
the cloud, most often because Terraform's state for `anyscale_cloud_resource.primary` was lost
while the backend resource itself is still alive (for example, state was deleted, or you're
re-running this configuration against a cloud that already has a resource attached from a previous
apply). Re-running `apply` sends the same name again and hits the same collision rather than
adopting it - recover with `terraform import` instead; see the [Cloud Resources
guide](../../docs/guides/cloud-resources.md#multiple-resource-deployments-on-one-cloud) for the
`cloud_id:name` import syntax.

## See also

- [Cloud Resources guide](../../docs/guides/cloud-resources.md) - the multi-resource cloud pattern
  this example uses, including the cardinality rules for attaching more than one
  `anyscale_cloud_resource` to a cloud
- [Create a VM cloud guide](../../docs/guides/create-a-vm-cloud.md) - a narrated walkthrough built
  around the all-in-one pattern; see its link to the Cloud Resources guide for how the
  multi-resource shape shown here differs
- [aws-vm-basic](../aws-vm-basic/) - the all-in-one equivalent, same minimal scope
- [aws-vm](../aws-vm/) - the fuller sibling using this same multi-resource pattern, with EFS and
  MemoryDB toggles
- [kitchen-sink](../kitchen-sink/) - extends this pattern to two resources (VM and K8S) attached to
  one cloud
- [Cloud resource documentation](../../docs/resources/cloud_resource.md) and [Cloud
  documentation](../../docs/resources/cloud.md)
