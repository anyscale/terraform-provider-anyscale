# Deferred: `anyscale_user_group` / `anyscale_resource_policy` (groups + policy write surface)

**Status: fully researched against live source and a live org, deliberately not built.**

This is not a stalled effort and there is no half-finished code to pick back up — the decision
this time was made at the *design* stage, before any resource code was written. What exists is
the research: a real backend/schema trace plus one live confirmation, both expensive to redo.
This document exists so nobody has to redo them.

## Why this is deferred

The user's original ask (2026-07-27, RBAC/organization_user redesign) included an ideal-world
`anyscale_group` resource: users belong to groups, groups get permissions. Anyscale's API turned
out to already have exactly that shape — `POST/GET/PATCH/DELETE /user_groups/` for the group
object, and a uniform, declarative, full-replace policy API (`PUT /policy/{resource_type}/{id}`,
`resource_type` ∈ `cloud | project | organization`) for granting a group permissions at all three
levels. On paper this is the closest thing in the whole Anyscale API to a Terraform-native design.

The user confirmed deferring the write surface on 2026-07-27 ("skip for now"), after reviewing the
findings below in summary form. It is not being built this pass because five independent findings,
taken together, mean a Terraform resource here would be either non-functional, silently unsafe, or
both:

1. **The group path cannot express the roles this redesign was prompted by (decisive).** The
   policy API's role vocabulary is per resource type and narrower than the per-user APIs:
   `policy cloud: write | readonly`, `policy project: owner | write | readonly`,
   `policy organization: owner | collaborator`. None of the Phase 2B roles —
   `project_viewer`, `compute_config_viewer`, `workload_operator` — are assignable to a group at
   all. The group/policy path and the new per-user-roles path are disjoint. A group cannot be
   granted the very roles the user asked to model. Whoever revisits this should treat "can the
   policy API express role X" as a per-role question, not assume parity with the per-user API.

2. **Group membership has no write path anywhere in the backend, not just the public API.**
   `POST /user_groups/` creates a group with a name only. The only production code path that ever
   inserts a row into `user_group_memberships` is the WorkOS directory-sync event poller
   (`workos_users_reconciler` → `_handle_group_user_added` → `create_user_group_membership`) — a
   background job reacting to an external identity provider's webhook, not anything an API caller
   can invoke. The DAO's batch member-write helpers exist in code but are dead — referenced only
   from tests. Anyscale is not itself a SCIM service provider; WorkOS is, and Anyscale polls it.
   There is no `/scim/v2/Groups` endpoint to reach for as an alternative. A Terraform resource with
   a `members` argument would be a state-only fiction: it would appear to work, diverge silently
   from the real directory on the next sync, and Anyscale would never see what Terraform thinks
   membership is. `members` may only ever be `Computed`, sourced from
   `GET /user_groups/memberships/list`.

3. **Group destroy is provably unclean.** `delete_user_group` soft-deletes the group and its
   memberships but never touches `resource_permissions` — the group's role bindings survive the
   delete permanently. Worse, the binding-merge helper on every subsequent policy write carries
   every existing principal forward except the one actually being changed, so a later role grant
   to a *different* group silently re-writes the dead group's id back into the policy, forever.
   The dangling binding is invisible through the API — `GET /user_groups/{group_id}` 404s once the
   group is deleted, while its binding persists — and there is no way to clean it up after the
   fact: an explicit policy `PUT` that still includes the dead id is rejected as referencing a
   nonexistent principal. A create → grant → destroy → recreate cycle leaves permanent residue,
   and the recreated group gets a new id, so the old binding never resolves to anything again. A
   resource whose `Delete` provably leaks backend state is not shippable without either a backend
   fix or a very loudly documented, user-accepted caveat — this pass did neither.

4. **Directory-synced groups are unprotected and the provider cannot even detect them.**
   `PATCH` (rename) and `DELETE` have no guard of any kind against operating on an IdP-synced
   group — nothing stops Terraform from renaming or deleting a group the customer's directory
   owns, only to have the next sync fight it. And the provider has no way to tell the two apart:
   the `UserGroup` model returned by every `user_groups` route omits the WorkOS-origin columns
   entirely; the one place a `created_by_type: "scim"` marker is actually exposed is a completely
   different, unrelated endpoint (`POST /organization_user_groups_collaborators/search`). A safe
   implementation would have to consult a second endpoint before every destructive call just to
   know whether the destructive call is safe. For contrast: the existing org-collaborator surface
   already guards this exact case (a 409 pointing at directory sync); the user_groups router has
   no equivalent today.

5. **No other Anyscale client writes groups today.** The Anyscale CLI's user-group surface is
   read-only (`list`, `get`, `membership list`); there is no CLI create/delete/rename/set-roles,
   and the public SDK mirrors that. Under this repo's own "expose a surface only for a real
   end-user consumption path" rule, a *managed* group resource would put this provider ahead of
   every other Anyscale client, with no precedent anywhere else that the write shape is even
   correct in practice.

