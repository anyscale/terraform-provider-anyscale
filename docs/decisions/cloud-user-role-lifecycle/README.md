# Design record: `anyscale_cloud_user_role`'s lifecycle

**Status: shipped** (PR #222, squash-merged as `4b2a8a2`, released in v0.24.0). This is the
companion to `docs/deferred/rbac-groups-policy/README.md` — same investigation, opposite outcome.
That document preserves why a piece of this redesign was **not** built. This one preserves why the
piece that **was** built looks the way it does, because the code comments explain what it does, not
why the simpler, more obvious version does not work.

## Why Create is two ordered API calls, not one

There is no Anyscale endpoint that both grants an RBAC role and establishes the underlying
cloud membership a later `destroy` needs in order to revoke it. Two separate APIs exist, and
between them they cover the full lifecycle but neither covers it alone:

- The **new roles API** (`PUT /clouds/{cloud_id}/collaborators/users/{user_id}/roles`) can grant
  and read a role, but has **no delete**.
- The **legacy collaborator API** (`POST`/`PUT`/`DELETE /clouds/{cloud_id}/collaborators/...`)
  can create, update, and delete a plain collaborator, but knows nothing about the new roles.

So `Create` calls both, in order: the legacy `POST` first (establishing the membership edge
`Delete` will need), then the roles `PUT` (setting the role Terraform actually asked for). `Update`
touches only the second call. `Delete` uses the legacy endpoint, since that is the only one that
has a delete at all. This is forced by the API shape, not a design preference.

**The order is load-bearing.** The legacy `POST` writes the user's managed-group membership with
SET semantics — if it ran *after* the roles `PUT`, it would silently clobber the role that call
just set. There is no version of this resource that could safely run the calls in the other order
or combine them into one.

## Why the bootstrap call must be unconditional, not conditional

The first version of this design (the original draft, not the implementation that shipped) said:
skip the legacy bootstrap `POST` when the cloud collaborator search already shows the
person as a collaborator, since the call would be redundant. **This was wrong, and it was the
single most consequential mistake in this whole design** — live-confirmed, not just
source-traced:

- The legacy `POST` returns **409** if the target already has *any* permissions row for that
  cloud — not just if they have a membership edge.
- A user granted a role through the roles `PUT` **alone** (no bootstrap ever run) ends up with a
  permissions row but **no membership edge**. That is exactly the shape the conditional-skip logic
  would have produced on its *second* invocation against the same person, or on anyone who reached
  that state some other way.
- That state is a trap: the bootstrap `POST` now 409s (there is a permissions row), and the legacy
  `DELETE` now 404s (there is no membership edge). **There is no API sequence that repairs it.**
  This is the one state — permissions row yes, membership edge no — with no way out.

The fix is not clever: `Create` must attempt the bootstrap call **every time, unconditionally**,
never skip it based on what a search appears to show. Skipping it is exactly what walks a user into
the unrecoverable state. The 409 the bootstrap call returns when the person is already a genuine
collaborator is expected and handled as success, not as a reason the call was unnecessary to make.

## Why `import` deliberately does not attempt detection

The natural instinct, once the trap above is known, is to have `import` check for it and refuse:
if someone is being imported with a permissions row but no membership edge, tell them up front
instead of letting a later `destroy` fail as a surprise. **This was the actual design for a period,
and it was withdrawn after being live-tested and found not to be buildable — not because it was a
bad idea, but because there is no way to implement it.**

This was tested directly: a role was granted through the roles `PUT` alone, with no bootstrap call
anywhere in the sequence, on a fresh disposable cloud. The cloud collaborator search — the only
plausible read-only probe — **returned the user anyway**, as a full, ordinary-looking record
(identity id, user id, email, `permission_level: write`), indistinguishable from someone who had
gone through the real bootstrap flow. The search reflects the *permissions row*, not the *membership
edge* — and a roles-only grant writes the former without ever touching the latter. There is no other
candidate read: the legacy `DELETE` itself distinguishes the two states, but only by performing the
destructive act, which is not something a `Read` or an `Import` may do speculatively.

So `import` proceeds normally, always. There is nothing to check. The condition can only ever
surface later, at `destroy` time, which is the next section.

**One correction worth preserving alongside this:** the original reasoning also claimed this state
could *only* arrive through `import`, never through this resource's own `Create` — reasoning being
"`Create` always bootstraps first, so `Create` itself can never produce this." That claim was
**false**, and it shipped into the first draft of the `Delete` diagnostic and the resource's own
schema description before being caught in review. The real path: someone already has a permissions
row from an out-of-band grant (roles `PUT` used directly, or by a script, outside Terraform); a
practitioner then writes this resource for them; `Create`'s mandatory
bootstrap call 409s (same shape as a genuine pre-existing collaborator — the API cannot tell the
two apart either); `Create` correctly treats the 409 as "already a member" and proceeds; the role
grant succeeds; the resource is created directly into the undestroyable state. **`Create` cannot
detect this either — it is the identical problem `import` has — and proceeding past the 409 is
still the right behavior**, since the alternative would be refusing every legitimate pre-existing
collaborator. Only the claim that this "can only happen via import" was wrong; the behavior itself
was always correct. Anywhere this resource's docs or diagnostics describe this condition, they now
say it can arise through `Create` layered on a pre-existing out-of-band grant, not only through
`import` — if you are editing this resource and see the older, narrower claim reappear, it is a
regression of this exact mistake.

## Why `destroy` carries the full explanation instead of the raw API error

Since neither `Create` nor `import` can detect or prevent the state above, the failed `destroy` —
the legacy `DELETE` returning 404 — is the **only** place it can ever surface, and by then the
practitioner is already confused because a destroy they expected to work did not. The diagnostic
therefore does not pass the raw `"user ... is not a member of clouds ..."` API error through. It
explains, plainly: that this assignment was granted outside Terraform without the membership
record the revoke path requires; that no API sequence can repair it after the fact; and that
`terraform state rm` is the only clean exit, because retrying `destroy` will never succeed. A
diagnostic that only reports the raw error sends someone looking for a way to fix something that
cannot be fixed; this one tells them the actual, only available next step.

## Why the two `destroy`-time failures are ordered, and why that matters

A cloud role assignment can fail to destroy for two different reasons, and they are not siblings —
they are ordered, because both live inside the same backend validation function
(`_validate_cloud_removal`) and one branch runs before the other ever executes:

1. **The missing-membership-edge check runs first** — the trap above, a permissions row with no
   membership edge — and it returns its 404 before the function ever reaches the second check.
2. **The `auto_add_user` check runs second**, and only matters if the first check passes — i.e.,
   only for a role assignment that *was* properly bootstrapped. If a cloud has `auto_add_user`
   enabled, `DELETE` returns a **409** (`Users cannot be removed from clouds which have auto add
   users enabled.`) instead, because the backend's async reconciler refuses to leave any user
   without cloud access while that setting is on.

Practical consequence: this resource's `Delete` handles the 404 case first, since it is both the
earlier branch and the more likely one — and because the two never compete for the same request,
handling them in the wrong order would make the 409 branch's diagnostic unreachable for exactly the
people who hit it. This ordering also **narrows the cost of not live-testing `auto_add_user`
directly** (see below): the 409 is a second-order path, reachable only by a correctly-bootstrapped
resource on an `auto_add_user` cloud, not the more general hazard it first appeared to be.

## Why `auto_add_user` was documented and diagnosed, but never live-tested

`anyscale_cloud.auto_add_user` — an attribute this provider already exposed before this
redesign — auto-enrolls every org member as a cloud collaborator through a background reconciler
when enabled. Confirming its 409-on-destroy interaction live would have required enabling it on a
real cloud in a shared internal org containing roughly ninety other real people's accounts,
handing out real, unrequested cloud access as a side effect of a test. That trade was declined
deliberately: the thing being confirmed (an exact, already twice-source-traced error message) was
low-value against a real cost (unrequested access for real people outside this work). The
interaction is documented on both `anyscale_cloud` and `anyscale_cloud_user_role`, and the ordering
finding above is what makes this an acceptable place to stop rather than a corner cut.

## Why the resource keys on `email`, not `user_id`

The original example in this design used `user_id` as the resource's primary identifier. That was
revised — surfaced while reviewing the docs draft, before any user-facing schema shipped — because
this resource's lifecycle genuinely spans three different identifiers and no single one covers all
of it:

| Operation | Identifier it needs |
|---|---|
| Create step 1 — legacy `POST .../collaborators/users` | `email` (the request body has no `user_id` form at all) |
| Create step 2 / Read — roles `PUT` / `GET` | `user_id` |
| Delete — legacy `DELETE` | `identity_id` |

`email` was chosen as the one the practitioner supplies, for three reasons: it is the only one of
the three that reliably derives the other two (searching by email returns both IDs on one record;
the reverse direction is unreliable, since `user_id` is an optional field on the org-wide search
response and can legitimately come back empty); it matches this repo's own in-repo precedent
(`anyscale_project`'s collaborator block already keys and diffs on `email`, and keying this
resource differently would reintroduce exactly the cross-resource inconsistency this redesign
exists to remove); and this repo's usual "resources take IDs, data sources resolve names" rule does
not actually apply here, since that rule exists to avoid ambiguous *display-name* resolution (names
collide and change), and an org email is a unique, stable identifier for a principal, not a display
name. `user_id` and `identity_id` are both `Computed` outputs only, surfaced for debugging and for
the destroy diagnostic above, never practitioner inputs — the same identity-hiding shape
`anyscale_organization_user` already uses (its resource ID is the identity ID, while its role
hydration is keyed by user ID internally). Import is therefore `cloud_id/email`: importing by
`user_id` cannot reliably populate the required `email` field, which would make the very next
`plan` show a diff and violate this repo's no-op-import contract.

## Simpler designs that were tried and rejected

For anyone tempted to simplify this resource later, these were all considered, in this order, and
none of them work:

1. **A single API call for Create.** Does not exist. See "why Create is two calls," above — this
   is a hard API gap, not an implementation choice.
2. **A conditional bootstrap** (skip the legacy `POST` if the person already looks like a
   collaborator). This was the actual first design. It produces the unrecoverable
   permissions-row-but-no-edge state on exactly the users it was meant to optimize for. Rejected
   after live confirmation; see "why the bootstrap must be unconditional," above.
3. **Import detects and refuses the unrecoverable state.** This was the actual design for a period,
   reversed after a live test proved no read-only signal distinguishes the bad state from a healthy
   one. See "why import does not attempt detection," above.
4. **Keying the resource on `user_id`.** The original schema. Replaced by `email` once the
   three-identifier lifecycle was fully mapped; see "why `email`," above.
5. **Live-testing the `auto_add_user` 409 directly.** Considered and declined on a cost/value
   basis specific to the shared internal org available at the time, not because the interaction
   is unimportant — it is documented on both resources regardless.

## Live evidence behind this design, and what is still source-level

Per this repo's Design Verification Policy, a design is not confirmed by source-tracing and
spec-reading alone. What was actually live-confirmed while this resource was built, against a
disposable cloud created and destroyed for each test (never the shared fixtures, never another
org member's real access):

- The roles `PUT` has no precondition and will grant a role to a non-collaborator (real request,
  real 204).
- The legacy `DELETE` 404s against exactly that state, with the real error text quoted above.
- The cloud collaborator search returns a roles-only-granted user as an ordinary-looking record
  (the finding that killed the import-refusal design).
- Self-modification is blocked on the grant side too (403), not only on revoke — previously
  unknown until tested.
- The exact `auto_add_user` 409 text was sourced from `cloud_collaborators_service.py` directly
  (not a live call — this is the one hazard documented on source alone, per the ruling above).

Everything else in this design — the API shapes, the flag-gating, the legacy-read lossiness — was
confirmed against the live OpenAPI spec and, for the pieces above, an actual live request/response,
not inferred from documentation or assumed from a summary.

## What would simplify this, if it ever lands

Two API changes were drafted as an external request to Anyscale (not part of this provider, and not
preserved here since the draft itself is meant to be sent rather than kept): a
`DELETE` on the roles path, which would collapse this resource's two-call, non-atomic `Create` and
its unrecoverable-state trap into a single clean call with a real revoke; and a group
add-member/remove-member endpoint, unrelated to this resource but drafted alongside it. If a
`DELETE` on the roles path ever ships, this entire document's account of *why* `Create` is two
calls becomes obsolete, and the resource should be revisited rather than left running the more
complex path out of inertia.

## On how confident to be in this

Three defects were found in this design after code was written, and all three were wording, naming,
or test-scoping — not logic errors, in a design whose central mechanism is a non-atomic, two-call,
order-dependent `Create` with a state that cannot be detected or repaired once reached. Each
surfaced only from reading the actual committed artifact rather than a summary of it. Whoever
revisits this resource next should expect the same discipline to still find something, and should
apply it themselves rather than trusting this document as a substitute for reading the current code.
