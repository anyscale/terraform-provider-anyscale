---
page_title: "Cloud Resources: Provider Support, Naming, and Known Limitations"
subcategory: ""
description: |-
  Cloud provider support, naming differences between the Cloud resources and data sources, and known limitations.
---

# Cloud Resources: Provider Support, Naming, and Known Limitations

This guide covers cross-cutting behavior for the Cloud surface of the provider:
[`anyscale_cloud`](../resources/cloud.md) and [`anyscale_cloud_resource`](../resources/cloud_resource.md)
(resources), and [`anyscale_cloud`](../data-sources/cloud.md) and
[`anyscale_clouds`](../data-sources/clouds.md) (data sources). It exists because several of these
behaviors aren't obvious from any single resource's schema table.

## Supported cloud providers

**AWS** and **GCP** support both VM and Kubernetes (AKS-equivalent) compute stacks. **Azure**
supports Kubernetes only — Anyscale does not support Azure VM clouds, so setting
`cloud_provider = "AZURE"` (directly, via `azure_config`, or via auto-detection) together with any
`compute_stack` other than `"K8S"` (including the default when `compute_stack` is omitted) fails
at plan time with an explicit error, before anything reaches the API. `cloud_provider = "GENERIC"`
is not yet supported by this provider at all, for any compute stack, and fails the same way.