**Empirical, not just theoretical — checked live, 2026-07-27:** the one real org this session's
token could reach had **zero user groups**. That is the identical precondition that made the
acceptance tests for this repo's *first* attempt at this surface silently skip
(`anyscale_user_group`, `anyscale_user_groups`, `anyscale_policy_binding`, removed 2026-07-10 in
PR #85, `38b8420` — "wrapped an Anyscale SCIM provisioning feature that has never been enabled
behind its backend feature flag" with acceptance tests that "silently skipped for lack of a real
SCIM-synced user group"). Building the write surface again now would very likely re-ship the same
untestable surface for the same reason it was removed the first time. **Whoever revisits this
should check group count in the target test org first** — if it is still zero, the answer is
still no, regardless of how the other four findings above have changed.

Finally, and separately from all of the above: the policy-write endpoint itself
(`PUT /policy/{resource_type}/{id}`) is gated by a flag (`enable-new-policy-api-access`) whose own
docstring states its purpose plainly — *"we block access to the policy api to all users who have
not already used the policy api. This effectively disables all practical uses of SCIM."* That is a
deliberate lock-down, not an incomplete rollout in progress. A live probe against this session's
org returned exactly the 403 that docstring predicts. Do not treat "the endpoints exist in the
OpenAPI spec" as evidence the feature is reachable by a normal customer org.

## What this does NOT block

The **read-only** half of this investigation is not deferred and should not be confused with the
parts above:

- **`anyscale_user_group` / `anyscale_user_groups` data sources** (read-only: `list`, `get`,
  `membership list`) are a low-risk, independent addition — the exact same read paths the Anyscale
  CLI itself uses, so there is a demonstrated consumption path (finding 5 above is about *writes*,
  not reads). These make `anyscale_user.user_group_ids` (an existing, previously-unresolvable
  opaque id list) actually useful, and give anyone who loses the `user_group_ids` attribute in the
  D6 fix (see the main design record for this quest, if it still exists at the time you are
  reading this) a real replacement instead of a dead end.
- **The org/cloud/project per-user role resources** (the rest of this redesign — the
  `organization_user` rename and `anyscale_cloud_user_role`) are unrelated to this deferral. They
  model the existing per-user RBAC surface directly and do not depend on anything above.

## What already exists (do not redo this)

No code. What exists is the research, all captured above and cited from primary sources at the
time this was written:

- The full request/response shapes for `user_groups` CRUD and `policy/{resource_type}/{id}`,
  traced against the live OpenAPI spec.
- The backend source trace behind findings 2–4 above (`user_groups_dao.py`, the WorkOS reconciler
  call chain, `delete_user_group`, the policy binding-merge helper, the `UserGroup` response
  model's omitted WorkOS columns).
- The SpiceDB schema finding, which is the deepest thing this investigation surfaced and is not
  written down anywhere else in this repository: `go/infra/iam/spicedb/schema.zed` defines
  `role_binding` with `relation bound_user: user_group#member` — **groups only**. An individual
  user cannot be bound to a role at Anyscale at all, at any level; every per-user permission API
  (org collaborator, cloud roles, project roles) is a facade in front of a system-managed,
  per-user `UserGroupType` bucket that the backend creates and maintains for you. This is *why*
  the policy API accepts only group principals (finding 1's narrower vocabulary aside) — it is not
  an arbitrary restriction, it is the one principal type the backend's authorization model has.
  The group-shaped model the user originally asked for is, structurally, the model Anyscale
  actually runs on internally. The entire gap discussed in this document is that the *customer-
  facing* half of that model — who is in a group — is owned by the directory sync integration, not
  by any API a Terraform provider can call.
- One live, empirical result (not just source-level): the target org's user-group count (zero),
  and a live 403 from the policy-write endpoint matching its gating flag's stated intent.

## Two API asks that would change this calculus

Both were drafted this session (as a single, external, one-page request; not preserved in this
repository, since it is meant to be sent rather than kept) and both remain open asks, not
confirmed roadmap:

1. **A group add-member and remove-member endpoint.** This is the one gap that actually matters —
   everything about groups and policy is otherwise real, first-class, and already API-manageable.
   The argument for asking: Anyscale's own authorization model is already group-first (see the
   SpiceDB finding above) and its policy API already accepts only group principals — so this is
   not a request to add a new concept, it is a request for the one missing write that would let a
   caller populate a concept the backend already has everywhere else.
2. **Folded into the same ask, two smaller and cheaper fixes if anyone is already touching this
   backend surface:** cascade role-binding revocation into group delete (closes finding 3, the
   permanent dangling-binding leak), and a way for a client to tell a directory-synced group apart
   from a manually-created one (closes finding 4's detection gap, whether or not delete/rename
   protection is added alongside it).

## Revisiting this

In order, before writing any resource code:

1. **Check live group count in the target org again.** If it is still zero, stop — the acceptance
   test problem that killed this surface once (PR #85) and again in this pass has not changed, and
   no amount of code quality on the resource side fixes an empty test fixture.

2. **Check whether finding 1 (role-vocabulary parity) has changed** — i.e., whether the policy
   API's per-resource-type role lists have grown to include the roles this redesign actually
   wants to model. If the group path still cannot express `project_viewer` /
   `compute_config_viewer` / `workload_operator`, groups are not a substitute for the per-user
   resources regardless of anything else on this list.
3. **Check whether either API ask above has landed** — an add-member endpoint in particular is a
   precondition for a `members` argument to ever be anything other than `Computed`.
4. **Re-verify findings 3 and 4 (destroy leak, sync-protection gap) against current source** —
   these are backend behaviors this document did not get fixed, not backend behaviors this
   document expects to have silently changed.
5. Only then design the resource(s) — at that point, `PUT /policy/{resource_type}/{id}`'s
   authoritative-full-replace shape (§5.2 of this quest's design record, if still available) is
   still the right resource shape for the policy half: one resource owning the whole binding set
   for a given `resource_type` + `resource_id`, never an additive per-binding variant, since the
   API itself cannot do additive and faking it would mean read-modify-write races between
   concurrent applies.
