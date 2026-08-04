# Design: `anyscale_container_image_registry.name` — import round-trip and `name_version` provenance

**Status: design, GATES CLOSED, cleared for implementation.** Measured at `0e862fd`; gates closed
2026-08-04 with logged evidence (see "Gate results" below). Two claims in the original draft were
corrected by those results and are marked inline.

## The reported problem

`name` is `Optional` + `RequiresReplace` and **not** `Computed`
(`internal/provider/resource_container_image_registry.go:107`). `ImportState` is a bare
`ImportStatePassthroughID` on `id` (`:553-556`), and `Read` deliberately never populates `name`
(`:515-519`). So after `terraform import`, `name` is null in state. A config that *sets* `name` then
plans null → value on a `RequiresReplace` attribute: **destroy and recreate a real cluster
environment.**

## The detail that decides the design

When `name` is omitted, the provider does not leave the backend to name the object. It mints one
itself (`:249-251`):

```go
baseName := sanitizeImageURIForName(plan.ImageURI.ValueString())
timestamp := time.Now().UnixNano()
name = fmt.Sprintf("%s-%d", baseName, timestamp)
```

**The generated name embeds a nanosecond timestamp.** It is therefore not derivable from any
configuration input, not reproducible, and exists only in the backend. Today it is never written to
`name`, so a practitioner who omits the attribute — the documented default ("If not specified, a
name will be auto-generated") — has no way to learn what their cluster environment is called except
by parsing `name_version`.

That makes `name` a genuinely backend-held value whenever it is omitted, which is precisely what
Terraform models with `Computed`.

## Why `ImportState`-only recovery is the wrong fix here

`CLAUDE.md` offers three remedies for this bug class. Recovering in `ImportState` while leaving
`name` `Optional`-only is **actively worse than the current bug**, and the reasoning is the same
sentence `CLAUDE.md` already uses: config-absent versus state-present is a diff, and the attribute
forces replace.

- Today: users who **set** `name` are broken on cold import.
- Under `ImportState`-only recovery: users who **omit** `name` — the documented default — would be
  broken instead, because import would populate a value their config does not declare.

It trades a smaller broken population for a larger one. Rejected.

The block-versus-attribute check that `CLAUDE.md` requires first: `name` is a `schema.StringAttribute`,
not a framework Block, so `Computed` is available with no HCL-breaking conversion.

## The design: mirror `ray_version`, which is already this exact shape on this exact resource

`ray_version` on the same resource is already `Optional` + `Computed` + `RequiresReplace` +
`UseStateForUnknown`, populated from the backend only when the practitioner left it unset. It has a
passing acceptance test (`TestAccContainerImageRegistryResource_RayVersionUnset_PopulatesOnRefresh_MockServer`).
`name` should be made identical to it. This is not a novel design; it is applying the resource's own
established pattern to the one attribute that was left out of it.

Three changes, each with its `ray_version` counterpart:

| # | Change | `ray_version` counterpart |
|---|---|---|
| 1 | Schema: add `Computed: true` and `UseStateForUnknown()` to `name`, keeping `RequiresReplace()`. | `:107` block vs. the `ray_version` block below it |
| 2 | Create: fill `name` from the backend **only when the planned value is Unknown**. | `:389-394` |
| 3 | Read: fill `name` from `template.Name` **only when state is null**. | `:507-513` |

**`UseStateForUnknown` — ship it, but the original rationale here was overstated.** The draft called
it "mandatory, not stylistic", reasoning that an omitted `name` would re-mark Unknown each plan and
that an Unknown on a `RequiresReplace` attribute forces replacement. Gate 2 disproved the premise for
*this* resource: removing the modifier left the test passing. Traced in framework source
(`internal/fwserver/server_planresourcechange.go`): Core's proposed-new-value for a `Computed`
attribute with null config starts from **prior state** — already post-refresh by the time Plan runs —
and the "mark computed nils unknown" sweep is skipped entirely when proposed equals prior. Because
every other user-settable attribute on this resource is already `RequiresReplace`, no in-place-update
path exists that could force that sweep while `name` rides along omitted.

So it is **correct future-proofing, not the load-bearing piece**: it matches `ray_version`, costs
nothing, and protects a path that is real but not currently reachable. Read's fill-on-null is what
actually carries this transition. Ship both; do not describe the modifier as what makes the fix
work.

**Fill-on-Unknown / fill-on-null, never unconditional overwrite.** This is what keeps a
practitioner-supplied `name` from ever being overwritten by an API value, which is the failure mode
that would force-replace every existing user. It also means Gate 1 below cannot turn this fix into a
regression — only into an incomplete one.

Note the `Optional+Computed` Unknown-versus-Null trap, and note what it does **not** require here.
It is tempting to conclude that because an omitted `name` arrives Unknown, the Create guard must
gain an `IsUnknown` clause or it will send `name=""`. It will not. The guard already reads:

```go
if !plan.Name.IsNull() && plan.Name.ValueString() != "" {
```

Two clauses, not one. Since `ValueString()` on an Unknown returns `""`, the pre-existing `!= ""`
clause *already* sends an Unknown down the generate-a-name branch. The regression the added guard was
said to prevent does not reach the wire.

**Still add `&& !plan.Name.IsUnknown()`** — it matches `ray_version`'s guard at `:226`, states the
intent directly instead of relying on an emergent property of `ValueString()`, and stops a later
edit to the `!= ""` clause from silently reopening the hole. But it is defensive hardening, not a bug
fix, and reverting it alone breaks no test.

**No state upgrader is required.** Adding `Computed` does not change the state shape — the attribute
is a string before and after — so no schema `Version` bump. Existing state carrying `name = null`
is legal for an `Optional+Computed` attribute and is populated by the first refresh, with config
omission absorbed rather than diffed. *That last clause is Gate 2; it is not assumed.*

## Second, independent finding: `name_version` has two different sources

`name_version` is `Computed` and documented as "for use with Anyscale APIs", so its value must be
the name **the backend knows**. It is currently derived from two different places:

- **Create** (`:398-399`) uses `templateName` — the name echoed by
  `POST /api/v2/application_templates/byod`.
- **Read** (`:520-523`) prefers `state.Name` — a configuration-derived value — and only falls back
  to `template.Name` when state is null.

If the backend ever returns a name different from the one submitted (sanitization, truncation, or
uniquification on a name collision — cluster environment names are unique per organization), then
Create writes `<backend-name>:N` and the next Read overwrites it with `<config-name>:N`. Two
consequences, both real: perpetual drift on a Computed attribute, and a `name_version` that does not
work against the Anyscale API — which is its entire purpose.

**Ruling, REVISED after Gate 1: do not change this, and do not land it with the import fix.**

Gate 1 established that the backend never renames — an invalid character is a hard 422 and a name
collision is a hard 409, so a `201` always carries back exactly the submitted name. The divergence
described above therefore has **no reachable trigger**: Create's `templateName` and Read's
`state.Name` cannot hold different values.

This is the same shape-without-mechanism pattern recorded for `kubernetes_config.zones` in
`CLAUDE.md`, and consistency requires the same answer. The two code paths genuinely do read from
different sources, which is why it looked like a defect — but a change with no observable effect,
riding along in a PR that already carries three changes and two tests, is a change bought for
nothing.

**What would reopen it:** any backend behavior that returns a name differing from the one submitted.
Note that such a change would break more than `name_version` — with `name` modeled
`Optional+Computed`, a config-supplied name that the backend altered would leave state holding the
config value while the backend holds another, silently. If that day comes, this design is revisited
as a whole, not patched at `name_version`.

One caveat on the evidence, stated because it affects how far the conclusion travels: Gate 1 probed
two scenarios (invalid character, collision) and both hard-rejected. That proves those two paths do
not rename. The broader claim that *no* backend path renames is an extrapolation from two negative
probes unless separately source-traced — untested shapes such as length truncation or Unicode
normalization were not exercised. The ruling above does not depend on the stronger claim: the two
probed paths are the two the provider can actually produce, since it either passes a
practitioner-supplied name straight through or generates one from a sanitized URI plus a
nanosecond timestamp.

## Gates — no implementation until both are closed

### Gate 1 (API response shape): does `POST /api/v2/application_templates/byod` echo `Name` verbatim?

Everything above is safe under fill-on-null regardless of the answer, but the `name_version` ruling
and the residual risk assessment both depend on it. Not answerable from source.

Two real creates, both cheap and both swept by the existing container-image sweeper:

1. Submit a `name` containing a character outside `^[A-Za-z0-9._-]+$` (e.g. a slash). Record whether
   the response `Result.Name` is the submitted string, a sanitized string, or an error.
2. Submit a `name` that already exists in the organization. Record whether the response is a 409, or
   a success carrying a uniquified name.

Log the request and response bodies for both and cite them here. If either returns a name differing
from what was sent, the `name_version` ruling becomes load-bearing rather than tidy-up, and the doc
for `name` must say the backend may rename.

### Gate 2 (Framework/Core contract): does a previously-null `Optional+Computed` attribute populate on refresh without planning a diff when config omits it?

This is the migration claim for every existing user, and `CLAUDE.md` is explicit that framework
source describes the mechanism without revealing every constraint Core enforces at plan time. A unit
test built on that source shares its blind spot. Requires a real `resource.Test`.

`ray_version` is strong precedent — same resource, same shape, passing test — but precedent is not
proof for a *transition*: `ray_version` has been `Optional+Computed` since it was introduced,
whereas `name` would be changing shape underneath state that already exists. Prove the transition,
not the steady state: seed state with `name` absent under the current schema, then plan under the
new one.

## Acceptance criteria

Two tests, because a Create → Import → re-apply → assert-no-op sequence provably cannot detect an
import-recovery bug (`ImportState` without `ImportStatePersist` runs in a throwaway directory).

- **Test A — what import actually recovered.** Cold import: the cluster environment is seeded out of
  band and import is the **first** step, since `terraform import` refuses an address already managed
  in that working directory. Assert with `ImportStateCheck` that `name` is the real backend name and
  not null. `ImportStateVerify` is unavailable in a cold import — there is no prior state to compare
  against — so the `ImportStateCheck` assertion is the whole proof and must be specific.
- **Test B — planning against the recovered shape.** Two sequential `Config`-only steps, which do
  carry state forward: step 1 omits `name`, step 2 declares it at the value the backend holds.
  Assert `plancheck.ExpectEmptyPlan()` on step 2 — **and an explicit value assertion alongside it,
  which is not optional.** Gate 2 demonstrated that `ExpectEmptyPlan` alone stays green with Read's
  fill-on-null removed: a stably-null state produces a trivially empty plan whether or not Read ever
  populates the attribute, so the empty-plan check is a placebo on its own. Assert that `name` holds
  the real backend value.

Mutation-proof what can be mutation-proofed, and know in advance which change cannot be. Gate 2
already ran this: reverting **Read's fill-on-null** fails correctly (on the value assertion, not the
plancheck). Reverting the **`IsUnknown` guard** alone breaks nothing — see the correction above; the
pre-existing `!= ""` clause already catches Unknown. Two of the three changes therefore have no
failing test to point at, and only Read's fill-on-null does. Reverting **`UseStateForUnknown` does not fail any test** — it guards a path that is not
currently reachable on this resource, so do not spend time hunting for a test that goes red without
it, and do not conclude from that green result that the modifier is unnecessary. Restore
byte-identically after each. A build that fails to compile is not a failing test.