Unlike `aws_config`/`gcp_config`, `azure_config` takes a single field, `tenant_id`: AKS setup
creates no VNet/subnet resources of its own, and real authentication is operator
workload-identity federation (`kubernetes_config.anyscale_operator_iam_identity`, using the
managed identity's **principal ID**, not its client ID), not network or IAM-role wiring. Azure
object storage uses its own URI scheme, `abfss://<container>@<account>.dfs.core.windows.net` —
pass the complete URI in `object_storage.bucket_name` yourself; unlike AWS's `s3://` and GCP's
`gs://`, this provider does not prepend or strip any scheme for Azure, and a bucket name that
doesn't already start with `abfss://` fails at plan time.

Kubernetes (`compute_stack = "K8S"`) is supported for AWS, GCP, and Azure, via either the
**all-in-one pattern** (`anyscale_cloud` with an embedded `kubernetes_config` block) or the
**split pattern** (an empty `anyscale_cloud` plus a separate `anyscale_cloud_resource` with
`compute_stack = "K8S"`). Previously, a K8S-only configuration was misclassified as an empty cloud
and silently never created any deployment at all; that's fixed, and the provider now correctly
creates and round-trips a K8S cloud (no more `compute_stack` flipping from `"K8S"` to `"VM"` on the
next read) for both patterns.

Precisely what that fix has been validated against, so this claim doesn't overreach: for AWS and
GCP, it's confirmed against the real Anyscale API, for both patterns, both providers. It has
**not** been separately confirmed end-to-end against a real EKS/GKE cluster with the Anyscale
Operator actually installed and running a workload — that's a distinct validation step, still in
progress. If you're standing up a new EKS/GKE cluster specifically to test this, treat that path
as being actively verified rather than already a known-good story. **Azure/AKS support is newer
still and validated only at the schema and mock-server level** — there is no real Azure
subscription in this provider's test environment, so unlike EKS/GKE, AKS has no real-cluster
acceptance coverage at all yet. Validate the AKS example against your own Azure subscription
before relying on it.

One thing to get right in a split deployment regardless: on the parent `anyscale_cloud`, **omit
`compute_stack` entirely** — an empty cloud derives its compute stack from whichever resource ends up
attached to it, and setting `compute_stack` explicitly on the parent produces an inconsistency. Set
`compute_stack` on the `anyscale_cloud_resource` only.

**Known limitation (AWS split pattern only):** replacing an AWS K8S `anyscale_cloud_resource` — a
destroy-then-recreate triggered by changing any `RequiresReplace` attribute — can hit a backend `500`
on the re-attach step. Initial creation is unaffected, and GCP is unaffected either way; this is a
backend issue under investigation, not something this provider can currently retry around.

A K8S cloud with no explicit `region` and nothing to infer one from produces a clear error at plan time
rather than sending an empty region to the API — set `region` explicitly on the resource.

## Renaming a cloud

`anyscale_cloud.name` is immutable after creation — there's no API endpoint to rename a cloud at all.
Changing `name` in your configuration produces a plan-time error ("Cloud Name Is Immutable") rather
than either an update or an automatic replacement; the provider deliberately does not mark `name` as
`RequiresReplace`, since that would silently destroy and recreate a live cloud on `apply` the moment
someone's configuration drifted, with no chance to review it first. To rename a cloud, destroy and
recreate it deliberately.

The other mutable fields on `anyscale_cloud` — `auto_add_user`, `enable_lineage_tracking`, and
`enable_log_ingestion` — update in place normally; each is backed by its own dedicated API endpoint,
called only when its value actually changes.

## Kubernetes operator status

`anyscale_cloud_resource` exposes three Computed attributes sourced from the Anyscale Operator running
in a Kubernetes cluster: `operator_status` (the same value as `status`, named explicitly for clarity),
`operator_version`, and `reported_at`. All three are null for VM resources, and null for a Kubernetes
resource whose operator hasn't reported in yet — they populate once it has.

## Multiple resource deployments on one cloud

A cloud can have more than one `anyscale_cloud_resource` attached to it — for example, to add a
second AWS region or a second compute stack alongside the one a cloud started with. The
[`kitchen-sink`](../../examples/kitchen-sink) example includes a complete, working two-resource
configuration: its Cloud A attaches both a VM and a K8S resource to the same cloud, which doubles
as the mixed-compute-stack case described above.

A few things aren't obvious from the resource's schema alone:

- **Only registered (BYOC) clouds accept additional resources.** A cloud that Anyscale manages
  end-to-end (never had your own infrastructure explicitly attached) rejects a second
  `anyscale_cloud_resource` at apply time with a `400`. To use more than one resource, the cloud needs
  to be the "bring your own cloud" kind from the start.
- **`name` is required, and only has to be unique per cloud, not globally.** Give every
  `anyscale_cloud_resource` on the same cloud its own distinct value — there's no default to fall back
  on, so this is enforced by Terraform at plan time (a missing `name` fails before anything is sent to
  the API), not discovered later as a collision.
- **A `name` collision on the same cloud fails loud, never silent.** If two resources on the same cloud
  end up with the same `name`, `apply` fails with the API's own `409`: `A cloud deployment with the name
  <name> already exists in this cloud.` There's no "adopt" behavior — every `anyscale_cloud_resource` you
  apply either creates a new backend resource or fails outright, never merging into or silently attaching
  to one that's already there. If you land on this page from that error message: check for a duplicate
  `name` among the `anyscale_cloud_resource` blocks attached to the same cloud, including ones in other
  `.tf` files or workspaces targeting the same `cloud_id`.
- **State loss recovers via `terraform import`, not a plain re-apply.** If a state entry for an
  `anyscale_cloud_resource` is lost while the backend resource itself is still alive, re-running `apply`
  sends the same `name` your configuration already specifies, so it hits the same `409` collision rather
  than reconciling the two or creating a duplicate. `terraform import` is the fix — see
  [Import](../resources/cloud_resource.md#import) for the `cloud_id:name` syntax.

## Upgrading to a required `name`

As of v0.2.0, `name` on `anyscale_cloud_resource` is `Required` — it has no default, and a
configuration that omits it now fails at `terraform plan`, before anything reaches the API. Add an
explicit, non-empty `name` to every `anyscale_cloud_resource` block; that's the entire change.

Previously, an omitted `name` fell back to a value the provider computed from
`compute_stack`/`cloud_provider`/`region`. That computed value was never echoed back by the API, so
it produced a permanent plan diff on every run after the first. Requiring `name` closes that at the
source instead of working around it — see [Multiple resource deployments on one
cloud](#multiple-resource-deployments-on-one-cloud) above for what a good value looks like once a
cloud has more than one resource attached.

## Naming differences between resources and data sources

A few concepts are named differently depending on which resource or data source you're looking at.
These are intentional (renaming any of them would be a breaking change), not something to work around:

- **Lineage tracking**: `enable_lineage_tracking` on the `anyscale_cloud` resource and the singular
  `anyscale_cloud` data source; `lineage_tracking_enabled` on the plural `anyscale_clouds` data source.
- **Log ingestion**: `enable_log_ingestion` on the `anyscale_cloud` resource and the singular
  `anyscale_cloud` data source; `is_aggregated_logs_enabled` on the plural `anyscale_clouds` data source.
- **Private networking**: `is_private_cloud` on the `anyscale_cloud` resource refers to the cloud as a
  whole; `is_private` on the `anyscale_cloud_resource` resource refers to that specific resource
  deployment. They are distinct attributes on distinct objects, not a typo.

## Deprecated attributes

### Kubernetes configuration fields with no effect

`kubernetes_config.namespace`, `ingress_host`, `cluster_name`, `context`, and `kubeconfig_path` are
marked Deprecated on both `anyscale_cloud` and `anyscale_cloud_resource` (`terraform plan`/`validate`
will warn on them): the provider never sends them to the Anyscale API, so setting them has no effect
on the deployed cloud, and never has. They're deprecated rather than removed immediately, since
removing a schema attribute outright is itself a breaking change; they'll be removed in a future major
release, with migration guidance at that time — remove them from your configuration now rather than
waiting. Only `anyscale_operator_iam_identity`, `zones`, and `redis_endpoint` in `kubernetes_config`
actually reach the API today.

### `is_k8s` on the `anyscale_clouds` data source

`is_k8s` is superseded by `compute_stack` (`"VM"` or `"K8S"`), which is exposed everywhere `is_k8s` is
and carries the same information plus room to grow (e.g. a future compute stack type wouldn't fit a
boolean). Prefer `compute_stack == "K8S"` in new configurations.

## Credentials handling

`anyscale_cloud.credentials` is write-only: the provider never reads it back from the API, so it will
never appear in a `terraform plan` diff after creation and is never exposed through either data source.
Treat the value itself (an AWS IAM role ARN, or a JSON blob for GCP) as sensitive in your own tooling —
the provider marks the attribute `Sensitive` in state, but redaction is only as good as how you source
the value into your configuration.

If you don't set `credentials` and the provider can't derive one from `aws_config`/`gcp_config` either
(most commonly: you set the config block but left out the actual role or service-account field), it
generates a placeholder so `apply` can still succeed — but the resulting cloud won't be able to
provision any real infrastructure. For an all-in-one cloud, the provider now warns
("Placeholder Credentials Generated") when this happens, so it isn't silent. This case doesn't warn for
the empty-cloud pattern, since a placeholder there is the expected, intentional starting point — real
credentials arrive later with whatever `anyscale_cloud_resource` gets attached.

## Deleting a cloud

Deleting an `anyscale_cloud` resource first detaches any machine pools attached to it, then deletes the
cloud itself. This happens automatically — there's no separate step to take beforehand.

## Importing an existing cloud

If you already manage a cloud's configuration through this provider, upgrading changes nothing:
config blocks are never touched by regular `plan`/`apply` reads, only by `terraform import` itself, so
there's nothing new for an already-populated state to notice.

Plain `terraform import` on `anyscale_cloud` or `anyscale_cloud_resource` used to leave every config
block null (import only ever set `id`), which meant the next `plan` saw your `.tf` configuration as a
brand new addition to a `RequiresReplace` attribute and forced a destructive replace of a live cloud.
Import now recovers config directly, as part of the import step itself — but deliberately only the
block(s) that compute stack requires, and nothing else:

- A **VM** cloud/resource recovers `aws_config` (AWS) or `gcp_config` (GCP) — whichever matches its
  provider. Azure has no VM variant to recover (see [Supported cloud providers](#supported-cloud-providers)
  above).
- A **K8S** cloud/resource recovers both `kubernetes_config` and `object_storage` — both are required
  for K8S regardless of provider, Azure included.
- Everything else — `file_storage` always, `object_storage` for VM, `aws_config`/`gcp_config`/
  `azure_config` for K8S — is **never** recovered at import, even if you actually configured it. Add
  these to your `.tf` after importing if you use them; expect a replace on the next `apply`; there's
  no supported way around that for an optional block. This is deliberate, not an oversight: recovering
  an optional block can't distinguish "you didn't configure this" from "recovered at import," and only
  a required block avoids that ambiguity in the first place.

A few more things worth knowing:

- **On AWS and GCP, `bucket_name` is scheme-tolerant: a bare name and its scheme-prefixed form
  (`s3://`, `gs://`) count as the same value.** A plan modifier treats a scheme-only difference
  between your configuration and the recovered state as equal, so it never forces a diff or a
  replace (`bucket_name` is `RequiresReplace`) — whether right after importing a K8S cloud or on
  any later plan. This is deliberately NOT achieved by canonicalizing to one form: forcing
  everyone's bare names to gain a prefix (or vice versa) would spuriously replace existing clouds
  already written the other way, so both forms simply compare equal instead. **Azure is
  different**: `abfss://<container>@<account>.dfs.core.windows.net` is a compound URI with no bare
  equivalent, so there's no scheme-tolerance to speak of there — write the exact URI, since it's
  the only valid form to begin with (see [Supported cloud providers](#supported-cloud-providers)
  above).
- **`compute_stack` is read from the cloud's own attached resource, not just the cloud-level
  summary field.** `GET /clouds/{id}` includes its own `compute_stack`, but that value is
  backend-derived from whichever resource the API considers primary, and defaults to `VM` if it
  doesn't recognize one. Every `anyscale_cloud` and `anyscale_cloud_resource` read now prefers the
  attached (default) resource's own `compute_stack` - the same resource used to recover
  `kubernetes_config`/`object_storage` at import - falling back to the cloud-level field only when
  no resource can be listed at all (a genuinely empty cloud, or a failed API call). A cloud with
  exactly one resource resolves to that resource even if nothing is explicitly flagged as the
  default. In the common case (a K8S cloud created and later re-read through Terraform) the two
  values already agreed, so this is defense-in-depth rather than a behavior change you'd notice day
  to day - it specifically protects a cold import of a cloud registered outside Terraform (e.g. via
  the CLI) from ever showing `compute_stack = "VM"` for what is actually a K8S cloud.
- The five inert `kubernetes_config` fields (see [Deprecated attributes](#deprecated-attributes) above)
  can't be populated from an API that never received them: `namespace` comes back as its documented
  default (`"anyscale"`); `ingress_host`, `cluster_name`, `context`, and `kubeconfig_path` come back
  null regardless of what your configuration says. They have no effect either way, so this doesn't
  produce a plan diff on those specific fields — just don't expect them to round-trip.
- Importing an `anyscale_cloud` that was originally created empty (the split pattern) but already has a
  resource attached looks, from the API, identical to one created all-in-one — both simply have a
  default resource present. Importing in that situation recovers the resource's required block(s) into
  the *cloud's* own configuration, as if it were all-in-one. If you're recovering a split deployment,
  import the `anyscale_cloud_resource` itself as well to get resource-level configuration into the
  right place.

## Known gaps (not yet supported)

- **VPC peering** (`vpc_peering_ip_range`, `vpc_peering_target_project_id`, `vpc_peering_target_vpc_id`)
  is read from the Anyscale API internally but not yet exposed as a Terraform attribute on any Cloud
  resource or data source.
- **External Kubernetes connectors** (the KubeRay-based connector configuration, including OIDC/JWKS
  settings) are not modeled by this provider.
- A handful of platform-computed fields aren't surfaced yet, since they're informational rather than
  configurable: `external_id`, `availability_zones`, `cluster_management_stack_version`, and the
  Anyscale-managed infrastructure IDs created when provisioning a cloud (AWS CloudFormation stack ID;
  GCP Deployment Manager / Infrastructure Manager IDs).
- **`max_stopped_instances`** (an instance-reuse cap) isn't exposed either, deliberately: no API
  endpoint updates an existing cloud's configuration at all today, only cloud creation accepts one, so
  supporting it as a normal `Optional`/`Computed` attribute would need new plumbing for something that
  could only ever be set once and never changed in place — deferred as low-value for the scope of this
  effort.
- `is_private_service_cloud`, `is_bring_your_own_resource`, and `is_aioa` can be read via either the
  singular `anyscale_cloud` data source or the plural `anyscale_clouds` data source, but cannot be set
  — there's no supported way to create a cloud with any of the three from this provider today.
- The Kubernetes Anyscale Operator's individual named health checks (`check_results` in the API's
  `operator_status_details`) aren't surfaced as their own attribute yet — see
  [Kubernetes operator status](#kubernetes-operator-status) above for what is.
