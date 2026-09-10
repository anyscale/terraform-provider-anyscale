# Anyscale Scheduler — Terraform surface contract

Status: **design confirmed for reads; write path pending one verification gate** (see
[Verification gates](#verification-gates)).

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

Every enum above gets a framework `Validator` so a typo fails at plan rather than as a 422 at apply.

**`advanced_instance_config` is a JSON string, not `Dynamic`.** Upstream types it as an untyped
`object` with no properties. The repo already made exactly this call for the per-node
`advanced_instance_config` on `anyscale_compute_config`, and the recorded reason transfers precisely:
the field lives inside a list, and `Dynamic` inside a list is the known-broken case. Reuse the
convention rather than re-litigate it.

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
| `is_active` | Boolean. Always true for the version this resource manages; surfaced because the API returns it and it disambiguates a config read by version. |

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

  *Rejected alternative — apply an empty config on destroy.* An empty config is not a neutral state.
  The product docs warn that once any scheduling rule exists, unmatched workloads *fail instead of
  running*, and that a `nominal_quota` of `0` blocks a resource outright. A destroy that writes to the
  org could therefore take down workload admission. Writing on destroy also invents a mutation the
  user did not ask for. Same conclusion, and same reasoning, as `anyscale_org_user_role`, whose Destroy
  is state-only for the identical "no DELETE verb" reason.

### Plan-time behavior

`POST /api/v2/scheduler/config/validate` returns **204** on a valid document. Call it during
`ValidateConfig`/plan so an invalid config fails at plan with the server's own diagnostic, instead of
surfacing as a 422 mid-apply.

`POST /api/v2/scheduler/config/preview` is deliberately **not** adopted in v1. It returns a
projection whose meaning we would have to explain before it helps anyone, and plan-time disclosure is
worth adding only once we know what the projection actually says. Revisit with a named use case.

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
- 422 from apply/validate → surface the server's `detail` verbatim; do not paraphrase a validation
  message we did not author.
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

**Writes: NOT CONFIRMED. This is the one thing blocking full design confirmation.** Unverified:

1. **Round-trip normalization** — whether `POST /config` followed by `GET /config` returns the
   document unchanged. If the server reorders, defaults, or rewrites anything, the result is a
   perpetual diff, and the fix (semantic-equality plan modifiers, or `Optional+Computed` on the
   normalized fields) is a *schema* decision that cannot be retrofitted quietly.
2. Whether `version` increments predictably and `is_active` behaves as assumed.
3. Whether `validate` returns diagnostics specific enough to be worth surfacing at plan time.

**Why this is not just done:** there is no `DELETE`, so applying a first config to this org is not
cleanly reversible to "no config" — and the product docs warn that once any scheduling rule exists,
unmatched workloads *fail instead of running*. A careless first apply could break workload admission
on this org. Authorization requested under [open decisions](#open-decisions).

### Gate 2 — Framework/Core contract

Required before the write path is confirmed, and only reachable after Gate 1(1):

- If normalization exists, whether a semantic-equality plan modifier may rewrite `resp.PlanValue` for
  a nested list attribute at plan time. Framework source describes the mechanism without revealing
  every constraint Core enforces, so this needs a real `resource.Test` plan/apply, not a unit test.
- That a singleton resource's 404-Read → remove-from-state path does not trip
  "provider produced inconsistent result after apply."

---

## 7. Acceptance criteria

Exercisable without reference to implementation internals.

**Retirement**

1. `make build` and `make test` green with the GRS files deleted; no dangling references.
2. `go test ./internal/acctest/` compiles with **no** `grs_enabled` tag (guard test deleted alongside
   its subject).
3. `make docs` produces **no** diff — the deleted surface contributed no doc pages.
4. `anyscale_compute_config`'s four `machine_pool` paths are byte-identical before and after, on both
   the resource and the data source, and `docs/resources/compute_config.md` is unchanged.
5. `anyscale_cloud` Delete still detaches machine pools — existing cloud lifecycle tests unchanged and
   passing.

**New resource** — each blocked on the corresponding Gate 1 item.

6. Create → `version` set, `is_active` true, document readable back.
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

Every test must be shown to genuinely run, not skip — the CI shards match
`^TestAcc[A-Za-z]+Resource` and `^TestAcc[A-Za-z]+DataSource`, and a non-matching name neither runs
nor fails. Each regression test must be mutation-proven: introduce the regression, confirm the test
fails, revert byte-clean.

---

## 8. Risks

- **Beta schema churn** (§4) — accepted, with eyes open.
- **Perpetual diff from server normalization** — unquantified until Gate 1(1). Highest-impact unknown
  in this design.
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

Escalated rather than assumed.

1. **Authorization to satisfy Gate 1 for the write path** — apply a scheduler config to this org.
   Not cleanly reversible (no DELETE), and a config containing scheduling rules can affect workload
   admission. A flavors-only config with no `scheduling_rules` and no zero quotas looks materially
   safer and would still answer the normalization question; confirmation needed either way.
2. **Migration guide for the retirement** — repo policy makes this the user's call every time. The
   recommendation is no, on the stronger-than-usual ground that the types were never registered in any
   released build.

Closed since first draft:

- **Typed vs encoded document** — decided typed (§4). The beta-tracking commitment is accepted as
  part of owning the resource; it is a maintenance cost, not a design fork.
- **Retirement scope** — settled on upstream evidence (§2b/2c preamble): machine pools are a
  separate, live, undeprecated product. `anyscale_compute_config.machine_pool` and the
  `anyscale_cloud` detach path stay.
- **Admission-flag rollout** — no longer blocking. The flag is on for the test org, so the surface is
  buildable and testable; the 403 diagnostic ships regardless. Rollout breadth is
  [flagged](#the-admission-flag-is-still-a-gate-and-it-still-defaults-off-in-code) as a
  code-vs-configuration observation, not a design dependency.
