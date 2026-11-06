# Decision: move the remaining access-control types under `anyscale_iam_`

Status: decided 2026-11-06. Ships in v0.38.0. Follows the v0.37.0 rename of the role binding
resources to `anyscale_iam_role_grant` and `anyscale_iam_role_binding`, which made `anyscale_iam_`
the namespace root for Anyscale access control.

## Decision

Hard swap. Each old type name is removed in the same release its replacement is added. No alias,
no deprecation period, no `moved {}` support.

| Old | New |
|---|---|
| data source `anyscale_role` | data source `anyscale_iam_role` |
| resource `anyscale_user_group` | resource `anyscale_iam_user_group` |
| data source `anyscale_user_group` | data source `anyscale_iam_user_group` |
| data source `anyscale_user_groups` | data source `anyscale_iam_user_groups` |
| resource `anyscale_user_group_members` | resource `anyscale_iam_user_group_members` |

Each type is a pure rename. Schema, attribute names, IDs, import ID formats, validation, CRUD
behavior and schema version are unchanged.

One consequence is removed rather than renamed: the `anyscale_user_group_members` v0→v1 state
upgrader (v0.30.0's `group_id` → `user_group_id`). State written under the old type name cannot
reach `anyscale_iam_user_group_members`; import and create write version 1 directly. The upgrader
and its tests go away with the rename, and the schema stays at version 1 because a schema version
is never lowered.

## Rationale

- **No customer can hold state for these types.** Roles are read behind the role-definition flag
  (`roles_router.py`, `ROLE_DEFINITION_AS_DATA_BUILTIN_ONLY`), the same flag as role bindings. The
  core user-group routes are not behind that flag (they require only MANAGE_IAM), so the backend
  alone does not rule out users; the product owner confirmed user groups are not yet visible to
  customers in production. A deprecation period would protect nobody and add a second type name
  per object to the docs and the provider.
- **The swap has to land before user groups become customer-visible.** After that point the same
  change needs deprecation plus `MoveState` instead.
- **`user_group`, not `group`.** It is the product's word everywhere a practitioner meets it: the
  API (`/api/v2/user_groups`), the CLI (`anyscale user-group`), and `anyscale_iam_role_grant`'s
  `user_group_id` attribute and `user_group` import principal type. `anyscale_iam_group` would make
  the grant's attribute name disagree with the resource it points at.

## Not renamed

- Attributes and values that already use the product's vocabulary: `user_group_id`, the
  `user_group` principal type in `anyscale_iam_role_grant` import IDs, API paths.
- `anyscale_organization_user_role`, `anyscale_organization_user(s)`, `anyscale_cloud_access`:
  they manage organization and cloud membership on the legacy model, not role bindings.
- `anyscale_iam_policy` stays unclaimed until `/api/v2/policy` is traced (see WORKBENCH).

## Consequences

- Breaking for configuration that uses an old name: Terraform reports an unsupported resource or
  data source type. Anyone holding state under an old name runs `terraform state rm` and then
  `terraform import` under the new name; the import IDs are unchanged. Changelog entry only, no
  migration guide.
- The Schema Gate needs the `breaking-change` label and a `release-note:breaking-change` fragment
  on every PR that removes a type name.

## Alternative rejected

Deprecate the old names and support `moved {}` through `ResourceWithMoveState` (Terraform 1.8+,
within the 1.10 floor). It is the right design once a type has users; here it would carry two
names per object for a release cycle with no one to migrate.

## Acceptance criteria

1. Each new name passes the existing tests for its old name, renamed; import ID formats unchanged.
2. A unit test asserts the old type names are absent from the provider's resources and data sources.
3. Renamed acceptance tests match the CI shard regex and are seen running in the shard log.
4. User-group sweeping still works (`make sweep-dry-run`).
5. `git grep -nP 'anyscale_(role|user_group|user_group_members|user_groups)\b' -- . ':!CHANGELOG.md'
   ':!.changelog' ':!docs/decisions' ':!docs/deferred'` finds no reference
   to an old type outside `CHANGELOG.md`, `.changelog/`, `docs/decisions/` and `docs/deferred/`
   (historical records keep the names they were written with). The only expected hits are the
   removed-name lists in `internal/provider/provider_registry_test.go`, which must name the old
   types. Use `-P`: with `-E`, `\b` matches nothing
   and the check passes vacuously.
6. `make check`, `make docs`, `make docs-validate` and `make validate-examples` pass.
7. All renames ship together in v0.38.0.
