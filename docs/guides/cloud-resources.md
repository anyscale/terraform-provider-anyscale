---
page_title: "Cloud Resources: Provider Support, Naming, and Known Limitations"
subcategory: "Behavior & Limitations"
description: |-
  Cloud provider support, naming differences between the Cloud resources and data sources, and known limitations.
---

# Cloud Resources: Provider Support, Naming, and Known Limitations

This guide explains how the Cloud surface of the provider —
[`anyscale_cloud`](../resources/cloud.md) and [`anyscale_cloud_resource`](../resources/cloud_resource.md)
(resources), and [`anyscale_cloud`](../data-sources/cloud.md) and
[`anyscale_clouds`](../data-sources/clouds.md) (data sources) — behaves in practice: which cloud
providers and compute stacks are supported and how their configuration differs, how to rename,
delete, import, and attach multiple resources to a cloud, which attributes are deprecated,
removed, or not yet supported, and where naming diverges between a resource and its data source.

New to Anyscale clouds? For a step-by-step walkthrough that stands up a working AWS VM cloud from
scratch, start with the [Create a VM Cloud](./create-a-vm-cloud.md) guide; this page is the
behavior-and-limitations reference to reach for once you know the shape and need the specifics.

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
**multi-resource cloud pattern** (an empty `anyscale_cloud` plus a separate `anyscale_cloud_resource` with
`compute_stack = "K8S"`). See the [Anyscale Kubernetes documentation](https://docs.anyscale.com/clouds/kubernetes)
for cluster requirements and how `kubernetes_config`'s fields map to the Anyscale Operator installation.
Previously, a K8S-only configuration was misclassified as an empty cloud
and silently never created any deployment at all; that's fixed, and the provider now correctly
creates and round-trips a K8S cloud (no more `compute_stack` flipping from `"K8S"` to `"VM"` on the
next read) for both patterns.

Precisely what that fix has been validated against, so this claim doesn't overreach: for AWS and
GCP, it's confirmed against the real Anyscale API, for both patterns, both providers. For AWS
specifically, the fully-native Operator install path goes further: the
[`aws-eks-basic`](../../examples/aws-eks-basic) example (Envoy Gateway plus the Anyscale Operator,
via a two-phase `terraform apply`) has been confirmed end-to-end against a real EKS cluster — both
Gateway listeners reaching `Programmed`/`Accepted`/`ResolvedRefs: True`, the Operator pod running
`2/2` with zero restarts, a second `terraform plan` after the full apply reporting zero diff (real
convergence, not just a clean first apply), and `terraform destroy` tearing down cleanly with no
leaked load balancer (checked against the live AWS API, not just Terraform's own report). GKE has
**not** had the equivalent install-path validation yet — only cloud creation itself is confirmed
against the real API, as above. If you're standing up a new GKE cluster specifically to test the
Operator install (see [`gcp-gke-basic`](../../examples/gcp-gke-basic)), treat that path as still
being actively verified rather than already a known-good story. **Azure/AKS support is newer
still and validated only at the schema and mock-server level** — there is no real Azure
subscription in this provider's test environment, so unlike EKS/GKE, AKS has no real-cluster
acceptance coverage at all yet. Validate the [`azure-aks-basic`](../../examples/azure-aks-basic)
example against your own Azure subscription before relying on it.

One thing to get right in a multi-resource cloud deployment regardless: on the parent `anyscale_cloud`, **omit
`compute_stack` entirely** — an empty cloud derives its compute stack from whichever resource ends up
attached to it, and setting `compute_stack` explicitly on the parent produces an inconsistency. Set
`compute_stack` on the `anyscale_cloud_resource` only.

**Known limitation (AWS multi-resource cloud pattern only):** replacing an AWS K8S `anyscale_cloud_resource` — a
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

The other mutable fields on `anyscale_cloud` — `auto_add_user`, `enable_lineage_tracking`,
`enable_log_ingestion`, and `enable_system_cluster` — update in place normally; each is backed by
its own dedicated API endpoint, called only when its value actually changes. `enable_system_cluster`
is Optional-only rather than Optional+Computed like the other three (see its [schema
description](../resources/cloud.md#schema)) — set it like any other cloud-level boolean.

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
  deployment. They are distinct attributes on distinct objects, not a typo. Both are self-asserted
  flags: setting either to `true` does not configure, verify, or provision any VPN or PrivateLink
  connectivity — arranging that remains your own responsibility, not something either attribute gates.
  Prior to v0.15.3, setting `is_private_cloud` on an all-in-one `anyscale_cloud` never actually reached
  the API, so a real cloud got created and the very next apply failed with a generic "Provider produced
  inconsistent result after apply" error; that's fixed, and the value now round-trips correctly.

## Deprecated and removed attributes

### Removed: `cloud_deployment_id`

As of v0.13.0, `cloud_deployment_id` is gone — not deprecated, fully removed — from `anyscale_cloud`,
`anyscale_cloud_resource`, and the singular `anyscale_cloud` data source: the attribute, its schema
entry, and the backing model field no longer exist on any of the three. `cloud_resource_id` — already
present on all three surfaces by the time of this removal (`anyscale_cloud_resource` has had it since
long before v0.13.0; `anyscale_cloud` and its data source gained it earlier in the same v0.13.0 release)
— is the one-for-one replacement, carrying the same populated identifier `cloud_deployment_id` was
originally meant to expose. To migrate: replace every `cloud_deployment_id` reference in your
configuration or outputs with `cloud_resource_id`, and re-run `terraform plan` to confirm there's no
remaining diff.

The plural `anyscale_clouds` data source deliberately has **neither** attribute, before or after this
removal: populating `cloud_resource_id` per list item would cost an extra API call per cloud in the
result, which this data source intentionally avoids. Look up a specific cloud's `cloud_resource_id`
through the singular `anyscale_cloud` data source or either Cloud resource instead. See also [Naming
differences between resources and data sources](#naming-differences-between-resources-and-data-sources)
above for other cases where the singular and plural data sources deliberately don't expose identical
attributes.

### Kubernetes configuration fields with no effect

`kubernetes_config.namespace`, `ingress_host`, `cluster_name`, `context`, and `kubeconfig_path` are
marked Deprecated on both `anyscale_cloud` and `anyscale_cloud_resource` (`terraform plan`/`validate`
will warn on them): the provider never sends them to the Anyscale API, so setting them has no effect
on the deployed cloud, and never has. They're deprecated rather than removed immediately, since
removing a schema attribute outright is itself a breaking change; they'll be removed in a future major
release, with migration guidance at that time — remove them from your configuration now rather than
waiting. Only `anyscale_operator_iam_identity`, `zones`, and `redis_endpoint` in `kubernetes_config`
actually reach the API today.

### `is_k8s` on the `anyscale_cloud` and `anyscale_clouds` data sources

`is_k8s` is present on both the singular `anyscale_cloud` data source and the plural `anyscale_clouds`
data source — but not on either Cloud resource (`anyscale_cloud` or `anyscale_cloud_resource`). Wherever
it's exposed, `is_k8s` is superseded by `compute_stack` (`"VM"` or `"K8S"`), which is exposed everywhere
`is_k8s` is and carries the same information plus room to grow (e.g. a future compute stack type
wouldn't fit a boolean). Prefer `compute_stack == "K8S"` in new configurations.

## Credentials handling

`anyscale_cloud.credentials` is write-only: the provider never reads it back from the API, so it will
never appear in a `terraform plan` diff after creation and is never exposed through either data source.
Treat the value itself (an AWS IAM role ARN, or a JSON blob for GCP) as sensitive in your own tooling —
the provider marks the attribute `Sensitive` in state, but redaction is only as good as how you source
the value into your configuration.

If you don't set `credentials` explicitly, the provider derives one: from `aws_config`/`gcp_config` for
a VM cloud, or from `kubernetes_config.anyscale_operator_iam_identity` for a Kubernetes cloud on AWS,
GCP, or Azure alike. The Kubernetes derivation is ceremonial — real authentication is the operator's
own workload identity, not this field — but it's a real, valid credential, not a placeholder. Only when
derivation itself comes up empty (most commonly: a VM cloud's config block is present but left out the
actual role or service-account field, or a Kubernetes cloud omits `anyscale_operator_iam_identity`
entirely) does the provider fall back to generating a placeholder so `apply` can still succeed — the
resulting cloud won't be able to provision any real infrastructure. For an all-in-one cloud that hits
this fallback, the provider warns ("Placeholder Credentials Generated") so it isn't silent — in
practice, an all-in-one Kubernetes cloud only reaches this if it omits `anyscale_operator_iam_identity`
(needed for the Operator to authenticate at all, so a well-formed configuration always sets it); the
common case is a VM cloud with underivable credentials, not "any all-in-one cloud" unconditionally.
This case doesn't warn for the empty-cloud pattern, since a placeholder there is the expected,
intentional starting point — real credentials arrive later with whatever `anyscale_cloud_resource` gets
attached.

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
Import now also recovers `object_storage` and `file_storage` directly from the live resource, in
addition to the block(s) the compute stack requires:

- A **VM** cloud/resource recovers `aws_config` (AWS) or `gcp_config` (GCP) — whichever matches its
  provider — plus `object_storage` and `file_storage` whenever the live resource actually has them.
  Azure has no VM variant to recover (see [Supported cloud providers](#supported-cloud-providers)
  above).
- A **K8S** cloud/resource recovers `kubernetes_config` and `object_storage` — both are required for
  K8S regardless of provider, Azure included, so both have always been recovered — plus `file_storage`
  whenever the live resource actually has it, which is new.
- `aws_config`, `gcp_config`, and `azure_config` are still **never** recovered for a K8S cloud/resource,
  even if you actually configured one: K8S doesn't require a provider-specific block the way VM does,
  and unlike `object_storage`/`file_storage` these remain out of scope for import recovery. Add these
  to your `.tf` after importing if you use them; expect a replace on the next `apply`; there's no
  supported way around that for these specific blocks.

This is not purely new coverage: recovering `object_storage` through one shared code path for both
compute stacks also fixes a pre-existing bug in **K8S** import specifically. The backend fills in a
bucket's region to match the cloud's own region even when you never configured one, and previously
that backend-filled value was recovered verbatim — so a `.tf` that correctly left `region` unset still
saw a spurious diff after importing a K8S cloud. Import now recovers `region` only when it genuinely
differs from the cloud's own region, for both compute stacks, so a K8S import that matches your `.tf`
plans cleanly too.

One tradeoff worth knowing: at import time the provider can't see your `.tf`, only the live cloud, so
`object_storage` and `file_storage` recover whatever genuinely exists on the backend regardless of what
your configuration declares (with one deliberate exception — see `mount_targets` below). If a cloud's storage was auto-provisioned (or configured outside
Terraform) and was never written into your `.tf`, importing it now surfaces a one-time reconcile diff
on the very next `plan` — where before, that same gap stayed silent because these blocks were never
recovered at all. This is the intended tradeoff: a reviewable diff is safer than the destructive
replace this fix exists to prevent. It also only ever affects a fresh `terraform import` from here
forward — none of this runs outside `ImportState`, so an already-imported, already-applied cloud's
state (K8S included, even one already carrying the incorrect region described above) is completely
untouched by upgrading the provider.

A few more things worth knowing:

- **On AWS and GCP, `bucket_name` is scheme-tolerant: a bare name and its scheme-prefixed form
  (`s3://`, `gs://`) count as the same value.** A plan modifier treats a scheme-only difference
  between your configuration and the recovered state as equal, so it never forces a diff or a
  replace (`bucket_name` is `RequiresReplace`) — whether right after importing a cloud or on
  any later plan. This is deliberately NOT achieved by canonicalizing to one form: forcing
  everyone's bare names to gain a prefix (or vice versa) would spuriously replace existing clouds
  already written the other way, so both forms simply compare equal instead. **Azure is
  different**: `abfss://<container>@<account>.dfs.core.windows.net` is a compound URI with no bare
  equivalent, so there's no scheme-tolerance to speak of there — write the exact URI, since it's
  the only valid form to begin with (see [Supported cloud providers](#supported-cloud-providers)
  above).
- **`aws_config.subnet_ids` (the plain list form) and `subnet_ids_to_az` (the map form) now both
  round-trip cleanly on import, in either direction.** The backend always returns subnets sorted
  by availability zone, which previously looked like a real order change to Terraform's
  order-sensitive list comparison and forced a replace on import for any cloud registered with the
  plain list form. A plan modifier now treats the two forms as equivalent whenever they describe
  the same set of subnets, regardless of order, so importing a cloud that used either form plans
  cleanly. A genuine subnet change — adding, removing, or swapping a subnet — still correctly
  proposes a replace, exactly as before.
- **`aws_config.memorydb_cluster_arn`/`memorydb_cluster_endpoint` and `gcp_config.memorystore_endpoint`
  are Computed and now round-trip cleanly whether or not your configuration sets them.** All three
  are backend-derived from `memorydb_cluster_name` (AWS) or `memorystore_instance_name` (GCP)
  whenever left unset — the provider reads the derived value from the same API response already
  used to recover it at import, so a configuration that only sets the name/instance-name field
  plans cleanly either way, and one that sets the derived field explicitly still compares normally
  against the real value. Timing matters for an existing cloud: one already imported before this
  fix had the correct value recovered into state all along (the bug was `RequiresReplace` treating
  that as a diff, not a wrong value) — it self-heals on its very next plan after upgrading, no
  re-import needed. A cloud created directly (never imported) with these fields left unset kept
  state null under the old behavior; upgrading alone doesn't retroactively populate that null (no
  diff, no regression, just no backfill) — a fresh create or a re-import is what picks up the value
  from here forward.
- **`file_storage.mount_path` only recovers a real value on GCP, Azure, and Generic.** AWS has no
  backend field for it at all, so the `/mnt/shared` you see there is always the schema default, never
  something recovered from the API — import leaves it to that default, exactly as before. GCP,
  Azure, and Generic do store a real value, so import recovers whatever the backend actually holds —
  import reflects that reality rather than resetting to a default it would otherwise conflict with.
  On GCP specifically, if `mount_targets` isn't also set, Anyscale auto-discovers the Filestore share
  name and silently overwrites whatever `mount_path` you configured (see the `mount_path` attribute
  reference for the full mechanics); import now recovers whatever value that auto-discovery already
  produced. One consequence worth knowing: if you're importing such a cloud and your `.tf` leaves
  `mount_path` unset (relying on the `/mnt/shared` default), import instead recovers the real
  auto-discovered value, so your next `plan` shows a one-time reconcile diff on `mount_path` rather
  than silently defaulting to a path the backend isn't actually using. This is strictly better than
  before this fix, when `file_storage` wasn't recovered at import at all; it's a one-time consequence
  of the pre-existing GCP auto-discovery behavior, not a new limitation of its own.
- **`file_storage.mount_targets` is deliberately excluded from this recovery, unlike the rest of
  `file_storage`.** Import always leaves it null, even when the backend has auto-discovered real
  mount target addresses for a registered cloud. On AWS and GCP the backend derives a single address
  from the EFS/Filestore resource itself; only Azure and Generic (always K8S) carry a genuine
  multi-element, per-zone list. Either way these addresses are backend-discovered, not something you
  can reliably declare in your configuration, so recovering them would just relocate the same
  forced-replace problem this fix exists to solve onto a field nobody can actually redeclare. A
  configuration that only declares `file_storage_id` (and omits `mount_targets` entirely) plans
  cleanly after import and stays that way, since state never gains a value your config didn't put
  there. `mount_targets` remains a valid input when creating a new cloud through Terraform, sourcing
  the address from your EFS/Filestore module output (see the aws-vm or gcp-vm example) — this only
  affects what import recovers into state.
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
- The five inert `kubernetes_config` fields (see [Deprecated and removed attributes](#deprecated-and-removed-attributes) above)
  can't be populated from an API that never received them: `namespace` comes back as its documented
  default (`"anyscale"`); `ingress_host`, `cluster_name`, `context`, and `kubeconfig_path` come back
  null regardless of what your configuration says. They have no effect either way, so this doesn't
  produce a plan diff on those specific fields — just don't expect them to round-trip.
- Importing an `anyscale_cloud` that was originally created empty (the multi-resource cloud pattern) but already has a
  resource attached looks, from the API, identical to one created all-in-one — both simply have a
  default resource present. Importing in that situation recovers the resource's config - the required
  provider block plus `object_storage`/`file_storage` whenever present - into the *cloud's* own
  configuration, as if it were all-in-one. If you're recovering a multi-resource cloud deployment,
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
- The Kubernetes Anyscale Operator's individual named health checks (`check_results` in the API's
  `operator_status_details`) aren't surfaced as their own attribute yet — see
  [Kubernetes operator status](#kubernetes-operator-status) above for what is.
