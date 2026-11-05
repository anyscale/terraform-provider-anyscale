# Design: `anyscale_role_binding_policy` resource and data source

**Status: approved for build (2026-11-04).** Base `dfda4f2`. Wire facts are either **measured**
(logged on a premerge converted org with role bindings on, 2026-11-04) or **traced** (read off
product `origin/master` `f00cd95`, not observed live). Each fact says which.

## What it manages

The full set of **direct** role bindings on one scope: an organization, a cloud, or a project. It is
the Terraform form of `anyscale role-binding policy generate` / `apply`. Authoritative: on apply,
any direct binding on the scope that the configuration does not name is revoked. Bindings held on a
parent scope are not part of a scope's policy and are never touched.

Use it when one team owns all access to a scope. Use `anyscale_role_binding` (one grant) when access
to a scope is shared between owners. **Never use both on one scope**, and never pair either with a
CLI `policy apply` on that scope: each side revokes or re-creates the other's bindings on every
apply (measured, R3 below).

Converted organizations only. Requires role bindings to be enabled.

## API

| Call | Use | Fact |
|---|---|---|
| `GET /api/v2/role_bindings/{organization\|cloud\|project}/{id}/policy/generate` | Read | measured |
| `PUT .../policy` | Create, Update, Delete | measured |
| `PUT .../policy?dry_run=true` | plan-time checks | measured |
| `GET /api/v2/role_bindings/{rb_id}` | resolve a frozen binding named in a 409 | measured (lags ~5s after create) |
| `GET /api/v2/userinfo/` | the caller's user id | measured |
| `GET /api/v2/roles/?count=1` | tell "role bindings off" from other 404s | measured |

Body and generate result: `{"roles": [{"role_id", "principals": [{"principal_type": "user"|"user_group", "principal_id"}]}]}`.
Generate carries no binding ids and no frozen flag (measured).

Apply result: `{"added": [binding], "removed": [binding]}`. A dry run's `added` entries carry the key
only, with no id; `removed` entries are full records with ids (measured).

Semantics (traced, consistent with every measurement):

- Comparison is by **fact** (principal, role, scope), never by binding id, and is order-blind. A
  fact already held keeps its id; applying the same policy twice writes nothing.
- One atomic write. A refusal changes nothing.
- A new grant gets `origin = declarative`. A fact revoked and later re-granted gets a **new** id
  (measured).

Responses that matter (all measured unless marked):

| Condition | Status | Detail |
|---|---|---|
| policy omits the caller's own direct binding | 403 | `This policy would revoke your own role binding 'rb_..'. Name it in the policy to keep it.` |
| policy adds the caller as a user principal | 403 | `roles[i].principals[j]: You cannot assign a role to yourself directly. Assign it to a user group you belong to.` |
| unknown role id | 404 | `roles[i].principals[j]: Role not found.` |
| unknown group id | 404 | `roles[i].principals[j]: User group '<id>' not found.` |
| role entry with zero principals | **200, no-op** | silently ignored; the role is simply absent from the next generate |
| scope deleted, nonexistent, or caller lacks manage-IAM | 403 | `You do not manage IAM on <type> '<id>'.` |
| policy would revoke a frozen binding | 409 | `This policy would revoke the frozen role binding 'rb_..'. Name it in the policy to keep it.` (traced; no frozen binding could be produced) |
| policy would leave the org with no owner | 409 | `This change would leave the organization without an Organization owner.` (traced) |
| unconverted org | 409 | (traced: route is `REFUSED_BEFORE_CONVERSION`) |
| role bindings off | 404 `Not found.` | (measured on the CI org, 2026-10-02) |

A scope's creator receives a direct owner binding: `Project owner` on a project, `Cloud owner` on a
cloud, `origin = imperative` (measured).

## Schema

```hcl
resource "anyscale_role_binding_policy" "p" {
  project_id = anyscale_project.p.id     # exactly one of organization_id | cloud_id | project_id

  roles = {
    (data.anyscale_role.project_owner.id) = {
      user_ids = [data.anyscale_user.me.id]
    }
    (data.anyscale_role.project_reader.id) = {
      user_group_ids = [anyscale_user_group.analysts.id]
    }
  }
}
```

