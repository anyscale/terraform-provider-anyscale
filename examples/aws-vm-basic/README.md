# AWS VM Basic Example

Creates a new AWS VPC and registers it with Anyscale as a `VM` cloud, using the **all-in-one
pattern** (a single `anyscale_cloud` resource with an embedded `aws_config` block). Unlike
`gcp-gke-basic`'s Kubernetes pattern, this is the simplest possible path to a working Anyscale
cloud on AWS: no Kubernetes cluster, no EFS, no MemoryDB - just a VPC, an S3 bucket, and the IAM
roles Anyscale needs to manage VM-based Ray clusters in your account.

If you already have a VPC and IAM roles you want to reuse instead of provisioning new ones, this
example isn't that - it always creates fresh AWS infrastructure via Anyscale's [cloud foundation
modules](https://github.com/anyscale/terraform-aws-anyscale-cloudfoundation-modules). For a
Kubernetes-based cloud instead of VM, see [aws-eks-basic](../aws-eks-basic/).

## What this creates

- A VPC, public subnets, and a security group allowing HTTPS (443) from
  `customer_ingress_cidr_ranges`, via Anyscale's `anyscale-cloudfoundation-modules/aws` module
- An S3 bucket for Anyscale object storage (`anyscale_s3_force_destroy = true` by default, so
  `terraform destroy` doesn't get blocked by a non-empty bucket in this example)
- Two IAM roles: a control-plane role Anyscale's control plane assumes cross-account (trust policy
  scoped to `anyscale_org_id` + `anyscale_external_id`), and a data-plane role attached to the Ray
  cluster nodes themselves
- An `anyscale_cloud` resource (`compute_stack = "VM"`) wired to that VPC, security group, S3
  bucket, and both IAM roles
- On top of the cloud: three `anyscale_compute_config` variants (`basic`, `advanced` - showing
  custom resources, labels, and advanced instance config, and `simple`) and two `anyscale_project`
  resources (one referenced by `cloud_id`, one by `cloud_name`), plus `anyscale_project` /
  `anyscale_projects` data source lookups reading them back

## Prerequisites

- Terraform >= 1.9
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
have no default and must be supplied - there's no `terraform.tfvars.example` for this one (unlike
the Kubernetes examples), so create your own `terraform.tfvars` or pass them with `-var`.
`anyscale_external_id` is your own choice of string; it just has to match between the IAM trust
policy this example creates and whatever Anyscale stores for the cloud, which this example handles
for you automatically since both come from the same variable.

## Running the example

```bash
cd examples/aws-vm-basic
terraform init
terraform plan
terraform apply
```

Or use the repo's Makefile wrapper, which runs apply and destroy with a unique `cloud_name` suffix
and a cleanup trap so a failed apply doesn't leak resources - you still need your own variables
supplied first (via `terraform.tfvars` or `TF_VAR_*` environment variables), since the wrapper only
overrides `cloud_name`:

```bash
make test-aws-vm-basic
```

If you only need one half of the cycle (for example, debugging a failed apply without
re-destroying working resources), use the paired targets directly with a shared `SUFFIX`:

```bash
make apply-aws-vm-basic SUFFIX=dev1
# ... inspect, iterate ...
make destroy-aws-vm-basic SUFFIX=dev1
```

Real AWS costs apply for as long as resources stay up - this example's compute configs define
*what Ray clusters would look like* if launched against this cloud, but don't launch anything
themselves, so the ongoing cost here is the VPC/NAT/S3 infrastructure itself, not compute.

## Key variables

| Variable | Description | Default |
|----------|-------------|---------|
| `aws_region` | AWS region for all resources | *(required, no default)* |
| `customer_ingress_cidr_ranges` | CIDR block allowed through the security group on 443 | *(required, no default)* |
| `anyscale_org_id` | Your Anyscale organization ID (`org_...`) | *(required, no default)* |
| `anyscale_external_id` | External ID for the IAM trust policy - your own choice of string | *(required, no default)* |
| `cloud_name` | Name of the Anyscale cloud to create | `tf-aws-basic-test` |
| `anyscale_cloud_id` | Existing Anyscale cloud ID, if re-running against one already created | `null` |
| `anyscale_deploy_env` | Deployment environment tag (`production`, `test`, or `development`) | `test` |
| `common_prefix` | Prefix for AWS resource names; must be unique per scenario/run | `as-aws-basic-` |
| `anyscale_s3_force_destroy` | Allow `terraform destroy` to remove a non-empty S3 bucket | `true` |
| `tags` | A map of tags applied to every taggable resource | `{test = "true", environment = "test", scenario = "aws-basic"}` |

## Outputs

| Output | Description |
|--------|-------------|
| `cloud_id` | ID of the created Anyscale cloud |
| `cloud_name` | Name of the created Anyscale cloud |
| `cloud_is_default` | Whether this cloud is the organization's default cloud (read-only, managed by Anyscale, can change out of band) |
| `basic_project_id` / `basic_project_name` / `basic_project_directory` | Identifiers for the `basic` project created on this cloud |
| `basic_project_datasource_lookup_id` | The same project's ID, read back through the `anyscale_project` data source rather than the resource |
| `all_project_ids` / `all_project_names` | Every project on this cloud, via the `anyscale_projects` data source |
| `basic_compute_config_id` / `basic_compute_config_name` / `basic_compute_config_version` | Identifiers for the `basic` compute config |

## Troubleshooting

**`terraform apply` fails validating the AWS provider version, or a module argument it doesn't
recognize** - this example intentionally uses the cloud foundation module's `latest` version
(see the `tflint-ignore: terraform_module_pinned_source` comment in `aws_anyscale.tf`) rather than
pinning it, so a new module release could occasionally introduce a breaking argument change before
this example catches up. Run `terraform init -upgrade` if you suspect a stale cached version, and
open an issue if the module's current release has genuinely diverged from what this example expects.

**`anyscale_org_id` validation error** - the value must start with `org_`. Use the
`anyscale_organization` data source shown under Prerequisites above instead of guessing or
copy-pasting a truncated value.

**IAM trust policy errors on `terraform apply`** - double check `anyscale_external_id` matches
between your Terraform state and what's already registered for this cloud in Anyscale, if you're
re-applying against an existing `anyscale_cloud_id` rather than starting fresh.

## See also

- [gcp-vm-basic](../gcp-vm-basic/) - the equivalent all-in-one VM pattern on GCP
- [aws-eks-basic](../aws-eks-basic/) - the Kubernetes equivalent on AWS
- [Cloud resource documentation](../../docs/resources/cloud.md)
