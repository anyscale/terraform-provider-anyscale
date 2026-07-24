---
page_title: "Compute Config: Versioning, Identity, and Write-Only Fields"
subcategory: "Behavior & Limitations"
description: |-
  Versioning model, identity attributes, and write-only fields for the Compute Config resource and data source that aren't obvious from the schema table alone.
---

# Compute Config: Versioning, Identity, and Write-Only Fields

This guide covers how [`anyscale_compute_config`](../resources/compute_config.md) (resource) and
[`anyscale_compute_config`](../data-sources/compute_config.md) (data source) actually behave: how
the versioning model separates a stable identity (`id`, `name`) from an advancing version
(`config_id`, `version`, `name_version`), why renaming a compute config and changing its cloud are
handled differently, the difference between `resources` and `required_resources`, which fields are
write-only at the top level but readable per node, and what import and the data source do and don't
recover.

For a compute config created as part of a full setup, see the [Create a VM
Cloud](./create-a-vm-cloud.md) getting-started walkthrough.

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

Archiving is scoped to the whole `(name, cloud_id)` lineage, not just the one version Terraform happens to
be tracking: destroying or replacing this resource archives **every** version under that name, including any
version a teammate or the CLI/console created out-of-band that Terraform never knew about. There is no way
to archive a single version in isolation — archiving a name always takes its entire history with it.

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

## Worker group names must be unique

The compute config itself tolerates two worker groups sharing the same `name` — both are stored and read
back as distinct entries, nothing merges or is lost at the Terraform/API layer. The risk is downstream: the
cluster's own autoscaler organizes worker groups by name, and a name genuinely cannot exist twice there, so
at cluster launch, whichever duplicate-named group is processed last would shadow the other — you configure
two worker groups and only ever get one running, with no error anywhere. This provider can't verify that
directly without launching a real cluster — treat it as real, not theoretical.

Leaving `name` unset on two or more `worker_nodes` entries that share the same `instance_type` is the case
this provider actively protects you from: each unnamed entry would otherwise derive the identical default
name (the `instance_type` itself). The provider automatically appends a disambiguating suffix (`-2`, `-3`,
and so on) to keep unset names distinct, and warns at plan time when it does so. Setting explicit, distinct
names is still recommended for clarity, but omitting them no longer risks a silent collision.

~> **Warning:** explicitly setting the *same* `name` on two different `worker_nodes` entries — typically by
mistake, such as a copy-pasted block — is not auto-corrected. The provider never overwrites an explicit
name, since silently changing a value you configured would misrepresent your own configuration back to
you. It warns at plan time (naming the colliding worker groups and why), but the plan still proceeds —
give each worker group its own distinct name rather than treating the warning as a fix.

## `flags` and `advanced_instance_config`: write-only at the top level, masked per-node

`flags` and `advanced_instance_config` each appear in two places, and Terraform tracks the two
placements differently — this split is the part that isn't obvious from the schema:

- **Top-level `flags` and `advanced_instance_config` (cluster-level) are write-only.** The provider
  sends whatever you configure but never reads either back from the API on refresh, so your
  configuration is the sole source of truth for them. `terraform plan` will never show drift in the
  top-level pair — not if the value changes outside Terraform (through the Anyscale CLI or web
  console), and not if the API's own normalized representation differs from what you wrote (for
  example, omitted-versus-null array handling). This is deliberate — it avoids a perpetual diff from a
  value the API is free to re-represent differently than you wrote it — not an oversight.