| Attribute | Kind | Notes |
|---|---|---|
| `id` | Computed | `<scope_type>/<scope_id>`; UseStateForUnknown |
| `organization_id`, `cloud_id`, `project_id` | Optional, exactly one (ConfigValidator), RequiresReplace | names match `anyscale_role_binding` |
| `roles` | Required, MapNestedAttribute keyed by role id (`rol_...`) | `{}` is valid and means "revoke every direct binding the server allows" |
| `roles[*].user_ids` | Optional Set(String), SizeAtLeast(1) | `usr_...` |
| `roles[*].user_group_ids` | Optional Set(String), SizeAtLeast(1) | `ug_...` |

Per-entry validator: at least one of `user_ids` / `user_group_ids` is set. **Required by measurement:**
the server accepts an empty entry and drops it, so without the validator such a config diffs forever.
Empty sets are rejected rather than equated with null, so null is the only "none" and Read never has
to guess which one the config used.

Why a map keyed by role id: one entry per role is enforced by the type; diffs name the role that
changed; the server's order-blind comparison maps onto map + sets with no ordering to stabilize. A
list would diff on server order; a set of objects shows whole-object churn for a one-member change.
Keys are role ids, not names, because names are display strings that differ per environment
(premerge: `Project reader`); docs steer users to `data.anyscale_role`.

No `principals` list mirroring the wire shape, and no `origin` / binding ids in state: they are
transport details that would churn on every re-grant.

## Lifecycle

**Read.** `generate`, then build the map. A role with no principals cannot appear. A user that
appears in `user_ids` and a group in `user_group_ids` map back exactly; anything else
(an unknown `principal_type`) is an error naming it, not a silent drop.
On 403: GET the scope itself (`/api/v2/projects/{id}`, `/api/v2/clouds/{id}`). If that is also
403/404, follow the provider convention for deleted scopes (`isGoneOrForbidden` +
`addForbiddenTreatedAsDeletedWarning`) and remove from state. If the scope reads fine, error: the
caller no longer manages IAM on it. Never drop state when the scope still exists.
On 404: classify with the roles probe as `anyscale_role_binding` does.

**Create / Update.** One PUT of the full config. No separate create path: Create on a scope that
already holds bindings takes them over. Read back with `generate` and set state from it. If the
result differs from config (it should not), error "provider produced inconsistent result" is the
correct outcome; do not mask it.

**ModifyPlan (create and update).** Runs only when the scope id and every role key and principal id
are known (check each value; never `Plan.Raw.IsFullyKnown()`, which is false for any plan with a
Computed attribute). Then:

1. `PUT ...?dry_run=true` with the planned policy.
2. A refusal becomes a plan **error** carrying the server's text. For the own-binding 403, find the
   caller's direct bindings in `generate` (user principal = the `userinfo` id) and print the exact
   entry to add (`role id` → `user_ids = ["usr_..."]`). Do not GET the named `rb_`: on a scope
   created seconds earlier that GET answers 403 (see consistency, below). For a 404, report the
   unknown role or group, never "role bindings are disabled".
3. On **create**, a non-empty `removed` becomes a plan **warning** naming each grant the first apply
   revokes (principal type, principal id, role id).
4. On update, no warning: the plan diff already shows every change.

When values are unknown (scope or groups created in the same apply), skip the dry run. If the scope
id is unknown on create, warn once: the identity that creates a scope is granted its owner role on
it, so the policy must name that binding or the first apply fails with 403. At apply time the same
403 gets the step-2 treatment.

**Delete.** Revoke everything the server lets Terraform revoke, keep what it refuses, and say so.

1. Desired policy = the caller's own direct bindings on the scope (from `generate`, matching the
   `userinfo` user id). The server refuses to revoke them; keeping them up front avoids a
   guaranteed 403.
2. PUT it. On a frozen-binding 409, GET the named `rb_`, add it to the desired policy, retry. Bound
   at 10 retries; past that, error listing what was kept.
3. On success, if anything was kept, warn naming it.
4. A lockout 409 is an error. Destroy does not work around it.

ModifyPlan on destroy: dry-run the step-1 policy; a refusal becomes a plan error, and `removed`
becomes a warning listing what the destroy revokes. This is the protection for organization scope:
an org-scope policy can hold owner groups, and destroy revokes them. The server stops a zero-owner
org (409), and the plan shows the rest before apply.

**Import.** `<scope_type>/<scope_id>`, `scope_type` in `organization|cloud|project`. Read fills
`roles`. A config naming exactly the scope's current bindings plans no-op.