**Fixture requirement, and it is the one most likely to be got wrong.** The mock must return a
`name` that is **not derivable from `image_uri`** — a timestamped or otherwise arbitrary string.
If the fixture's name is something the provider could have reconstructed from config, the test
cannot distinguish "recovered from the backend" from "rebuilt locally", and it passes against a
broken fix.

**Naming.** `TestAccContainerImageRegistryResource_...` — CI shards acceptance tests on
`-run '^TestAcc[A-Za-z]+Resource'` (`.github/workflows/ci.yml:210`); a non-matching name neither
runs nor fails. Confirm the tests genuinely RUN rather than SKIP by reading the shard's job log.

## Changelog

Provider-facing behavior change: needs a fragment. `name` becomes populated in state where it was
previously null for practitioners who omitted it — visible in `terraform plan` output and readable
via `.name`, so it is a user-visible change even though it is not breaking.


## Gate results (closed 2026-08-04)

**Gate 1 — API response shape: CLOSED.** Real API, static org, objects cleaned up afterwards.

- A name containing a character outside `^[A-Za-z0-9._-]+$` (a slash): `HTTP 422`, *"name should
  match this pattern: ^[A-Za-z0-9._-]+$"*. Hard rejection; nothing created; **no sanitization**.
- A colliding name: created `tfacc-gate1-dup-9661` (`201`), re-POSTed the identical name: `HTTP 409`,
  *"This name is already taken"*. Hard rejection; **no uniquification**.

