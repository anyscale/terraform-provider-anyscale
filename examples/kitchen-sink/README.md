# Kitchen Sink Example

Every resource and data source this provider registers, wired together into one comprehensive,
multi-cloud configuration. This absorbs what used to be a separate `multi-resource-cloud-basic`
example (multiple resource deployments on one cloud) as one piece of a larger build, so it is now
the single place to see the whole provider surface working together. Use the other, smaller
examples in this directory for a focused look at any one piece.

> [!WARNING]
> **This is not a "-basic" example.** Building fresh via modules means this apply creates a real
> VPC and a real EKS cluster, not just Anyscale-side resources. Expect a noticeably longer apply
> (the EKS cluster alone can take 15-20+ minutes to become ready) and real AWS spend for as long as
> the infrastructure exists. Read the whole "Before you apply" section below before running this.

## What this creates

**Shared infrastructure (built once, fed into everything below):** one AWS VPC, one S3 bucket, and
one EKS cluster, built via the same module pattern as [aws-eks-basic](../aws-eks-basic/) — in
`infra_aws.tf` and `infra_eks.tf`. Both Anyscale Clouds and all three cloud resources point at this
one set of infrastructure; nothing here provisions a second VPC, bucket, or cluster.

IAM roles are the one exception: Cloud A's VM leg and Cloud B each get their own IAM role module
call rather than sharing one. Not a missed optimization — the backend enforces a real unique index
on a cloud's `external_id`, scoped per org, so two clouds can't present the identical value. Both role sets
derive their `external_id` from the same `var.anyscale_external_id` you set once, suffixed `-a`
and `-b` internally, so you still only supply one value. These are cheap, fast IAM resources
(seconds, not the VPC/EKS/S3 layer's minutes), so splitting them doesn't undercut "one shared VPC."
Cloud A's EKS leg is unaffected either way — `kubernetes_config` has no `external_id` field at all;
that path authenticates via in-cluster pod identity, not a cross-account assume-role.

**Two Anyscale Clouds, three resource deployments:**

| Resource | What it does here |
| --- | --- |
| `anyscale_cloud` (Cloud A) | Empty cloud, BYOC/multi-resource cloud pattern, `compute_stack` omitted on the parent |
| `anyscale_cloud_resource` (`a_vm`) | A VM compute stack attached to Cloud A, on the shared VPC. Created first, which is what makes it the primary/default deployment — the backend assigns "primary" to whichever resource lands first, it isn't a settable field. |
| `anyscale_cloud_resource` (`a_eks`) | A K8S (EKS) compute stack attached to the *same* Cloud A, on the *same* shared VPC — this is the multiple-resources-on-one-cloud and mixed-compute-stack coverage in one place. `depends_on` the VM resource so it's created second and never mistaken for the primary. |
| `anyscale_cloud` (Cloud B) | A second, independent cloud — all-in-one VM pattern (`aws_config` embedded directly), also on the shared VPC |

See the [Cloud Resources guide](../../docs/guides/cloud-resources.md#multiple-resource-deployments-on-one-cloud)
for the cardinality rules Cloud A's two resources rely on (BYOC-only, per-cloud-unique `name`,
no "adopt" on a collision).

**Three compute configs — `anyscale_compute_config`, one per targeting style:**

| Compute config | Targets | Demonstrates |
| --- | --- | --- |
| `cc_a_default` | Cloud A, no `cloud_resource` set | The default: lands on Cloud A's primary (VM) deployment |
| `cc_a_eks` | Cloud A, `cloud_resource = anyscale_cloud_resource.a_eks.name` | Targeting a *specific*, non-primary deployment by name — the hook that makes multiple resource deployments actually usable |
| `cc_b` | Cloud B | An ordinary single-resource cloud, for contrast |

Every one of these lists `worker_nodes` explicitly — omitting it launches zero workers rather than
falling back to a sane default; see the [Compute Config guide](../../docs/guides/compute-config.md).

**Container images, projects, and org resources (4 applied automatically, 2 shown but not applied by default):**

| Resource | What it does here |
| --- | --- |
| `anyscale_container_image_build` | A training image built from an inline Containerfile |
| `anyscale_container_image_registry` | A base image registered from a public registry |
| `anyscale_project.a` | A project scoped to Cloud A |
| `anyscale_project.b` | A project scoped to Cloud B |
| `anyscale_organization_invitation` | **Opt-in.** Zero instances unless you set `var.invite_email`; see below |
| `anyscale_organization_collaborator` | Shown, not applied — see the note below |

`anyscale_organization_collaborator` is import-only (no `Create`) and manages an *existing* org
member's permissions, so it can't be part of a one-shot `apply` the way everything above is. It's
included, commented out, in `organization.tf` with the import command you'd run once a real member
exists. See [`organization_user_workflow`](../resources/organization_user_workflow/main.tf) for the
full invite -> wait -> import -> manage lifecycle.

**Data sources (all 13 registered by the provider):** `anyscale_cloud`, `anyscale_clouds`,
`anyscale_compute_config`, `anyscale_container_image`, `anyscale_container_images`,
`anyscale_project`, `anyscale_projects`, `anyscale_user`, `anyscale_organization`,
`anyscale_organization_user`, `anyscale_organization_users`, `anyscale_services`,
`anyscale_service` — see `data_sources.tf`. Most read back the resources created above;
`anyscale_user` and `anyscale_organization` are zero-argument connection-level singletons (see the
[`anyscale_organization` data source docs](../../docs/data-sources/organization.md) for why those
two attributes live there and not on every resource).

`anyscale_projects` filtered to Cloud A will show **2** projects, not 1 — every new cloud gets an
auto-created `default` project from the backend in addition to `project.a`. Not a bug and not
something this data source got wrong; if you're counting projects per cloud elsewhere, expect that
implicit extra one.

`anyscale_services` is unconditional and filters by `anyscale_project.a`'s id — safe even with zero results.
`anyscale_service` (singular) is **opt-in**: it needs an id or name to look up, and this provider
has no matching resource to create one with, so a fresh apply sets it to zero instances unless you
set `var.existing_service_name` to a real, pre-existing service in your org.

Global Resource Scheduler (machine pools) resources/data sources are not included — they're
currently disabled in the provider (`internal/provider/provider.go`), pending a backend API
rework, so there's nothing to wire up yet.

## Before you apply

1. **Real AWS cost and apply time.** Unlike every "-basic" example in this directory, this one
   does not assume existing infrastructure — it builds a VPC and an EKS cluster for you via
   modules, the same way [aws-eks-basic](../aws-eks-basic/) does. Budget real time (EKS cluster
   creation alone is commonly 15-20+ minutes) and real AWS spend for as long as you leave it up.
   **Unlike [aws-eks-basic](../aws-eks-basic/), this example has no `make test-kitchen-sink`
   wrapper and no exit-trap destroy** — running it is a plain `terraform apply`/`terraform
   destroy` by hand, and this repo's sweeper only ever reaches Anyscale-side resources, never into
   your own AWS account. A forgotten or abandoned `terraform destroy` here leaves a real VPC and
   EKS cluster billing indefinitely, with nothing automated to clean it up. Track your own state
   and tear it down when you're done.
2. **This creates two real clouds, two projects, three compute configs, and two container images**
   in your Anyscale org, plus the AWS infrastructure above.
3. **The real invitation email is opt-in, not automatic.** Leave `invite_email` unset (the
   default) and `anyscale_organization_invitation` applies zero instances. Set it to an address you
   own or control if you want to exercise that resource — it will receive a real invitation.
4. **The existing-service lookup is opt-in, not automatic.** Leave `existing_service_name` unset
   (the default) and the singular `anyscale_service` data source applies zero instances. Set it to
   the name of a real service already running in your org if you want to exercise that lookup.
5. **Re-applying against the same org**: every name is derived from `var.name_prefix` (default
   `kitchen-sink`) and is stable across reapplies — nothing timestamp-based. Either
   `terraform destroy` between runs or change `name_prefix` to avoid the `409` collision the
   [Cloud Resources guide](../../docs/guides/cloud-resources.md#multiple-resource-deployments-on-one-cloud)
   describes for a duplicate `anyscale_cloud_resource` name on the same cloud.
6. **Cloud B enables the system cluster (`enable_system_cluster = true`).** This only flips a
   config flag - it does not by itself provision a running cluster, so there is no leaked
   infrastructure or ongoing cost from this alone (real compute is created lazily, only if
   something opens the console's Observability tab for this cloud). The one residual risk: if a
   cluster is running when you `terraform destroy`, the backend actively terminates it first and
   waits, but blocks the destroy outright (a loud `409`, not a silent leak) if termination does not
   finish in time. This is not expected in normal use of this example - noted for completeness, not
   because it is likely.
7. **Already have your own VPC and EKS cluster?** The module wiring lives entirely in
   `infra_aws.tf` and `infra_eks.tf`, feeding the `anyscale_*` resources through a small number of
   outputs (VPC/subnet/security-group IDs, IAM role ARNs, the EKS operator identity and zones).
   Swap those two files for variables holding your existing infrastructure's IDs — matching the
   pattern [aws-vm-basic](../aws-vm-basic/) and [gcp-gke-basic](../gcp-gke-basic/) already use — and
   the rest of the configuration is unaffected. This is a structural note, not a second maintained
   code path: the files in this repo build fresh.

## Known limitations this example runs into

- **A first apply may need one retry for Cloud A's project and default compute config.**
  `anyscale_project.a` and `anyscale_compute_config.cc_a_default` are both scoped to Cloud A, and
  if Terraform schedules their creation only moments after Cloud A itself, they can fail with a
  bare `403 Permission denied` — a backend permission-propagation lag on a freshly-created cloud,
  not anything wrong with your configuration. Re-running `terraform apply` with no changes
  resolves it; a second `terraform plan` afterward shows no diff. This isn't specific to
  `cc_a_default` or `project.a` by name — it's a timing race tied to *how soon* a cloud-scoped
  resource is created after its parent cloud, so it can in principle land on any resource attached
  to a just-created cloud, not only the ones named here. If a retry doesn't resolve it, that's a
  different, more persistent case worth reporting rather than retrying indefinitely.
- **Replacing the EKS resource can hit a backend `500`.** A destroy-then-recreate of `a_eks` (any
  change to a `RequiresReplace` attribute) can fail on the re-attach step — a known backend issue
  specific to the AWS + multi-resource + K8S combination this example uses. Initial creation is unaffected,
  and it's under investigation upstream; it is not a bug in this provider. See the
  [Cloud Resources guide](../../docs/guides/cloud-resources.md) for the full note.
- **This proves attachment and configuration, not a running workload.** `infra_eks.tf`'s node
  group is sized for cluster system components, not Ray workloads, and confirming that the
  Anyscale Operator is installed and actually running one is a distinct validation step, still in
  progress upstream. `cc_a_eks` demonstrates a `cloud_resource`-scoped compute config resolving and
  applying cleanly against the EKS deployment — treat that as the extent of what's verified here,
  not already a known-good story, if you extend this example to submit real workloads.

## The one Terraform gotcha this example exists to show

Every singular data source below (`anyscale_cloud.lookup_a`, `anyscale_compute_config.lookup`, etc.)
looks up the resource created earlier in this same configuration **by referencing that resource's
own attribute** — e.g. `name = anyscale_cloud.a.name` — not by repeating the same literal string in
both places. That attribute reference is what gives Terraform a dependency edge: it defers the data
source read until after the resource exists. Hardcode the same name in both blocks instead and
there's no such edge — on a first apply, Terraform is free to read the data source before the
resource is created, and the lookup 404s. The same rule is why `cc_a_eks` references
`anyscale_cloud_resource.a_eks.name` instead of a literal string for `cloud_resource`: without that
reference, Terraform has no reason to wait for `a_eks` to exist first. See the comment at the top
of `data_sources.tf` for the list-lookup equivalent (`depends_on`, since there's no attribute to
reference).

## Usage

```bash
cp terraform.tfvars.example terraform.tfvars
# edit terraform.tfvars with your own AWS account details

terraform plan
terraform apply
```

## See also

- [Kitchen Sink tour guide](../../docs/guides/kitchen-sink-tour.md) — a curated tour of this example, condensed from this README
- [Cloud Resources guide](../../docs/guides/cloud-resources.md)
- [Compute Config guide](../../docs/guides/compute-config.md)
- [Container Images guide](../../docs/guides/container-images.md)
- [aws-eks-basic](../aws-eks-basic/) — the module wiring this example's shared VPC/EKS cluster is
  based on, in isolation
- [`organization_user_workflow`](../resources/organization_user_workflow/main.tf) — the invite/
  import lifecycle for `anyscale_organization_collaborator`
- Any `examples/resources/anyscale_*` or `examples/data-sources/anyscale_*` directory for a
  minimal, single-resource look at one schema in isolation
