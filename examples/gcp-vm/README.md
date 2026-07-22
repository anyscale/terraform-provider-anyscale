# GCP VM Full Example

Creates a new GCP project and registers it with Anyscale as a `VM` cloud, using the
**multi-resource cloud pattern**: an empty `anyscale_cloud` resource, followed by a separate
`anyscale_cloud_resource` that attaches the actual GCP configuration to it. This is the
consolidated ("full") GCP VM scenario - unlike [`gcp-vm-basic`](../gcp-vm-basic/)'s all-in-one
pattern (a single resource with an embedded `gcp_config` block), the cloud and its resource
deployment are managed as two independent resources here, and Filestore (shared storage) and
Memorystore (Ray GCS fault tolerance) are both available as opt-in feature toggles rather than
omitted outright.

Like `gcp-vm-basic`, this example always creates a brand new GCP project from scratch - via
`billing_account_id` + `root_folder_number` - rather than attaching to a project you already have;
it isn't that kind of example. For the equivalent multi-resource pattern on AWS (EFS and MemoryDB
in place of Filestore and Memorystore), see [`aws-vm`](../aws-vm/).

## What this creates

- A new GCP project (via `root_folder_number` + `billing_account_id`), a VPC, a public subnet, and
  firewall rules allowing HTTPS (443) from `customer_ingress_cidr_ranges`, via Anyscale's
  `anyscale-cloudfoundation-modules/google` module (`module.google_anyscale_v2`)
- A GCS bucket for Anyscale object storage
- Two service accounts: a control-plane service account Anyscale's control plane assumes via
  workload identity federation, and a data-plane service account attached to the Ray cluster nodes
  themselves
- Optionally, a Filestore instance for shared storage (`enable_filestore`) and/or a Memorystore
  instance for Ray GCS fault tolerance (`enable_memorystore`) - both default to `false`, so neither
  is created unless you turn it on
- An empty `anyscale_cloud` resource (`anyscale_cloud.primary`): just `name`, `is_private_cloud`,
  `auto_add_user`, and the cloud-level `lineage_tracking_enabled` / `is_aggregated_logs_enabled`
  toggles (both hardcoded `true` in this example). No `gcp_config`, `object_storage`, or `file_storage`
  block lives on the cloud itself - following the multi-resource pattern's rule that
  `compute_stack` and the provider config blocks belong on the attached resource, not the parent
  cloud
- A separate `anyscale_cloud_resource` (`anyscale_cloud_resource.primary`) attached to that cloud
  via `cloud_id`, carrying the real `gcp_config` (project, workload identity provider, VPC,
  subnet, both service accounts, firewall policy, and the Memorystore endpoint when enabled), the
  `object_storage` block for the GCS bucket, and a conditional `file_storage` block for Filestore
  when enabled

