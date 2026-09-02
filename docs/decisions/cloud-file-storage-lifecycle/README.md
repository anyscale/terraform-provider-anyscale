# `file_storage` lifecycle on `anyscale_cloud` / `anyscale_cloud_resource`

Status: **contract agreed, implementation gated on Gate 1.**

Origin: a user bug report against v0.24.1 (behaviour unchanged through v0.27.0 — see
[Version applicability](#version-applicability)) covering two defects on the `file_storage` block:
adding it to a live cloud plans a full destroy-and-recreate, and the block is invisible to
`terraform plan` in both drift directions.

Both defects are real. Neither report's suggested fix is correct, and the first internal diagnosis of
Defect 1 was also wrong. This document records what the backend actually does, the contract that
follows from it, and what still needs real-execution confirmation before code is written.

---

## What the user is trying to do

Attach shared storage to an already-live, already-populated cloud — a Kubernetes cloud, using a
Kubernetes PersistentVolumeClaim — by adding one attribute to config:

```hcl
file_storage {
  persistent_volume_claim = "anyscale-shared-fuse"
}
```

That is an additive configuration change to a running production cloud. The only acceptable
outcomes are an in-place update or a clear refusal. Today the provider proposes to delete the cloud.

---

## Confirmed API contract

Source-traced in the product monorepo (read-only). Each claim below cites the code that establishes
it. Everything in this section is **confirmed source behaviour**; the
[verification](#verification-gates) section separates out what still needs a live request.

### The update route exists, and `file_storage` is writable through it

`PUT /api/v2/clouds/{cloud_id}/resources`, body a `List[CloudDeployment]`. Handler
`backend/server/api/base/resources/clouds_resource.py:2846 update_cloud_resources`. This is what
`anyscale cloud update -f` calls (`frontend/cli/anyscale/controllers/cloud_controller.py:2762`), and
it reaches `file_storage` via
`clouds_resource.py:2346 _convert_cloud_deployment_to_cloud_resource_proto` →
`backend/go/appsidecar/cloud_config_service.go:95 ConvertCloudDeploymentToCloudResource` (clones the
existing record) → `cloud_deployment_model.go:21 updateInternalCloudResource` →
`:126 updateInternalFileStorage`.

So `file_storage` is **not create-only**. The provider's existing comment on
`updateMutableFields` — "There is no general PATCH on this resource … `/clouds/{id}` only supports
GET and DELETE" — is accurate about the *cloud* object and does not cover the *resources*
sub-collection. That is the gap this design closes.

### `file_storage` is the one block that is destructive by omission

`cloud_deployment_model.go:456`:

```go
func updateInternalFileStorage(internal *datapb.CloudResourceRecord, external *openapi.CloudDeployment) error {
	// Clear any existing file storage fields before updating
	clearFileStorageFields(internal)

	if external.FileStorage == nil {
		return nil
	}
	...
```

`clearFileStorageFields` (`:516`) nulls `nfs_mount_path`, `nfs_mount_targets`,
`aws_nfs_resources`, `gcp_nfs_resources`, and `k8s_shared_storage_resources`. So a PUT that omits
`file_storage` **wipes the live shared-storage configuration.**

Every sibling block preserves on omission instead:

| Block | Omission behaviour | Cite |
| --- | --- | --- |
| `object_storage` | preserved (`if externalSpec.ObjectStorage != nil`) | `cloud_deployment_model.go:92` |
| `aws_config` | preserved (`if … == nil { break }`) | `:133` |
| `gcp_config` | preserved, same shape | same provider switch |
| `kubernetes_config` | preserved (`if … != nil`) | `:41` |
| **`file_storage`** | **cleared** | `:458` |

This is the same collateral-wipe class as `user_tag_annotation_prefix` in
[`../cloud-iam-mapping/README.md`](../cloud-iam-mapping/README.md), and it is the single most
dangerous fact in this design: **any write path the provider builds must send `file_storage` on
every PUT, or setting an unrelated field silently destroys a customer's shared storage.**

Corollary, and a useful one: *clearing* `file_storage` is expressible — omit the block. Both
directions of the lifecycle are reachable, so nothing forces a replacement.

#### What made the report's CLI evidence look additive

The report is right that `anyscale cloud update -f` applied the same change cleanly, and right that
this proves the forced replace is provider-side. It does **not** show the endpoint is additive. The
CLI does a whole-spec read-modify-write (`cloud_controller.py:2680-2765`): it fetches the existing
resources, parses the user's full YAML, diffs them, and PUTs the changed deployments *entire*. The
additivity is in the CLI, not the API. Any provider implementation that skips the read-modify-write
and PUTs only what it models will behave differently from the CLI.

### Hard gate: no in-place update while clusters are running

`clouds_resource.py:2913`:

```python
await self.verify_no_running_clusters_in_resource(
    cloud_id, resource_id,
    "Existing resources can not be updated while there are active clusters using the resource.")
```

The sole exemption is a pure GCP Deployment-Manager→Infrastructure-Manager migration
(`:2905-2912`). So an in-place `file_storage` update **fails whenever any cluster is running on that
cloud resource** — a condition driven by unrelated user activity, not by the Terraform config.

This is a genuine cost of the design and it is accepted deliberately: an `apply` that sometimes
errors with an actionable message is strictly better than a `plan` that proposes deleting a
production cloud. It must be a designed diagnostic, not a raw 400.

### Hard gate: Anyscale-managed clouds cannot have `file_storage` updated at all

`_validate_anyscale_managed_resource_update` (`clouds_resource.py:2261`, called from the update path
at `:2459`) refuses **every** update to an Anyscale-managed cloud resource except three:

- adding AWS `memorydb`,
- adding GCP `memorystore`,
- the GCP Deployment-Manager→Infrastructure-Manager migration.

Anything else — `file_storage` included — gets
`400 "Cloud resource … can not be updated because it is Anyscale-managed."` The check is a full
proto comparison with only the redis fields cleared, so it is exact: a `file_storage` change fails.

"Anyscale-managed" means the record carries `aws_provisioner_resources.cloudformation_id` or GCP
`deployment_manager_id`/`infrastructure_manager_id` — i.e. the cloud was created with
`anyscale cloud setup` rather than registered. So the update path splits by provenance:

| Cloud | In-place `file_storage` update | Provider can predict at plan time? |
| --- | --- | --- |
| Any Kubernetes cloud | **yes** — never Anyscale-managed | n/a, always allowed |
| Registered VM cloud | **yes** | n/a, always allowed |
| Anyscale-managed GCP VM | no — 400 | **yes** — `gcp_config.deployment_manager_id` / `infrastructure_manager_id` are exposed on the external spec (`cloud_deployment_model.go:406-407`) |
| Anyscale-managed AWS VM | no — 400 | **yes** — `aws_config.cloudformation_id` is exposed (`cloud_deployment_model.go:378`) |

Both provisioner markers are exposed on the live GET, so the refusal is detectable at **plan** time
for either provider — it still needs to be a written diagnostic rather than a raw API error. Note the
fields are *declared* but were never *read* before this change, which is why
[criterion 8](#acceptance-criteria) requires proof the guard actually fires.

This bounds how much the fix delivers: `persistent_volume_claim` and `csi_ephemeral_volume_driver` are
Kubernetes-only, so **every user who can hit the reported bug is in the always-updatable row.** The
reported cloud is K8S, which is why the CLI run in the report succeeded.

### Other unconditional rewrites on the same PUT

`updateInternalCloudResource` writes these regardless of what the request omits:
`CloudResourceId`, `CloudId`, `Provider` (`:30`), `ComputeStack` (`:35`), `Region` (`:81`),
`IsPrivateCloud` (`:90`, derived as `NetworkingMode == PRIVATE` — so omitting `networking_mode`
silently flips a private cloud to public).

`_verify_primary_cloud_resource` (`clouds_resource.py:2474`) rejects changes to `compute_stack`,
`provider`, `region`, `is_private_cloud`, and credentials — **on the primary resource only.** A
non-primary resource has no such guard, so a malformed PUT corrupts it silently.

Nested trap worth recording: sending `kubernetes_config` with an empty `redis_endpoint` sets
`K8SRedisResources = nil` (`cloud_deployment_model.go:55`) — clear-by-omission one level down.
The provider must therefore **not** send `kubernetes_config` on a `file_storage`-only update.

### `mount_path` is more inert than the provider's own docs claim

The current schema text says "AWS EFS-backed clouds have no backend field for it." True, but it
understates the situation:

- `updateInternalK8SSharedStorage` (`:557`) writes **only** `CsiEphemeralVolumeDriver` and
  `PersistentVolumeClaim` into `K8SSharedStorageResources`. `MountPath` is never read.
- `updateInternalNfsMountTargets` (`:477`) returns early when `FileStorageId == ""` **and**
  `MountTargets` is empty.

So for a PVC-only or CSI-only spec, `mount_path` reaches **no backend field on any provider** — not
just AWS. It has a real destination only on the NFS path: GCP `GcpNfsResources.RootDir` (`:498`),
Azure/Generic `NfsMountPath` (`:501`). AWS `AWSNFSResources` carries only `EfsId` and
`MountTargetIp` (`:488-489`) — no path field anywhere.

---

## Defect 1 — the forced replace

### Root cause, corrected

The report attributes the replace to `mount_path`'s computed default. An early internal read agreed
and a fix was nearly scoped on that basis. **Both are wrong, and the fix they imply is a placebo.**

All five `file_storage` attributes carry an unconditional `RequiresReplace()` —
`resource_cloud.go:533-605` and `resource_cloud_resource.go:550-620`:
`file_storage_id`, `mount_path`, `persistent_volume_claim`, `csi_ephemeral_volume_driver`, and
`mount_targets`. `persistent_volume_claim` is not innocent.

The report's own plan output carries both markers:

```
+ mount_path              = "/mnt/shared"          # forces replacement
+ persistent_volume_claim = "anyscale-shared-fuse" # forces replacement
```

Two independent triggers fired. Suppressing the `mount_path` one leaves
`persistent_volume_claim` firing on `null → "anyscale-shared-fuse"`, and the plan still says
"must be replaced". A plan modifier that special-cases the static-default materialisation removes a
cosmetic second marker and fixes nothing.

There are two genuinely separate defects here, and they need separate fixes.

### Confirmed by mutation, not only by reading

`TestAccCloudResource_FileStorageAddForcesReplace` (`internal/acctest/`, mock-backed GCP/K8S cloud,
two `Config` steps: no `file_storage` → `file_storage { persistent_volume_claim }` only) reproduces
the report exactly — a `PreApply` plan check sees `ResourceActionReplace`.

Commenting out **all five** `RequiresReplace` modifiers flips the plan to **update** and the test
correctly fails; `resource_cloud.go` was then reverted byte-identical. That drill establishes two
things beyond the source read:

1. All five triggers are load-bearing, so a `mount_path`-only fix cannot change the plan outcome.
2. **Terraform Core will plan an in-place update for this block once the modifiers are gone.** The
   plan shape D2 depends on is reachable — it does not error, and it does not fall back to replace
   for some other reason. That is a real answer to a question the framework source could not settle.

This test pins today's behaviour, so **it must be inverted when D2 lands**, and renamed with it: a
test called `…ForcesReplace` that asserts a non-replacing plan reads as a bug being locked in, and
the next reader "fixes" it in the wrong direction.

### 1a — `RequiresReplace` is the wrong lifecycle for the whole block

The backend supports in-place writes in both directions (set and clear). `RequiresReplace` here is
not modelling an API constraint; it is standing in for an update path nobody wrote. Per CLAUDE.md's
own rule — *do not use replacement to avoid designing an update path when safe updates exist* — this
is the case that rule exists for.

### 1b — `mount_path`'s `Default` fabricates a value

`Default: stringdefault.StaticString("/mnt/shared")` (`cloud_config_flatten.go:396` defines the
constant) materialises a value that:

1. the backend stores nowhere for a PVC/CSI config on any provider (see above);
2. the schema itself declares `ConflictsWith` `persistent_volume_claim`; and
3. then trips its own `RequiresReplace`.

Note that `ConflictsWith` **cannot** catch this. It evaluates configuration, and configuration has
`mount_path` null; the value appears later, during plan, from `TransformDefaults`. The provider
writes a value into state that its own schema declares illegal next to what the user actually set.

### Not a defect: `requires_replace` absent from `terraform providers schema -json`

The report treats this as a second bug — that a caller cannot detect the replace behaviour without
running a plan. That is inherent to Terraform's plugin protocol, not to this provider: the schema
wire format has no requires-replace field for any attribute in any provider. Replacement is computed
per-plan during `PlanResourceChange`, because it can depend on the prior state and the proposed
value. There is nothing here to close, and it must not be documented as a gap.

The underlying complaint is still fair, and D1/D2 answer it directly: after this change there is no
hidden replace behaviour on `file_storage` to discover.

---

## Defect 2 — invisible in both directions

The report is right, and right that it is worse than the schema text admits: not just stale-value
drift but total invisibility. Confirmed on our side — `Read` → `readCloudState`
(`resource_cloud.go:1709`) never populates `file_storage`, by deliberate design (the C3-v2 comment at
`:1753-1765`).

### Why the report's suggested fix is not available

"Refresh `file_storage` from the API on every `Read`, same as every other block on this resource"
fails on both halves:

- **No config block on this resource is refreshed.** `aws_config`, `gcp_config`,
  `kubernetes_config`, `object_storage`, and `file_storage` are all recovered only in `ImportState`.
  `file_storage` is not the exception; it is the rule.
- **Refreshing it is not a non-breaking change.** `file_storage` is a framework
  `SingleNestedBlock`. Blocks cannot be `Computed`. A non-Computed block populated by `Read` against
  a config that omits it produces a diff on the next plan — which is exactly the C12 regression the
  C3-v2 design exists to prevent. Making it `Computed`-capable means converting the block to a
  `SingleNestedAttribute`, i.e. `file_storage { … }` → `file_storage = { … }`: a breaking HCL change
  for every existing user. There is no in-between (CLAUDE.md, "Import round-trip safety").

### What is available, and it is cheap

`readCloudState` **already fetches the live value on every Read and discards it.**
`listCloudResources` runs at `resource_cloud.go:1765` for the Computed-field backfill, and
`CloudDeploymentResult` carries `FileStorage *FileStorage` (`models.go:194`).

So the live and state values can be compared on every Read at zero additional API cost, and a
mismatch reported as a **warning diagnostic**. Read writes nothing to the block, so this carries
none of the C12 exposure that populating it would.

---

## Decisions

### D1 — `mount_path` stops fabricating a default

`Optional + Computed`, **no `Default`**, plus `stringplanmodifier.UseStateForUnknown()`.

| Situation | Behaviour |
| --- | --- |
| config omits it, no prior state | unknown at plan; resolved from the API at apply — null when the backend has none |
| config omits it, prior state present | Core preserves the prior value — **no diff, no replace** |
| config sets it | unchanged; `validateMountPathSupported` still errors on AWS |
| import | `flattenFileStorage` yields the live value, or null when the API has none |

**Correction to this document's original rationale.** It attributed the second row to
`UseStateForUnknown`. That is wrong: Terraform Core's `ProposedNewState` already carries the prior
state value forward for any `Optional+Computed` attribute whose configuration is null, with or
without the modifier. Established by mutation — the acceptance test written to guard that row stayed
green against a genuinely reintroduced bug, which is what a test with no discriminating power looks
like.

This makes the non-breaking guarantee **stronger**, not weaker: it rests on Core's proposed-state
algorithm rather than on a plan modifier a later change could drop. `UseStateForUnknown` is retained
for parity with `mount_targets`, and that parity is the whole justification — no code comment, test
message, or doc line may claim it is what prevents the diff.

The modifier is also harmless to keep: `file_storage` is never refreshed, so the value is a frozen
snapshot rather than a volatile computed field, and it does not repeat the
volatile-`UseStateForUnknown` hazard.

**Three coupled changes that must land in one commit.** Dropping the `Default` alone breaks the
create/import symmetry the current code deliberately maintains:

1. **Schema** (both live resources): drop `Default`, add `UseStateForUnknown`.
2. **`flattenFileStorage`** (`cloud_config_flatten.go:440-443`) currently resolves an empty API
   value to `fileStorageDefaultMountPath`, *because* Defaults are not applied on import and a null
   would mismatch freshly-created state. Once the Default is gone that reasoning inverts: it must
   yield null. Leaving it produces `/mnt/shared` on import and null on create — the same mismatch in
   the other direction. This helper is shared by **both** resources' `ImportState`.
3. **`mergeFileStorageDerivedFields`** (`:497`) patches only `mount_targets` today. It must also
   resolve `mount_path`, or an `Optional+Computed` attribute with no `Default` stays unknown after
   apply and Terraform hard-errors. Unknown is not null on create for an `Optional+Computed`
   attribute — an `IsNull()` check will not catch it.

**Compatibility: additive.** No state upgrader, no config change, no new diff for any existing
configuration. Both resources already carry schema versions (`anyscale_cloud` 4,
`anyscale_cloud_resource` 2) if one were needed; it is not.

**Accepted residue, deliberately not fixed.** A user whose state already holds a fabricated
`/mnt/shared` alongside a PVC keeps it, frozen, via `UseStateForUnknown`. Scrubbing it would need a
state upgrader with a heuristic — a state upgrader cannot see configuration, so it cannot distinguish
"the user wrote `/mnt/shared`" from "the default materialised it". The value is inert and invisible;
a version bump on two resources to clean a cosmetic string is not worth the migration risk. Document
it, do not migrate it.

### D2 — `file_storage` becomes updatable in place; `RequiresReplace` is removed

Remove `RequiresReplace()` from all five attributes on both resources and implement `Update` against
`PUT /api/v2/clouds/{cloud_id}/resources`.

**The write must be a read-modify-write of the live deployment spec, never a projection of Terraform
state.** Two independent reasons:

- State is not a reliable picture of live `file_storage` — by design it is a frozen create/import
  snapshot (Defect 2). Building the PUT from state can resend a stale value.
- Omitting `file_storage` wipes it. Any PUT the provider ever issues for any reason must carry it.

Required shape:

1. `GET /api/v2/clouds/{id}/resources` for the live deployment spec.
2. Substitute **only** `file_storage`.
3. Echo `provider`, `compute_stack`, `region`, and `networking_mode` verbatim — all four are rewritten
   unconditionally from the request, and only the primary resource has a guard against getting them
   wrong.
4. Omit `object_storage`, `aws_config`, `gcp_config`, and `kubernetes_config`. They preserve on
   omission, and sending `kubernetes_config` would clear `K8SRedisResources`.
5. PUT the single deployment.

`anyscale_cloud` targets its primary deployment via the `cloud_resource_id` already in its state;
`anyscale_cloud_resource` targets its own.

**Three apply-time refusals, each needing a written diagnostic** rather than a raw API error:

1. **Clusters running** — stop them, or apply with the CLI.
2. **Anyscale-managed cloud** — `file_storage` cannot be changed in place on a cloud created with
   `anyscale cloud setup`; only registered clouds and Kubernetes clouds can. Say so, and say that
   `persistent_volume_claim`/`csi_ephemeral_volume_driver` are unaffected because they are
   Kubernetes-only.
3. **`infrastructure_manager_id` immutability** (GCP) — reachable if the read-modify-write echoes a
   changed value; the guard against it is to echo verbatim, never derive.

Refusal 2 is predictable at **plan** time for both providers, from the live GET:
`gcp_config.deployment_manager_id`/`infrastructure_manager_id` (`cloud_deployment_model.go:406-407`)
and `aws_config.cloudformation_id` (`:378`). Raise it at plan time rather than letting the apply fail.

**Fallback, if and only if Gate 1 disproves the minimal PUT.** Replace `RequiresReplace` with a
plan-time error guard modelled exactly on `cloudNameImmutablePlanModifier`
(`resource_cloud.go:87-124`) — a proven mechanism in this repo, and attribute-level rather than
resource-level `ModifyPlan` deliberately: attribute plan modifiers are not invoked on a destroy plan,
so the guard cannot break `terraform destroy` the way a resource-level `ModifyPlan` would without an
explicit null-plan check.

Be clear about what the fallback is worth: it converts a silent proposal to delete a production
cloud into a loud, actionable error. It does **not** give the reporter what they asked for, and it
leaves them in a dead end — set the field with the CLI, declare it in `.tf`, and every subsequent
plan errors until they `terraform state rm` and re-import. **The fallback is a safety fix, not a
feature fix.** Ship it alone only if Gate 1 fails.

### D3 — drift becomes visible as a warning

`Read` compares live `file_storage` against state and emits a warning on mismatch, in both
directions the report identified:

- state has a value, live has none or a different one → declared-but-dropped;
- state has none, live has one → live-but-undeclared.

The warning names both values and says the block is not managed on refresh. `Read` writes nothing to
the block, so there is no plan-consistency exposure. Zero additional API calls.

#### Required suppression: D1's legacy residue is not drift

D1 interacts with this directly, and getting it wrong makes D3 fire on **every plan for every
existing user**. Any state written before D1 holds `mount_path = "/mnt/shared"`. The live API returns
no `mount_path` at all on AWS — `toExternalFileStorage` sets it only for GCP (`RootDir`) and
Azure/Generic (`NfsMountPath`) — and returns empty on GCP whenever no Filestore exists. So a naive
live-vs-state comparison sees `live=<none>` against `state=/mnt/shared` and reports drift that is
really the inert residue D1 deliberately chose to leave alone.

**Rule:** suppress the `mount_path` comparison when the live value is empty **and** the state value
equals `fileStorageDefaultMountPath`. Compare every other field normally.

The cost of that rule, recorded rather than quietly accepted: it also suppresses one genuine signal —
a GCP user who really did configure `/mnt/shared` and whose backend then dropped it. That case is
indistinguishable from the residue using state alone, and `mount_path` is inert on AWS and
backend-overwritten on GCP anyway, so the signal is worth less than a warning on every plan for
everyone. Do not widen the rule beyond that exact pair of conditions; a suppression keyed on
`mount_path` alone, or on state-equals-default alone, would hide real drift.

This is explicitly a mitigation, not a fix. Full drift management requires the breaking
block→attribute conversion, which is out of scope here and should be raised as its own decision if
it is ever wanted.

#### D2 makes refreshing this block *more* dangerous, not less

Worth stating plainly, because the opposite is an easy and costly assumption: that once Update exists,
`Read` could safely start refreshing `file_storage` — the C12 objection being about there being no
update path. It is not, and the inference runs backwards.

C12 is a plan-consistency constraint. Blocks cannot be `Computed`, so a `file_storage` populated by
`Read` against a config that omits the block yields config-absent vs state-present, and Terraform's
proposed new state follows config: **null**. Today that diff is a replace, which is loud and
obviously wrong, and users see it before applying. With D2 shipped it becomes a quiet `update` whose
apply issues a PUT that omits `file_storage` — and omission clears it. A user whose cloud has shared
storage configured out of band, applying an unrelated change, would have that mount **destroyed**.

So implementing Update raises the cost of refreshing this block from "an alarming plan" to "silent
data loss". D3 stays a warning. Any future attempt at real drift management must convert the block to
an attribute so `Computed` can absorb config-absence — the conversion is not an optional nicety that
Update makes unnecessary, it is the precondition that Update makes mandatory.

### D4 — `ConflictsWith` stays, with its limit recorded

The bidirectional `mount_path` ↔ `persistent_volume_claim` / `csi_ephemeral_volume_driver`
validators are correct and stay. They cannot catch a default-materialised value, because they read
configuration. D1 removes the only case where that mattered.

---

## Compatibility classification

| Change | Class | Notes |
| --- | --- | --- |
| D1 `mount_path` default removal | **additive** | No config change; `UseStateForUnknown` preserves existing state. New creates record null instead of `/mnt/shared` — truthful, and not user-visible as a diff. |
| D2 `RequiresReplace` removal | **behaviour-changing, non-breaking** | Strictly removes an incorrect forced replace. No configuration that plans today stops planning. |
| D2 `Update` implementation | **additive** | New capability. Previously-impossible applies now succeed on Kubernetes and registered clouds. On Anyscale-managed clouds they fail with a diagnostic instead of destroying the cloud — also an improvement, but not a success. |
| D2 fallback error guard | **behaviour-changing** | An apply that previously destroyed-and-recreated now errors. Needs a migration note (`state rm` + re-import) if shipped alone. |
| D3 drift warning | **additive** | New warning on existing configurations. Some users will see a warning where they saw silence — intended, and the point. |

Nothing here requires an HCL change, a state upgrader, or a re-import — **provided D2's primary path
ships.** The fallback alone does need the re-import note.

Changelog: one fragment covering D1+D2+D3 as a single user-facing story ("`file_storage` can now be
changed in place instead of forcing the cloud to be replaced"). D1 and D3 are not independently
interesting to a reader.

---

## Verification gates

Per CLAUDE.md's real-execution gate, at design-confirmation time — not at ship time.

### Gate 1 — API response shape · **CLOSED**

| Item | Result |
| --- | --- |
| G1.1 minimal PUT | **falsified the original design** — nested wipe of `kubernetes_config.redis_endpoint` |
| G1.7 round-trip | **PASS** — mandatory block taken verbatim survives; `aws_config` must be omitted outright |
| G1.2 negative control | **PASS** — omitting `file_storage` clears it; siblings untouched |
| G1.4 `mount_path` on the PVC path | **PASS** — accepted and silently ignored, exactly as predicted |
| G1.1b VM variant | inconclusive → **closed as not-required**, designed around |
| G1.3 running-clusters 400 | **unrun** → resolved as G1.5 was; see below |
| G1.5 Anyscale-managed 400 | unreachable → **unverified, nothing depends on it** |
| G1.6 `networking_mode` | **withdrawn** — designed away |
| read-back consistency | **synchronous**; no settle-wait needed |

#### G1.1 result: minimal-PUT falsified

Run against a throwaway AWS/K8S cloud in `us-east-2`, baseline established and confirmed by GET
before the test write.

Prerequisite findings, both contradicting the original spec:

1. **`kubernetes_config` is not omittable for an AWS Kubernetes resource.** Omitting it returns
   `422: "kubernetes_config" with "anyscale_operator_iam_identity" is required for AWS Kubernetes
   resources`.
2. **The body must be a JSON list.** A bare object returns `422 "value is not a valid list"`.

Result of the minimal PUT that did succeed (`200`, body `null`):

| Field | Before | After | Verdict |
| --- | --- | --- | --- |
| `object_storage.bucket_name` | a set value | unchanged | **preserved** — top-level omission is safe |
| `file_storage.persistent_volume_claim` | null | the requested PVC | **applied**; siblings correctly null |
| `kubernetes_config.anyscale_operator_iam_identity` | set | echoed value | preserved (it was sent) |
| **`kubernetes_config.redis_endpoint`** | `redis.…:6379` | **null** | **WIPED** |

So the nested clear-by-omission predicted from `cloud_deployment_model.go:55` is real and reachable
through the exact request the original spec prescribed. Any live AWS-K8S cloud with a real
`redis_endpoint` would have lost it on every `file_storage`-only write. This is why
[the design is now a round-trip](#why-round-trip-beats-minimal-put-prefer-the-design-whose-errors-are-loud).

**Answered in passing:** the GET is **synchronous** — it reflected both the applied `file_storage`
and the wiped `redis_endpoint` immediately after the `200`. No settle-wait needed in `Update`;
[that open question](#open-question-for-g11-to-answer-in-passing) is closed.

Also worth noting the provider models all three `kubernetes_config` fields
(`anyscale_operator_iam_identity`, `zones`, `redis_endpoint` — `models.go:168-178`), so a
field-by-field echo *was* expressible. It is still the wrong design, for the loud-vs-silent reason
above.

#### G1.7 result: round-trip PASSES, with one block that must be omitted

Same throwaway resource, `file_storage.persistent_volume_claim` changed. `200`, body `null`. GET
after: the new PVC applied, `object_storage.bucket_name` unchanged, and
**`kubernetes_config.redis_endpoint` survived unchanged** — the failure that killed minimal-PUT does
not occur when the mandatory block is taken verbatim from the GET.

One refinement, and it is load-bearing: `aws_config` **cannot** be round-tripped on a K8S resource
even though the GET returns it. Sending it back verbatim — every field null, unchanged from the GET —
produced `400 Changing the Anyscale IAM role or external ID of the primary cloud resource … is not
supported.` Omitting the key returned `200`.

So the guard fires on the block's **presence**, not on a value mismatch, and the deeper fact is that
**the GET is not a lossless representation of the record**: the returned `aws_config` does not carry
the stored Anyscale IAM role or external ID, so echoing it writes nulls over credentials the read
side never exposed. That invalidates "round-trip everything minus a strip-list" as a strategy and is
why [the request](#the-request) now enumerates what to send.

It also confirms the round-trip design's premise empirically: this mistake was **loud** — a 400
before any write — exactly the property it was chosen for.

#### Remaining Gate 1 items

##### The two lossy read-side transforms on `aws_config`

These justify the [lossless-round-trip check](#lossless-round-trip-check-before-sending-any-get-sourced-block),
so they are recorded rather than left in the trace:

| Field | Read side | Write side | Symmetric? |
| --- | --- | --- | --- |
| `external_id` | blanked when it equals the cloud ID (`cloud_deployment_model.go:356-360`) | `""` restored to the cloud ID (`:142-146`) | **yes — lossless** |
| `anyscale_iam_role_id` | blanked when not a valid ARN — "can be a randomly generated value for k8s" (`:350-354`) | assigned verbatim (`:149`) | **no — lossy** |

The asymmetry is the exact cause of G1.7's 400: on a K8S resource the role is a random non-ARN value,
the read side hides it, and echoing the block writes `""` over it. On a VM cloud the role *is* a valid
ARN, so the round-trip should be lossless there — untested, see G1.1b.

##### G1.1b: inconclusive, and designed around

A VM resource built with placeholder AWS identifiers returned `500` with no useful diagnostic. VM
creation appears to call real AWS APIs to validate or derive from `subnet_ids`, unlike K8S which never
validates cluster connectivity, so a wholly fake account raises rather than rejecting. Confirming it
needs a real VPC and subnet — more real infrastructure than the question is worth.

**Closed as not-required**, because the lossy condition is detectable locally: if
`anyscale_iam_role_id` comes back empty, the block cannot be round-tripped, so refuse instead of
sending. That covers the case with no server-side protection at all —
`_verify_primary_cloud_resource` guards only the **primary** deployment, so on a *non-primary* VM
deployment a lossy round-trip would destroy the stored credential **silently**.

##### G1.3 and G1.5: never run, and nothing depends on their wording

Neither was reachable. G1.3 needs a live workload, and the throwaway cloud had placeholder IAM so
nothing could start on it; running it against the shared static fixture is worse than useless, since
with nothing running the update **succeeds** and mutates a protected fixture. G1.5 needs an
Anyscale-managed cloud, and none exists in the test org.

Both were originally load-bearing only because the diagnostics were to be written from captured
backend text. They are not, and this document briefly required something impossible — Gate 1 was
declared closed with G1.3 absent from the results table while the Diagnostics section still demanded
its captured text. Resolution for both:

- author the guidance locally; match narrowly on a source-read substring;
- **append the API's real response body verbatim**, so the user sees the truth even on a missed match;
- fall through to a generic but still actionable refusal;
- say in the code comment that these were never run.

**The running-clusters substring is the one part of D2 resting on unverified source text**, and its
failure mode is safe: a wording change makes the match miss, the generic branch fires, and the
verbatim body still carries the real reason. Degraded guidance, never a wrong claim. Running G1.3
later would upgrade it from source-read to captured; it is not a ship blocker.

##### G1.6: withdrawn

It asked whether a private cloud's GET returns `networking_mode: "PRIVATE"` or null. The design no
longer depends on the answer — `networking_mode` is
[derived from `is_private_cloud`](#networking_mode-is-derived-not-echoed) rather than echoed. No
private cloud needed to exist to make the write safe, and none does in the test org.

### Gate 2 — Framework/Core contract · **SATISFIED**

**G2.1 — `Optional+Computed` with no `Default`.** A `resource.Test` proving that a config omitting
`mount_path` applies with the resolved value recorded — no "value remains unknown" error — and that a
second apply with the same config plans empty. Framework source describes the mechanism without
revealing what Core enforces at plan time, so a unit test built on that source shares its blind spot.

A **mock-backed** `resource.Test` satisfies this fully: a mocked API still exercises real Core, a real
plan, and a real apply. Gate 1 is the only gate needing real requests, because only it depends on what
the API actually sends.

**Satisfied**, run under `TF_ACC=1` with a RUN/PASS line for the specific test — a green `make test`
proves nothing here, since `resource.Test` skips silently without `TF_ACC`.

Both of D1's resolution paths are additionally mutation-proven: the create side via
`mergeFileStorageDerivedFields`, and the import side via `flattenFileStorage`, where reintroducing the
fabricated default fails the import test on `+ "file_storage.mount_path": "/mnt/shared"`.

That second one closed a real gap. The import-path assertion had first been *updated to match* the new
expected value, which reads like coverage and proves nothing — an assertion rewritten to the fix's own
output cannot fail against the bug. **Whenever a fix changes an expected value, updating the assertion
is necessary and is not a guard**; only mutating the fix back shows it discriminates. Apply per call
site, not per commit.

### Not needed

The clear-by-omission asymmetry between `file_storage` and its sibling blocks is settled by source
alone — it is a straight-line read of `updateInternalCloudResource`, with the contrasting `!= nil`
guards visible in the same function. G1.2 confirms the behaviour end to end anyway, so no separate
argument is required.

---

## Implementation constraints

Two rules that outlive the sequencing:

- **Never edit `resource_cloud_upgrade.go` or `resource_cloud_resource_upgrade.go`.** They are frozen
  historical schemas; changing one rewrites what past state upgrades to.
- **Both live resources change together.** `anyscale_cloud` targets its primary deployment via the
  `cloud_resource_id` already in state; `anyscale_cloud_resource` targets its own.

D2 is the only change that fixes the reported bug. D1 stops `mount_path` fabricating a value and D3
makes drift visible, but `persistent_volume_claim` keeps its own unconditional `RequiresReplace` until
D2 removes it — so a release carrying D1 and D3 without D2 would be a partial improvement presented as
a bug fix.

### Documentation scope for lane 5

Beyond regenerating `docs/`, two things need writing rather than regenerating:

- **The corrected claims.** `mount_path` is inert on *every* provider for a PVC/CSI spec, not just
  AWS; and in-place updates are unavailable on Anyscale-managed clouds. Neither depends on the code
  landing.
- **A brevity pass on `file_storage`'s own descriptions.** The `mount_path` and `mount_targets`
  `MarkdownDescription` strings are each several hundred words of accumulated caveat. Rewrite them to
  the behaviour that survives, rather than appending — appending is how they reached that length.
  Same for the in-code comments the change touches: keep the reasoning, drop the history.

#### Claims that become false, by lane

These are corrections, not trims, and a brevity pass will not find them. Each must be fixed in the
same lane that falsifies it, or the published page states the opposite of the shipped behaviour.

| Claim | Where | Falsified by |
| --- | --- | --- |
| "Changing this requires replacement; the provider has no in-place update path for it." | `mount_path` | **D2** — both halves. |
| "Changing this list requires replacement; the provider has no in-place update path for it." | `mount_targets` | **D2** — both halves. |
| "`terraform plan` won't surface that later drift." | `mount_path`, `mount_targets` | **D3 — ALREADY FALSE IN SHIPPED CODE.** Plan now surfaces it as a warning. The block still is not refreshed, so only this clause changes. Fix in D3's lane; do not let it wait for D2. |
| "(neither has a backend `mount_path` field either)" | `mount_path`, trailing parenthetical | Already redundant post-D1 — the same fact is stated up front. Drop it. |
| "Reconciling it today requires replacing the resource (`file_storage` forces replacement on change)." | **the drift warning body**, `cloud_config_flatten.go` | **D2.** Was telling practitioners at plan time to destroy a live cloud for a change apply now performs in place. |
| "Only re-import corrects state." | `mount_path`, `mount_targets` | **D2.** An apply that changes the value to a new one now corrects state in place. Re-import is needed only when config still *matches* the stale value, since then there is no diff for apply to act on. |

#### Enumerate falsified claims by audience, not by file type

The first sweep of this list found the false replacement claim in the two `MarkdownDescription`
strings and **missed the drift warning body**, which asserted the same falsehood. The miss was
structural, not careless: the review's scope was "documentation", so it looked at the strings that
reach the registry pages — and a warning body is not one of those, even though it reaches the same
practitioner more directly, at plan time, in their terminal.

**Rule: when a change falsifies a user-facing claim, enumerate the places that claim is *told to a
user*, not the places of one file type.** For this provider that is at least: schema
`MarkdownDescription`, **diagnostic bodies — errors *and* warnings**, `templates/guides/*`, runnable
`examples/`, and the changelog. The published pages are the most visible surface; they are not the
only one, and they are not the closest to the user.

Two further notes on the surviving text:

- "a prior refresh attempt caused a state-consistency regression" is incident history. Keep the
  *reason* and drop the incident: the block is not refreshed because framework Blocks cannot be
  `Computed`, so populating one would break plan consistency. That is the durable fact, and it also
  tells a reader why D3 is a warning rather than a fix.
- Post-D1 the `updateMutableFields` comment's "no general PATCH on this resource" claim needs scoping
  to the **cloud object**. There is no PATCH on `/clouds/{id}`, but the `resources` sub-collection has
  a PUT — and that unscoped sentence is what led the first pass at this bug to conclude no update path
  existed at all.

## D2 implementation spec — **FROZEN**

Gate 1 is closed. This section is frozen as of the commit that added this line: implement from it, and
if something here turns out to be wrong, report it rather than working around it — that is how the
last two revisions happened, and both times it was the right call.

Four things were revised or removed by gate evidence, so notes predating this freeze are unreliable:
the body is **enumerated**, not everything-minus-a-strip-list; `networking_mode` is **derived**, not
echoed; a provider-config block from another stack must be **omitted outright**; and there is a new
client-side **lossless-round-trip check** before sending any block taken from the GET.


Written out so that the moment G1.1 clears, this is implementation rather than design. Everything
here follows from the [confirmed API contract](#confirmed-api-contract); G1.1 confirms the request
shape in step 3.

### When to issue a resources PUT at all

**Only when `file_storage` actually changed between plan and state.** Compare the plan and state
objects; if equal, issue no PUT. Every resources PUT unconditionally rewrites `provider`,
`compute_stack`, `region`, and `is_private_cloud` on the record, so an unnecessary one is pure
downside — an unrelated `auto_add_user` toggle must not drag `file_storage` through a rewrite.

Plan-vs-state is the right comparison for *whether* to write, because state is what the practitioner
last declared. The live GET is for *constructing* the body, never for deciding.

### Order within `Update`

Issue the resources PUT **before** `updateMutableFields`. Terraform has no transaction across these
calls, so on failure something will be half-applied either way; putting the failure-prone call first
means the likely failure (clusters running) happens before anything else has changed. Then
`readCloudState` as today.

### The request

> **Revised after G1.1.** The original minimal-PUT design — omit every block you do not own — is
> **falsified**, and the way it failed argues for the opposite approach. See
> [G1.1 result](#g11-result-minimal-put-falsified). The design below replaces it.

**Send the minimum the API requires; populate what you must send verbatim from the GET; omit
everything else.** Confirmed working by G1.7.

1. `GET /api/v2/clouds/{id}/resources`; select the target deployment — `anyscale_cloud` uses the
   `cloud_resource_id` in its state, `anyscale_cloud_resource` its own.
2. Build the body from the GET result, containing **only**:
   - `cloud_resource_id`
   - `provider`, `compute_stack`, `region` — verbatim from the GET
   - `networking_mode` — **derived, not echoed.** See [below](#networking_mode-is-derived-not-echoed).
   - `file_storage` — from the plan; omit the key entirely to clear it
   - the **one** provider-config block this resource's stack actually owns, verbatim from the GET:
     `kubernetes_config` for K8S. For VM, see [G1.1b](#remaining-gate-1-items) — unconfirmed.
   - `object_storage` may be included verbatim or omitted; both were confirmed safe. Prefer
     **omitting** it, per the rule below.
3. Omit every other block, including a provider-config block that is present in the GET but belongs
   to a different stack — **even when its content is identical to what was returned.**
4. `PUT` as a JSON **list** — a bare object is rejected with `422 "value is not a valid list"`.

### Omit any block this resource's stack does not own — presence alone is the trigger

G1.7 established this the hard way. Round-tripping `aws_config` **verbatim** on a K8S resource — the
GET returned it with every field null, and it was sent back unchanged, null for null — produced:

```
400 Changing the Anyscale IAM role or external ID of the primary cloud resource … is not supported.
```

Omitting the key entirely returned `200`. So the guard fires on the **presence** of the block, not on
a value mismatch. Sending a block back exactly as received still trips it.

The reason matters, because it invalidates "round-trip the whole object" as a general strategy:
**the GET is not a lossless representation of the record.** `aws_config` as returned does not carry
the stored Anyscale IAM role or external ID, so echoing the block verbatim writes nulls over
credentials that the GET never showed. There are stored fields no round-trip can preserve, because
the read side never exposes them.

This is why step 2 enumerates what to send rather than saying "everything minus a strip-list". It is
not a return to the falsified minimal-PUT: the difference is that the mandatory blocks are now
**enumerated by evidence** rather than guessed, and populated **from the live GET** rather than from
Terraform state. The failure mode that killed minimal-PUT — an under-echoed field inside a mandatory
block — is handled by taking that block verbatim from the GET, which is exactly what G1.7 confirmed
works for `kubernetes_config` including `redis_endpoint`.

### `networking_mode` is derived, not echoed

The original instruction to echo it verbatim was **wrong**, and G1.6 was written to find out how
wrong. It is better to remove the question than to answer it.

G1.1/G1.7 ran against a public cloud whose GET returned `networking_mode: null`; sending null back
was harmless. But `is_private_cloud` is derived as `networking_mode == PRIVATE`
(`cloud_deployment_model.go:90`), so if a **private** cloud's GET also returns null, echoing verbatim
would silently flip it public — an unrelated, unrecoverable change on a `file_storage` write.

The provider does not need the GET for this. `is_private_cloud` is a modelled attribute and is
refreshed from the cloud-level GET on **every** `Read` (`resource_cloud.go`, `state.IsPrivateCloud =
types.BoolValue(cloudResp.Result.IsPrivateCloud)`), so it is authoritative and current. Derive:

- `is_private_cloud == true` → send `networking_mode: "PRIVATE"`
- `is_private_cloud == false` → send `networking_mode: null` — the exact value G1.7 proved round-trips
  correctly for a public cloud

That is correct by construction for private clouds and verified for public ones, and it needs no
private cloud to exist anywhere in order to be safe. **G1.6 is withdrawn.**

Keep the fail-closed check for `provider`, `compute_stack`, and `region`: if any disagrees with the
live GET, refuse rather than send. It does not apply to `networking_mode`, which is now deliberately
*not* a copy of the GET value.

### Lossless-round-trip check before sending any GET-sourced block

The GET does not represent the record losslessly, so a block is only safe to send back if the fields
that get blanked on read came back populated. Check before sending; refuse rather than write.

| Block | Field | Refuse when | Why |
| --- | --- | --- | --- |
| `aws_config` (VM only — never sent on K8S) | `anyscale_iam_role_id` | empty | blanked on read when not a valid ARN (`cloud_deployment_model.go:350-354`), assigned verbatim on write (`:149`) — sending empty overwrites the stored credential |
| `aws_config` | `external_id` | never | symmetric: blanked on read when equal to the cloud ID, restored on write (`:142-146`) |

The error must say that the provider cannot safely update `file_storage` on this deployment because
the API does not return a value it would have to rewrite, and name the CLI as the way to make the
change.

This is not belt-and-braces. `_verify_primary_cloud_resource` guards only the **primary** deployment,
so on a *non-primary* VM deployment a lossy round-trip destroys the Anyscale IAM role with no server
error at all. This check is the only thing standing between that case and silent credential loss, and
it is why [G1.1b](#g11b-result-inconclusive-and-designed-around-rather-than-left-open) could be closed
without an answer.

### Why round-trip beats minimal-PUT: prefer the design whose errors are loud

This is the load-bearing rationale, so it is recorded rather than left implicit.

The two designs fail in opposite ways:

| Design | If you get the field set wrong | Failure mode |
| --- | --- | --- |
| **Minimal PUT** — omit what you do not own, echo what is required | you under-echo a field inside a block you were forced to send | **silent** — the field is wiped, `200 OK`, no diagnostic |
| **Round-trip** — send everything back, strip a known list | you leave an extra response-only field in | **loud** — `422`, before anything is written |

G1.1 proved the silent mode is real: `kubernetes_config` cannot be omitted for an AWS Kubernetes
resource (`422`: `anyscale_operator_iam_identity` is required), and once it *is* present in the body,
sibling fields inside it that were not echoed are **wiped, not preserved** — a live
`redis_endpoint` went to null on a `file_storage`-only write.

So minimal-PUT requires an echo-list that is *exhaustive* across every field of every block that any
(provider, compute-stack) combination makes mandatory, and every gap in it destroys customer
configuration silently. Round-trip requires a strip-list that is merely *correct*, and every mistake
in it surfaces as a 422 before a write happens.

Prefer the design whose mistakes are loud. An over-sent field costs a failed apply; an under-sent one
costs a customer's Redis endpoint or shared-storage mount with no signal at all.

This is also what the CLI does (`cloud_controller.py:2680-2765`) — the one path already known to
work in production against this endpoint.

### Top-level omission preserves; nested omission does not

Worth stating as its own rule, because the asymmetry is not obvious and G1.1 demonstrated both halves
in a single request:

- **Top-level block omitted** → preserved. `object_storage` survived a PUT that omitted it.
- **Field omitted inside a block that IS sent** → wiped. `kubernetes_config.redis_endpoint` was
  destroyed by a PUT whose `kubernetes_config` carried only `anyscale_operator_iam_identity`.

Under the round-trip design neither case arises, which is precisely the point of adopting it.

### Still echo the four scalars verbatim

`provider`, `compute_stack`, `region`, and `networking_mode` are rewritten from the request whatever
it contains, and only the *primary* deployment is guarded against getting them wrong
(`_verify_primary_cloud_resource`, `clouds_resource.py:2474`). Under round-trip they come along
automatically — but keep the **fail-closed check**: if any disagrees with the live GET, refuse rather
than send.

`networking_mode` needs its own confirmation before this ships; see
[G1.6](#g16--networking_mode-echo-safety-on-a-private-cloud). G1.1 ran against a cloud whose value
was null and sent null back, which is correct for a public cloud and would silently flip a **private**
one, since `is_private_cloud` is derived as `networking_mode == PRIVATE` (`cloud_deployment_model.go:90`).

**Fail closed on step 4.** If any echoed value disagrees with what the live GET returned, refuse the
PUT with an error rather than sending it. Cheap, and it is the only protection a non-primary
deployment has.

#### Build a dedicated request type; do not marshal the response struct

`CloudDeploymentResult` is the wrong type for the body, for two reasons. Its blocks have **no**
`,omitempty` (`models.go:193-198`) where `CloudDeploymentRequest`'s all do (`:87-92`), so nil blocks
serialise as explicit nulls rather than being omitted. And it carries `created_at`, `is_default`,
`operator_status`, `operator_status_details`, `auto_add_user`, `lineage_tracking_enabled`, and
`is_aggregated_logs_enabled` — the last three being cloud-level settings with their own endpoints
(`models.go:93-94`), so sending them is a second write nobody designed.

Populate a dedicated type field by field instead. That also makes the omissions explicit at the call
site rather than an emergent property of struct tags a later edit could change.

One tag is load-bearing: `FileStorage.FileStorageID` has **no** `omitempty` (`models.go:106`), so a
PVC-only config always sends `"file_storage_id": ""`. The backend treats empty as unset
(`updateInternalNfsMountTargets` returns early; `updateInternalK8SSharedStorage` gates on `!= ""`), so
this works — but by relying on empty-means-unset rather than on omission.

### Diagnostics

Three refusals, each needing its own message rather than a raw API error:

| Condition | Surface | Message must say |
| --- | --- | --- |
| Clusters running on the deployment | apply-time, from the 400 | Stop the clusters, or apply the change with the CLI. |
| Cloud is Anyscale-managed | **plan-time, both providers** — live spec carries `gcp_config.deployment_manager_id`/`infrastructure_manager_id` or `aws_config.cloudformation_id`. Keep the apply-time 400 translation as a backstop. | `file_storage` cannot be changed in place on a cloud created with `anyscale cloud setup`; registered and Kubernetes clouds can. |
| `infrastructure_manager_id` changed | apply-time | Should be unreachable given step 4's echo; if it fires, the echo is broken. |

**Neither G1.3 nor G1.5 was run**, so there is no captured backend text to write against — an earlier
revision of this section said to use it, which nothing could satisfy. Instead: author the guidance
locally, match narrowly on a source-read substring, and **append the API's real response body
verbatim** so the user sees the truth even when the match misses. A miss must fall through to a
generic but still actionable refusal.

Never present source-read wording as captured, and say so in the code comment. Details and the
reasoning: [G1.3](#g13-result-unrun--and-this-document-closed-gate-1-with-an-instruction-nothing-could-satisfy),
[G1.5](#g15-result-unreachable-and-no-longer-needed).

### Schema changes

Remove `RequiresReplace()` from all five `file_storage` attributes on **both** live resources. Leave
`mount_path`'s `UseStateForUnknown` and `mount_targets`' modifiers otherwise intact. Do not touch the
`*_upgrade.go` files.

### Testing shape

Criteria 1, 2, 5, 7 and 8 cover this lane. Two notes specific to D2:

- **Criterion 2 is the one that matters.** It must fail against a write path that omits
  `file_storage` or over-sends `kubernetes_config` — that is the silent-data-loss mode.
- **Mutate the guard, not only the feature** on all three diagnostics, and on step 4's fail-closed
  check. A test asserting "this error is raised" proves nothing unless the raising condition can be
  shown to fire; see [criterion 9](#acceptance-criteria) for the worked example.

### Open question for G1.1 to answer in passing

Does the `GET` immediately reflect the change, or does the admin-zone deploy the handler triggers
make it eventually-consistent? If the latter, `Update`'s read-back needs a bounded settle-wait rather
than an immediate re-read. Cheap to observe while running G1.1; expensive to discover from a flaky
acceptance test later.

## Acceptance criteria

Exercisable without reference to implementation details.

1. **The reporter's exact scenario updates in place.** A live K8S cloud with `kubernetes_config` and
   `object_storage` populated and no `file_storage`; add a block setting only
   `persistent_volume_claim`; `plan` reports an **update**, not a replacement, and names no
   `# forces replacement`. `apply` succeeds, and an independent `GET` shows the PVC on the live
   deployment.
2. **No collateral wipe.** After criterion 1, an independent `GET` shows `object_storage`,
   `kubernetes_config` (including `redis_endpoint`), and the provider config block byte-identical to
   before. **This test must be written so that it FAILS against a write path that omits
   `file_storage` or over-sends `kubernetes_config`** — otherwise it is not testing the hazard the
   design is built around.
3. **`mount_path` is not fabricated.** A config setting only `persistent_volume_claim` produces state
   with `mount_path` null. Mutation-proof: restore the `Default` and confirm this test fails.
4. **Existing state is undisturbed** — *no test possible; satisfied by documented Core behaviour.*
   Originally written as two sequential `Config`-only steps asserting an empty plan for a
   pre-upgrade state shape (`mount_path` = `/mnt/shared`, config omitting it). That criterion is not
   testable: `ProposedNewState` guarantees it for any `Optional+Computed` attribute independent of
   anything in this provider, so no mutation of our code can make such a test fail. A test was
   written, proven to have no discriminating power, and deleted — correctly.
   **Recorded as Core-guaranteed rather than test-covered, so it is not mistaken for closed by a test
   that no longer exists.** The adjacent property that *is* fix-dependent, and does need a
   mutation-proven test, is criterion 6a: import now yields null where it previously yielded
   `/mnt/shared`.
5. **Clearing works.** Remove the `file_storage` block from a config that had one; `plan` reports an
   update; after apply an independent `GET` shows the shared storage gone.
6. **Import still round-trips.** Two tests, per CLAUDE.md — a three-step
   Create→Import→re-apply sequence cannot prove import recovery, because an `ImportState` step
   without `ImportStatePersist` runs in a throwaway directory:
   - **6a** — `ImportStateCheck` inside the import step asserting what import actually recovered:
     `mount_path` null where the API has none, the live value where it has one.
   - **6b** — two sequential `Config`-only steps reconstructing the imported shape (block omitted,
     then declared), asserting the plan action on the second is not a replace.
   The mock must **return** `file_storage` in the scenario under test. A fixture that omits it cannot
   represent the failure — that omission is precisely why the v0.15.2 `mount_targets` import bug
   shipped green.
7. **Running clusters produce a usable error.** With a cluster up, `apply` fails with a diagnostic
   naming the cause and the ways out. Real-infra; may be gated.
8. **The plan-time Anyscale-managed refusal actually fires — for both providers.** Every field it
   reads was unexercised before D2, so this is not a formality:
   - `aws_config.cloudformation_id` was **declared and never read** — a struct field with zero
     consumers until D2's guard. "Pre-existing" is evidence nobody deleted it, not evidence it
     populates.
   - `gcp_config.deployment_manager_id`/`infrastructure_manager_id` were **added by D2**, so they have
     never been observed carrying a value either.

   If the resources GET does not populate one of these — wrong key, different nesting, or simply not
   returned by the endpoint the provider calls — the guard reads nil forever and **silently never
   fires**, while looking like protection. The user then hits the apply-time 400 instead, which is the
   exact asymmetry this design claims to have removed.

   Needs no real infrastructure: a mock returning the field in the live-GET response, asserting the
   plan refuses. **Mutate the guard both ways** — populated must refuse, absent must not. A test that
   only checks the absent case passes against a field that never populates.

   **Partially de-risked by a read-only call, and only partially.** A real
   `GET /api/v2/clouds/{id}/resources` against the registered static fixture returns snake_case keys
   with `aws_config.cloudformation_id` **present, value null**. So for AWS the key name and nesting are
   confirmed — the external spec flattens it out of the internal provisioner record
   (`cloud_deployment_model.go:378`) — and the absent case behaves: null reads as not-managed and the
   update is allowed. Build the mock on that shape.
   Still unverified: the two **GCP** fields (no GCP cloud exists in the test org, and they are emitted
   two lines below the AWS one at `:406-407` but have never been seen on the wire), and a **populated**
   value for any of the three, since no managed cloud of either provider exists. So the observed
   evidence covers one of two providers and only the negative case — the mock-side work is still fully
   required.

   Cover the apply-time 400 translation too, as the backstop for a managed cloud the plan check
   somehow misses. Both messages must name the managed-versus-registered distinction. A mocked test
   suffices for the translation; see [G1.5](#g15-result-unreachable-and-no-longer-needed) for why
   there is no captured backend text to assert against.

9. **Drift is visible both ways** — **satisfied.** A warning naming the live and state values, in
   both the declared-but-dropped and live-but-undeclared directions, plus no warning for the
   [D1 residue](#required-suppression-d1s-legacy-residue-is-not-drift).
   Proven by two independent mutations, each reverted byte-clean:
   - **Removing the warn call** turns both direction subtests red; the suppression and
     nil-diagnostics subtests correctly stay green, since neither depends on a warning firing.
   - **Disabling only the suppression condition** turns the suppression subtest red *for the right
     reason* — a real spurious warning appears (`mount_path: state="/mnt/shared" live=""`) rather
     than the test passing vacuously.

   The second mutation is the transferable technique: **mutate the guard, not only the feature.** A
   suppression test passes both when the suppression works and when the thing it suppresses never
   occurred, and only breaking the guard distinguishes those. Apply the same to each of D2's three
   apply-time diagnostics — a test asserting "this error is raised" says nothing unless the condition
   that raises it can be shown to fire.
10. **CI actually runs all of it.** Names must match `^TestAcc[A-Za-z]+Resource` for the
   `acctest-resource` shard. Confirm by reading the shard's job log for a RUN line, not the green
   checkmark.

---

## Alternatives rejected

**Suppress `RequiresReplace` only for `mount_path`'s default materialisation.** The report's
suggestion and the first internal diagnosis. Rejected: `persistent_volume_claim` carries its own
unconditional `RequiresReplace`, so the plan still replaces the cloud. It removes one of two
redundant markers and reads as a fix while changing nothing — a placebo.

**Gate the `Default` and `RequiresReplace` by provider or compute stack.** Rejected: it makes the
schema's meaning depend on a sibling attribute's value, which practitioners cannot see in the schema
and reviewers cannot check locally. The value is inert on the PVC path for *every* provider, so
there is no provider for which the default is right — removing it is simpler and more honest.

**Refresh `file_storage` on `Read` as a non-breaking change.** Rejected: not available. Blocks
cannot be `Computed`; a non-Computed block populated by `Read` reproduces C12.

**Convert `file_storage` to a `SingleNestedAttribute` so it can be `Computed` and fully refreshed.**
The only complete fix for Defect 2. Rejected *here* as out of scope: it is a breaking HCL change
(`file_storage { … }` → `file_storage = { … }`) for every existing user, and it should not ride along
inside a bug fix. If full drift management on the config blocks is wanted, it deserves its own
decision covering all five of them together, not just this one.

**A separate `anyscale_cloud_file_storage` resource** owning the deployment's file storage, keyed by
`cloud_resource_id`, using the same read-modify-write preservation pattern as the IAM-mapping design.
Genuinely attractive: it puts the mutable/immutable split into the type system instead of hiding an
update path inside a resource whose every other config block is create-only, and it matches an
accepted precedent. Rejected for now: `anyscale_cloud.file_storage` already exists in users' configs
and state, so a second writer for the same backend fields creates the two-writers hazard this
provider has ruled against repeatedly. Introducing it would mean deprecating the block —
a migration the reported bug does not justify. Revisit if D2's read-modify-write proves unworkable.

**State upgrader to scrub fabricated `/mnt/shared` from existing state.** Rejected: a state upgrader
cannot see configuration, so it cannot tell a user-declared `/mnt/shared` from a
default-materialised one. Any rule is a heuristic, and the value it would clean is inert.

---

## Risks

1. **G1.1 fails and the read-modify-write has to echo more blocks.** Each echoed block brings its own
   clear-by-omission trap (`kubernetes_config` → `K8SRedisResources` is the one already found).
   Mitigation: G1.1 before any code; fall back to D2's error guard if it fails.
2. **A bug in the write path wipes live shared storage.** The highest-severity failure available
   here, and it is silent. Mitigation: acceptance criterion 2 written to fail against an omitting
   write path.
3. ~~**Anyscale-managed AWS VM clouds get a plan that says "update" and an apply that fails.**~~
   **Retired.** It rested on `cloudformation_id` being absent from the external spec, which was a
   false negative from a search in the wrong place — it is emitted at
   `cloud_deployment_model.go:378`. The refusal is plan-time detectable on both providers, so this
   risk does not exist. Kept visible rather than deleted, because a risk that turns out to be
   imaginary is worth recording as such: the next reader who rediscovers the field should not think
   they have found a new problem.
4. **Non-primary deployments have no guard on `provider`/`region`/`networking_mode`.** A malformed
   echo corrupts them silently — `_verify_primary_cloud_resource` only covers the primary.
   Mitigation: echo verbatim from the live GET; never derive from Terraform state.
5. **The running-clusters gate makes `apply` outcomes depend on unrelated user activity.** Accepted
   tradeoff, recorded as such: an actionable error beats a proposed cloud deletion.
6. **D3's warning appears on existing configurations that were silent before.** Intended. Worth a
   changelog sentence so it does not read as a new fault.

---

## Open decisions

1. **Do D2's primary path and its fallback ship together, or does the fallback ship first?** The
   fallback removes a destructive plan immediately but leaves the reporter without a way forward.
   Recommendation: ship the primary path; ship the fallback alone only if G1.1 fails. **Needs the
   user's call if Gate 1 slips.**
2. **Does a `file_storage` change on a non-primary deployment need extra guarding**, given no backend
   guard covers `provider`/`region`/`networking_mode` there? Recommendation: the provider refuses to
   PUT when the live spec's echoed values disagree with what it read, rather than trusting itself.
   Cheap, and it fails closed.
3. **Is a migration guide wanted?** Per repo policy this is the user's call every time. On the
   primary path there is nothing to migrate. On the fallback there is (`state rm` + re-import).
4. **Should the provider model an `is_anyscale_managed` observation?** It would let plan predict
   refusal 2 on GCP and would be genuinely useful beyond `file_storage` — but only GCP's provisioner
   IDs are exposed, so the attribute would be honest on GCP and silent on AWS. A half-true Computed
   attribute may be worse than none. Recommendation: no new attribute; put the distinction in the
   diagnostic and the docs. **Flagging rather than deciding — this is a provider-surface question
   wider than this bug.**
5. **The same block exists on `anyscale_cloud_resource` and is untested here.** The report exercised
   only `anyscale_cloud`. Every decision above applies to both, and both must be changed together —
   but `anyscale_cloud_resource`'s Update path has not been read as closely as `anyscale_cloud`'s.

## Version applicability

The report is against v0.24.1; the repo is at v0.27.0. Only four commits touched
`resource_cloud.go`, `resource_cloud_resource.go`, or `cloud_config_flatten.go` in that range
(`ab2c3b5`, `79ea1d0`, `539645d`, `500a122`), none of them on `file_storage`. Both defects reproduce
at HEAD as described. Line numbers cited in this document are HEAD's.
