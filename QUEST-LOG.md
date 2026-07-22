# Quest Log — terraform-provider-anyscale

Dated engineering-journal entries, newest first. One entry per quest/session.

---

## 2026-07-22 — System Cluster Support (kickoff)

**Goal.** Implement Terraform support for Anyscale System Clusters. Initial scope: let
Terraform ensure that a Cloud's System Cluster is running (declarative goal, not an
imperative "start" action). Party: tfp-architect (design/coordination), tfp-assayer
(API discovery/validation), tfp-forge (client/polling/timeout foundations), tfp-shipwright
(Terraform resource/data-source surface), tfp-scribe (decision record, docs, handoff).

**Base.** All five hero branches (`crystl/tfp-*`) confirmed at `main @ 7d59a9e` (v0.17.0,
mount_targets attribute conversion) — no prior work on this feature exists on any branch.

**Execution model.** Two waves. Wave 1 (concurrent, this entry): discovery + design, gated
by a design checkpoint before any implementation. Wave 2: implementation + validation,
unblocked only once the interface and backend API behavior are both confirmed.

**Interface options on the table** (from the mission brief, expanded by tfp-architect):

- **A — Declarative resource** (architect's prior lean, pending API evidence):
  ```hcl
  resource "anyscale_system_cluster" "primary" {
    cloud_id = anyscale_cloud.primary.id
  }
  ```
  Meaning: "ensure the System Cluster for this Cloud is running." Declarative, idempotent,
  simple lifecycle — the natural Terraform shape *if* the backend supports it cleanly.
- **B — Action-like attribute**: `resource "anyscale_system_cluster" "primary" { cloud_id = ...; start = true }`.
  Mirrors an API action but raises open questions about what `start = false` would mean and
  whether Terraform can meaningfully reconcile drift on it.
- **C — Sub-block on `anyscale_cloud`**: `system_cluster { running = true }` nested in the
  existing cloud resource. Raises questions about independent lifecycle and whether it
  introduces unexpected diffs to the Cloud resource or breaks its existing boundaries.

**Critical design questions** (tfp-architect, gating the checkpoint):

1. Cardinality — exactly one System Cluster per Cloud?
2. Discovery — can the cluster be found reliably using only `cloud_id`?
3. State identity — what stable ID do we store: `cloud_id`, a system-cluster ID, or both?
4. Status model — can refresh distinguish running / starting / terminated / missing /
   disabled / failed?
5. **SCOPE GATE** — is "enable" separable from "start"? Per the mission: if the backend
   cannot separate them, we pause at the checkpoint and document the constraint rather than
   silently expanding scope. Highest-priority unknown; assigned to tfp-assayer.
6. Idempotency — is starting an already-running cluster a safe no-op, or does it error/restart?

**Non-goals** (explicit, from the mission): stop, restart, resizing, autoscaling, compute
configuration, enabling/disabling the System Cluster (unless the minimal design requires it),
and any unrelated Cloud features.

**Workstreams in flight** (Wave 1, concurrent):

- tfp-assayer (+ fanout worker): trace the console UI (enable/start System Cluster) through
  the frontend into the backend API/SDK; answer design questions 1–6 with verified evidence,
  clearly separated from inference.
- tfp-forge: catalogue reusable client/polling/timeout/diagnostic patterns already
  established in the provider (`api_helpers.DoRequestAndParse[T]`, existing async
  settle-wait loops, `timeouts.Block` usage) — review only, no build yet.
- tfp-shipwright (+ fanout worker): catalogue resource/data-source/import/lifecycle
  conventions on the Cloud resources — how `Read` avoids perpetual diffs, import identity
  shape, Computed vs. Optional conventions, plan modifiers — review only, no build yet.
- tfp-scribe (me): stood up this decision record and `HANDOFF.md`; researching doc-facing
  precedent (non-destructive-delete language, async/polling doc conventions, child-of-Cloud
  import format, singleton-by-parent-ID data source shape) to ground the eventual docs and
  to feed the design checkpoint with "how has this provider documented similar shapes before."
- tfp-architect: architecture recon; owns checkpoint synthesis once evidence lands.

**Decisions.** None locked yet — this entry will be updated in place as Wave 1 findings
arrive, and a new dated entry will follow once the design checkpoint resolves the interface.

**Status.** Wave 1 discovery in progress. No code written. No decision made.

---

### Wave 1 findings (appended as they land)

**tfp-forge — client/polling/timeout/diagnostic catalogue (complete).** Full detail in
`spec.json:forge_client_patterns`. Headline points:

- **Closest polling analog**: `waitForServiceState`/`WithTiming`
  (`internal/provider/service_helpers.go:109,125`) — generic "poll GET until `current_state`
  hits a target, a terminal-error bucket, or timeout." Same function serves Create
  (target=`RUNNING`) and Delete (target=`TERMINATED`); only the target differs. Shape: a pure
  `evaluate*State(result, target) -> (done, err)` classifier, map-based error/continue-state
  buckets, an unrecognized state treated as CONTINUE not a hard error (so a new benign backend
  state doesn't break every apply, per the "F6 resilience rule"), and a `WithTiming` split
  (timeout+interval as params) for millisecond-scale tests over a production wrapper pinning
  real values as package consts. This is forge's proposed template to adapt for System Cluster,
  pending assayer's real status enum.
- **No `terraform-plugin-framework-timeouts` usage anywhere in this repo** (confirmed absent
  from go.mod/go.sum). The established idiom is a plain `schema.StringAttribute` holding a Go
  duration string (service-style, e.g. `rollout_timeout` default `"45m"`, parsed via
  `time.ParseDuration`) **or** a hardcoded internal Go const with no user-facing attribute at
  all (cloud-style, e.g. `resource_cloud_resource.go`'s `createTimeout := 30*time.Minute`).
- **State-persistence idiom** (near-identical wording in `resource_service.go` and
  `resource_cloud_resource.go` Create): persist state immediately after the create/start call
  succeeds and returns an identifier, **before** the poll/wait step — so a later failure never
  orphans a real backend object with zero Terraform record. Even a failed/timed-out wait still
  writes its last-observed result into state before surfacing the diagnostic. Implication for
  System Cluster: persist cloud_id/cluster identity to state right after the start response,
  before polling for healthy.
- **Diagnostics helpers** (`internal/provider/error_helpers.go`): `AddAPIError`,
  `AddJSONError`, `AddConfigError`, `extractAPIErrorDetail` (unwraps the backend's
  `{"error":{"detail":...}}` shape so callers surface Anyscale's own message), and a
  warn-not-fail pattern (`AddDigestNotSettledWarning`) usable if a System Cluster poll timeout
  should warn-and-proceed rather than hard-fail — though forge notes that's probably not right
  for "start," which the mission treats as needing to reach healthy.
- **Test harness pattern**: every poll-loop precedent is tested via an `httptest.NewServer`
  handler that advances through a caller-supplied list of states across successive requests,
  asserting both the final outcome AND the request count (proving the loop actually polled
  repeatedly). Reference: `serviceStatePollTestServer` (`service_helpers_test.go:198`) — shape
  to copy, not reusable code (typed to `ServiceResult`).

**Two open questions forge flagged for the design checkpoint** (unresolved, need assayer's
real backend behavior, not a guess):

1. Should a System Cluster start-poll **fail-fast on a GET error** (service-rollout style —
   a single failed GET aborts the wait) or **tolerate transient GET failures** (digest-settle
   style — logs and continues)? This is a correctness-critical rollout gate per the mission,
   which leans toward the fail-fast/service style, but not decided.
2. Should the start-poll timeout be a **user-configurable `*_timeout` string attribute**
   (service-style, if start time is highly variable) or a **fixed internal constant**
   (cloud-style, if start time is small/predictable)? Depends on assayer's real timing data.

**tfp-scribe — doc-facing precedent research (complete).** Full detail in the agent
transcript; headline findings, most architecturally-relevant first:

- **No live data source in this codebase takes a single required parent ID (e.g. `cloud_id`)
  and returns one object.** Every shipped data source is either a zero-arg singleton
  (`organization`, `user`) or an optional either/or lookup (`id`/`name`, both `Optional`)
  sometimes narrowed by *optional* parent filters (`compute_config`/`service` take optional
  `cloud_id`) — never a bare required-parent-in/single-object-out. The one structural cousin,
  `global_resource_scheduler` (`Required: true` `name` → one object), is **disabled** in
  `provider.go` pending a backend rework and was never shipped/documented. **Implication: if
  Option A's data source takes a required `cloud_id` and returns one object, it establishes a
  new shape in this provider** — not wrong, but tfp-architect should decide this deliberately
  rather than assume it mirrors an existing pattern.
- **No existing resource's `Delete` is purely state-only (zero backend calls) either** — every
  live `Delete` makes at least one real API call. The one partial precedent is a conditional
  branch in `resource_cloud_resource.go:1004-1008`: deleting the cloud's *primary/default*
  resource returns immediately with no backend call — but this is undocumented in
  `cloud_resource.md` (no callout, no mention in the `is_default` field description). **The
  mission's required "delete removes Terraform state only" behavior would make
  `anyscale_system_cluster` the first resource in this codebase to document that pattern.**
  The *callout format* to reuse is well-established though: a `~> **Note:**` (informational)
  or `~> **Warning:**` (consequential) block as the first line of the resource's
  `MarkdownDescription`, right after the one-sentence summary — see `organization_collaborator.md`
  (destructive-delete warning, points at `terraform state rm` as the escape hatch for the
  *opposite* case) and `service.md` (explains destroy's two-step terminate-then-delete). We
  have the format, not the exact wording — this doc will be coining new phrasing for a
  genuinely new (documented) behavior.
- **Import identity**: since System Cluster is a true one-per-cloud singleton (not a named,
  possibly-multiple resource like `cloud_resource`), a plain single-ID import
  (`terraform import anyscale_system_cluster.example <id>`, `service.md`/`compute_config.md`
  style) fits better than `cloud_resource`'s composite `cloud_id:name` ID — that composite form
  exists specifically because a cloud can have *multiple* named `cloud_resource`s, which isn't
  our shape. Import section format itself is byte-identical tfplugindocs boilerplate across
  every resource doc; only the shell-block comment + example ID are hand-authored, sourced from
  `examples/resources/<resource>/import.sh`.
- **Async/polling doc vocabulary**: `service.md` is the rich, deliberate precedent —
  user-configurable `rollout_timeout` (`Optional+Computed`, default `"45m"`, description
  "Maximum time to wait for ... to reach `RUNNING`"), and `current_state` enumerates example
  values inline with backticks. `cloud.md` is the **counter-example**: `waitForCloudReady`
  polls for real (hardcoded 30 min timeout) but `cloud.md` documents *none* of it — no timeout
  attribute, no mention of polling/waiting/ACTIVE anywhere. **Model System Cluster's docs on
  `service.md`, not `cloud.md`.** Reusable phrases: "Maximum time to wait for `<X>` to reach
  `<STATE>`" for a timeout attribute description; "converges to" for describing what a poll
  loop settles on.
- **Parent-cloud-reference wording**: `cloud_resource.go`'s `cloud_id` description — "The cloud
  ID to attach this resource to." — is the closest existing phrase for a schema attribute that
  points a child resource at its parent Cloud; worth riffing on even though the import shape
  differs.

**tfp-forge — prior-art warning: `enable_system_cluster` already exists (major finding,
confirmed by the user).** There is already a shipped, tested `enable_system_cluster` bool on
`anyscale_cloud` (`resource_cloud.go:229`, Go field `EnableSystemCluster`,
`system_cluster_config_id` as the opaque readable ID) — **already documented** in
`docs/resources/cloud.md:228`. **The user directly confirmed via live UI observation** that
this is a *different* control from the mission's target: the console's "enable_system_cluster"
control is a **slider** (separate config toggle), while "Start System Cluster" is a **distinct
button** — confirming forge's code-level inference that enable/disable and start/terminate are
already separate backend operations. This substantially de-risks architect's SCOPE GATE (Q5):
the new resource's scope (start/status of an already-*enabled* cluster) does not overlap the
existing `enable_system_cluster` toggle, and the mission's non-goal ("do not implement
enabling/disabling... unless required") is cleanly satisfiable — enable/disable already exists
elsewhere and needs no new work.

Implications for docs (tfp-scribe lane) and design (flagging for tfp-architect/tfp-assayer):

- **Reusable "no side-effect-free read" phrasing precedent**: `cloud.md`'s existing
  `enable_system_cluster` description is the established in-repo template for documenting a
  write-only/no-clean-read boolean: *"Deliberately NOT Computed... the Anyscale API has no
  side-effect-free way to read back whether the system cluster is currently enabled... the one
  endpoint that resolves the true value has a real side effect... and requires broader
  permissions."* **If assayer confirms the operational-status read (RUNNING/STARTING/
  TERMINATED) has the same side-effect problem, this is the phrasing template to adapt** — and
  it would be a serious blocker for Option A's clean declarative Read, per forge's flagged open
  risk.
- **Primary-vs-secondary `cloud_resource` nuance (new, not yet raised by others)**: `cloud.md`'s
  existing text states *"Only the cloud's primary `anyscale_cloud_resource` ever gets a working
  system cluster - a secondary resource attached to the same cloud does not."* If this holds for
  the start/status axis too, a bare `cloud_id` may be an **ambiguous** identity for the new
  resource on a cloud with multiple `cloud_resource`s — worth confirming whether "System
  Cluster" is truly keyed by `cloud_id` alone or implicitly resolves to the cloud's *primary*
  `cloud_resource`. Feeds directly into architect's Q1–Q3 (cardinality/discovery/state identity).
- **Public docs (docs.anyscale.com/clouds/system-cluster) fetched for terminology grounding.**
  Confirms: one system cluster per cloud, powers task+actor dashboards together (no independent
  per-dashboard toggle), auto-restarts when a user views a dashboard, auto-terminates after 8h
  idle (default), and termination is non-destructive (data is retained, metrics keep exporting).
  Documents a `terminate-system-cluster` CLI/SDK command but **does not document an explicit
  "start" command** — only the implicit auto-restart-on-dashboard-view trigger. **Open question
  for assayer**: does the console's "Start System Cluster" button (the mission's actual target)
  call a real, distinct, directly-invocable API endpoint, or does it simulate the
  auto-restart-on-view trigger some other way? Worth confirming which, since it affects what the
  new resource's Create actually calls.
- Reusable lifecycle vocabulary from the public docs for the eventual resource/data-source
  copy: "restarts... when a user views," "terminates after 8 hours if no users are actively
  viewing," "doesn't delete data when the system cluster terminates."

**tfp-shipwright — TF surface catalogue (task #3, complete) + corrections.** Full detail in
`spec.json:shipwright_tf_surface_patterns`.

- **Correction to forge's prior-art note**: `enable_system_cluster` lives ONLY on
  `anyscale_cloud`, not also on `anyscale_cloud_resource` (zero grep hits; verified twice, and
  again via `Metadata` `TypeName` — `resource_cloud.go`'s struct is confusingly named
  `CloudResource` but `TypeName = "anyscale_cloud"`, while `resource_cloud_resource.go`'s struct
  is `CloudResourceResource` with `TypeName = "anyscale_cloud_resource"`; `resource_cloud_upgrade.go`'s
  `UpgradeState` is on `*CloudResource` only, i.e. `anyscale_cloud`'s schema-version migration,
  unrelated to cluster lifecycle despite the filename). Forge confirmed and corrected their
  original broadcast.
- **The `terminate` endpoint already exists and is already referenced (never called) in the
  provider today**: `updateCloudSystemClusterConfig`'s own doc comment names
  `POST /api/v2/clouds/{cloud_id}/terminate` as the distinct, heavier, async runtime op it
  deliberately does not call. This is a real candidate for a *future* stop capability, but the
  mission is explicit that **Delete must never call it** — worth flagging clearly so nobody
  reaches for the "obviously right there" endpoint when wiring Delete.
- **Identity trap** (independently important beyond what's already in `HANDOFF.md`):
  `system_cluster_config_id` (`models.go:69`) "stays non-null regardless of the current
  enabled/disabled state" per its own doc comment — a config pointer, not a run identity. Real
  risk because it looks exactly like the stable ID architect's Q3 is asking for.
- **Computed-refresh shape guidance**: this codebase draws a sharp line between `is_default`
  (Computed, no plan modifier, unconditionally refreshed every Read because it's genuinely
  server-mutable out-of-band) and `is_empty_cloud` (Computed +
  `boolplanmodifier.UseStateForUnknown()`, deliberately sticky because it cannot legitimately
  change post-creation). Shipwright's call: whatever status field System Cluster ends up with
  is almost certainly the `is_default` shape (someone can stop the cluster from the console,
  out-of-band) — refresh every Read, no `UseStateForUnknown`.
- **Existing acctest lifecycle test** (`resource_cloud_system_cluster_apply_acc_test.go`, 3
  tests) already proves a null-guard contract for the *enable* toggle (unconfigured never
  calls; explicit true/false calls exactly once per real change; removed-from-config stays null,
  no implicit disable) — a good template for the new resource's own "don't call start when
  nothing changed" idempotency tests.
- **`resource_service.go` CRUD template, beyond forge's poll-loop focus**: a create-adoption-
  guard (probably not needed here if start is an idempotent upsert-by-`cloud_id`, since
  "adopting your own cloud's own cluster" is the intended behavior, not a collision); an
  idempotent-delete pattern that deliberately excludes 404 from a call's accepted-statuses list
  so an explicit error-string check can catch and short-circuit on it (relevant only if a future
  stop path is added, since this mission's Delete never calls terminate at all); ImportState
  seeding write-only fields via one deliberate extra GET; and Read's habit of documenting,
  field-by-field, which inputs it does *not* refresh and why — a habit worth carrying into this
  resource's docs (state per-field whether Read refreshes it, not just that Read exists).
- **Data-source shape gap — independently converged** with my own research (§4 above): neither
  `anyscale_cloud`'s data source (optional id-or-name, self-identified) nor the connection-level
  singletons (`organization`/`user`, zero-arg) match "required parent ID → exactly one object."
  Genuinely new shape; not a blocker, but the checkpoint should treat the data source's
  missing-cluster Read semantics (error vs. all-null computed fields) as a first-class question.

**tfp-architect — Design Checkpoint, PRELIMINARY (gated on tfp-assayer, not yet ratified).**
Full detail in `spec.json:architect_design`. This is the most complete design statement so far;
recorded here in full since it will become the actual implementation spec once the one
remaining gate clears.

- **Scope gate (Q5): RESOLVED, separable.** Evidence: `resource_cloud.go:1327-1331` explicitly
  documents start/terminate as a distinct runtime concept from the `update_system_cluster_config`
  toggle; the user's live-UI observation confirms slider ≠ button. No pause, no scope expansion.
- **Recommended interface: Option A**, `anyscale_system_cluster { cloud_id }` — "ensure the
  (already-enabled) System Cluster for this cloud is running." B rejected (imperative,
  `start = false` incoherent, confusing plans). C rejected (collides with the existing flat
  `enable_system_cluster`; couples async start+poll into every cloud apply; hits the cloud
  resource's documented config-block import/perpetual-diff hazards; blurs the config-vs-runtime
  line the codebase already drew).
- **Lifecycle semantics (Create)** — four branches: already `running` → no-op, read + set state;
  `starting` → poll to healthy without a redundant start call; `terminated` → issue start, then
  poll to healthy; `disabled`/missing → clear, actionable diagnostic — **do NOT auto-enable**.
  This last point is the concrete mechanism behind the docs instruction below: the new resource
  must error, not silently flip `enable_system_cluster` on, if the cloud's system cluster isn't
  enabled yet.
- **Delete**: state-only, **never calls terminate**, documented prominently (ties directly to
  shipwright's finding that `terminate` already exists and would be the tempting-but-wrong call
  to reach for).
- **Read/drift — decision still needed, leaning stated**: with only `cloud_id` configured
  (ForceNew) and everything else Computed, a plain re-apply after an out-of-band termination
  won't naturally retrigger Update. Leaning toward Read dropping the resource from state when
  observed `TERMINATED` (a non-transitional state) so the next apply recreates and re-starts it
  — true "ensure running" semantics — over a weaker reflect-only-plus-user-`-replace` approach.
  Gated on assayer confirming `TERMINATED` is stable/observable and distinguishable from
  transitional states.
- **Precondition**: requires `enable_system_cluster = true` on the parent cloud already; if
  disabled, error clearly rather than enabling it implicitly. **Direct instruction to
  tfp-scribe: the docs must explain this relationship explicitly** — tracked as a hard
  requirement, not optional polish.
- **Identity/import**: key = `cloud_id` (pending assayer confirming exactly-one-per-cloud);
  state ID still TBD (either the runtime cluster ID if stable, or `cloud_id` itself); import by
  `cloud_id` via `ImportStatePassthroughID` if the singleton-per-cloud assumption holds —
  consistent with the child-of-cloud convention.
- **Answers to forge's two open questions**: poll GET-error handling → service-style fail-fast
  ("ensure running" is a correctness gate, not a settle-wait; a small bounded retry is allowed
  *only* if assayer finds the status endpoint flaps/404s immediately post-start). Timeout knob →
  leaning a user-configurable duration-string attribute (tentative name `start_timeout`,
  `rollout_timeout` precedent), falling back to a fixed const only if assayer's real timing data
  shows start is fast and predictable.
- **Reuse plan**: `DoRequestAndParse[T]`/`DoRequestRaw`/`MarshalRequestBody`
  (`api_helpers.go`); adapt the `waitForServiceStateWithTiming` shape (classifier + state-bucket
  maps + F6 unrecognized-continues + `WithTiming` test split); persist `cloud_id`/cluster
  identity to state immediately after start returns an ID, before polling; `AddAPIError`/
  `AddConfigError`/`extractAPIErrorDetail` for diagnostics.
- **Remaining gate before this locks**: assayer must confirm (1) the start endpoint's real
  method/payload, (2) the full status enum including terminal-failure and transitional states
  and `TERMINATED`'s observability, (3) whether starting an already-running cluster is safe/
  idempotent, (4) that the cluster is discoverable from `cloud_id` alone, (5) what happens when
  starting a *disabled* cluster.

**tfp-assayer — API discovery (complete, source-traced, not yet live-verified).** Full detail
in `spec.json:assayer_api_discovery`. This is the definitive evidence the checkpoint was
waiting on. Recorded in full — this becomes the client/resource implementation spec.

- **Three real endpoints**, all under `/api/v2/`:
  - `PUT /clouds/{cloud_id}/update_system_cluster_config` — the existing, shipped enable/
    disable toggle. `is_enabled: Optional[bool]`; `None` is a safe no-op. Independent of
    cluster existence/state — toggling it never starts, stops, or touches the actual cluster.
  - `POST /system_workload/{cloud_id}/describe` — **the new endpoint this mission needs.**
    Request: `workload_name` (enum; console only ever sends `RAY_OBS_EVENTS_API_SERVICE` — must
    be hardcoded, never exposed as configurable, since changing it on a live cluster forces an
    unconditional restart), `cloud_resource_id` (optional, always omitted by real callers —
    confirmed safe to never expose in our schema), **`start_cluster` (bool — the router
    defaults this to `true` if omitted! Every real caller passes it explicitly. Our client must
    never omit it — a poll loop that forgets this would re-request a start on every single
    tick).** Response includes `cluster_id`, `status` (the `ClusterState` enum), `is_enabled`,
    workload URLs.
  - `POST /system_workload/{cloud_id}/terminate` — 202, async `ClusterOperation`. 404 if no
    cluster found; **409 if already `Terminated`** (unlike start's silent no-op, already-
    terminated is an error here). Requires `CrudActions.read` + a cross-cloud permission check,
    not `update` like the enable/disable call. **Mission's Delete must never call this** — ties
    directly to shipwright's earlier flag that this endpoint is a tempting-but-wrong reach.
- **THE GATE #1 ANSWER — nuanced, not a clean yes/no:**
  - No cluster yet + `is_enabled=False` → all-null/false, no error, **no side effect**. Clean.
  - **No cluster yet + `is_enabled=True` → describe ALWAYS creates the cluster (compute
    template + cluster env + `CreateClusterInternal(is_system_cluster=True)`), REGARDLESS of
    `start_cluster`.** `start_cluster` only additionally gates whether `start_cluster()` is
    *also* invoked on that same request. **This means describe is side-effect-free ONLY once a
    cluster already exists** — not universally, as Option A's clean-Read assumption hoped.
  - Cluster exists + enabled + workload not yet present → adds it and **unconditionally**
    restarts, regardless of `start_cluster` (another reason `workload_name` must stay
    hardcoded — we'd never trigger this branch by accident).
  - Cluster exists + enabled + workload present + `start_cluster=True` + state NOT in the
    4-state "converging" bucket (see below) → retry-start.
  - Cluster exists + enabled + workload present + (`start_cluster=False` OR state IN the
    4-state bucket) → **pure read, zero side effects.** Confirms Q6: starting an already-
    running/converging cluster is a safe no-op (source-level, not yet live-verified).
  - Cluster exists + `is_enabled=False` → still returns the real current cluster state
    regardless of `start_cluster`. **Disabling never hides or terminates a running cluster** —
    `running=true, enabled=false` is a real, valid, non-transient combination our Read must
    represent as such, not collapse to null.
  - **tfp-shipwright's safety flag on this (important, addressed to the team before the hold
    lifts)**: this makes the **data source** a real correctness risk, distinct from the
    resource. A `data.anyscale_system_cluster` lookup against a cloud that has
    `enable_system_cluster=true` but where nobody has ever clicked "Start" (no cluster row yet)
    would — merely by calling describe to check status, even with `start_cluster=false` —
    **create a cluster as a side effect of what Terraform treats as a pure, harmless
    refresh/plan.** Severity unconfirmed (does the no-start describe call provision billable
    compute, or just a dormant DB/session row? source doesn't say) — flagged as needing
    assayer's live-severity confirmation and an explicit architect design call, not something
    shipwright is proposing to fix unilaterally.
- **State model — 10 `ClusterState` values**: `Terminated`, `StartingUp`, `StartupErrored`,
  `Running`, `Updating`, `UpdatingErrored`, `Terminating`, `AwaitingStartup`,
  `TerminatingErrored`, `Unknown`. **Three different "terminal/non-terminal" lists exist in
  this codebase and must not be conflated:**
  1. Backend **service-local** `NON_TERMINAL_STATES` (`system_workload_service.py`) = `{StartingUp,
     Running, Updating, AwaitingStartup}` — 4 states. **This is the only one that gates real
     side effects** — describe's own "already converging, a start request is a no-op" bucket.
     Notably excludes `Terminating` and `Unknown` — a `start_cluster=True` call against either
     of those **will** invoke `start_cluster()` again.
  2. Backend **DB-level** `CLUSTER_INACTIVE_STATES` = `{StartupErrored, Terminated,
     TerminatingErrored, UpdatingErrored}` — a general-purpose constant, **not** referenced by
     describe's own gating logic.
  3. Frontend-only `TERMINAL_STATES` (`ObservabilityTab.tsx`) — happens to equal set 2 above,
     but is a separately-maintained FE constant, not shared/imported from the backend.
  4. Frontend-only `NON_TERMINAL_STATES` (same file, decides Start-vs-Terminate button) — 9 of
     10 states, broader than either backend set, purely cosmetic. **Confirmed divergence**: it
     makes the UI show only a "Terminate" button (no direct restart) for the 3 error states,
     even though the API itself *would* accept and act on a `start_cluster=True` retry from an
     error state per set 1. **Do not take frontend button-enablement as a proxy for API
     behavior.**
  - **Recommendation (assayer's evidence, not yet ratified)**: our poll/reconcile logic should
    treat backend set 1 (the service-local 4-state set) as the authoritative "already
    running-or-converging" bucket — it's the one the server itself uses.
  - **Unverified edge case, flagged as a real operational risk, not assumed safe**: what happens
    if `start_cluster()` is invoked while state is `Terminating` or `Unknown`? Source shows the
    call would be issued (both are outside the no-op bucket) but downstream behavior against a
    cluster already mid-teardown isn't traced further — worth an explicit live test.
  - **A 501 is a real, recoverable-by-nobody-on-our-side error case**: `_is_system_cluster_enabled_for_cloud`
    can raise 501 for an unsupported provider/compute-stack + feature-flag combination
    (real-time LaunchDarkly flags — the *same* cloud can flip between enabled and 501 across two
    calls with zero config change on our end). Read must surface this as a diagnostic, never
    silently treat it as "missing."
- **Design-checkpoint questions, answered directly:**
  - **Q1 (cardinality)**: exactly one system workload cluster per cloud, DB-enforced (unique
    constraint; the backend itself raises a 500 if a search ever returns more than one).
  - **Q2 (discovery from `cloud_id` alone)**: confirmed yes; `cloud_resource_id` is optional
    everywhere and no real caller ever passes it.
  - **Q3 (stable identity)**: `cluster_id` (same underlying entity as a "session" elsewhere in
    this codebase) is the natural Computed identifier, but **it doesn't exist until the cluster
    has been created at least once** (only happens on an `is_enabled=True` describe call).
    `cloud_id` is the only identifier guaranteed to always resolve to *something* — **`cloud_id`
    should be both the resource's primary key and its import identifier; `cluster_id` is
    Computed-only.** (Matches shipwright's independent draft schema outline exactly.)
  - **Q4 (status model distinguishes everything needed)**: yes — missing = `cluster_id` null;
    disabled = `is_enabled:false` (**can coexist with a real running cluster** — not to be
    collapsed to null); running/starting/terminated/failed = the status enum directly. No
    ambiguous combination.
  - **Q5 (scope gate) — separable, but ORDER-DEPENDENT, not independent.** Two distinct backend
    entities (`SystemClusterConfig.is_enabled` vs. the `Cluster` row itself). Enabling is a
    **precondition with no error if skipped**: calling describe with `start_cluster=True` while
    `is_enabled=False` is a **silent no-op** (no exception, `cluster_id` stays null) — not a
    hard block, just quietly does nothing. **Critical implication**: Option A's "ensure running"
    Create/Update must perform enable-then-start as **two sequential calls**
    (`update_system_cluster_config{is_enabled:true}` THEN `describe{start_cluster:true}`) —
    never just the second — or `apply` reports success with nothing actually provisioned. This
    does *not* force exposing `is_enabled` as a user-facing attribute on the new resource (it
    can always enable-then-start internally and just surface status as Computed), but it *is* a
    genuine, unresolved design fork — architect's earlier preliminary lean ("disabled/missing →
    clear diagnostic, do NOT auto-enable") is not automatically validated by this evidence; the
    backend supports **either** design (auto-enable-then-start, or refuse-and-diagnose).
    **Needs an explicit ruling, not an assumption.**
  - **Q6 (idempotency)**: confirmed safe no-op by source (starting an already-converging
    cluster does nothing) — not yet live-verified.
- **Permissions needed for full lifecycle**: `CrudActions.create` on `sessions` (describe/
  start), `CrudActions.update` on `clouds` (enable/disable), `CrudActions.read` on `clouds`
  (reject_readonly) + a cross-cloud cluster check (terminate).
- **Console-vs-CLI capability gap**: the console's "Start cluster" button
  (`describe{start_cluster:true}`) has **no CLI or SDK equivalent anywhere** in the product
  codebase — only enable/disable and terminate are exposed as CLI commands. **Our Terraform
  resource would be the first non-console client ever to exercise this exact call** — there is
  no second call site to cross-check start semantics against; assayer's source trace is the
  primary evidence until a live smoke test happens.
- **Not yet done**: none of this is live-verified against a real cloud this session (source-
  trace only, cross-checked between backend router/service/DAO and both console FE and CLI
  call sites). Assayer recommends a live enable→describe(start)→poll→terminate smoke test as
  the first real acceptance test once Wave 2 has a client to drive it, and will own that test.

**tfp-shipwright — draft schema outline (prep only, respecting the hold).** Full detail in
`spec.json:shipwright_draft_schema_outline`. Separates what's settled from what's still gated,
so Wave 2 can move immediately once the remaining decisions land. Notable points beyond what's
already captured above:
- File plan: `resource_system_cluster.go` (mirrors `resource_service.go`'s shape),
  `data_source_system_cluster.go`, a polling helper file (forge's lane, likely
  `system_cluster_helpers.go` mirroring `service_helpers.go`'s `evaluate*State`/`waitFor*State`/
  `WithTiming` split), `internal/acctest/resource_system_cluster_*_acc_test.go`
  (mock-server lifecycle tests), and `provider.go` registration.
- **A second open question, not yet answered by anyone**: for the data source, what should Read
  do for a cloud with no system cluster configured/enabled at all — a diagnostic error naming
  `enable_system_cluster` as the fix, or all-null computed fields with a status like
  `"disabled"`/`"not_configured"`? Shipwright leans error (an all-null data source on live,
  simply-unconfigured infra tends to read as "broken" rather than "expected") but is flagging it
  as open, not asserting it.
- Import: leaning `cloud_id` itself as the single-value import identifier
  (`ImportStatePassthroughID`-style, no compound ID) — cross-referenced against my own
  child-of-cloud research rather than asserted independently; we converge.
- **Actions-framework side note**: `terraform-plugin-framework` v1.19.0 is present in go.mod
  but this repo has **zero existing usage** of the framework's `Action` primitive. Reinforces
  Option A (plain resource) as the path of least novelty. Worth remembering for later: a future
  stop/restart capability would be a **provider Action layered on top of this resource**,
  mirroring how `anyscale_service`'s canary promote/rollback was deliberately deferred rather
  than bent into the resource's converging lifecycle — same shape would apply here, staying
  inside this mission's Non-Goals for now.

**tfp-assayer — data-source safety hole: likely closed.** Confirmed the side-effect precisely
(a plain SQL insert of a `Cluster` row, no inline autoscaler/launch call — not confirmed
cost-incurring by itself, but still a real unwanted mutation from something that's supposed to
be read-only). Then found a candidate fix: `GET /api/v2/decorated_sessions/?cloud_id=&name_match=system_workload_cluster`
(`decorated_clusters_router.py`) is a plain, side-effect-free, `api/v2`-generation GET; the
system cluster's name is a stable hardcoded literal (`system_workload_cluster`) set at creation,
so `name_match` reliably picks it out without ever going through the create-on-demand `describe`
path. Recommendation: `describe_system_workload` is used only by Create/Update (where
get-or-create is the intended behavior); Read/refresh and the data source use the
`decorated_sessions` lookup instead, which can never create anything. (An `ext/v0` alternative
also exists but is deliberately not preferred, per this repo's standing `api/v2`-over-`ext/v0`
policy.) forge confirmed this "closes the data-source safety hole cleanly." **Not yet
live-verified this session** — same caveat as the rest of assayer's trace.

**tfp-forge — client-lane landmines, both real, both theirs to guard against:**
1. **`omitempty` landmine on `start_cluster`**: since the router defaults `start_cluster` to
   `true` when omitted, a naive Go request struct tagged `` `json:"start_cluster,omitempty"` ``
   would have `encoding/json` **silently drop the field whenever it's `false`** (Go's
   `omitempty` treats a bool zero-value as absent) — meaning every "just polling" call would
   omit it, the backend would default to `true`, and the poll loop would re-request a start on
   *every single tick*. Forge is ruling out `omitempty` on this field, commenting the struct so
   it doesn't get "cleaned up" later, and considering avoiding a bare bool client-API entirely
   (two named functions, or an explicit small enum) so no call site can mean the wrong thing by
   accident.
2. Asked assayer to double-check for an existence-only search endpoint before the
   `decorated_sessions` answer landed — resolved by the above.

**tfp-architect — DESIGN CHECKPOINT: RATIFIED on source evidence.** *("live-verify still
pending — nothing merges before assayer's smoke test.")* Full detail in
`spec.json:architect_design`. This is the locked implementation spec (except the two items
explicitly held back, below) — recorded verbatim-equivalent since Wave 2 code will be built
directly against it.

- **Interface**: Option A — dedicated `anyscale_system_cluster` resource + data source,
  `cloud_id`-keyed. B and C rejected and recorded (see earlier entries this log).
- **Identity**: `cloud_id` = primary key + import ID (always resolves; DB-enforced 1:1).
  `cluster_id` = Computed-only, nullable (exists only after the first enable+create).
  `system_cluster_config_id` is **not** identity or status (the trap shipwright flagged). No
  `cloud_resource_id` in the schema. `workload_name` hardcoded to `RAY_OBS_EVENTS_API_SERVICE`,
  never exposed as configurable.
- **Status**: Computed `state` (the `ClusterState` enum, 10 values), refreshed on **every**
  Read, **no** `UseStateForUnknown` (matches shipwright's `is_default`-shape call). Also
  Computed `is_enabled` — surfaced, not hidden, because `running=true, enabled=false` is a real
  combination.
- **Poll design**: target = `Running`; terminal-error bucket = `{StartupErrored,
  UpdatingErrored, TerminatingErrored}`; continue bucket = `{StartingUp, Updating,
  AwaitingStartup}`; `Terminating`+`Unknown` → continue-with-warning (the "F6" unrecognized-
  state resilience rule) backstopped by the timeout (flagged for live-test confirmation);
  **fail-fast on a GET error** (service-rollout style, per forge's original question); interval
  10s; adapt `waitForServiceStateWithTiming`. **Critical, repeated for emphasis**: every
  poll/Read `describe` call must pass `start_cluster=false` explicitly (never omitted) — the
  router defaults to `true` if omitted, so a forgetful loop would re-start on every tick.
- **Read drift — DECIDED (option ii from the earlier "deferred" list): reflect-only for v1.** A
  terminated row persists, so Read shows `state=Terminated` (never "missing"); no surprising
  auto-recreate. Re-starting after external termination is a user-initiated
  `terraform apply -replace`, documented explicitly. The auto-recreate option (i) is deferred,
  not chosen.
- **Delete**: state-only, **never calls terminate** (final, matches the mission requirement).
  A `describe` 501 (unsupported provider/compute-stack + flag combination) must surface as a
  diagnostic, never be silently treated as "missing."
- **`start_timeout`**: user-facing `Optional+Computed+Default` duration-string attribute (name
  now settled, not just tentative). Default value still gated on live timing data — the CLI's
  own terminate-wait uses a 500s deadline, so architect is proposing roughly a 20-minute default
  for start, to be refined once live-tested.
- Endorses shipwright's file plan and the "stop/restart is a future provider Action, not a
  reason to bend this resource's converging lifecycle" framing outright.

**TWO ITEMS EXPLICITLY HELD BACK — architect says do not build these branches yet:**

1. **A USER decision, posed directly to the user, not a shard call**: **A1** (self-sufficient —
   Create/Update internally does enable-then-start automatically; architect's recommendation)
   vs. **A2** (precondition-required — only ever calls start; errors at apply-time if the
   cloud's `enable_system_cluster` isn`t already `true`). This also decides the disabled-cloud
   guard shape: A1 needs none (it self-heals); A2 needs an explicit apply-time error. **This is
   the item that determines what `anyscale_system_cluster`'s docs need to say about its
   relationship to `enable_system_cluster` — the exact cross-reference tfp-architect instructed
   me to write depends on which of A1/A2 the user picks.** Watching for the user's answer before
   drafting that section's final prose.
2. **A confirmation, assayer's to close**: does a genuinely pure-GET status endpoint exist (no
   create-on-read)? As of the *previous* log entry this looked open, but assayer's
   `decorated_sessions` finding (above, this entry) appears to answer it — architect's ratify
   message poses it as still-open, likely written concurrently with or just before that answer
   landing. Treating this as **source-answered but not yet formally re-confirmed by architect**
   until they explicitly acknowledge the `decorated_sessions` fix closes it. If it holds, the
   data source is safe to build as originally scoped; if not, architect said the data source
   gets deferred (resource is unaffected either way — its own Read runs post-create, never
   against a definitely-cluster-less cloud in the same way the data source could).

**GREENLIGHT — Wave 2 implementation has formally started for the locked parts:**
- tfp-forge: client request/response models (describe/enable/terminate), endpoints,
  `system_cluster_helpers.go` poll loop (`evaluate*State` + `WithTiming` split), unit tests.
- tfp-shipwright: resource schema (`cloud_id` key; Computed `cluster_id`/`state`/`is_enabled`/
  `start_timeout`), Read (`describe` with `start_cluster=false`), Delete (state-only),
  ImportState (passthrough `cloud_id`), `provider.go` registration. **Holding** only Create's
  enable-branch and the data source, pending the two items above.
- **Nothing merges** until assayer live-verifies the full
  enable→describe(start)→poll→terminate cycle against a real cloud — this quest's feature would
  be the first non-console caller of the start path ever.

**Open item #2 — formally CLOSED by tfp-assayer, with one precise nuance kept, not rounded
off.** `GET /api/v2/decorated_sessions/?cloud_id=<id>&name_match=system_workload_cluster` is
confirmed pure-GET, side-effect-free, can never create anything — covers existence+state fully,
which was the part actually at risk of provisioning on read. Nuance: it's slightly narrower than
"data source fully safe, full stop" —
- **The resource's own Read is safe unconditionally**, always, because Create already
  guarantees `cluster_id` exists before Read ever runs.
- **The data source (and any cold Read before a cluster is known to exist)** is safe for
  existence+state via `decorated_sessions`, but there's a residual gap specifically on the
  `is_enabled` boolean: when `decorated_sessions` finds no cluster yet, the *true* `is_enabled`
  value is only knowable by calling `describe_system_workload` — which is side-effect-free if
  `is_enabled` happens to be `false`, but **would create a cluster** if it happens to be `true`
  (e.g. someone flipped the `anyscale_cloud.enable_system_cluster` slider on but never hit
  Start). Assayer's recommendation: when `decorated_sessions` finds no cluster, report status
  as absent/not-created and `is_enabled` as **unknown/null** rather than calling `describe` to
  force an answer — don't guess, and don't risk the side effect for one field. Full `is_enabled`
  visibility becomes available (and safe) the instant a cluster exists.
- This does not reopen the gate; the part that mattered (existence+state never provisioning on
  read) is fully covered. It's a precision the eventual data-source docs need to state plainly:
  a data source hitting a truly cluster-less cloud reports `is_enabled = null`/unknown, not a
  guessed `false`.

**Open item #1 (A1 vs A2) — NOT closed. New structural option raised directly by the user,
superseding the original A1/A2 framing.** tfp-architect posed A1 (self-sufficient: internally
enable-then-start, recommended by architect) vs. A2 (precondition-required: error if the cloud
isn't already enabled) directly to the user, with the exact tradeoffs (A1 "touches the same
enable bit `anyscale_cloud.enable_system_cluster` controls, so you should not manage both on one
cloud — docs will call this out"; A2 "cleaner separation, mirrors the UI... two coordinated
steps, less one-click"). **Before answering A1-vs-A2, the user asked a bigger question**: should
`enable_system_cluster` move *out* of `anyscale_cloud` entirely and become part of
`anyscale_system_cluster` instead — i.e., a potential **Option D** where the new resource owns
both enable and running, rather than splitting the concept across two resources at all. This is
a genuinely different, deeper question than A1-vs-A2: it's not just "does Create silently enable
or does it require enable first," it's "should the already-shipped `enable_system_cluster`
attribute on `anyscale_cloud` continue to exist at all." **Not yet answered by tfp-architect as
of this entry.**

**Flag for whenever this resolves (mine to raise, not to decide)**: `enable_system_cluster` on
`anyscale_cloud` is an already-shipped (v0.16.x-era), already-documented, already-tested
attribute. If the user's Option D is adopted, removing or deprecating it is a breaking change
to real existing user configs, not a greenfield design choice — this repo's own
`CLAUDE.md` deprecation policy says a removal/deprecation of a user-facing attribute should
prompt asking the user whether it warrants a migration guide (here the user is already in the
loop proposing it, but the *migration path* for anyone who already set
`anyscale_cloud.enable_system_cluster` still needs an explicit answer, not an assumption that
greenfield-clean design wins over compatibility for free). Watching for how this resolves before
writing anything that assumes today's `enable_system_cluster` location is permanent.

**Status.** Wave 1 discovery complete. Design checkpoint ratified on source evidence, live-
verification still pending. Wave 2 implementation greenlit for the locked parts only (client
models, poll helper, resource schema/Read/Delete/ImportState/provider.go registration — NOT
Create's enable-branch, NOT the data source). Open item #2 (pure-GET) is closed. **Open item #1
is now the single blocking question**, widened by the user's Option D proposal beyond the
original A1/A2 framing — this determines not just Create's precondition behavior but which
resource `enable_system_cluster` even lives on, which flows directly into the docs
cross-reference tfp-architect instructed me to write. No docs prose written yet for anything
downstream of that decision; doc scaffolding for the parts already locked regardless of outcome
(cloud_id-keyed identity, import-by-cloud_id, non-destructive-delete callout, async/poll
vocabulary, state model) can proceed now.

---

### 2026-07-22 (continued) — the user chose removal; migration-guide question posed

**tfp-forge — concrete client function shapes** (posted early so shipwright can plan against
stable signatures while forge builds behind them; not yet implemented, still respecting the
hold):
- `findSystemWorkloadCluster(ctx, client, cloudID) -> (*result, error)` — wraps
  `decorated_sessions`. Always side-effect-free. Returns not-found (`nil`, no error) when no
  cluster exists yet. Used by the data source and by resource Read/Import in the cold case.
- `describeSystemWorkload(ctx, client, cloudID string, startCluster bool) -> (*result, error)`
  — wraps `POST .../describe`. `startCluster` is always an explicit required parameter, never
  defaulted/omitted (closes off the `omitempty` landmine by construction). Safe to call whenever
  a cluster is already confirmed to exist (true unconditionally post-Create, or after
  `findSystemWorkloadCluster` confirms existence). **Not** safe to call speculatively to learn
  `is_enabled` when existence is unconfirmed.
- **Orchestration this implies** (useful for the eventual Create/Read docs prose): Create =
  enable (`PUT update_system_cluster_config`) → `describeSystemWorkload(startCluster=true)` →
  persist state (`cluster_id` now known) → poll via `describeSystemWorkload(startCluster=false)`
  → done. Resource Read (cluster_id already in state) = `describeSystemWorkload(startCluster=false)`
  directly, always safe, richest single-call data. Data source / cold Read (no cluster_id yet) =
  `findSystemWorkloadCluster` only; if found, may layer in `describeSystemWorkload(startCluster=false)`
  for full fields (also safe, existence now confirmed); if not found, report absent +
  `is_enabled=null`, no escalation to `describe`.

**tfp-assayer — Terminating/Unknown edge case, traced one layer deeper (refines, doesn't
reverse, the earlier "unverified, treat as risk" note).** `session_operations_service._validate_start`
(the guard `start_cluster()` runs through) checks user permission, archived cluster, archived
compute config, build-succeeded, and cloud-exists — **it does not check `cluster.state` at
all.** So there is no backend validation-layer safety net against calling start while a cluster
is mid-`Terminating`; whatever happens next is decided by the underlying cluster-manager/state-
machine, untraced further (real diminishing returns without a live test). **Practical
recommendation**: client-side poll/reconcile logic should put `Terminating` and `Unknown` in a
"continue waiting, do not retry start" bucket rather than trusting the backend to no-op it
safely — a cheap defensive move that sidesteps an untraced race entirely. Confirms (doesn't just
assume) that architect's checkpoint framing (`Terminating`+`Unknown` → continue-with-warning) is
the safer choice, not merely the conservative-sounding one.

**tfp-shipwright — independently converges with my own migration-guide flag, with a more
complete blast-radius list (verified by me via direct grep, not just trusted paraphrase).**
`enable_system_cluster` is not a small/unreleased tweak: shipped in **v0.10.0** (2026-07-15,
confirmed via `CHANGELOG.md`), received two further docs-only follow-ups in later releases, is
the one cloud-level boolean that breaks the resource's usual Optional+Computed+Default pattern,
has 3 dedicated acctests proving its null-guard contract, and is referenced live in
**`docs/resources/cloud.md`, `docs/guides/cloud-resources.md`, `examples/aws-vm-basic/main.tf`,
`examples/resources/anyscale_cloud/resource.tf`, `examples/kitchen-sink/README.md`, and
`examples/kitchen-sink/cloud_b.tf`** (six files, confirmed by grep — this is the concrete list
I'll work from if/when the removal lands). Shipwright's framing: this contrasts with a past
breaking-in-theory change that had zero real users (always errored on apply) — this one has
genuinely worked since it shipped, so the "no production users yet" precedent from v0.13.0 does
not obviously transfer. **Recommends the migration-guide question be asked explicitly, separate
from the removal decision itself** — the user asking for the removal doesn't by itself answer
whether a migration guide is warranted. This is exactly the question I (tfp-scribe) already
posed to the user directly (see below) — independent convergence, not a duplicate ask.

**tfp-scribe (me) — asked the user the required policy question directly.** Per CLAUDE.md's
Deprecation Policy ("ask the user... before writing one unprompted... that call belongs to the
user each time, not a default"), I posed: given `enable_system_cluster` has been live since
v0.10.0 (not a v0.13.0-style zero-user case), does removing it warrant a migration guide (old
HCL + state, step-by-step move to the new resource), or does a similar "no production users yet"
argument still apply? **Awaiting the user's answer.**

**Schema-shape consequence of the removal decision (shipwright), holding on code pending
re-lock:** if `enable_system_cluster` truly moves into `anyscale_system_cluster`, the new
resource's schema gains a **real input** enable/enabled attribute (`Required` or
`Optional+Default`) instead of the originally-planned Computed-only status field — a schema
shape change from what shipwright was about to start building. Holding resource code until this
re-locks; forge's client-level function signatures are unaffected either way.

**Status.** The interface's *resource boundary* itself is being reopened by the user's Option D
(not just the A1/A2 precondition question within the original boundary). Confirmed by the user:
remove `enable_system_cluster` from `anyscale_cloud`, fold enable+start into
`anyscale_system_cluster`. **Two things still open**: (1) tfp-architect's formal re-lock of the
resource schema shape given this boundary change (not yet posted as of this entry); (2) the
user's answer on whether a migration guide is warranted for the breaking removal. Both block
further schema/doc work on the enable-handling portion specifically; the cloud_id-keyed
identity, import format, non-destructive-delete callout, async/poll vocabulary, and state model
are unaffected by this reopening and remain safe to draft docs against.

**Implementation-detail correction (does not change design/docs, noted for completeness and as
a good example of the team's verify-don't-trust discipline in action).** Forge verified their
own earlier `findSystemWorkloadCluster` description directly against source rather than leaving
it as posted, and found two things wrong: `decorated_sessions`'s `cloud_id` query param is
lint-suppressed unused (`# noqa: ARG001`) and never actually reaches the query — the endpoint is
**not** server-side cloud-scoped, so in any org with more than one cloud that's ever had a
system cluster it returns one row per cloud, not just ours; and `name_match` is a substring/ILIKE
match, not exact. Fix stays entirely client-side: paginate (reusing `PaginatedRequest[T]`) and
filter on `(cloud_id==target AND is_system_cluster==true)` — a real, robust bool field on the
base `Session` model, confirmed present — with `name_match` kept only as a coarse server-side
pre-filter. Also surfaced a three-incompatible-enums trap: `DecoratedSession`'s own `state`
(deprecated `SessionState`, 13 values) and `status` (`ClusterStatus`, 6 values, ALL-CAPS) are
**neither** the 10-value `ClusterState` the rest of this design is built on — resolved by using
`decorated_sessions` strictly as an existence+`cluster_id` oracle, never reading its own
state/status fields, always getting authoritative status from `describe_system_workload` once
existence is confirmed (already established safe at that point). **Assayer independently
re-verified forge's correction against source rather than trusting the paraphrase** (and caught
that their own original trace had missed the same `cloud_id`-unused detail, and had missed
`is_system_cluster` by grepping the wrong base class) — both confirmed accurate. Net effect: the
design/docs are unaffected; this only changes the internal implementation of one client
function. `findSystemWorkloadCluster`'s signature (posted earlier for shipwright to plan
against) is unchanged.

**tfp-shipwright status recap**: task #3 done and posted; holding all resource/data-source code
until tfp-architect's schema re-lock, watching the channel live, ready to build the moment it
lands. No code written yet, so nothing to rework from the pause.

**tfp-architect — Read design refined** (folding in forge's `decorated_sessions` correction).
Full detail `spec.json:architect_design.lifecycle.read` + `live_verify_items`. **Read (both the
resource and the data source) is now a two-call, still-fully-side-effect-free flow:**
1. `decorated_sessions` = existence + `cluster_id` **oracle only**. Paginated (reuse
   `PaginatedRequest[T]`); `name_match=system_workload_cluster` is a coarse server-side
   pre-filter only; then filter **client-side** to this cloud's system cluster specifically
   (exact `cloud_id` match + `is_system_cluster` when present), expecting exactly one match.
   **Its own `state`/`status` enums are never read or exposed** — sidesteps the three-
   incompatible-enums trap entirely.
2. If it exists → `describe{start_cluster=false}` is the **one** authoritative status call
   (`ClusterState`). Safe because existence is already confirmed — the create-on-read hazard
   fires *only* on `is_enabled=true` + no cluster, and gating `describe` behind the oracle
   eliminates that path structurally, not just by convention.
3. Not-found at step 1 → cluster was never created; return null/absent; **stop** — no
   `describe` call, no side effect. This is also the data source's clean not-configured result.
**Exactly one enum surfaced anywhere in our schema/docs: `ClusterState`.** The
`decorated_sessions`-native enums (`SessionState`, `ClusterStatus`) are never exposed to users.
**Three items assayer must fold into the live smoke test**: (1) confirm what `decorated_sessions`
actually serializes per row (a cloud identifier, and whether `is_system_cluster` survives
serialization — forge found it on the parent `Session` model source-side, but the wire
response needs confirming); (2) a real multi-cloud-collision test (>1 system-cluster cloud in
the org, confirm the client-side filter picks the right row and the name substring doesn't
false-match); (3) confirm `describe{start_cluster=false}` is a pure read for an *existing*
cluster in every state, including `Terminated`. **Everything else in the Read design is now
locked**; `findSystemWorkloadCluster`'s public signature is unchanged, only its now-corrected
internal implementation.

**THE MIGRATION-GUIDE QUESTION — RESOLVED, directly by the user.** *"I'm ok with this being a
deliberate removal. I'll make sure I communicate this one change to the 1 customer using this
provider right now. Yes, it should be a breaking change in the docs, but no migration doc
required."* Recorded precisely: (1) the removal is deliberate and confirmed; (2) **no
standalone migration guide** — the user is personally handling communication with the provider's
one real customer directly, out of band; (3) **it must still be documented as a breaking change**
— a `.changelog/<PR#>.txt` fragment in the breaking-change format, plus clear callouts in
`docs/resources/cloud.md` (removal) and the new resource's docs (this is where the attribute
lives now). This is a useful precedent alongside the v0.13.0 `cloud_deployment_id` case: that
one skipped a guide because there were zero production users; this one skips a guide with
*exactly one* real customer, because the user chose to handle that single relationship directly
rather than have the team draft a formal doc for it. Two different reasons, same "no guide"
outcome — worth remembering as its own data point, not a rule that low-user-count always means
skip.

**Status.** Both blockers from the prior entry are now resolved or substantially advanced: the
migration-guide question is fully resolved (no guide, breaking-change docs+changelog instead);
the Read design is fully locked (two-call oracle+status flow). **What's still outstanding**:
tfp-architect's formal confirmation of the new resource's exact schema shape for the
enable-as-input attribute (name, `Required` vs. `Optional+Default`) — the Read-design message
didn't cover this specifically, only Create/Update's precondition-vs-input question remains
functionally open as of this entry. Once that lands, shipwright can resume building and I can
write final doc prose.

---

### FULL SCOPE LOCKED — design phase complete

**tfp-architect — FORMAL RE-LOCK, Part 1 implementation is GO.** *("build now, in parallel with
assayer's live test; live-verify gates MERGE, not build.")* "Core has nothing open." Final
schema, confirmed:
- `cloud_id`: `Required` + `RequiresReplace` — the key.
- Computed: `cluster_id`, `state` (`ClusterState`), `is_enabled`; optional
  `workload_service_url`.
- `start_timeout`: `Optional+Computed+Default` duration string.
- **`is_enabled` is Computed, not a user-settable input** — this resolves the whole A1-vs-A2
  tension by construction: Create always does enable-then-start
  (`PUT is_enabled=true` THEN `describe{start_cluster=true}`), so there is no user-facing toggle
  to reconcile and no "disabled" precondition error case to design — the resource's mere
  existence means "enabled and running." `is_enabled` is surfaced purely for observability.
- Create: enable-then-start, persist state before polling, poll to `Running`.
- Read: the two-call oracle+status flow (locked previous entry).
- Delete: state-only (final, unchanged).
- Import: passthrough `cloud_id`.
- forge unblocked on the client (`describe`/`enable`/`terminate` models, `findSystemWorkloadCluster`,
  `waitForSystemClusterState`); shipwright unblocked on resource+data source+registration+acctests.

**A brand-new, explicit validation requirement, raised directly by the user** — closes the last
scope question: *"there is definitely a gap with the system cluster and multi-resource clouds.
It's currently only supported on the primary/default cloud resource. Can that be added as a
check? If the cloud_id is not the primary resource, it should fail to get created."* This turns
the earlier "cardinality is `cloud_id`-only by deliberate choice" note (originally recorded as
an accepted tradeoff, not a blocker) into an **explicit Create-time precondition**: if the target
cloud's primary/default `cloud_resource` isn't the one implied, Create must fail with a clear
diagnostic rather than silently proceeding against the wrong (or an ambiguous) resource. **This
needs its own clearly-worded doc callout** — a "known limitations / prerequisites" note stating
plainly that this resource only supports a cloud's primary `cloud_resource`, and what error to
expect otherwise. Mine to write once shipwright's exact diagnostic wording exists.

**USER DECISION — removal mechanics, final, closes the last open scope item entirely.** *"I'm ok
with this being a deliberate removal... Yes, it should be a breaking change in the docs, but no
migration doc required."* Architect's formal recording: **hard removal** of
`enable_system_cluster` from `anyscale_cloud` (no deprecation window/shim). Breaking-change note
in changelog + docs. **No migration guide** — matches the v0.13.0 `cloud_deployment_id`
precedent in outcome, but for a different reason (there, zero production users; here, exactly
one, and the user is personally notifying that customer rather than having the team draft a
formal guide document). My exact Part 2 assignment, verbatim: *"changelog BREAKING fragment
(feat!/breaking type) + breaking-change note in docs (`cloud.md` loses the attr; new resource
doc explains the enable-then-start behavior). NO migration guide."* (I had independently flagged
a possible discrepancy between this and architect's earlier looser "draft migration guide"
phrasing — architect's own next message resolved it the same way before I even needed a reply;
no actual conflict, just crossed messages.)

**STATE-COMPAT FLAG, architect's own lane, worth me knowing for changelog wording**: removing an
`Optional` attribute from `anyscale_cloud` will collide with any existing state that carries
`enable_system_cluster` — almost certainly needs a `SchemaVersion` bump + a `StateUpgrader` on
`anyscale_cloud` that drops the field (`resource_cloud_upgrade.go`'s v0→v1 is the precedent; this
would be v1→v2), to protect the one real customer's state from a broken upgrade. Shipwright owns
confirming whether the framework errors without it and building the upgrader if so. **Worth a
line in the changelog fragment** noting the upgrade is handled automatically, even though there's
no user-facing migration guide — precedent: this repo's changelog entries for past schema-version
bumps (e.g. the mount_targets attribute conversion) note the mechanical upgrade path even when no
separate guide exists.

**Part 2 assignments (from architect):**
- shipwright: straight removal of `enable_system_cluster` (attribute, schema entry, the
  `updateCloudSystemClusterConfig` call path from the CLOUD resource specifically — the endpoint
  itself moves into the new system_cluster client as forge's relocated Create-time enable call,
  it does not disappear), remove/migrate its 3 acctests, add the StateUpgrader if needed.
- scribe (me): changelog BREAKING fragment + breaking-change docs note (`cloud.md` loses the
  attribute; the new resource's docs explain the enable-then-start behavior). No migration guide.

**Status — DESIGN PHASE COMPLETE.** Full scope locked per tfp-architect: *"No open scope items
remain... Only the MERGE gate (assayer live smoke test) remains."* Wave 2 implementation is
underway (forge: client; shipwright: resource/data source + Part 2 cloud removal). My own next
work, now fully unblocked: draft the actual resource/data-source `MarkdownDescription` prose
(including the primary-cloud-resource precondition callout and the non-destructive-delete
callout), example HCL for the resource/data source/import, the `docs/resources/cloud.md`
attribute-removal diff, and the changelog BREAKING fragment content (actual `.changelog/<PR#>.txt`
filename TBD at PR time, per this repo's own convention — content can be drafted now).

---

### Part 2 delivered; AC24 settled documentation-only; live smoke test PASSED

**AC24 (multi-resource primary guard) settled as documentation-only, not enforced.** Assayer
traced the proposed create-time guard and found it isn't mechanically buildable against
`api/v2` today — no `is_default`/primary field on cloud resources, no resources-list endpoint at
that shape (confirmed by reading the full `CloudResourceRecord` model end to end). "Primary" is
a purely implicit, query-time convention server-side ("oldest non-PCP cloud resource for this
cloud_id, ordered by `created_at`"), not a stored, checkable attribute. The user separately
confirmed this is forward-looking, not an observed gap: *"Engineering is working on the ability
to add the system cluster to cloud resources, but it hasn't been done yet."* Resolution: carry
the identical, previously-undetected constraint forward as a documentation-only caveat, exactly
as the now-removed `enable_system_cluster` attribute did for its whole shipped lifetime — no
enforced check, phrased with "today"/"currently" language since it may lift later.

**My Part 2 deliverables, complete:**
- Drafted full `MarkdownDescription` prose for both new schemas (top-level + every attribute),
  handed to shipwright via `.crystl/quest/DRAFT-system-cluster-schema-docs.md` (the shared quest
  directory, not my own worktree, since Edit/Write are worktree-sandboxed but Bash isn't — used
  `cp` rather than a heredoc to avoid any risk of the backtick/command-substitution hazard this
  quest has already hit twice on `quest_msg`).
- Example HCL: `examples/resources/anyscale_system_cluster/{resource.tf,import.sh}`,
  `examples/data-sources/anyscale_system_cluster/data-source.tf` — matched to the established
  convention of a literal placeholder ID (`cld_abc123`) rather than a cross-resource reference,
  confirmed against `compute_config`'s own examples before writing these.
- Removed `enable_system_cluster` references from `examples/aws-vm-basic/main.tf` and
  `examples/resources/anyscale_cloud/resource.tf` (safe now — deleting a reference to a
  still-optional attribute is harmless before or after Part 2's Go-side removal lands).
- `examples/kitchen-sink/cloud_b.tf` + `README.md` point 6: removed the `enable_system_cluster`
  line/paragraph, but **deliberately did not yet wire in `anyscale_system_cluster`** — that would
  break this real-infrastructure example by referencing a resource type that doesn't exist in
  the provider binary yet. Tracked as a separate follow-up (my task #9) for once the resource
  actually builds.
- Changelog fragment content drafted (3 blocks — `new-resource`, `new-data-source`,
  `breaking-change` — matching `.changelog/README.md`'s exact format, including its explicit
  "new-resource, not added, for a brand new resource" rule) at
  `.crystl/quest/DRAFT-changelog-fragment.txt`. Not the real file yet (needs a PR number in the
  filename per this repo's convention); whoever opens the PR copies the fenced blocks verbatim.

**tfp-forge — client + unit tests complete.** `system_workload_helpers.go` +
`system_workload_helpers_test.go`, builds/vets/gofmt clean, full suite green. 20 tests covering
AC12–AC17 plus endpoint-shape and `findSystemWorkloadCluster` coverage (multi-cloud collision,
both possible `cloud_id` wire shapes, `is_system_cluster` filtering, not-found, pagination).
**AC17 verified genuinely mutation-proof** — flipped the poll loop's hardcoded
`start_cluster=false` to `true`, confirmed the specific test fails naming which poll number and
what it sent, reverted byte-clean, reran green (not just inspected).

**AC13 correction (both forge and assayer independently confirmed the same gap, from source
and from live responses)**: `DescribeSystemWorkloadResponse` has no error/message field at all
— just `{cluster_id, workload_names, workload_service_url, workload_service_url_auth, status,
is_enabled}`. A richer "backend message" for a terminal-error state doesn't exist at this
endpoint; the console gets that detail from two entirely separate calls (`decorated_sessions`'
`stateData.startup.startupError`/`stateData.stopping.stopError`, and a filtered cluster-events
endpoint) that are out of scope for this resource. **My `state` attribute description was
already accurate on this** (describes the plain enum string, never implies a richer message),
so no doc fix needed — worth confirming precisely rather than assuming, given the two-source
convergence made it easy to just trust. AC13's own wording is likely getting amended by
architect to match what's actually available.

**tfp-assayer — AC26 live smoke test: PASSED.** Ran the full lifecycle via direct authenticated
API calls (not through forge's client, so this validates the wire contract independently)
against the real static fixture cloud. Confirmed live, matching the source trace exactly:
- Enable+describe(start=true) on a cloud with no prior cluster creates it, returns immediately
  with `status=Terminated` (the `StartingUp` transition is genuinely async, invisible in the
  same response) — the one thing source tracing alone couldn't fully pin down.
- **Real timing** (one sample each, not a guarantee): `Terminated→StartingUp→Running` ~49s;
  `Terminating→Terminated` ~16s. Folded into `start_timeout`'s rationale sentence — kept the
  default generous (20m) rather than tightened to match the sample, same reasoning
  `rollout_timeout`'s 45m default already uses in this codebase.
- Idempotency confirmed live, not just source-traced: `describe(start=true)` again while
  `Running` is a true no-op.
- `decorated_sessions` wire shape fully captured on a real row: `cloud_id` DOES serialize (good
  news — resolves what was flagged uncertain earlier), `is_system_cluster:true` present, and the
  enum-vocab mismatch is real and visible on the *same* row (`state='Running'` vs.
  `status='RUNNING'` — different fields, different vocabularies, confirms the "avoid the
  `decorated_sessions`-native enums entirely" design decision was correct, not overcautious).
- AC4 (enable-then-start order) verified live as a durable no-op on a disabled+terminated
  cluster, rechecked independently 15s later — not just a single-call misread.
- Fixture restored to its original state afterward; one permanent, expected, correct side
  effect (a `Terminated` cluster row now exists, same as any real user's first cycle would
  leave). No secrets persisted.

**Status.** Design fully validated end-to-end: source trace, unit tests, and now a real live
smoke test all agree. Merge gate (AC26) cleared from assayer's side. Remaining work is Wave 2
completion (shipwright's resource/data-source code + StateUpgrader) and architect's final
integration review — no further design questions open. My own remaining work: verify the actual
generated docs once shipwright's schema code lands (likely at integration time, since
`tfplugindocs`/`make docs` needs the real Go schema to exist), and the deferred kitchen-sink
addition (task #9).

---

### Shipwright's code lands; integration review PASSES; my content verified against the real implementation

**tfp-architect — INTEGRATION REVIEW: PASS.** Assembled the *combined* tree (forge's approved
client + shipwright's resource/DS/removal) in a scratch dir and actually built + tested it —
not inference. Builds/vets clean, full unit suite passes with zero regressions, all 3 resource
acctests RUN and PASS (Create AC1-5, Delete AC10 mutation-proof, Import AC11 — the post-import
re-apply really is a no-op plan, which also resolves the `start_timeout`-on-import question).
State upgrader v1→v2 drops `enable_system_cluster` cleanly (AC22); v0→v1 has no regression.
Two items: (1) AC20 test gap — the data source had no test at the time of review (shipwright
already had one in flight by the time I checked, see below); (2) **the same
integration-co-dependency pattern this repo's memory already documents** — forge's client and
shipwright's resource are co-dependent branches (shipwright's worktree doesn't build without
forge's client) and must land together atomically, not independently.

**I checked whether this co-dependency pattern also applied to my own deliverables — it did.**
Reading shipwright's actual worktree directly (Bash, cross-worktree; Edit/Write stay sandboxed
to my own worktree, this is exactly the situation that distinction exists for):
- **Verified my drafted `MarkdownDescription` content against the real
  `resource_system_cluster.go`/`data_source_system_cluster.go`**: high-fidelity match. Every key
  nuance survived intact — the documentation-only (not enforced) primary-cloud-resource caveat
  with "today"/forward-looking framing, the non-destructive-delete `~> **Note:**` callout
  verbatim, the enable-then-start behavior, the real ~49s timing folded into `start_timeout`'s
  rationale. Shipwright made good small refinements beyond my draft (e.g. `start_timeout` now
  explicitly states it's "Purely local to this provider - never sent to or read from the
  Anyscale API"; a conventional `id` attribute I hadn't anticipated, mirroring `cloud_id`, with
  a clear description). `ImportState` confirmed as `ImportStatePassthroughID(cloud_id)` exactly
  as documented.
- **Found a real, live version of the same co-dependency risk architect just flagged**:
  shipwright's worktree had already removed `enable_system_cluster` from `resource_cloud.go`,
  but (a) my new `examples/resources/anyscale_system_cluster/*` and
  `examples/data-sources/anyscale_system_cluster/*` files didn't exist there at all (needed for
  `make docs` to render an Example Usage section), and (b) three existing example files
  (`aws-vm-basic/main.tf`, `resources/anyscale_cloud/resource.tf`,
  `kitchen-sink/cloud_b.tf`+`README.md`) still referenced the now-removed attribute — would have
  failed `terraform validate`/`make docs` the moment someone tried to build docs against the
  combined tree. **Fixed by copying my own worktree's versions over via Bash** (cross-worktree,
  same mechanism the git-worktree-shared-refs memory already documents as safe/available),
  verified clean with `diff -rq` afterward. Deliberately did **not** run `make docs` myself in
  shipwright's worktree — that generation step, and verifying its output, is shipwright's to run
  and own.
- Confirmed `data_source_system_cluster_acc_test.go` already exists (untracked) in shipwright's
  worktree — the AC20 test gap architect flagged appears to already be in progress/addressed.

**Status.** Implementation functionally complete and cross-verified against my own docs intent,
not just trusted. The only remaining known gap is the AC20 data-source test (shipwright's, in
progress) and whatever final polish comes out of it. My own remaining work: task #9
(kitchen-sink `anyscale_system_cluster` addition) once the resource is confirmed buildable
end-to-end, and a final HANDOFF.md wind-down summary once architect calls this done.

---

### AC20 closed; the user asks for outside validation; team converges on strong precedent

**AC20 (data-source test) — closed by shipwright, mutation-tested properly.**
`TestAccSystemClusterDataSource_ReturnsObservedState` (create via the resource, data source
reflects real state) and `TestAccSystemClusterDataSource_NotConfiguredReturnsCleanNull` (a
cloud with no system cluster → clean null on every computed field, **and asserts `describe` was
called zero times** — the actual side-effect-free proof, not just absence-of-error). Verified
mutation-proof: temporarily removed the existence-gate, confirmed the not-configured test fails
loudly, reverted byte-identical. Re-verified against a combined-tree overlay of forge's real
client (temporary, removed after), full suite green. Shipwright also confirmed my synced example
files coexist cleanly in their worktree without conflict.

**The user asked the team to justify the core design decision against outside precedent**: *"How
do other terraform providers deal with a resource that could be configured from the top-level
resource but also could be their own sub-resource? ... please push back if appropriate."* Three
of us (forge, shipwright, myself) independently researched this in parallel and converged on the
same real-world precedent, each adding something distinct:
- **forge**: `aws_iam_role`'s inline `managed_policy_arns`/`inline_policy` vs. the standalone
  `aws_iam_role_policy`/`aws_iam_role_policy_attachment`, and `aws_security_group`'s inline
  `ingress`/`egress` vs. standalone `aws_security_group_rule` — verified against the real
  Registry docs, not recalled from memory. HashiCorp's own resolution both times: pick one
  owner, never both; the ecosystem has been actively deprecating the inline form.
- **tfp-scribe (me)**: added a third example (`aws_route_table`'s inline `route` vs. standalone
  `aws_route` — same documented conflict), plus checked whether HashiCorp's *more permissive*
  "exclusive vs. additive" pattern (from the AWS provider's own contributor design guide, where
  multiple independent resources CAN safely co-manage a many-to-many membership) might apply
  here instead of the stricter "pick one" pattern — concluded it doesn't: that pattern is for
  collection/membership relationships, not a single scalar toggle moving from a parent resource
  to a child one, which is the exact shape of our case.
- **shipwright**: found the closest and strongest analog — the AWS provider's **v4 "resource
  decomposition" of `aws_s3_bucket`**, where a monolithic resource with dozens of optional
  sub-blocks (versioning, lifecycle, encryption, ACLs) was deliberately split into a family of
  standalone resources each keyed by `bucket = aws_s3_bucket.example.id`, with HashiCorp
  publishing a dedicated migration guide for it. Tied it precisely to our case: `enable_system_cluster`
  had "the EXACT failure shape IAM's `inline_policy` did — a boolean toggle with no reliable
  read-back living inside an otherwise-unrelated resource's apply cycle," per its own removed
  schema doc's language.

**tfp-architect's synthesized answer to the user** (the definitive record): the pattern is named
plainly — inline-block-on-parent vs. standalone-child-resource is one of the most well-trodden
tensions in provider design, and shipping both invites a "mutual-exclusivity footgun" (each
apply reverting the other). Rule of thumb given: *if the sub-thing has its own independent
lifecycle — can be started, stopped, changed, or observed independently of the parent's
existence — model it as a standalone resource, not an inline block.* System Cluster clearly
qualifies. **Honest trade-off named explicitly, not glossed over**: standalone-only drops the
"enabled-but-deliberately-not-running" state the old inline toggle could express — under the new
design, declaring the resource always means enable-and-run, no parked/enabled-only state.
Architect's judgment: acceptable, since an enabled-but-not-running System Cluster serves no real
purpose (dashboards don't work until it's actually running), and the console/CLI still cover
that workflow directly for anyone who genuinely wants it outside Terraform. **Bottom line:
stick with the plan — remove the inline attribute entirely rather than keep both; this is where
the ecosystem is heading, not a novel or risky choice.**

**Status.** Design decision now independently corroborated against real industry precedent, not
just internal consistency. AC20 closed. Team status: forge (client) done+approved; shipwright
(resource/DS/removal/StateUpgrader) done, AC20 closed, ready for architect's re-verify; assayer
(API discovery + live smoke test) done; scribe (docs/examples/changelog) done, verified against
real implementation, one deferred follow-up (kitchen-sink, task #9). Awaiting architect's final
combined re-verify and integration-ready call.
