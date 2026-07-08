# Quest Log — terraform-provider-anyscale

Dated engineering-journal entries, newest first. One entry per quest/session.

---

## 2026-07-08 — Compute Config API Sync (CC1–CC12)

**Goal.** Synchronize `anyscale_compute_config` (resource + data source) with the current
Anyscale Platform API — design-first, aiming for idiomatic Terraform behavior rather than a
1:1 REST mirror. Party: tfp-architect (design/review/integration), tfp-forge (implementation),
tfp-assayer (tests/CI), tfp-scribe (docs), tfp-shipwright (release).

**Base / integration.** Started from `origin/main @ f5325a5` (all worktrees were 9 commits
stale at quest-cut and rebased). Integrated into `integration/compute-config-sync` (main
untouched). `crystl merge` was deliberately **not** used — it targets `main` only.

**What landed (CC1–CC12).**
- **CC1 (fix)** — `physical_resources` was dead-on-arrival: the backend rejects that JSON key
  with a 400 (renamed to `required_resources` server-side). Renamed the Terraform attribute +
  send `required_resources`; **mandatory** schema `Version` bump + `UpgradeState` (a successful
  past apply could have stored an empty `physical_resources` block).
- **CC2 (add)** — made `idle_termination_minutes` + `maximum_uptime_minutes` settable
  (were read-only via the data source only). Both `Optional+Computed+UseStateForUnknown`,
  populated from the API response in Create/Update/Read (no static default — an omitted
  `max_uptime` without this was unknown-after-apply, which crashed every apply, and the missing
  `UseStateForUnknown` would have inflated a new version on every apply).
- **CC3a (fix)** — `name` → `RequiresReplace`. Renaming silently orphaned the old config
  (live-verified). Opposite call from Cloud C11 (there replace was the trap) because a compute
  config is a lightweight versioned template.
- **CC3b (fix)** — cloud identity immutable via an **Update-time error guard**, NOT
  `RequiresReplace` (detecting a cloud change needs a `cloud_name`→id network resolution, and
  plan modifiers must stay offline). Classified `fixed` (C11 precedent), not breaking.
- **CC4 (add)** — `cpu_architecture` (permissive string, no client enum).
- **CC5a (refactor)** — data source reuses the resource's typed parsing (was hand-parsing an
  untyped map). Side-effect fix: DS `enable_cross_zone_scaling` had read `false` for every user
  (checked a top-level key that never existed; real value is `flags["allow-cross-zone-autoscaling"]`).
- **CC5b (deferred)** — endpoint convergence `ext/v0`→`api/v2`. 5 of 8 sites near-free, but the
  sweeper search's body-vs-query pagination difference risks *silent sweep truncation*; failed
  the near-free bar. Recorded as a dedicated follow-up.
- **CC6 (add)** — data source node-topology parity (`zones`, `head_node`, `worker_nodes`),
  unmasked (a data source reports current state).
- **CC11 (fix)** — `Read` now treats an `archived_at` config as gone (RemoveResource).
- **CC12 (fix)** — import recovers write-only `flags`/`advanced_instance_config` (ImportState
  only) so an unrelated later apply no longer silently wipes them.

**Pre-existing bugs surfaced by the design-first read (none introduced by us).** The
`physical_resources` 400 (CC1); a CI false-green where the headline acceptance test had been
silently skipping on every run (`GetAllConfiguredClouds` checked a field the API never
populates); the DS cross-zone always-`false` (CC5a); and an inaccurate `resources` description
(words-only fix — the fallback is Anyscale's control plane at launch-spec-build, not Ray core).

**Key decisions.** MINOR release (CC1 rename + CC3a `RequiresReplace` are breaking-change
fragments; everything else fix/additive; CC3b is `fixed`). `api/v2` over `ext/v0` codified in
CLAUDE.md as provider-wide policy. Decision framework for immutable identity attrs
(RequiresReplace vs Update-guard vs plan-diagnostic) captured in memory.

**Validation.** Provider `8e5cef1` (forge), certified at assayer `d6fd629` (byte-identical),
full acceptance suite live-green vs real AWS, one honest disclosed `_K8S` skip. Integration
branch independently re-verified (build/vet/gofmt/unit + full real-AWS suite) by assayer.

**Outcome.** Integration branch `integration/compute-config-sync` (tip `6fe7c49`) built, docs
regenerated (no drift), vet/gofmt/unit clean, top-level + per-node real-AWS acceptance green (one
honest `_K8S` skip). Release verdict: **MINOR**. Open as **PR #50** with `.changelog/50.txt`; main
untouched.

**Wind-down close addendum (same day).** The integration + verification pass surfaced and resolved
more, each caught by refusing a passive "green": **CC13** schema-contract pins (design invariants now
CI-enforced); **CC14** `enable_cross_zone_scaling` left null on import → permanent phantom diff /
silent version inflation, fixed (Read resolves false-if-absent); **CC15** `interfaceToAttrValue` built
a `List` for JSON arrays where a Dynamic attribute infers a `Tuple` (+ a mixed-type-array coercion
bug), fixed (per-element `TupleValue`/`TupleType`), blast-radius-checked to exactly two callers. And
the **CC12 test gap**: the claimed import round-trip test did not exist; assayer wrote real ones —
top-level (incl. arrays) and, per user direction, **per-node nested on workers** (`f7e566f`, real
teeth) — both green, no fix needed (per-node values are plain JSON strings that `json.Marshal`
byte-matches to `jsonencode`). Stale `ImportStateVerifyIgnore` skips removed. Every claim reconciled
to exactly what is tested.
