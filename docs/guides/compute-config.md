---
page_title: "Compute Config: Versioning, Identity, and Write-Only Fields"
subcategory: ""
description: |-
  Versioning model, identity attributes, and write-only fields for the Compute Config resource and data source that aren't obvious from the schema table alone.
---

# Compute Config: Versioning, Identity, and Write-Only Fields

This guide covers cross-cutting behavior for [`anyscale_compute_config`](../resources/compute_config.md)
(resource) and [`anyscale_compute_config`](../data-sources/compute_config.md) (data source). It exists
because several of these behaviors aren't obvious from either schema table alone.

## Versioning model

Anyscale compute configs are versioned, not mutated in place. A few attributes carry the identity
versus the version:

- `id` and `name` are the same value and stay stable for the life of the resource — this is what
  Terraform tracks the resource by, and what you reference from other resources or data sources.
- `config_id` is the version-specific API identifier. It changes every time the compute config gets a
  new version.
- `version` is the version number, and `name_version` is `name` and `version` formatted as
  `name:version` — the format the Anyscale APIs expect when you need to reference an exact version
  rather than whatever is latest.

Changing almost any attribute (`flags`, `head_node`, `worker_nodes`, `min_resources`, and so on)
updates the resource by creating a **new version** of the compute config under the same name:
`config_id` and `version` advance, `id` and `name` do not change, and the change applies in place from
Terraform's point of view — no plan-time replacement. The previous version is not automatically
archived when this happens; it stays in your organization's compute config history, superseded but not
deleted. Only destroying or replacing the resource archives a version.

If a compute config is archived outside Terraform — through the Anyscale CLI or web console — the next
`plan` or `apply` detects this and removes it from state, the same as any other resource that
disappears out from under Terraform; it then plans to create it fresh rather than erroring.

## Renaming a compute config, or changing its cloud

`name` is part of the resource's identity: changing it replaces the resource (`RequiresReplace`)
instead of updating it in place. Terraform's plan shows this as a destroy-then-create — the compute
config under the old name is archived, and a new one is created under the new name.

This is safe by construction: if the compute config you're renaming is currently backing a running
cluster, the archive step fails with a clear error instead of proceeding, rather than silently tearing
anything down. Simply retry once it's no longer in use.

`cloud_id` and `cloud_name` are also part of the resource's identity, but behave differently on
purpose. Changing which cloud a compute config actually points at is not `RequiresReplace`: instead,
`apply` fails with an explicit error ("Compute Config Cloud Is Immutable") telling you to replace the
resource deliberately — `terraform apply -replace` against this resource, or `terraform taint` it
first — rather than proceeding automatically. Switching how you *express* the same cloud — for example,
from `cloud_name` to the `cloud_id` it already resolves to — isn't a real change, and plans clean either
way.

This asymmetry is intentional, not an inconsistency: a `name` change is always detectable purely from
your configuration and prior state, so Terraform can safely auto-replace. Whether a `cloud_id`/
`cloud_name` change actually points at a different cloud can't always be determined without resolving
`cloud_name` to an ID, which a plan-time check can't safely do — so this heavier, rarer operation gets a
deliberate two-step instead of an automatic one. Both are equally safe against orphaning the old compute
config; they just surface the choice to you at different points.

## `resources` versus `required_resources`

`head_node` and `worker_nodes` each have two attributes that both sound like "how big is this node,"
and it's easy to reach for the wrong one:

- **`resources`** is the *logical* resources Ray schedules against for that node group — CPU, GPU,
  memory, and custom resource counts. Leave it unset to fall back to the instance's actual capacity;
  set it to override what Ray sees, independent of the instance's real hardware. `resources`
  legitimately stays null in your config and state when you don't set it, on both API generations —
  nothing pre-populates it for you at the config level.
