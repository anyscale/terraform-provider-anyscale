# tfp-forge: MoveState feasibility + implementation plan

Reply to task 01a1c120 (architect's draft contract `docs/decisions/iam-organization-rename/README.md`,
commit 7f56823). Verified against framework v1.19.0 source in the module cache (not docs alone) and
against origin/main b38abb9 files. **Verdict: the contract's MoveState design is feasible exactly as
written.** Details below; one Gate-2 design note (test factory) needs assayer's eyes.

## 1. Framework v1.19 MoveState surface (verified, `resource/move_state.go` + `internal/fwserver/server_moveresourcestate.go`)

- Interface: `ResourceWithMoveState = { Metadata, Schema, MoveState(ctx) []resource.StateMover }`.
  Each `resource.StateMover` = `{ SourceSchema *schema.Schema; StateMover func(MoveStateRequest, *MoveStateResponse) }`.
- `MoveStateRequest` fields: `SourceTypeName`, `SourceProviderAddress`, `SourceSchemaVersion int64`,
  `SourceRawState *tfprotov6.RawState` (always populated), `SourceState *tfsdk.State` (populated ONLY
  if that mover's `SourceSchema` matched and decoded — decode failure is logged at DEBUG and the
  mover is **still called**), `SourcePrivate`, plus identity fields (ignore).
- `SourceProviderAddress` is the full `HOSTNAME/NAMESPACE/TYPE` form (e.g.
  `registry.terraform.io/anyscale/anyscale`). Suffix match `strings.HasSuffix(req.SourceProviderAddress, "/anyscale/anyscale")`
  satisfies the contract's "any host/namespace form" rule. Core sends the *target* provider's own
  address for a same-provider moved block, and `moved` from a type the provider no longer registers
  routes to the target's MoveResourceState RPC — this is the feature's designed path (Core ≥1.8;
  floor is 1.10, no change).
- **Skip semantics match the contract:** no error diagnostics AND no `TargetState` = framework tries
  the next StateMover; if all skip, Core gets "implementation not found". So wrong type name /
  provider address return silently (skip), and an *error* from one mover preempts the others.
- **Version check is mandatory before touching `SourceState`**: movers are called even when their
  SourceSchema failed to decode the request (DEBUG-log only). Each mover must gate on
  `SourceSchemaVersion` first.

## 2. organization_user MoveState (two movers)

