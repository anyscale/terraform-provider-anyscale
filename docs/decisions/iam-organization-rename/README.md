# Decision: move organization membership under `anyscale_iam_`

Status: decided 2026-11-09. Ships in v0.39.0.
Follows `docs/decisions/iam-namespace-rename` (v0.38.0).

## Decision

Rename the organization membership types into the `anyscale_iam_` namespace. Old type names are
removed in the same release (no alias, no deprecation period).

| Old | New |
|---|---|
| resource `anyscale_organization_user` | resource `anyscale_iam_organization_user` |
| resource `anyscale_organization_invitation` | resource `anyscale_iam_organization_invitation` |
| data source `anyscale_organization_user` | data source `anyscale_iam_organization_user` |
| data source `anyscale_organization_users` | data source `anyscale_iam_organization_users` |

Each type is a pure rename: schema, attribute names, IDs, import ID formats (`organization_user`:
email; `organization_invitation`: invitation ID), validation, CRUD behavior and schema version are
unchanged.

## Rationale

- **They are IAM by the namespace rule.** Org member add/remove and invitation create/invalidate
  require MANAGE_IAM in SpiceDB (`organization_collaborators_router.py`,
  `organization_invitations_router.py`), the same permission behind user groups and role bindings.
  The test is the permission, not whether the surface "feels like" membership or grants.
- **Now, not later.** Adoption is 0-2 customers and expected to grow within about a month. The user
  ruled hard swap, soon (2026-11-06).
- **`organization`, not `org`.** The provider never abbreviates it (`anyscale_organization`,
  `organization_id`, `anyscale_organization_default_cloud`); a prefix-only change is the easiest
  upgrade to explain.

## Upgrade path: hard swap + `MoveState`

Unlike v0.38.0, these types have real users. The old names are still removed, but both new
resources implement `ResourceWithMoveState` so an existing user upgrades with a `moved` block
instead of `state rm` + `import`:

```hcl
moved {
  from = anyscale_organization_user.alice
  to   = anyscale_iam_organization_user.alice
}
```

`moved` across resource types needs Terraform 1.8; the provider floor is 1.10, so no floor change.

### `MoveState` contract

- Accept only `SourceTypeName` equal to the old type name of the same resource, and a
  `SourceProviderAddress` whose last segment (the provider type) is exactly `anyscale`, whatever the
  host and namespace. Production sends `registry.terraform.io/anyscale/anyscale`; the acceptance-test
  harness sends `registry.terraform.io/hashicorp/anyscale`, and pinning the namespace would make the
  Gate 2 test exercise a different gate than production. Any other source: return without setting
  state, so Core reports its own "unsupported move" error.
- Framework v1.19 `StateMover` has only `SourceSchema` and the mover function; all type, address and
  version matching happens inside the function, on the request.