- **`required_resources`** is a *physical* resource specification used to select a custom instance
  shape (a "free pod shape") when `instance_type = "custom"` — explicit CPU, memory, GPU, accelerator,
  TPU, and TPU host counts that tell the cloud provider what to actually provision. It only makes sense
  alongside `instance_type = "custom"`; it doesn't apply to a concrete instance type like `m5.2xlarge`.

They aren't interchangeable, and setting one doesn't imply anything about the other.

## `flags` and `advanced_instance_config` are write-only

Both `flags` (cluster-level) and `advanced_instance_config` (the top-level attribute, and the
equivalent per-node attribute on `head_node`/`worker_nodes`) are write-only from Terraform's
perspective: the provider sends whatever you configure, but never reads either back from the API into
state. Your configuration remains the sole source of truth for these fields specifically.

Practically, this means:

- `terraform plan` will never show drift in `flags` or `advanced_instance_config`, even if the value
  changes outside Terraform (through the Anyscale CLI or web console), or if the API's own normalized
  representation differs from what you wrote (for example, omitted-versus-null array handling).
- Every other resource attribute that can drift — `resources`, `min_resources`, `max_resources`,
  `enable_cross_zone_scaling`, `auto_select_worker_config` — does not have this limitation and
  participates in normal drift detection.

This is deliberate, to avoid a perpetual diff from a value the API is free to re-represent differently
than you wrote it, not an oversight. It also means neither is truly free-form: `advanced_instance_config`
is validated server-side against something close to the real cloud provider's instance-launch request
shape, and `flags` only accepts a fixed, specific set of recognized key names — an arbitrary custom key
is rejected outright, not passed through silently. Supply values shaped the way each is actually
validated, not arbitrary keys.

## Importing an existing compute config

Import takes the version-specific `config_id` (for example `cpt_abc123`), not `name` — find it via the
`anyscale_compute_config` data source's `config_id` attribute, or `anyscale compute-config get <name>`
in the CLI. Importing a `config_id` that's already archived fails immediately with a clear error,
rather than importing a resource that the next refresh would just remove again.

After import, `id`, `name`, `version`, `instance_type`, and a few other node fields are recovered
directly from that version. Two different things happen to the *rest* of `head_node`/`worker_nodes`,
and it's worth knowing which is which:

- **`flags` and `advanced_instance_config` — top-level, and the same two nested inside `head_node` and
  every `worker_nodes` entry — are recovered from the API and populated into state.** This is the one
  place these write-only fields (see above) are ever read back at all, specifically because import is
  the only moment there's no prior configuration to preserve, so there's nothing ambiguous about
  populating them from what's actually there. A matching configuration plans clean right after import;
  omitting one of these that the backend actually has shows an honest diff wanting to remove it, instead
  of silently dropping it on some later, unrelated apply.
- **`resources`, `required_resources`, `labels`, and `cloud_deployment` are not recovered — they're left
  null**, the same way they'd read as unconfigured on an ordinary refresh. If the compute config you're
  importing sets any of these, add them to your `.tf` yourself; until you do, they'll plan as unset
  rather than reflecting what the backend actually has.

## The data source's node topology is unmasked

The `anyscale_compute_config` data source exposes `zones`, `head_node`, and `worker_nodes` too, but
reports them differently than the resource does: with no masking at all. Every sub-attribute — including
`resources`, `required_resources`, `labels`, and `cloud_deployment` — always reflects exactly what the
API returns.

This is intentional, not an inconsistency with the resource's behavior described above: masking exists
specifically to stop a *resource's* plan from drifting toward values you never configured. A data source
has no plan to protect — it's a lookup, refreshed in full on every read — so there's nothing to mask
against, and no reason to hide real values behind a null.

## Cluster-level fields not yet on the data source

`min_resources`, `max_resources`, top-level `flags`, top-level `advanced_instance_config`, and
`cloud_resource` are resource-only today. This is different from the per-node
`flags`/`advanced_instance_config` nested inside `head_node`/`worker_nodes` above, which the data
source already exposes and unmasks fully — only these five cluster-level fields are the gap. Adding
resource-parity read-back for them is tracked for a future session.