- **The per-node `flags` and `advanced_instance_config`, nested inside `head_node` and each
  `worker_nodes` entry, are not write-only.** They are *masked*, exactly like the other per-node
  fields (`resources`, `required_resources`, `labels`, `required_labels`, `cloud_deployment`): the
  provider keeps them null in state while you leave them unset, but reads the API's value back once
  you set them — so a per-node value you configure participates in ordinary drift detection, and can
  show a diff if the API normalizes it differently than you wrote. All of them, per-node and top-level
  alike, are recovered on import; see [Importing an existing compute
  config](#importing-an-existing-compute-config) below.

The top-level `flags`/`advanced_instance_config` pair is the only write-only exception in the schema:
every other attribute — including the per-node pair above, plus `min_resources`, `max_resources`,
`enable_cross_zone_scaling`, and `auto_select_worker_config` — participates in normal drift detection.

Neither field is truly free-form: `flags` only accepts a fixed set of recognized keys (an unrecognized one
is rejected, not passed through), and `advanced_instance_config` is validated server-side against something
close to the real cloud provider's instance-launch request shape — on AWS, the EC2 `RunInstancesInput`
shape. The arbitrariness it supports is structural (nesting depth and shape — maps, lists, and scalars can
nest freely), not content: the keys still have to be real fields that shape recognizes.

## Targeting more than one cloud resource: `additional_resources`

A compute config normally targets a single cloud resource: the top-level `head_node`, `worker_nodes`,
`zones`, and related attributes describe that one deployment, and `cloud_resource` (if you set it) says
which one — or the cloud's primary resource if you don't. `additional_resources` lets ONE compute config
also cover other cloud resources on the same cloud, each with its own independent `head_node`/
`worker_nodes`/etc., without changing anything about the common single-resource case: an existing config
that never sets `additional_resources` behaves byte-identically to before this attribute existed. Each
entry's own `worker_nodes` gets the same [name-uniqueness handling](#worker-group-names-must-be-unique)
described above, independently per entry — a name defaulted or disambiguated in one entry has no effect on
any other.

Each `additional_resources` entry is required to set `cloud_resource` — unlike the top-level attribute,
this is how the provider tells entries apart, so it can't default to "the primary resource" the way the
top-level one can. `cloud_resource` must be unique across every entry, and distinct from the top-level
`cloud_resource` when that is also explicitly set; this is validated at plan time. One real limit on that
validation: if you leave the top-level `cloud_resource` unset (targeting the cloud's implicit primary
resource), the provider has no way to know that resource's name without an extra network call, so it
can't check an `additional_resources` entry against it — a collision there only surfaces as a backend
error at apply, not a plan-time diagnostic.

Like `head_node`/`worker_nodes`, each entry's `advanced_instance_config` and `flags` are JSON-encoded
strings rather than native/dynamic values — `additional_resources` is a list, and Terraform doesn't support
a dynamic type nested inside a list, the same constraint that already applies per-node.

On refresh, entries are matched back to your configuration by `cloud_resource` name, not by position — this
protects against the backend echoing entries back in a different order than you configured, the same
reorder-stability `worker_nodes` has. Reordering `additional_resources` entries in your own `.tf` file still
shows as a plan diff, same as reordering `worker_nodes` does — that's normal, correct behavior for an
ordered list whose content you actually changed, not something either mechanism hides.

One current limitation worth knowing: a cold import of a multi-resource compute config has no signal for
which entry you intend as the top-level (primary) one versus which belong in `additional_resources`. The
split after import is deterministic — the first entry in the API's response order becomes top-level, the
rest become `additional_resources` sorted by `cloud_resource` name — but that's an arbitrary resolution, not
one based on your intent. Check the import result against what you expect before relying on it.

The `anyscale_compute_config` data source also exposes `additional_resources`, reported the same unmasked
way it reports every other node attribute — with no prior configuration to match entries against, an
unresolvable shape produces the same clear diagnostic the resource uses rather than silently showing only
one entry.

## Importing an existing compute config

Import accepts either the version-specific `config_id` (for example `cpt_abc123`) or a `name:version`
string (for example `my-compute-config:3`) — never a bare `name`. Find either via the
`anyscale_compute_config` data source's `config_id`/`name_version` attributes, or `anyscale compute-config
get <name>` in the CLI (see the [Anyscale compute-config CLI documentation](https://docs.anyscale.com/reference/cli/compute-config#compute-config-cli)).

A `name:version` string is resolved to its `config_id` at import time and then behaves identically to
importing by `config_id` directly — both pin the exact version you specify. A bare `name` with no version
is rejected: there's no way to pin an exact version from a name alone, since a name resolves to whatever
the latest version happens to be at the moment you ask. If a `name:version` matches more than one cloud,
import fails with a clear error asking you to use `config_id` instead — `terraform import` has no way to
pass a separate cloud selector to disambiguate. Importing a `config_id` that's already archived fails
immediately with a clear error, rather than importing a resource that the next refresh would just remove
again.

Import also reverse-looks-up `cloud_name` from the recovered `cloud_id`, on a best-effort basis, regardless
of which one your configuration actually sets: `cloud_name` is Optional+Computed (like `cloud_id` itself),
so a recovered value never conflicts with a `cloud_id`-only configuration, and a `cloud_name`-based
configuration plans clean right away instead of needing a one-time diff to catch up. This lookup can fail
silently (a network error, or a since-removed cloud) — if it does, `cloud_name` is simply left null.

After import, everything is recovered directly from that version, including the fields that stay masked on
an ordinary refresh: `flags` and `advanced_instance_config` (top-level and per-node), `resources`,
`required_resources`, `labels`, `required_labels`, and `cloud_deployment`. Import is the only moment with
no prior configuration to preserve, so populating all of them from what's actually there is unambiguous —
a matching configuration plans clean right after import, and omitting a field the backend actually has
shows an honest diff wanting to remove it, instead of silently dropping it on some later, unrelated apply.

This is different from what these same fields do on an *ordinary* refresh, where they stay masked (null
while unconfigured, real-and-diffable once you set them) rather than recovered unconditionally — see the
write-only/masked section above for why. Import gets to populate all of them unconditionally because it's
the one case with no prior configuration to protect: there's nothing to accidentally overwrite.

## The data source's node topology is unmasked

The `anyscale_compute_config` data source exposes `zones`, `head_node`, and `worker_nodes` too, but
reports them differently than the resource does: with no masking at all. Every sub-attribute — including
`resources`, `required_resources`, `labels`, `required_labels`, and `cloud_deployment` — always reflects
exactly what the API returns.

This is intentional, not an inconsistency with the resource's behavior described above: masking exists
specifically to stop a *resource's* plan from drifting toward values you never configured. A data source
has no plan to protect — it's a lookup, refreshed in full on every read — so there's nothing to mask
against, and no reason to hide real values behind a null.
