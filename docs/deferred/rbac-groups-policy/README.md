# User groups: what shipped (Alpha) and what is still deferred

**Status: groups, membership, and per-group role bindings ship as Alpha. Group organization roles and an
authoritative whole-set policy resource remain deferred.** This document records the reasoning so the deferred half is not re-researched from
scratch. User-facing behavior is in the "User groups (Alpha)" and "Role bindings (Alpha)" sections of `templates/guides/rbac.md`.

## Shipped (Alpha)

`anyscale_user_group` (create, rename, delete), `anyscale_user_group_members` (authoritative member
list keyed by email), and the `anyscale_user_group` / `anyscale_user_groups` data sources, over
`/api/v2/user_groups`. Both the Anyscale API and the provider schema may change before Beta.

Two earlier blockers no longer apply:

- **Membership has a write path.** `POST|DELETE /user_groups/{id}/members` exist and are idempotent.
  Membership is no longer written only by the directory-sync poller, which is what made a `members`
  argument a state-only fiction.
- **Synced groups are detectable.** The group model carries a `source` marker (`scim`, `user`, or
  absent for groups created before April 2026), so the provider can refuse to manage directory-owned
  groups instead of fighting the identity provider.

The first attempt at this surface (`anyscale_user_group`, `anyscale_policy_binding`, removed in
PR #85) failed because its acceptance tests silently skipped for lack of a SCIM-synced group. With a
member write path, tests create their own groups, so that failure mode no longer applies.

## Still deferred

1. **Group organization roles** (`PUT /user_groups/{id}/roles`). There is no "clear roles" route:
   `base_role` is required, so removing the block from configuration could only write
   `collaborator, []`, and whether that equals "no grant" is unverified (additional roles also carry
   deny semantics). The first write also creates an organization-level permission row, which opens the
   policy API for the whole organization. That side effect needs an explicit product decision.
2. **An authoritative per-resource policy resource** (`PUT /policy/{resource_type}/{id}`). The
   endpoint is gated by `enable-new-policy-api-access`, and its role vocabulary is narrower than the
   role-bindings API (cloud `write|readonly`, project `owner|write|readonly`, organization
   `owner|collaborator`). Its full-replace authority would also collide with `anyscale_cloud_access`,
   which already refuses to fight group bindings. Group grants ship instead as granular, immutable
   `anyscale_role_binding` resources (Alpha, behind a feature flag), which can grant any built-in or
   organization role (where the newer cloud roles `project_viewer`, `compute_config_viewer`, and
   `workload_operator` are expected to live; not yet observed) and never own a resource's whole
   binding set. If a whole-set resource is ever wanted, it must own every direct binding per
   `resource_type` + `resource_id`; the API cannot do additive writes through that route, and faking
   it means read-modify-write races.
3. **A per-member group resource.** Deliberately not shipped alongside the authoritative one: two
   authority models over one set is the mistake that removed `anyscale_cloud_user_role`.

## Backend behavior the Alpha design works around

- **Group delete does not revoke what the group's membership derived.** It soft-deletes the group and
  its memberships but does not touch the role bindings the group wrote, and a later policy write can
  carry the dead group's id forward. `anyscale_user_group` therefore empties the group before deleting
  it. That a removed member actually loses access is not yet confirmed by a real write probe.
- **Deleting a directory-synced group is not guarded by the API** (rename is, with a 409). The provider
  guards it by refusing to manage `scim` groups.
- **`/memberships/list` silently drops departed users** and is unpaginated; the members resource
  re-reads the whole organization's memberships on every refresh.
- **Duplicate group names** return a 409 on create. The provider surfaces it with a hint to import
  the existing group, and does not retry.

## Why membership is keyed by email

Member IDs on this API are `usr_...`, which is `user_id` on `anyscale_organization_user(s)` and not
the `ide_...` `id`. The resource takes emails, the key every other RBAC resource here uses, and
resolves them through the existing organization-user lookup.

## Underlying model

SpiceDB's `role_binding` binds only `user_group#member`: an individual user cannot hold a role at any
level. Every per-user permission API is a facade over a system-managed per-user group. This is why the
policy API accepts only group principals, and why exposing real, customer-managed groups is the
structurally natural model. The provider never talks to SpiceDB; Postgres is the system of record and
API reads are immediate.

## Revisiting the deferred half

1. Check whether a clear-roles route exists and whether `{collaborator, []}` is equivalent to no grant.
2. Check whether the `PUT /policy` gate has been lifted for ordinary organizations and whether its
   role vocabulary now matches the role-bindings API; only then is a whole-set resource worth
   considering.
3. Re-verify the delete behavior above against current source; it is a backend behavior the provider
   works around, not one it fixed.
