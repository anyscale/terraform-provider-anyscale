# Design: `anyscale_container_image_registry.name` — import round-trip and `name_version` provenance

**Status: design, gated.** Measured at `0e862fd`. Implementation must not begin until the two gates
below are closed with logged evidence, per the Design Verification Policy in `CLAUDE.md`.

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

**`UseStateForUnknown` is mandatory, not stylistic.** Without it, a plan whose config omits `name`
re-marks it Unknown on every run; an Unknown on a `RequiresReplace` attribute with a known prior
state plans a **replacement**. Omitting this modifier converts an import bug into a
replace-on-every-plan bug.

**Fill-on-Unknown / fill-on-null, never unconditional overwrite.** This is what keeps a
practitioner-supplied `name` from ever being overwritten by an API value, which is the failure mode
that would force-replace every existing user. It also means Gate 1 below cannot turn this fix into a
regression — only into an incomplete one.

Note the `Optional+Computed` Unknown-versus-Null trap: the existing Create guard at `:243` tests
`!plan.Name.IsNull()` only. Once `name` is `Computed`, an omitted attribute arrives as **Unknown, not
Null**, and that guard would read an Unknown as user-supplied. It must become
`!plan.Name.IsNull() && !plan.Name.IsUnknown()`, matching `ray_version`'s guard at `:226`.

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

**Ruling: `name_version` must derive from the backend's name unconditionally, in both Create and
Read.** Read's preference for `state.Name` is the defect. This is independent of the import fix and
should land with it, since both hinge on the same capture.

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
  Assert `plancheck.ExpectEmptyPlan()` on step 2. This is what proves "no spurious replace."

Both must be mutation-proof: revert each of the three changes in turn, confirm the relevant test
**fails**, then restore byte-identically. A build that fails to compile is not a failing test.

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