- Mover A (source v1): `SourceSchema = r.Schema()`'s schema (v1), gate `SourceTypeName ==
  "anyscale_organization_user" && SourceSchemaVersion == 1` → set `TargetState = req.SourceState`
  verbatim (same model type, same schema version 1). No transformation.
- Mover B (source v0): `SourceSchema = organizationUserSchemaV0()` (the existing frozen PriorSchema —
  reuse, do not re-freeze), gate version == 0 → decode `req.SourceState` into
  `organizationUserResourceModelV0` and set `TargetState` via the **shared** v0→v1 transform.
- Shared transform: refactor `upgradeOrganizationUserStateV0toV1` so the field-by-field mapping lives
  in one pure function `organizationUserV0ToV1(v0 organizationUserResourceModelV0) OrganizationUserResourceModel`,
  called by both `UpgradeState` and MoveState mover B. The current upgrader is already exactly that
  mapping inline — the refactor is mechanical, keeps the "adding an attribute fails to compile"
  property (map into `OrganizationUserResourceModel` in the one function).
- Other versions / wrong type / wrong address: skip (return nothing) per contract; resource-specific
  version errors only fire after the gates match (contract's "error" case is only reachable for a
  matching type name + non-matching version — since we register movers per version, "any other
  version" with a matching type name falls through all movers and Core reports not-found, which is
  an acceptable, self-consistent reading — recommend contract wording: *unsupported version →
  skip, not error*, because with per-version movers there is no mover left to produce the nicer
  diagnostic. Flagging this as a small contract amendment.)

  Correction to the above: we CAN produce the contract's error diagnostic — register a third mover
  with `SourceSchema = nil`, gated only on `SourceTypeName == "anyscale_organization_user"`
  (and provider-address), which errors if version not in {0,1}. With `SourceSchema: nil` it reads
  `SourceRawState` and never needs to decode. Framework guarantees an error from it preempts
  skip-fallthrough. Recommended: do this for both resources; it keeps the contract's
  "error naming the version + recovery" verbatim. Ordering note: keep nil-schema mover LAST so
  valid v0/v1 movers answer first.

- Private state: copy `req.SourcePrivate` → `resp.TargetPrivate` when non-nil, both resources.
  Read self-heals the membership record when absent (verified: `resolveMembership` +
  `writeMembershipRecord` path in resource_organization_user.go), so the no-private move plans clean
  — matches contract §c.
- No API call in any mover; next refresh reads under the new type.

## 3. organization_invitation MoveState (one mover + catch-all)

- Source schema version 0 only: mover with `SourceSchema = r.Schema()` (v0 == current), gate
  `SourceTypeName == "anyscale_organization_invitation" && version == 0` → copy-through, plus the
  nil-schema catch-all error mover for other versions. No transform exists or is needed.

## 4. Rename mechanics (boring, enumerate so nothing is missed)

1. `git mv` per file (impl, _test, upgrade + priorschema test), then:
   `Metadata TypeName` → `anyscale_iam_organization_user` etc. — TypeName comes from
   `req.ProviderTypeName + "_iam_organization_user"`; types already follow the v0.38.0 rename
   pattern (see `resource_iam_user_group*.go`), mirror it.
2. `internal/provider/provider.go` registry: new keys in `Resources`/`DataSources`; delete old.
3. Go type names (`OrganizationUserResource`) can stay; v0.38.0 rename precedent kept Go names.
4. Cross-reference text sweep (contract's "user-facing text" section) then the AC-6 grep with `-P`.
5. Examples: `git mv examples/resources/anyscale_organization_user` → `anyscale_iam_organization_user`,
   same for invitation, `examples/data-sources/...`, update in-file resource blocks + `terraform-
   import` example lines; width limit still applies.
6. `make docs` regenerates `docs/resources/`+`data-sources/`; old generated pages should be
   deleted manually if `tfplugindocs` doesn't clean them (verify with `git status`).
7. Sweepers: rename keys in `internal/acctest/sweeper_organization_invitation*.go` (observe
   `Dependencies` ordering); `make sweep-dry-run` to confirm.
8. Acctest names: `TestAccOrganizationUserResource` → `TestAccAnyscaleIAMOrganizationUserResource`
   — starts with `TestAcc`, still ends `Resource`/`DataSource`, matches the shard regex
   `^TestAcc[A-Za-z]+(Resource|DataSource)`. (v0.38.0 names follow this form; copy it.)
9. `provider_registry_test.go`: add the four old names to the removed-name absence lists (AC 2)
   — extend the existing lists from the v0.38.0 rename.
10. `deprecation_legacy_rbac_test.go`, `cloud_access_reconcile.go`, `user_groups_api.go`,
    `resource_iam_role_grant.go`, `data_source_iam_user_group.go`: rename the mentioned old type
    names in prose/diagnostics to the new ones (grep AC 6 is the catch-all).

## 5. Gate-2 test factory (AC 5a) — one design note for assayer

**SUPERSEDED by assayer's ruling (task #3, frozen): use per-step factories.** My claim below that
plugin-testing lacks per-step factories was WRONG — v1.16.0 has `TestStep.ProtoV5ProviderFactories`
(helper/resource/testing.go:817) and merges per step. Frozen shape: factories on BOTH steps, none at
TestCase level; step 1 = test-only thin-wrapper provider registering ONLY the old name; step 2 =
the real provider, only the new name, config has the `moved` block, `ExpectResourceAction(NoOp)` +
`ExpectEmptyPlan`. ProtoV5 — our provider serves protocol 5. This literally proves Core sends
MoveResourceState when the from-type is absent from the target provider. There is NO fallback test
shape (architect ruling): the both-names provider is rejected by contract AC5a. If step-level
factories fail mechanically, fix the mechanics; if Core refuses the move itself, that IS the Gate-2
result — stop and report. (Original rejected proposal kept for the record:)

## 6. Verification I will run

`make build`, focused `go test` on the new unit/mover tests, `make test-compile`, `make check`,
`make docs && make docs-validate`, `make validate-examples`, AC-6 grep. Acceptance/real-infra runs
and shard-log confirmation split with assayer. Gate-2 resource.Test: unit-harness first, mutation-
proofed v0 case (temporarily invert the transform, assert FAIL, revert, byte-diff clean).

## 7. Contract amendments I'm proposing

1. Unsupported source schema version: implement as nil-schema catch-all mover that ERRORs with the
   version + recovery text (preserves contract wording; feasible per §2). Alternative — skip and
   let Core say not-found — is worse UX; not recommended.
2. Gate-2 both-names test provider note (§5) — flag to assayer in the test plan.