Note that `is_private_cloud` (on the cloud) and `is_private` (on the cloud resource) are two
distinct attributes on two distinct objects, both driven by the same `var.is_private_cloud` value
in this example - not a typo. See [Naming differences between resources and data
sources](../../docs/guides/cloud-resources.md#naming-differences-between-resources-and-data-sources)
for other cases like this.

## Prerequisites

- Terraform >= 1.9
- GCP credentials with permission to create a new project under `root_folder_number`, associate it
  with `billing_account_id`, and create VPCs, service accounts, and GCS buckets within it (e.g.
  `gcloud auth application-default login`) - plus permission to create Filestore and Memorystore
  instances if you turn either toggle on
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
`terraform.tfvars.example` for this one, so create your own `terraform.tfvars` or pass them with
`-var`. As with `gcp-vm-basic`, there's no `anyscale_external_id` here - GCP's workload identity
federation setup doesn't need the cross-account IAM trust relationship that concept exists for on
AWS.

## Running the example

```bash
cd examples/gcp-vm
terraform init
terraform plan
terraform apply
```

Or use the repo's Makefile wrapper. **The directory is `gcp-vm`, but the Makefile - and every
wrapper target for this scenario - calls it "full"**, not "gcp-vm": there is no `make
test-gcp-vm` target.

```bash
make test-gcp-vm-full
```

Unlike `gcp-vm-basic`'s wrapper (which only overrides `cloud_name`), `test-gcp-vm-full` also
overrides `enable_filestore=true`, `enable_memorystore=true`, and
`vpc_public_subnet_cidr=10.103.0.0/16` on top of `cloud_name`/`common_prefix` - so running it
exercises the Filestore and Memorystore code paths by default, not just the toggles-off minimal
path. You still need your own required variables supplied first (via `terraform.tfvars` or
`TF_VAR_*` environment variables).

If you only need one half of the cycle (for example, debugging a failed apply without
re-destroying working resources), use the paired targets directly with a shared `SUFFIX` - they
carry the same Filestore/Memorystore/subnet overrides as `test-gcp-vm-full`:

```bash
make apply-gcp-vm-full SUFFIX=dev1
# ... inspect, iterate ...
make destroy-gcp-vm-full SUFFIX=dev1
```

Real GCP costs apply for as long as resources stay up - this example creates no compute configs
and launches no Ray clusters itself, so the ongoing cost is the project/VPC/GCS infrastructure
itself, plus the Filestore and Memorystore instances when their toggles are enabled (which they are
by default under `make test-gcp-vm-full`).

## Key variables

| Variable | Description | Default |
|----------|-------------|---------|
| `gcp_region` | GCP region for all resources | *(required, no default)* |
| `gcp_zone` | GCP zone for zonal resources; also Filestore's location when `enable_filestore` is on | *(required, no default)* |
| `billing_account_id` | Billing account to associate with the new GCP project | *(required, no default)* |
| `root_folder_number` | GCP folder the new project is created under | *(required, no default)* |
| `customer_ingress_cidr_ranges` | CIDR block allowed through the firewall on 443 | *(required, no default)* |
| `anyscale_org_id` | Your Anyscale organization ID (`org_...`) | *(required, no default)* |
| `cloud_name` | Name shared by the created cloud and its attached cloud resource | `tf-gcp-vm-test` |
| `is_private_cloud` | Whether this is a private cloud; feeds both `is_private_cloud` on the cloud and `is_private` on the resource | `false` |
| `auto_add_user` | Whether to automatically add users | `true` |
| `compute_stack` | Compute stack for the attached resource (`VM` or `K8S`) - exposed as its own variable here, unlike `aws-vm`, which hardcodes `compute_stack = "VM"` as a literal | `VM` |
| `anyscale_deploy_env` | Deployment environment tag (`production`, `test`, or `development`) | `test` |
| `common_prefix` | Prefix for GCP resource names; must be unique per scenario/run | `as-gcp-vm-` |
| `labels` | A map of labels applied to every taggable GCP resource | `{test = "true", environment = "test"}` |
| `enable_filestore` | Feature toggle: create a Filestore instance and attach it via `file_storage` | `false` |
| `enable_memorystore` | Feature toggle: create a Memorystore instance and wire it into `gcp_config` for Ray GCS fault tolerance | `false` |
| `vpc_public_subnet_cidr` | CIDR block for the public subnet | `10.100.0.0/16` |

## Outputs

| Output | Description |
|--------|-------------|
| `cloud_id` | ID of the created Anyscale cloud |
| `cloud_name` | Name of the created Anyscale cloud |
| `is_empty_cloud` | Whether the cloud was created as an empty shell - the starting point of the multi-resource pattern this example uses |
| `cloud_resource_id` | ID of the attached cloud resource |
| `cloud_resource_name` | Name of the attached cloud resource |
| `filestore_name` | The Filestore instance name; `null` unless `enable_filestore = true` |
| `memorystore_id` | The Memorystore instance ID; `null` unless `enable_memorystore = true` |

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
either alone will fail this example partway through. If you're running with `enable_filestore` or
`enable_memorystore` on, confirm those same credentials also cover Filestore/Memorystore instance
creation - a permission gap there surfaces partway through apply too, after the cheaper resources
already exist.

**`terraform apply` seems to hang** - Filestore instances in particular are slow to provision
relative to the rest of this example's infrastructure. Check the instance's status in the GCP
console before assuming `apply` is stuck.

**A second `anyscale_cloud_resource`, or one re-applied after lost state, fails with a `409`** -
the multi-resource cloud pattern this example uses has a few sharp edges worth knowing before you
adapt it elsewhere: `name` only has to be unique per cloud but is required with no default, a
collision on the same cloud fails loud with an explicit `409` rather than adopting the existing
resource, and recovering a lost state entry needs `terraform import`, not a plain re-apply. See
[Multiple resource deployments on one
cloud](../../docs/guides/cloud-resources.md#multiple-resource-deployments-on-one-cloud) for the
full detail.

## See also

- [gcp-vm-basic](../gcp-vm-basic/) - the all-in-one pattern equivalent on GCP, without Filestore or
  Memorystore
- [aws-vm](../aws-vm/) - the equivalent multi-resource pattern on AWS (EFS and MemoryDB instead of
  Filestore and Memorystore)
- [kitchen-sink](../kitchen-sink/) - a more advanced multi-resource cloud, attaching both a VM and
  a K8S resource to the same cloud
- [Cloud Resources: Provider Support, Naming, and Known Limitations](../../docs/guides/cloud-resources.md) -
  the behavior-and-limitations reference for the multi-resource cloud pattern this example uses
- [Cloud resource documentation](../../docs/resources/cloud.md) and [Cloud resource (deployment)
  documentation](../../docs/resources/cloud_resource.md)