**Timeouts / consistency.** The API is synchronous and `generate` reads the database the PUT writes,
behind a manage-IAM check on the scope that is visible at once. No `timeouts{}` block, no settle
wait. The exception is `GET /api/v2/role_bindings/{rb_id}`: it checks the binding's own permission
in SpiceDB, which lags a new binding by about 5s (measured: 403 "You may not read role binding
'rb_..'." at +1s to +3s, 200 at +5s). The same text is returned for a revoked id. The policy
resource uses that GET only for a frozen binding, which is never new; use the shared bounded
by-id retry there anyway.

## Data source `anyscale_role_binding_policy`

Same scope selector (exactly one, Required-one-of). `roles` Computed with the resource's shape. Same
403/404 handling, except a 403 is always an error (a data source has no state to drop).

## Other resources

- `anyscale_role_binding` on the same scope: conflict (above). Doc only; the provider cannot see
  across resources.
- `anyscale_cloud_access` and `anyscale_organization_user_role` write legacy stores (separate from
  role bindings, traced 2026-10-02); no interaction with this policy.
- Bindings Anyscale creates on its own (a scope creator's owner binding; roles an inviter picks at
  invitation, applied on acceptance) are direct bindings. A policy owning that scope shows them as
  drift, which the next apply revokes. Name them in config to keep them.

## Compatibility

Additive: a new resource and data source, no state or schema change elsewhere. No new framework
primitive, so the Terraform floor stays 1.10. Changelog: `new-resource` and `new-data-source`.
Mark both Beta in the description, as `anyscale_role_binding` is.

## R2 / R3 findings that shaped this (measured 2026-11-04)

- R2: a CLI policy re-grant gives a fact a new id, and `anyscale_role_binding` read by id, so it
  planned a create and the apply returned 409. Fixed separately (Read matches by fact). The policy
  resource keys by fact from the start and never stores binding ids.
- R3: `anyscale_role_binding` and a policy apply on one scope revoke and re-create each other.

## Acceptance criteria

Mock tests follow the measured wire shapes above verbatim (status, detail text, the `roles[i].principals[j]:`
prefix, dry-run `added` without ids). Test names must match the CI shard regexes
(`^TestAcc[A-Za-z]+Resource`, `^TestAcc[A-Za-z]+DataSource`).

Unit / mock (`resource.Test` against httptest):

1. Exactly one scope; changing it plans replace. An entry with neither set, or an empty set, is a
   plan-time error.
2. Create: one PUT with the full policy; state from `generate`; second plan empty.
3. Create on a scope with existing grants: plan warning naming each grant dry-run says it revokes
   (Gate 2: real `resource.Test` plan, not a unit call of ModifyPlan).
4. Dry-run 403 own binding: plan error naming the role and user to add. Dry-run 404 unknown group:
   plan error naming the group, not "disabled".
5. Unknown scope id on create: no dry-run call is made; warning text present.
6. Update adds and removes principals in place (no replace); PUT body matches.
7. Drift: generate gains a binding → plan removes it; loses one → plan re-adds it.
8. Import `project/<id>` → no-op plan; malformed id → error listing the format.
9. Destroy: own bindings kept, everything else revoked; frozen 409 → GET → retry → success with a
   warning naming what was kept; 11th frozen 409 → error.
10. Destroy plan: dry-run lockout 409 is a plan error.
11. Read 403 + scope 403 → removed with warning; Read 403 + scope 200 → error, state kept.
12. Roles probe 404 → the role-bindings-disabled error; 409 unconverted → error naming conversion.
13. Data source reads the same shape; a 403 is an error.

Each mutation-proofed: break the behavior, watch the named test fail, revert.

Real API (premerge, converted org; gated like the other RBAC real-API tests), on a tfacc project and
tfacc group:

14. Create with own owner binding + group → generate equals config; re-plan empty.
15. Out-of-band PUT adds a binding → plan shows its removal; apply; generate equals config.
16. Import → no-op plan.
17. Destroy → generate shows only the caller's own binding.

Not testable for real: the frozen path (no frozen binding can be produced on premerge) and lockout
(would need to strip org owners). Both stay mock-only, labeled as built from traced text.

## Open

- Frozen refusal and lockout texts are traced, not observed. Re-check if a frozen binding ever
  appears in a test org.
- Whether production built-in role names match premerge affects examples only.
