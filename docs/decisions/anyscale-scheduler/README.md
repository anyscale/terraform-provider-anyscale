# Anyscale Scheduler — Terraform surface contract

Status: **design confirmed — reads and writes both verified live against the API, and both verification
gates are closed** (see [Verification gates](#verification-gates)). No design decisions remain open.

The write probe changed three schema decisions from an earlier draft — `advanced_instance_config` now
needs semantic equality, empty lists are rejected at plan time, and `is_active` is dropped. Anything
citing this contract from before that probe is reading a superseded shape.

Product docs: <https://docs.anyscale.com/scheduler> — labeled **Beta: "The scheduler is in beta.
Names and fields may change."** That label is load-bearing for several decisions below.

---

## 0. Naming

The product's own naming is split, so the Terraform type name is a deliberate choice rather than a
transcription:

| Source | Name used |
|---|---|
| Docs site | "Anyscale Scheduler" (`docs.anyscale.com/scheduler`) |
| API route | `/api/v2/scheduler/config` |
| CLI command path | `anyscale scheduler config {apply,get,list}` |
| CLI/SDK help prose | "Anyscale Global Resource Scheduler" |
| Internal identifiers | `SchedulerConfigModel`, table `scheduler_configs`, service `globalresourcescheduler` |

A real rename did land upstream, but it was **identifiers only**: `op.rename_table("grs_configs",
"scheduler_configs")`. The user-facing prose name "Global Resource Scheduler" survived it, and the
feature flag is still `enable-grs-admission`. Separately, the CLI's `scheduler` group was *unhidden*
and marked beta on 2026-09-09 — a visibility change, with byte-identical help text before and after.

**Terraform type: `anyscale_scheduler_config`.** It matches the docs URL, the API route, and the CLI
command path — three of the four user-facing surfaces. Only the CLI help string says "Global Resource
Scheduler," and "GRS" is an internal-facing abbreviation this provider should not adopt.

**But the resource's `MarkdownDescription` must still name the alias once**, because "should not adopt"
is not the same as "users will never encounter it." A practitioner who runs the CLI's `--help` meets
"Global Resource Scheduler" with nothing connecting it to the type they just wrote, and the raw API
still returns the abbreviation in its own error text. One sentence: the Anyscale Scheduler is also
referred to upstream as the Global Resource Scheduler (GRS), the CLI help text and some API error
messages still use that name, and they are the same product. Not "formerly known as" — the upstream
rename was identifiers only and the prose name was never retired, so "formerly" would be false. And
none of the internal identifiers from the table above belong on a published page.

**The alias must NOT be justified by our own diagnostics, and this doc previously got that wrong.** An
earlier revision of this section claimed the decisive reason was that the admission-flag diagnostic
surfaces the server's `"GRS is not enabled for this organization."` verbatim. It does not: the
transport layer deliberately scrubs the abbreviation and substitutes the product name, and a unit test
asserts the diagnostic does *not* contain it. That scrub is correct and stays — it is the named,
actionable error §3 asks for, and the "surface the server's own words" rule in §3 governs the 422/400
*validation* messages, where the server knows constraints we do not, not the 403 capability gate, where
the actionable name is ours. Prose pointing a user at a string we guarantee they never see would be a
worse defect than the omission it was fixing.

---

## 1. This is not a rename

The predecessor surface (`anyscale_global_resource_scheduler` plus two data sources, disabled) modeled
**N named machine pools with per-cloud attach/detach**, driven by
`/api/v2/machine_pools/{create,update,delete,attach,detach}`.

The current surface is **one org-scoped, immutable-versioned configuration document**, driven by
`/api/v2/scheduler/config`. It has no name attribute, no per-cloud attachment, no delete verb, and
"update" means "apply a new version."

Renaming the old resource onto the new API would keep a type name while replacing its object model,
ownership boundary, and lifecycle. That is the worst available outcome: it offers no migration path
and makes the type name actively misleading. **Two independent changes instead:** retire the old
surface, introduce a new one.

---

## 2. Retirement scope — read this before deleting anything

"Retire the machine_pools-backed resources" resolves to three separable things. Only the first is in
scope.

### 2a. IN SCOPE — the disabled GRS surface. Delete. *(landed)*

`resource_global_resource_scheduler.go`, `data_source_global_resource_scheduler.go`,
`data_source_global_resource_schedulers.go`, their unit tests, the two `grs_enabled`-gated acceptance
test files, `globalResourceSchedulerSharedAttributes()` in `schema_shared_attributes.go`, and the
`CreateMachinePoolRequest` / `UpdateMachinePoolRequest` / `DeleteMachinePoolRequest` /
`AttachMachinePoolToCloudRequest` model structs plus the four response structs nothing references.

**Compatibility classification: not a change at all, from a user's perspective.** The resource and
both data sources were never registered in *any* released tag — `provider.go` carries the
commented-out entries at v0.1.0 through v0.27.1. No released binary ever exposed these types, so no
user state can contain them and no configuration can reference them without already failing to plan.
Confirmed independently by two shards, each with a positive control showing the check could find a
live registration.

Consequences that follow from that classification, not from convenience:

- **`skip-changelog`.** There is no user-reachable schema to remove and no version bump the deletion
  itself requires. The doc pages and example directories were already deleted (CHANGELOG.md:537).
- **No migration guide.** Nothing to migrate from. Per repo policy the call is the user's, so it is
  [asked explicitly](#open-decisions) rather than assumed — but the premise here is stronger than
  the usual "no production users": the types were never reachable.

**Implemented and independently verified** — ~3,350 lines net deletion; build, vet, lint, unit tests
and `test-compile` all clean, including under the now-vacuous `grs_enabled` build tag, and confirmed
by a second pass in a separate worktree. Both mechanical traps below were handled; they are recorded
because they are the parts a future reader would get wrong.

Two mechanical traps for whoever implements this:

- **`sweeper_scheduler_test.go` and `sweeper_scheduler_guard_test.go` must be deleted in the same
  commit.** The guard test (`TestSchedulerSweeperGatedBehindGRSEnabled`) reads the sweeper file as
  *text* and fails if its `//go:build grs_enabled` tag disappears. Deleting the sweeper alone leaves
  a live test reading a missing file. Also drop the sweeper from `sweeper_test.go:42`'s coverage
  comment and the `sweeper_org_guard_test.go` notes at `:20-22`.
- The sweeper is **not** currently dangerous, contrary to an earlier record: it is gated behind
  `grs_enabled` and no Makefile target or workflow passes that tag, so `make sweep` never compiles
  it. It is dead weight, removed for tidiness, not a live hazard.

### 2b/2c preamble — machine pools are a separate, live product, confirmed upstream

The two exclusions below rest on evidence, not caution. Traced against the upstream tree:

- Machine pools and the scheduler are enumerated as **separate subsystems** in the product's own
  architecture map, live in **separate Go services** (`globalresourcescheduler` vs
  `provisioned_cloud_provider`), and use **separate storage** — scheduler configs are a Postgres
  table, machine pools have no Postgres table at all and are served over gRPC.
- All 12 `/api/v2/machine_pools/*` routes still exist, and **none** carries a deprecation marker.
  Positive control: `deprecated=True` is used elsewhere in the same tree, so the zero is trustworthy.
- The `anyscale machine-pool` CLI group is registered, unhidden, and marked beta. Its only
  deprecation markers concern an unrelated `--format` flag.
- **Zero cross-references** between the two object models: `SchedulerConfig` has no machine-pool
  field, `MachinePool` has no scheduler-config field. The single link is directional and
  runtime-only — a describe response surfaces the scheduler's live *view* of a pool.

So "retire the machine_pools-backed resources" is a statement about *our disabled Terraform surface*,
not about the upstream feature. Removing live code that talks to a live, undeprecated API would be a
regression dressed as cleanup.

### 2b. OUT OF SCOPE — `anyscale_compute_config`'s `machine_pool` attribute. Do not touch.

Live and user-facing on a registered resource: four schema paths on `anyscale_compute_config`
(`head_node.cloud_deployment.machine_pool`, `worker_nodes[*]...`, and both under
`additional_resources`), four more on the data source, a backing Go model field whose `tfsdk` tag is
the wire name, three attribute-type/flatten maps, and — decisively — a **frozen V0 schema in
`resource_compute_config_upgrade.go:79` that `UpgradeState` uses to decode already-written state.**

Removing it would be a state-breaking change to a resource with real users. State carries schema
shape regardless of whether a given user set the field. This is a different object from the retired
resource: it *references* a machine pool by name rather than managing one.

### 2c. OUT OF SCOPE — the cloud-delete detach path. Do not touch.

`resource_cloud.go:1370,1401` call `GET /api/v2/machine_pools/` and
`POST /api/v2/machine_pools/detach` during `anyscale_cloud` Delete. The `machine_pools` router still
exists upstream and carries no deprecation marker. Removing this could make cloud deletion fail for
anyone with an attached pool. Retiring *our resource* says nothing about whether the *product*
feature is gone.

---

## 3. New surface: `anyscale_scheduler_config`

A singleton, org-scoped, **authoritative-write** resource. One org has zero or one active scheduler
config; this resource owns it.

### Purpose and ownership boundary

Owns the entire scheduler config document for the token's organization. It does not own queues,
workloads, or events — those are runtime state the scheduler derives from this document (§5).

Because the object is a singleton with no user-chosen key, **two Terraform configurations both
declaring it will silently fight**, each apply reverting the other. That hazard is inherent to a
singleton, not a flaw in this modeling, and it must be stated plainly in the resource's
`MarkdownDescription`. Same authoritative-write posture as `anyscale_cloud_access`.

### Configuration

Typed nested attributes mirroring the four document sections. All four optional — the API marks every
section optional, and an empty config is valid.

| Attribute | Shape | Notes |
|---|---|---|
| `resource_flavors` | list of nested | `name` (Required), `selector` (list of match expressions), `advanced_instance_config` (**JSON string**, see below) |
| `resource_queues` | list of nested | `name` (Required), `cohort_name`, `preemption`, `resource_groups` |
| `scheduling_rules` | list of nested | `resource_queue` (Required), `selector`, `priority_policy` |
| `recycle_policy` | single nested | `rotation_interval`, `max_workloads`, `max_idle_duration` |

Match expression: `key` (Required), `operator` (Required, enum `in` / `not_in` / `exists` /
`does_not_exist`), `values` (list of string).

`preemption`: `within_resource_queue` (`never` / `lower_priority`), `reclaim_within_cohort` (`never` /
`lower_priority` / `any`), `borrow_within_cohort` (`never` / `lower_priority`).

`resource_groups[]`: `covered_resources` (Required, list of string), `flavors` (Required, list of
`{name, resources[{name, nominal_quota, lending_limit, borrowing_limit}]}`).

`priority_policy`: `default`, `min`, `max`, `on_violation` (`reject` / `force_update`).

**Enum validators: yes for the closed semantic enums, no for the resource-name namespace.** `operator`,
the three `preemption` policies, and `on_violation` get a framework `Validator` so a typo fails at plan
rather than as a 422 at apply. `covered_resources` and `resources[].name` do **not**, even though the
server enforces a closed set — see below.

**`covered_resources` / `resources[].name` are a closed, case-sensitive, lowercase set upstream:
`cpu`, `gpu`, `memory_gb`, `tpu`.** `CPU` is rejected (logged in §6), and this set appears nowhere on
the docs page. Do not hardcode it in the schema: `tpu` was plainly added after `cpu`/`gpu`, accelerator
names keep arriving, and a frozen `OneOf` would reject a value the API had started accepting — a
provider release becoming the thing that blocks a new accelerator. The plan-time `/config/validate`
call returns the authoritative allowed list inside its own error message, which is precisely what that
call is for. List the current values in `MarkdownDescription` as *known at time of writing*.

**Empty lists are not representable — reject them at plan time.** The server collapses an empty array
to an absent key: `{"resource_flavors": [], "resource_queues": []}` reads back as `config: {}` with
both keys gone (logged in §6). So `resource_flavors = []` in HCL would be a permanent diff. Put
`listvalidator.SizeAtLeast(1)` on `resource_flavors`, `resource_queues`, and `scheduling_rules`, with a
description line telling the user to omit the section rather than declare it empty. Make the illegal
state unrepresentable rather than silently normalizing it. `recycle_policy` is the exception — an empty
*object* is preserved as `{}`, so it needs no such validator.

Two implementation constraints follow directly, and getting either half wrong is a perpetual diff: a
null section MUST be **omitted from the request body**, never sent as `[]`; and on read an **absent key
MUST map to null**, never to an empty list.

**`advanced_instance_config` is a JSON string, not `Dynamic`, and it needs semantic equality.**
Upstream types it as an untyped `object` with no properties. The repo already made the string call for
the per-node `advanced_instance_config` on `anyscale_compute_config`, and the recorded reason transfers
precisely: the field lives inside a list, and `Dynamic` inside a list is the known-broken case.

What does *not* transfer is byte comparison — but **not for the reason an earlier revision of this doc
gave, and the difference changes how the test must be written.** The wire is unstable (unordered dict,
integers widened to floats — §6 finding 2), and the original rationale stopped there. It should not
have: `flatten` re-marshals the parsed object through Go, and `encoding/json` sorts map keys and
renders `float64(3)` as `3`. **The API's reordering and widening therefore never reach state — state is
always Go-canonical.** Measuring the wire and asserting about state skipped a layer that canonicalizes.

The divergence that *does* survive is between Go-canonical state and whatever the practitioner wrote.
A hand-written or `file()`-loaded blob with its own key order and whitespace will differ from state
byte-for-byte on every plan, so a plain `types.String` still diffs forever — the conclusion holds, the
mechanism is the user's formatting rather than the server's. Use `jsontypes.NormalizedType`, whose
semantic equality absorbs key order and whitespace.

Two consequences follow, both load-bearing:

- **A `jsonencode()`-based test is a placebo.** `jsonencode()` emits sorted, whitespace-free JSON —
  byte-identical to what `flatten` produces — so such a test passes against a plain `StringAttribute`
  and proves nothing. It must use a hand-written or `file()`-loaded blob. (This is not hypothetical:
  the first implementation draft did exactly that and passed without the custom type.)
- **Numeric literal form is NOT absorbed, so it is a documented limitation, not a fixed one.**
  `jsontypes` decodes with `dec.UseNumber()` specifically to avoid normalizing numeric representation,
  so numbers compare as literal text and `1.0` does **not** equal `1`. A blob hand-written with `1.0`
  therefore diffs against the `1` that `flatten` produces — permanently, and because the API has no
  content dedupe and no delete verb, every such plan mints another immutable version. The schema
  description must say so plainly. Not worth "fixing": preserving the wire's numeric form in state
  would repair the rare hand-written `1.0` by breaking the common `1`, and a bespoke numeric-aware
  modifier would reimplement semantic equality and lose its contract.

Rejected fallback, recorded because it was the decided branch had Gate 2 failed: the `compute_config`
precedent — never refresh the blob, carry prior state forward, recover only at import — matched **by
flavor `name`, not by list index** the way `compute_config` does, since an index correspondence would
silently graft one flavor's blob onto another. Gate 2 passed, so this is not the shipped design.

**All ordering is significant and must be preserved, never sorted.** The product docs are explicit
that flavors within a resource group are tried in written order and that scheduling rules are
first-match-wins top to bottom. Terraform lists are order-sensitive, which matches — so `list`, never
`set`, for all four sections.

### Computed state

| Attribute | Notes |
|---|---|
| `version` | Integer. Every apply mints a new version. **No `UseStateForUnknown`** — it is volatile by construction, and that modifier on a volatile Computed attribute is a known crash source in this provider. |
| `created_at` | RFC3339 string, from the API. |
| `creator_id` | The identity that applied this version. |

**`is_active` is deliberately NOT modeled**, reversing an earlier draft of this contract. `GET /config`
returns only the active version — there is no way to reach a superseded one through the endpoint this
resource reads — so the attribute could only ever hold `true`. A Computed attribute with one possible
value is noise in every plan and state file that carries it. The flag becomes meaningful only for a
version fetched via `/config/versions/{n}`, which §5 declines to model.

`organization_id` is **not** an attribute. It is invariant across everything a given token sees, and
this repo's settled convention is that connection-level identity belongs in the zero-argument
`anyscale_organization` data source, not mirrored onto resources.

### Lifecycle

- **Create and Update are the same call:** `POST /api/v2/scheduler/config`. There is no create/update
  distinction on the wire; both apply a new version. Nothing about this resource should pretend
  otherwise.
- **No `RequiresReplace` on any attribute.** Every field is updatable by applying a new version.
  Using replacement here would be inventing a destroy the API does not have.
- **Read:** `GET /api/v2/scheduler/config` refreshes the document, `version`, and the other computed
  fields. An out-of-band apply (CLI, console) therefore surfaces as drift, and the next apply restores
  the declared config as a new version. That is correct authoritative-write behavior.
- **Read on absent:** the endpoint returns **404** with `{"error":{"detail":"No active scheduler
  config found."}}` when the org has no config (logged in §6). Read must treat that as *gone* and
  remove the resource from state. **Do not route this through `DoRequestAndParse`'s accepted-status
  list** — that helper swallows 404s, which would turn "config deleted out of band" into a silent
  success with stale state.
- **Delete:** **state-only removal, plus a warning.** There is no `DELETE` route on
  `/api/v2/scheduler/config` — confirmed against the OpenAPI path list (§6). Terraform forgets the
  resource; the org's scheduler config stays active. The warning must say exactly that, because a
  user reasonably reads `terraform destroy` as "the scheduler stops governing my workloads," and it
  will not.

  *Rejected alternative — apply an empty config on destroy.* An empty document **is** accepted by the
  server: `{"config": {}}` validates 204 and applies cleanly (logged in §6), so this alternative is
  mechanically available and was rejected on behavior, not feasibility. An empty config is not a
  neutral state. The product docs warn that once any scheduling rule exists, unmatched workloads *fail
  instead of running*, and that a `nominal_quota` of `0` blocks a resource outright — so clearing the
  document changes admission for every workload in the org. A destroy that writes to the org
  therefore invents a mutation the user did not ask for, at the moment they are least expecting one.
  Same conclusion, and same reasoning, as `anyscale_org_user_role`, whose Destroy is state-only for the
  identical "no DELETE verb" reason.

  What *does* change now that the empty document is confirmed accepted: the warning should name it as
  the **user's own remedy** rather than leaving them with a dead end. "To clear the config, declare
  this resource with every section omitted and apply, or use the Anyscale CLI/console." The version
  cited in the warning must be the value from **state** (`version`, as last observed by Terraform) —
  `DeleteRequest` carries State, not Config, so the live version may already be newer, and the wording
  must not claim otherwise.

### Plan-time behavior

`POST /api/v2/scheduler/config/validate` returns **204** on a valid document and is non-mutating
(confirmed in §6 — the version counter did not advance across repeated validate calls). Call it during
plan so an invalid config fails at plan with the server's own diagnostic, instead of surfacing as an
error mid-apply.

It carries more weight here than a typical plan-time nicety, because it is where the values this
schema deliberately does **not** enumerate get checked: an unknown `covered_resources` entry comes
back from `/validate` with the authoritative allowed list in the message. That is the whole reason the
resource-name namespace can stay un-hardcoded (§3).

Two distinct error shapes, both confirmed live, and the implementation must handle both:

| Status | When | `detail` shape |
|---|---|---|
| **422** | Document shape is wrong (bad enum value, wrong type, unknown key) | **Array** of `{loc, msg, type}` objects, plus a flat `message` string and an echo of the entire submitted request body |
| **400** | Shape is fine but cross-references don't resolve (queue naming an unknown flavor, rule naming an unknown queue) | **Plain string** |

Prefer the flat `message` field when present — it is already a readable, ready-to-print sentence — and
fall back to rendering `detail` for the 400 case. Do **not** blindly `detail`-stringify: on 422 that
yields a Go dump of a slice of maps. Do **not** include the echoed request body in a diagnostic; it is
the user's own config coming back and can be large.

`POST /api/v2/scheduler/config/preview` is deliberately **not** adopted in v1. It is now known to
return real content — `{corpus: {window_days: 90, total_events, distinct_combos, truncated,
snapshot_at}, verdicts[], lint[], unevaluable_rules[], preview_unavailable}` — i.e. an advisory
analysis of how the proposed document *would have* classified the last 90 days of workload events.
That confirms rather than weakens the deferral: it is a judgment call about historical traffic, not a
statement about the resource being planned, and surfacing it as a plan-time diagnostic would put
probabilistic prose in front of a user reviewing a diff. The one piece worth revisiting with a named
use case is the `lint[]` array, which is the closest thing to a plan-time warning the API offers.

### Import

**Import ID is the organization ID**, validated against the token's own org (from `userinfo`) and
erroring on mismatch.

A singleton has no natural identity, so the alternatives were a fixed sentinel (`current`) or an
ignored/empty ID. Organization ID is chosen because it makes the import state *which* org is being
adopted and fails loudly on a mismatch. This repo has a documented history of wrong-org operations
concealing themselves, and an import that silently adopts whatever org the ambient token points at is
the same failure shape.

Post-import expectation: a config declaring the same document plans clean. Because the whole document
is both configuration and refreshed state, there is no derived-field recovery problem here — the
import-round-trip bug class that affects the cloud resources does not apply.

### State compatibility

New resource, no prior state, `Version: 0`, no upgrader. **Additive.**

### Diagnostics

- 404 on Read → resource removed from state (no error).
- 422 / 400 from apply or validate → surface the server's own words; do not paraphrase a validation
  message we did not author. Per the table above, prefer the flat `message` field, render `detail` as
  a string only for the 400 case, and never echo back the submitted body.
- **Admission-flag rejection → a named, actionable error**, not a bare 403. Every scheduler route is
  behind an org-scoped feature-flag dependency upstream, which returns **403 with detail `"GRS is not
  enabled for this organization."`** An org without the flag must be told the scheduler is not enabled
  for them and how to get it enabled, matching how the K8S-gated system-cluster surface reports its
  501. Do **not** let this surface as a generic permission error — a 403 that reads like an RBAC
  problem will send users hunting the wrong thing. See §8 for why this path must be implemented even
  though the flag is on for the test org.
- Destroy → the warning described above.

---

## 4. Typed schema vs encoded document — DECIDED: typed

Two candidate models for the config document:

**Option A — typed nested attributes** (§3). Per-field diffs, plan-time enum validation, readable
plans, real Terraform citizenship. Cost: the product docs say field names may change during beta, so
every upstream rename or addition becomes a provider change.

**Option B — a single `config` JSON (or YAML) string** with semantic equality. Immune to beta churn,
ships in a fraction of the time, and mirrors the CLI's own `scheduler config apply -f file.yaml`
ergonomics. Cost: permanent. Every change shows in a plan as one opaque whole-document replacement,
no field is validated before apply, no drift is attributable to a field, and `terraform plan` output
stops being a review artifact.

**Decision: Option A, typed, with one carve-out.** The reasoning is asymmetry of permanence. Churn
costs are real but bounded and additive — a new upstream field is a small PR, and a renamed one is a
deprecation we already know how to run. Illegible plans are unbounded and permanent: a user reviewing
a quota change would see a single string attribute change and have to diff YAML by eye, which is
precisely the failure this provider exists to prevent. A blob attribute also makes every acceptance
criterion in §7 weaker, because there is nothing to assert on but the blob.

The carve-out is the leaf the API itself refuses to type: `resource_flavors[].advanced_instance_config`
is an untyped `object` with no declared properties upstream, so it is modeled as a **JSON string**,
matching the settled convention for the per-node `advanced_instance_config` on
`anyscale_compute_config`. Typing the spine and encoding the genuinely untyped leaf is not a
compromise between A and B — it is A, applied honestly to a field that has no schema to mirror.

Two obligations follow from choosing typed, and they are part of the decision, not caveats to it:

1. **Unknown upstream fields must not be silently dropped.** If the API returns a field the schema
   does not model, the user's state is quietly lossy. Track the upstream schema as part of owning
   this resource.
2. **The beta label goes in the resource's `MarkdownDescription`**, stating that the underlying
   product is beta and that field names may change. `tfplugindocs` renders no badge for this, so it
   has to be hand-written prose.

---

## 5. Queues, events, and the rest of the read surface — not in v1

The full read surface is larger than the config routes: `/scheduler/overview`, `/scheduler/queues`,
`/scheduler/queues/{queue_id}`, `/scheduler/queues/{queue_id}/workloads`, `/scheduler/events`,
`/scheduler/top-consumers`.

All of it is **observability, not configuration.** Modeling live queue depth, running workloads, or an
event stream as Terraform data sources would be mirroring REST endpoints into a declarative tool that
has nothing declarative to do with the answers. Worse, the values are volatile: anything interpolating
them produces plan churn on every refresh for reasons unrelated to the user's configuration.

Not adopted in v1. Revisit only for a specific surface with a named end-user consumption path — the
same bar that removed `workload_service_url_auth` before it shipped.

---

## 6. Verification gates

### Gate 1 — API response shape

**Reads: CONFIRMED**, live against the org, this session. The admission flag *is* enabled here — these
are real logged responses, not inferences:

```
GET /api/v2/scheduler/config            → HTTP 404  {"error":{"detail":"No active scheduler config found."}}
GET /api/v2/scheduler/config/versions   → HTTP 200  {"results":[],"metadata":{"total":0,"next_paging_token":null}}
GET /api/v2/scheduler/queues            → HTTP 200  {"results":[],"metadata":{"total":null,"next_paging_token":null}}
GET /api/v2/scheduler/events            → HTTP 200  {"results":[],"metadata":{"total":0}}
```

Route inventory and every model shape in §3 are transcribed from the live OpenAPI document
(`GET /api/v2/openapi.json`, HTTP 200), which confirms two facts the design rests on:

- **No `DELETE` on `/api/v2/scheduler/config`** — the basis for the Destroy decision.
- **No `cloud_id` (or any other) parameter on `GET /config`, `POST /config`, `POST /config/validate`,
  or `POST /config/preview`.** Only `/config/versions` (paging) and `/config/versions/{version}`
  take parameters at all. The config is therefore genuinely org-scoped with no per-cloud variant,
  which is what makes the singleton modeling correct rather than merely convenient. (An upstream
  trace described the config as "per-org, optionally per-cloud"; the wire contract does not support
  the second half, and the wire contract governs.)

`POST /config/validate` was additionally confirmed non-mutating and functional — **204** on a full
valid document, with no config created as a side effect.

Note the product doc page documents **three** config sections; the API has **four**. `recycle_policy`,
`resource_queues[].cohort_name`, and `resource_flavors[].advanced_instance_config` appear in the API
and not on that page, and `lending_limit` / `borrowing_limit` likewise. The schema above follows the
API.

**Writes: CONFIRMED.** Run under explicit user authorization. Seven real config versions were applied
to the test org and each was read back and diffed field by field against what was sent. The org was
left on **version 7 = the empty document**, governing nothing — no flavors, no queues, no rules, so no
workload admission is affected.

Three of the six findings changed the schema, and each is written into §3 rather than only recorded
here.

**1. Typed fields round-trip byte-stable. No normalization anywhere.** Zero server defaulting, zero
reordering, zero sorting. Every list came back in written order: flavors, queues, flavors within a
resource group, resources within a flavor, and `covered_resources` returned as the unsorted
`["gpu","cpu"]` exactly as sent. Duration strings are verbatim (`"30m"`, `"1h30m"` — not
canonicalized to a common unit). Integers arrive as floats on the wire (`8` → `8.0`), which is
invisible through a `Float64` attribute.

*Consequence:* **no semantic-equality plan modifiers and no `Optional+Computed` on any typed field.**
Plain `Optional` throughout. The normalization contingency this gate existed to detect does not exist.

**2. CHANGED — the untyped blob is not byte-stable, so `advanced_instance_config` cannot be a plain
string.** `advanced_instance_config` round-trips through an unordered dict *and* coerces numbers: a
sent `{"nested":{"a":1,"b":…}}` read back as `{"nested":{"b":…,"a":1.0}}`, and the top-level key order
differed between two reads of the *same* version. The `1.0` was confirmed to come from the server, not
from the local JSON printer, by grepping the raw response bytes.

*Consequence:* `jsontypes.NormalizedType` (§3) — but note the correction recorded in §3: this wire
instability is **not** what reaches state, because `flatten` re-marshals through Go and canonicalizes
key order and numeric form. The instability that survives into a plan is between Go-canonical state and
the practitioner's own formatting. The schema decision is unchanged; the reasoning and the test shape
are not.

**3. CHANGED — an empty list is not representable.** Sent `{"resource_flavors": [], "resource_queues":
[]}`; read back `config: {}` with both keys gone. The server collapses an empty array to an absent
key. *Consequence:* `listvalidator.SizeAtLeast(1)` on the three list sections, and the
omit-null-vs-send-`[]` / absent-maps-to-null implementation constraint in §3. An empty **object** is
*not* collapsed — `recycle_policy: {}` is preserved — so that section takes no such validator.

**4. CHANGED — drop `is_active`.** `GET /config` only ever returns the active version, so the
attribute can only hold `true`. Recorded in §3.

**5. `covered_resources` / `resources[].name` is a closed, case-sensitive, lowercase set:** `cpu`,
`gpu`, `memory_gb`, `tpu`. `CPU` is rejected. The allowed list is returned in the server's own error
message, and appears nowhere on the product docs page. §3 explains why this is deliberately *not*
hardcoded as a validator.

**6. No content dedupe.** A byte-identical re-POST of an unchanged document minted version 2 rather
than returning version 1. This is what raises findings 2 and 3 from polish to schema changes: a
perpetual diff here would not merely be noisy, it would append a junk version to an immutable audit
log on **every single apply**, forever, with no delete verb to clean up after it.

Two error shapes were confirmed, and both negative controls were run *before* trusting any 204 — a
deliberately malformed document returned **422** (array `detail`, flat `message`, echoed request body)
and a semantically invalid one returned **400** (string `detail`). That is the positive control
proving `/config/validate` discriminates; without it, its 204s would have meant nothing. The shapes
are tabulated in §3.

Also confirmed incidentally: `GET /config/versions` returns `{results, metadata.total,
next_paging_token}`; `GET /config/versions/99` → **404** `"Scheduler config version 99 not found."`;
and `/config/preview` returns the corpus/verdicts/lint structure described in §3.

**Referential integrity is enforced server-side in both directions** — a queue naming an unknown
flavor and a rule naming an unknown queue each return 400. This retroactively justifies
one-resource-per-document: had the sections been split across separate Terraform resources, no apply
ordering could keep the references valid, because it is one immutable document minted in a single
call.

### Gate 2 — Framework/Core contract

- **CLOSED — nested semantic equality holds.** `jsontypes.NormalizedType`'s semantic equality *is*
  consulted for a value nested inside a `ListNestedAttribute` element: empty plan confirmed with a real
  `resource.Test`, mutation-proven by dropping `CustomType` and observing red
  (`internal/acctest/resource_scheduler_config_advanced_config_acc_test.go`). The `compute_config`
  fallback in §3 is therefore not the shipped design.

  **Two facts about how it holds, both of which corrected this document rather than confirming it.**
  Whitespace and key order are absorbed; **numeric literal form is not** — `jsontypes` decodes with
  `dec.UseNumber()` (verified at `normalized_value.go:104`), so `1.0` does not equal `1`. And the case
  the gate exists to cover is a *hand-written* blob, not a `jsonencode()`d one: `flatten` re-marshals
  through Go (`json.Marshal(map[string]any)`), which sorts keys and renders `float64(3)` as `3`, so a
  `jsonencode()` config is byte-identical to state and passes against a plain `StringAttribute`. Both
  were found by running the test, not by reading framework source — which is precisely the failure mode
  Gate 2 exists to catch, and this time it caught the design doc.
- That a singleton resource's 404-Read → remove-from-state path does not trip "provider produced
  inconsistent result after apply."

The originally-planned Gate 2 item — whether a semantic-equality modifier may rewrite `resp.PlanValue`
for a *typed* nested attribute — is **moot**: Gate 1 finding 1 proved there is no normalization on any
typed field, so no such modifier exists to verify.

---

## 7. Acceptance criteria

Exercisable without reference to implementation internals.

### Test-strategy ruling: this resource can never have a sweeper, so default to the mock

**There is no `DELETE` verb anywhere on the scheduler API, and there is no content dedupe** — a
byte-identical re-`POST` minted version 2 rather than recognising the document (both verified live;
see §6 Gate 1). So **every apply in every acceptance test permanently appends an immutable version to
the target org's config history, and nothing can remove it** — not a sweeper, not manual cleanup, not
the console.

This makes the repo rule "a new resource type that creates real backend state gets a sweeper"
**unsatisfiable here rather than merely unmet.** Record it as a documented exception; a missing
scheduler sweeper is not a coverage gap and must not be filed as one. (The retirement deletes the old
`sweeper_scheduler_test.go` — see §2 — and no replacement is possible.)

Consequences for how the criteria below are written:

- **Default to the mock server** for every criterion that can be satisfied there — the whole
  lifecycle, plan-time, and diff-stability set. Criteria 15/15b already demonstrate a mock is
  sufficient for lifecycle work, and the mock is the only place a multi-apply test is free. Drift and
  update criteria are inherently multi-apply, which is exactly where real-API cost compounds.
- **Reserve the real API for what cannot be faked:** out-of-band drift, plus one minimal real
  create + read-back. Two real applies, not twenty.
- **Carry the confirmed wire shape into every mock** (already policy, restated because it is load
  bearing here): ints arrive widened to floats, an empty list reads back as an *absent key*, typed
  fields round-trip byte-stable with nothing sorted. A fixture sending something the real API would
  never send in that scenario can pass against a broken fix and prove nothing.

The constraint is not permissions — applying configs to the test org is authorized. It is that the
cost is permanent and one-way: free-looking while the tests are being written, unfixable afterward.

**Retirement**

1. `make build` and `make test` green with the GRS files deleted; no dangling references.
2. `go test ./internal/acctest/` compiles with **no** `grs_enabled` tag (guard test deleted alongside
   its subject).
3. `make docs` produces **no** diff — the deleted surface contributed no doc pages.
4. `anyscale_compute_config`'s four `machine_pool` paths are byte-identical before and after, on both
   the resource and the data source, and `docs/resources/compute_config.md` is unchanged.
5. `anyscale_cloud` Delete still detaches machine pools — existing cloud lifecycle tests unchanged and
   passing.

**New resource.** Gate 1 is closed, so none of these are blocked any longer; 15–18 exist because of
what it found.

6. Create → `version` set, document readable back field for field.
7. Re-apply the identical config → **empty plan**. This is the perpetual-diff guard and the single
   most important test here.
8. Update one field → in-place update, no replacement, `version` advances.
9. Out-of-band apply → next `plan` shows drift; apply restores the declared document.
10. Config deleted out of band → Read removes it from state rather than erroring. Must be proven
    against a mock that actually returns the real 404 body above; a fixture that cannot represent the
    failure cannot detect it.
11. Destroy → succeeds, emits the warning, makes **no** write call. Assert the absence of the write.
12. Import by org ID → `ImportStateCheck` asserts the recovered document; a mismatched org ID errors.
13. Invalid enum value → fails at **plan**, not apply.
14. Order of `scheduling_rules` and `flavors` round-trips exactly; a reordered config produces a
    non-empty plan.

The next four all follow from Gate 1 findings, and every one of them guards a failure mode that would
otherwise mint a junk config version on every apply forever (finding 6 — there is no dedupe and no
delete). Each must be **mutation-proof**: introduce the regression, confirm the test goes red, revert
byte-clean.

15. **Blob semantic equality, nested.** *(met — this is the Gate 2 proof.)* Config declaring a
    **hand-written** blob whose key order and whitespace differ from Go-canonical form → empty plan on
    re-plan. Mutate by dropping `CustomType` and confirm red.

    **Do not write this with `jsonencode()`.** `jsonencode()` output is byte-identical to what
    `flatten` produces, so the test passes against a plain `StringAttribute` and asserts nothing — the
    first implementation draft failed exactly this way. Use a literal heredoc or `file()`.
15b. **Numeric literal form diffs, and that is the documented contract.** A blob hand-written with
    `{"a": 1.0}` produces a **non-empty** plan, because `jsontypes` compares numbers as text. Assert
    the diff rather than an empty plan: this pins a known limitation so it is discovered by a test
    instead of by a user, and guards against a future "improvement" that silently swallows it. The
    schema description must state the rule (write integers without a decimal point), since the cost of
    tripping it is an extra immutable config version on every apply, forever.
16. **Empty-list rejection.** `resource_flavors = []` fails at **plan** with the omit-the-section
    guidance, never reaching apply. Mutate by removing the validator and confirm the test catches the
    resulting empty-array literal reaching the wire.
17. **Null section omitted from the request body.** Against a body-capturing mock: a config with no
    `scheduling_rules` sends a body with **no `scheduling_rules` key at all** — not `null`, not `[]`.
    Assert on the captured bytes. (Note the repo's recorded trap: capture a *snapshot*, not a live
    reference to a map the handler goes on to mutate.)
18. **Absent key reads back as null, not as an empty list.** Mock returns `config: {}`; state holds
    null for all three list sections; an immediately following plan is empty. 17 and 18 are two halves
    of the same contract and either half alone leaves a permanent diff.

Every test must be shown to genuinely run, not skip — the CI shards match
`^TestAcc[A-Za-z]+Resource` and `^TestAcc[A-Za-z]+DataSource`, and a non-matching name neither runs
nor fails. Each regression test must be mutation-proven: introduce the regression, confirm the test
fails, revert byte-clean.

---

## 8. Risks

- **Beta schema churn** (§4) — accepted, with eyes open.
- **Perpetual diff from server normalization** — **retired as a risk for typed fields** (Gate 1
  finding 1: no normalization exists) and **narrowed to one leaf** for the untyped blob, where it is
  now a confirmed certainty rather than a risk and is handled by `jsontypes.NormalizedType`. The
  residual is Gate 2: whether semantic equality holds for a value nested in a list. Sharper, and much
  smaller, than when this design was drafted.
- **Every apply is permanent** — no dedupe, no delete verb (Gate 1 finding 6). Any diff bug does not
  just annoy; it appends to an immutable org-level audit log on every apply. This is why the §7
  diff-stability criteria are non-negotiable rather than nice-to-have.
- **Silent singleton contention** — inherent; mitigated by documentation only.
- **Destroy surprises users** — mitigated by the warning; no better option exists without a DELETE
  verb.

### The admission flag is still a gate, and it still defaults off in code

Worth stating precisely, because the working assumption elsewhere is that the scheduler is enabled for
every organization by default.

**In the upstream source it is not unconditional.** Every scheduler route — config, queues, events, on
both `api/v2` and the one `ext/v0` events route — depends on an org-scoped feature-flag check whose
in-code fallback is `False`:

```
if not org_ld.variation(FLAG_ENABLE_GRS_ADMISSION, False):
    raise AnyscaleHTTPException(403, detail="GRS is not enabled for this organization.")
```

A second call site in the cluster-manager config service uses the same `False` fallback.

What that does and does not establish:

- **Does:** the 403 path is live code, not vestigial. Any org for which the flag evaluates false gets
  a hard 403 on every scheduler call, so the provider must implement the §3 diagnostic regardless of
  current rollout.
- **Does:** the flag is on for the test organization — every route returned a real body, no 403
  (§6).
- **Does not:** say anything about rollout breadth. The flag's *targeting* lives in the feature-flag
  service, not in the source tree, so "on for all orgs" is not verifiable from code. It may well be
  fully rolled out; the code-side default is simply the wrong place to read that from.

Provider consequence either way: keep the diagnostic, and do not make the schema or lifecycle depend
on the flag. If the intent is that the scheduler is unconditionally available, the gate itself is the
thing to remove upstream — flagged, not worked around.

---

## 9. Open decisions

**None outstanding, and both verification gates are now closed.**

Closed since first draft:

- **Gate 2 — nested semantic equality** — **passed**, mutation-proven (§6). It corrected two pieces of
  this document's own reasoning on the way through: state is Go-canonical rather than wire-shaped, and
  numeric literal form is not absorbed. Recorded at §3 and criteria 15/15b.
- **Whether to name the GRS alias, and on what grounds** — **yes, once, in the resource's top-level
  description**, on the strength of the CLI help text and raw API errors only. The earlier rationale
  citing our own admission-flag diagnostic was false and is retracted in §0; the transport's scrub of
  the abbreviation stands unchanged.

- **Authorization to satisfy Gate 1 for the write path** — **granted**, and exercised. Seven versions
  applied; the org was left on an empty document governing nothing. Results in §6; three schema
  changes followed.
- **Migration guide for the retirement** — **not required.** User's call, per repo policy, and made
  explicitly. The recommendation had been no on the ground that the types were never registered in any
  released build; that is now the decision.

- **Typed vs encoded document** — decided typed (§4). The beta-tracking commitment is accepted as
  part of owning the resource; it is a maintenance cost, not a design fork.
- **Retirement scope** — settled on upstream evidence (§2b/2c preamble): machine pools are a
  separate, live, undeprecated product. `anyscale_compute_config.machine_pool` and the
  `anyscale_cloud` detach path stay.
- **Admission-flag rollout** — no longer blocking. The flag is on for the test org, so the surface is
  buildable and testable; the 403 diagnostic ships regardless. Rollout breadth is
  [flagged](#the-admission-flag-is-still-a-gate-and-it-still-defaults-off-in-code) as a
  code-vs-configuration observation, not a design dependency.
