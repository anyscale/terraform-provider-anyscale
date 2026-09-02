# GCP VM Basic Example

Creates a new GCP project and registers it with Anyscale as a `VM` cloud, using the **all-in-one
pattern** (a single `anyscale_cloud` resource with an embedded `gcp_config` block). Unlike
`gcp-gke-basic`'s Kubernetes pattern, this is the simplest possible path to a working Anyscale
cloud on GCP: no Kubernetes cluster, no Filestore, no Memorystore - just a project, a VPC, a GCS
bucket, and the service accounts Anyscale needs to manage VM-based Ray clusters.

If you already have a GCP project and VPC you want to reuse instead of provisioning new ones, this
example isn't that - it always creates a fresh GCP project via Anyscale's [cloud foundation
modules](https://github.com/anyscale/terraform-google-anyscale-cloudfoundation-modules), under the
billing account and folder you supply. For a Kubernetes-based cloud instead of VM, see
[gcp-gke-basic](../gcp-gke-basic/).

## What this creates

- A new GCP project (via `root_folder_number` + `billing_account_id`), a VPC, a public subnet, and
  firewall rules allowing HTTPS (443) from `customer_ingress_cidr_ranges`, via Anyscale's
  `anyscale-cloudfoundation-modules/google` module
- A GCS bucket for Anyscale object storage
- Two service accounts: a control-plane service account Anyscale's control plane assumes via
  workload identity federation, and a data-plane service account attached to the Ray cluster nodes
  themselves - Filestore and Memorystore are both explicitly disabled for this scenario
  (`enable_anyscale_filestore = false`, `enable_anyscale_memorystore = false`)
- An `anyscale_cloud` resource (`compute_stack = "VM"`) wired to that project, VPC, GCS bucket, and
  both service accounts
- On top of the cloud: two `anyscale_compute_config` variants (`basic`, and `advanced` - showing
  custom resources, labels, and advanced instance config)

## Prerequisites

- Terraform >= 1.10
- GCP credentials with permission to create a new project under `root_folder_number`, associate it
  with `billing_account_id`, and create VPCs, service accounts, and GCS buckets within it (e.g.
  `gcloud auth application-default login`)
- Anyscale credentials - either:
  - `export ANYSCALE_CLI_TOKEN="your-token"`, or
  - `~/.anyscale/credentials.json` (same format `anyscale login` produces)
- Your Anyscale organization ID (`anyscale_org_id`, starts with `org_`) - either look it up
  yourself, or add a lookup to this config instead of hardcoding it:

  ```terraform
  data "anyscale_organization" "current" {}
  # then reference data.anyscale_organization.current.id as anyscale_org_id
  ```

`gcp_region`, `gcp_zone`, `billing_account_id`, `root_folder_number`, `customer_ingress_cidr_ranges`,
and `anyscale_org_id` all have no default and must be supplied - there's no
`terraform.tfvars.example` for this one (unlike the Kubernetes examples), so create your own
`terraform.tfvars` or pass them with `-var`. Unlike the AWS examples, there's no
`anyscale_external_id` here - GCP's workload identity federation setup doesn't need the
cross-account IAM trust relationship that concept exists for on AWS.

## Running the example

```bash
cd examples/gcp-vm-basic
terraform init
terraform plan
terraform apply
```

Or use the repo's Makefile wrapper, which runs apply and destroy with a unique `cloud_name` suffix
and a cleanup trap so a failed apply doesn't leak resources - you still need your own variables
supplied first (via `terraform.tfvars` or `TF_VAR_*` environment variables), since the wrapper only
overrides `cloud_name`:

```bash
make test-gcp-vm-basic
```

If you only need one half of the cycle (for example, debugging a failed apply without
re-destroying working resources), use the paired targets directly with a shared `SUFFIX`:

```bash
make apply-gcp-vm-basic SUFFIX=dev1
# ... inspect, iterate ...
make destroy-gcp-vm-basic SUFFIX=dev1
```

Real GCP costs apply for as long as resources stay up - this example's compute configs define
*what Ray clusters would look like* if launched against this cloud, but don't launch anything
themselves, so the ongoing cost here is the project/VPC/GCS infrastructure itself, not compute.

## Key variables

| Variable | Description | Default |
|----------|-------------|---------|
| `gcp_region` | GCP region for all resources | *(required, no default)* |
| `gcp_zone` | GCP zone for zonal resources | *(required, no default)* |
| `billing_account_id` | Billing account to associate with the new GCP project | *(required, no default)* |
| `root_folder_number` | GCP folder number the new project is created under | *(required, no default)* |
| `customer_ingress_cidr_ranges` | CIDR block allowed through the firewall on 443 | *(required, no default)* |
| `anyscale_org_id` | Your Anyscale organization ID (`org_...`) | *(required, no default)* |
| `cloud_name` | Name of the Anyscale cloud to create | `tf-gcp-basic-test` |
| `anyscale_cloud_id` | Existing Anyscale cloud ID, if re-running against one already created | `null` |
| `is_private_cloud` | Whether this is a private cloud | `false` |
| `auto_add_user` | Whether to automatically add users | `false` |
| `anyscale_deploy_env` | Deployment environment tag (`production`, `test`, or `development`) | `test` |
| `common_prefix` | Prefix for GCP resource names; must be unique per scenario/run | `as-gcp-basic-` |
| `labels` | A map of labels applied to every taggable resource | `{test = "true", environment = "test", scenario = "gcp-basic"}` |

## Outputs

| Output | Description |
|--------|-------------|
| `cloud_id` | ID of the created Anyscale cloud |
| `cloud_name` | Name of the created Anyscale cloud |
| `compute_config_id` | ID of the `basic` compute config |

## Troubleshooting

**`terraform apply` fails validating the Google provider version, or a module argument it doesn't
recognize** - the `module.google_anyscale_v2` block in `gcp_anyscale.tf` has no version constraint,
so it intentionally floats to the cloud foundation module's latest release rather than pinning it,
and a new module release could occasionally introduce a breaking argument change before this
example catches up. Run `terraform init -upgrade` if you suspect a stale cached version, and open
an issue if the module's current release has genuinely diverged from what this example expects.

**`anyscale_org_id` validation error** - the value must start with `org_`. Use the
`anyscale_organization` data source shown under Prerequisites above instead of guessing or
copy-pasting a truncated value.

**Project creation fails with a permission or billing error** - double check the credentials
Terraform is using have both project-creation permission on `root_folder_number` and the right to
associate a new project with `billing_account_id` - these are two separate IAM grants on GCP, and
either alone will fail this example partway through.

## See also

- [aws-vm-basic](../aws-vm-basic/) - the equivalent all-in-one VM pattern on AWS. Its
  [Create a VM cloud guide](../../docs/guides/create-a-vm-cloud.md) walkthrough is AWS-specific
  (no GCP equivalent exists yet), but the all-in-one pattern it explains is the same one this
  example uses
- [gcp-gke-basic](../gcp-gke-basic/) - the Kubernetes equivalent on GCP
- [Cloud resource documentation](../../docs/resources/cloud.md)
