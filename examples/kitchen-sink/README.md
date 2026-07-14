# Kitchen Sink Example

Every resource and data source this provider registers, wired together into one coherent
configuration. Use this to see the whole provider surface working at once; use the other examples
in this directory for a focused look at any one piece.

## What this creates

**Resources (7 applied automatically):**

| Resource | What it does here |
| --- | --- |
| `anyscale_cloud` | An empty cloud (split deployment pattern) |
| `anyscale_cloud_resource` | An AWS VM compute stack attached to that cloud |
| `anyscale_compute_config` | A head node + autoscaling worker group |
| `anyscale_container_image_build` | A training image built from an inline Containerfile |
| `anyscale_container_image_registry` | A base image registered from a public registry |
| `anyscale_project` | A project scoped to the cloud above |
| `anyscale_organization_invitation` | A real invitation email to `var.new_member_email` |

**Plus one resource shown but not applied:** `anyscale_organization_collaborator` is import-only
(no `Create`) and manages an *existing* org member's permissions, so it can't be part of a
one-shot `apply` the way everything above is. It's included, commented out, in `organization.tf`
with the import command you'd run once `new_member` accepts their invitation. See
[`organization_user_workflow`](../resources/organization_user_workflow/main.tf) for that full
invite -> wait -> import -> manage lifecycle.

**Data sources (all 11 registered by the provider):** `anyscale_cloud`, `anyscale_clouds`,
`anyscale_compute_config`, `anyscale_container_image`, `anyscale_container_images`,
`anyscale_project`, `anyscale_projects`, `anyscale_user`, `anyscale_organization`,
`anyscale_organization_user`, `anyscale_organization_users` — see `data_sources.tf`. Most read back
the resources created above; `anyscale_user` and `anyscale_organization` are zero-argument
connection-level singletons (see the [`anyscale_organization` data source
docs](../../docs/data-sources/organization.md) for why those two attributes live there and not on
every resource).

Global Resource Scheduler (machine pools) resources/data sources are not included — they're
currently disabled in the provider (`internal/provider/provider.go`), pending a backend API
rework, so there's nothing to wire up yet.

## Before you apply

1. **This creates a real cloud, project, compute config, and two container images in your
   Anyscale org**, backed by AWS infrastructure that must already exist in your account (this
   example doesn't build fresh infrastructure via modules — see
   [aws-vm-basic](../aws-vm-basic/) or [aws-vm](../aws-vm/) for that).
2. **This sends a real email.** `new_member_email` has no default specifically so you can't apply
   this by accident with a placeholder address. Use one you own or control.
3. **Re-applying against the same org**: every name is derived from `var.name_prefix` (default
   `kitchen-sink`). Either `terraform destroy` between runs or change `name_prefix` to avoid
   colliding with a previous run's resources.

## The one Terraform gotcha this example exists to show

Every singular data source below (`anyscale_cloud.lookup`, `anyscale_compute_config.lookup`, etc.)
looks up the resource created earlier in this same configuration **by referencing that resource's
own attribute** — e.g. `name = anyscale_cloud.main.name` — not by repeating the same literal
string in both places. That attribute reference is what gives Terraform a dependency edge: it
defers the data source read until after the resource exists. Hardcode the same name in both blocks
instead and there's no such edge — on a first apply, Terraform is free to read the data source
before the resource is created, and the lookup 404s. See the comment at the top of
`data_sources.tf` for the list-lookup equivalent (`depends_on`, since there's no attribute to
reference).

## Usage

```bash
cp terraform.tfvars.example terraform.tfvars
# edit terraform.tfvars with your own AWS infrastructure IDs and an email you control

terraform plan
terraform apply
```

## See also

- [Cloud Resources guide](../../docs/guides/cloud-resources.md)
- [Container Images guide](../../docs/guides/container-images.md)
- [`organization_user_workflow`](../resources/organization_user_workflow/main.tf) — the invite/
  import lifecycle for `anyscale_organization_collaborator`
- Any `examples/resources/anyscale_*` or `examples/data-sources/anyscale_*` directory for a
  minimal, single-resource look at one schema in isolation