- `anyscale_organization_user` sources:
  - `SourceSchemaVersion` 1: state copies over unchanged.
  - `SourceSchemaVersion` 0 (pre-v0.25.0, `id` = identity_id): apply the existing v0→v1 transform
    (`id` = `email`; `identity_id` and `reinvite_if_expired` follow the upgrader's existing rules).
    The transform is shared with `UpgradeState`, not duplicated.
  - Any other version: error diagnostic naming the version and the recovery
    (`terraform state rm` + `terraform import <email>`). Implemented as a last `StateMover` with
    `SourceSchema: nil`, gated on type name and provider address only, reading `SourceRawState`.
- Every mover checks `SourceTypeName`, `SourceProviderAddress` and `SourceSchemaVersion` before
  touching `SourceState`: the framework calls a mover even when its `SourceSchema` failed to decode
  the source. A mover that does not match returns no state and no diagnostics, so the next mover is
  tried; if none matches, Core reports that no move is implemented.
- `anyscale_organization_invitation` sources: version 0 (its only version) copies unchanged; any
  other version errors as above.
- Copy `SourcePrivate` to `TargetPrivate` when present. Not load-bearing: `organization_user`'s
  Read self-heals the membership record, so a move without private state must still plan clean.
- The move performs no API call. The next refresh reads the object under the new type.
- `UpgradeState` on `anyscale_iam_organization_user` stays (schema `Version: 1`): it is unreachable
  for state written under the new name, but harmless, and its v0 transform is the one `MoveState`
  reuses.
- Data sources hold no state; renaming them in config is the whole migration.

## Not renamed

- `anyscale_organization_user_role`, `anyscale_cloud_access`: legacy RBAC, deprecated for converted
  orgs; their path is removal, not renaming. Consequence accepted: `anyscale_organization_user_role`
  (legacy) and `anyscale_iam_organization_user` (current) no longer share a prefix. That is accurate
  — they belong to different models.
- `anyscale_user`, `anyscale_organization`: connection-level singletons, not access control.
- `anyscale_organization_default_cloud`: an organization setting, not access control.
- `anyscale_iam_policy`: stays unclaimed until `/api/v2/policy` is traced (WORKBENCH).

## User-facing text that names the old types (must change)

Diagnostics and descriptions that tell a practitioner which type to use, e.g.
`cloud_access_reconcile.go` ("Add them first - anyscale_organization_user manages membership..."),
`user_groups_api.go` ("Invite them first, for example with anyscale_organization_invitation"),
`resource_iam_role_grant.go` `user_id` description, `resource_organization_user_role.go`
("Membership is managed by ..."), `data_source_iam_user_group.go`, and both resources' own
cross-references. Verify with the grep in acceptance criterion 6, not this list.

## Decisions (user, 2026-11-09)

1. Names: `anyscale_iam_organization_*`, unabbreviated.
2. Upgrade path: hard swap + `MoveState`, with a `moved` example. The user expects no real users of
   these types; the example is to be removed later (WORKBENCH: "Remove the org-membership moved{}
   migration example").
3. Migration guide: none. The changelog's `moved` instructions are the whole migration.

## Acceptance criteria

1. Each new name passes the existing tests for its old name, renamed; import ID formats unchanged.
2. A unit test asserts the four old type names are absent from the provider's resources and data
   sources (`provider_registry_test.go` removed-name lists).
3. Renamed acceptance tests match the CI shard regex (`^TestAcc[A-Za-z]+Resource` /
   `^TestAcc[A-Za-z]+DataSource`) and are seen RUNNING in the shard log, not skipped.
4. Invitation sweeping still works (`make sweep-dry-run`); sweeper keys and `Dependencies` renamed.
5. `MoveState` (if chosen):
   a. Gate 2 — a real `resource.Test` with provider factories on each `TestStep`
      (`ProtoV5ProviderFactories`; the provider serves protocol 5) and none on the `TestCase`, which
      plugin-testing requires once any step declares its own. Step 1's provider registers only a
      test wrapper that embeds the real resource and overrides `Metadata` to the OLD type name, and
      creates the object. Step 2's provider is the real one (old name absent) with a `moved` block,
      asserting `plancheck.ExpectResourceAction(new, NoOp)` and an empty plan after apply. This
      proves Core accepts a move from a type the target provider does not register. Once for each
      resource. A single provider registering both names does not prove this and is not acceptable.
      Step 1 writes membership private state, so this path moves with `SourcePrivate` present.
      Mechanism confirmed by execution before the build (a throwaway spike test): Core sends
      `MoveResourceState` for a source type the step-2 provider does not register, and the move
      plans as a no-op.
   b. Unit tests on `MoveState` for: v1 source; v0 source (raw v0 JSON fixture with `id` =
      identity_id, asserts `id` = email); wrong type name (no state set); wrong provider address
      (no state set); unsupported version (error). Mutation-proof the v0 case.
   c. A moved `organization_user` with no source private state plans clean (unit test on
      `MoveState` with nil `SourcePrivate`, plus Read self-heal on the mock refresh).
6. `git grep -nP 'anyscale_organization_(user|users|invitation)\b' -- . ':!CHANGELOG.md'
   ':!.changelog' ':!docs/decisions' ':!docs/deferred' ':!tools/acctest-rerun/testdata'
   ':!tools/changelog-build/fragment_test.go'` (that test quotes a historical fragment verbatim) finds only:
   the removed-name lists in `provider_registry_test.go`; the `MoveState` source-type constants; the
   `MoveState` unit tests; the `moved` acceptance tests (old-name wrapper `Metadata` and `moved`
   `from` addresses); and the upgrader's history comment. Any hit outside those files is a miss. Use `-P` (with `-E`, `\b` matches nothing).
   `anyscale_organization_user_role` must still match where it did — `\b` does not stop at `_role`,
   so read the hits, do not just count them.
7. `make check`, `make docs`, `make docs-validate`, `make validate-examples` pass; examples move to
   `examples/resources/anyscale_iam_organization_*/` and `examples/data-sources/...`.
8. One PR, `breaking-change` label, one `release-note:breaking-change` fragment per removed type
   name, each with the `moved` example (or state rm/import steps under the fallback). Branch cut
   from `b38abb9` or later. Ships in v0.39.0 (minor). The `moved` example appears only in the two
   resource fragments; the two data source fragments say to rename the type in configuration.
9. Docs: examples, schema descriptions and `templates/guides/rbac.md` / `kitchen-sink-tour.md` use
   the new names; `make docs` leaves no page under an old name. Guides describe current behavior
   and do not narrate the rename; the changelog and the Registry version picker carry that.

## Alternatives rejected

- **Deprecate old names for a release, then remove.** The user ruled hard swap; `MoveState` gives
  users the clean path without carrying two names per object.
- **Plain hard swap (state rm + import).** Workable, but on `organization_user` a botched re-import
  (wrong email, forgotten resource) next to a config that still declares the member can plan a
  fresh invitation. A `moved` block cannot get the ID wrong.
- **Bundle `organization_user_role` / `cloud_access`.** Removal path, separate decision.
