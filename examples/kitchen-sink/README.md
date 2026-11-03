# Kitchen Sink Example

A representative subset of this provider's resources and data sources, wired together into one
comprehensive, multi-cloud configuration. [What it covers and what it leaves out](#what-is-included-gated-and-excluded)
is listed below. This absorbs what used to be a separate `multi-resource-cloud-basic`
example (multiple resource deployments on one cloud) as one piece of a larger build. Use the other, smaller
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
derive their `external_id` from the same `var.anyscale_external_id` you set once (it must start with your
Organization ID, e.g. `org_abc123xyz-kitchen-sink`; the AWS IAM module rejects anything else at plan time), suffixed `-a`
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

**Container images, projects, and org resources:**

| Resource | What it does here |
| --- | --- |
| `anyscale_container_image_build` | A training image built from an inline Containerfile |
| `anyscale_container_image_registry` | A base image registered from a public registry |
| `anyscale_project.a` | A project scoped to Cloud A |
| `anyscale_project.b` | A project scoped to Cloud B |
| `anyscale_organization_invitation` | **Opt-in.** Zero instances unless you set `var.invite_email`; see below |
| `anyscale_organization_user` | **Opt-in.** Zero instances unless you set `var.org_user_email`; see below |

`anyscale_organization_user` manages a member's membership (not their role -
that's `anyscale_organization_user_role`). Set `org_user_email` to an existing member to adopt them;
an address that is not a member yet is sent a real invitation. Destroy never removes a member
(it cancels a still-pending invitation). See
[`organization_user_workflow`](../resources/organization_user_workflow/main.tf) for the full
invite -> wait -> adopt -> manage lifecycle.

**Data sources:** `anyscale_cloud`, `anyscale_clouds`,
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

`anyscale_services` (filtered to `anyscale_project.b`) and `anyscale_service` read back the
`anyscale_service.hello` this example creates, so both always apply.

## What is included, gated, and excluded

**Included (applied by default, on Cloud B):**

- `anyscale_runtime_env_package` packages `./app`, and `anyscale_service` runs it as a Ray Serve
  hello-world on the built image. Apply waits for `RUNNING`; destroy terminates the service.
- `anyscale_system_cluster` keeps running after `terraform destroy`: removing the resource only drops it from state, and deleting Cloud B does not stop it.
- `anyscale_cloud_iam_mapping` maps jobs and services to Cloud B's own node role (a no-op at runtime).
- Data sources `anyscale_system_cluster`, `anyscale_cloud_iam_mapping` and `anyscale_role` (`project_viewer`).

**Gated (zero instances unless you set the variable):**

| Variable | Enables | Needs |
| --- | --- | --- |
| `org_user_email` | `anyscale_organization_user` and its data source | An existing member's email (a non-member gets a real invitation) |
| `enable_user_groups` | `anyscale_user_group`, `anyscale_user_group_members`, and the `anyscale_user_group` / `anyscale_user_groups` data sources | User groups enabled for your org (org-wide objects behind a feature flag) |
| `user_group_member_emails` | Group membership | Each email must be an accepted org member |
| `enable_role_binding` | `anyscale_role_binding` | `enable_user_groups`, and an org on role bindings (converted); legacy orgs answer `409` |

**Excluded (never declared here):**

- `anyscale_scheduler_config` - an org-wide singleton with no delete; destroy cannot undo it.
- `anyscale_organization_default_cloud` - an org-wide setting the API cannot clear.
- `anyscale_cloud_access`, `anyscale_organization_user_role`, and `anyscale_cloud.auto_add_user` - the
  deprecated legacy access model.
- Ephemeral `anyscale_service_credentials` - a root module has nothing to consume it; see its own example.

## Before you apply

1. **Real AWS cost and apply time.** Like every scenario example in this directory, this one builds
   its own infrastructure from scratch rather than assuming you already have some to point at — but
   it does more of it than any single "-basic" example: a shared VPC plus both a VM leg and an EKS
   cluster for Cloud A, and a second VM leg for Cloud B, all via the same modules
   [aws-eks-basic](../aws-eks-basic/) uses. Budget real time (EKS cluster creation alone is commonly
   15-20+ minutes) and real AWS spend for as long as you leave it up.
   `make test-kitchen-sink` runs the full cycle as a test (unique `name_prefix`, empty-plan and
   import checks, destroy, and a cleanup trap if a step fails). Run by hand, it is a plain
   `terraform apply`/`terraform destroy`: this repo's sweeper only reaches Anyscale-side
   resources, never your AWS account, so an abandoned apply leaves a real VPC and EKS cluster
   billing. Track your own state and tear it down when you're done.
2. **This creates two real clouds, two projects, three compute configs, two container images, and a
   running Ray Serve service** in your Anyscale org, plus the AWS infrastructure above. Deleting a
   cloud does not stop its running clusters: Cloud B's System Cluster (and any service cluster that has
   not finished terminating) keeps its EC2 instances, which pin the VPC's internet gateway, so
   `terraform destroy` can hang or leak the VPC. After destroying, terminate leftover instances in the
   VPC (or the cluster in the Anyscale console), then re-run destroy.
3. **The real invitation email is opt-in, not automatic.** Leave `invite_email` unset (the
   default) and `anyscale_organization_invitation` applies zero instances. Set it to an address you
   own or control if you want to exercise that resource — it will receive a real invitation.
4. **Org-wide pieces are opt-in.** `org_user_email`, `enable_user_groups`, `user_group_member_emails`
   and `enable_role_binding` (see [the gated table](#what-is-included-gated-and-excluded)) default to
   off. User groups and role bindings are org-wide objects; set them only in an org you can change.
5. **Re-applying against the same org**: every name is derived from `var.name_prefix` (default
   `kitchen-sink`) and is stable across reapplies — nothing timestamp-based. Either
   `terraform destroy` between runs or change `name_prefix` to avoid the `409` collision the
   [Cloud Resources guide](../../docs/guides/cloud-resources.md#multiple-resource-deployments-on-one-cloud)
   describes for a duplicate `anyscale_cloud_resource` name on the same cloud.
6. **Already have your own VPC and EKS cluster?** The module wiring lives entirely in
   `infra_aws.tf` and `infra_eks.tf`, feeding the `anyscale_*` resources through a small number of
   outputs (VPC/subnet/security-group IDs, IAM role ARNs, the EKS operator identity and zones).
   Swap those two files for variables holding your existing infrastructure's IDs and the rest of
   the configuration is unaffected — though no example in this repo demonstrates that swap today
   (every example here, including [aws-vm-basic](../aws-vm-basic/) and
   [gcp-gke-basic](../gcp-gke-basic/), builds its infrastructure fresh via modules). This is a
   structural note, not a second maintained code path.

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
  import lifecycle for `anyscale_organization_user`
- Any `examples/resources/anyscale_*` or `examples/data-sources/anyscale_*` directory for a
  minimal, single-resource look at one schema in isolation