Consequence: on any `201`, the returned name is the submitted name. This is what downgraded the
`name_version` finding above from a defect to be fixed into a change not to make.

**Gate 2 — Framework/Core contract: CLOSED, PASS.** The three designed changes were built as a
temporary env-var-gated spike, toggled per `TestStep` via `PreConfig`, and fully reverted afterwards
(verified byte-identical). The mock name was deliberately not derivable from `image_uri`, per the
fixture requirement above.

- Step 1 under the old schema creates with `name` omitted → null. Step 2 under the new schema, with
  the config unchanged, produces an **empty plan** *and* populates `name` to the real backend value.
  Step 3 reconfirms stability. The transition — not merely the steady state — is what was proven.
- Mutation-proofed in both directions, and both results changed this document: removing Read's
  fill-on-null fails on the value assertion but **not** on `ExpectEmptyPlan`; removing
  `UseStateForUnknown` does not fail at all.
- **The `IsUnknown` guard is hardening, not a fix — and the tempting argument that it is a fix does
  not survive.** That argument runs: `ValueString()` on an Unknown returns `""`, and Gate 1 shows an
  empty name is a `422`, therefore omitting the guard sends an empty name and breaks Create. Every
  step is true and the conclusion is still wrong, because it never asks what the *existing* guard
  does with that `""` — it already rejects it, via a `!= ""` clause that predates this work. Proven
  by mutation: revert only the `IsUnknown` clause and the name still generates, with nothing empty
  sent. Add it for the reasons above; do not describe it as fixing anything.
