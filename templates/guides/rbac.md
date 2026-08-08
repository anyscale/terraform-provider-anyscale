---
page_title: "RBAC: Roles Across Organizations, Clouds, and Projects"
subcategory: "Behavior & Limitations"
description: |-
  How access control is split across anyscale_organization_user and anyscale_organization_user_role - the vocabulary differences between scopes, what "authoritative" means for a role resource, and why destroying anyscale_organization_user_role does not revert a person's role.
---

# RBAC: Roles Across Organizations, Clouds, and Projects

Anyscale access control spans three scopes - organization, cloud, and project - and this provider
manages them through more than one resource because the backend itself treats them as genuinely
different concepts, not just different levels of the same one. This guide is the one place that
explains the whole picture: which resource manages what, and why the same word means different things
depending on which resource you're reading.

Read this before writing configuration that spans more than one scope.

## Which resource manages what

| Scope | Resource | Manages |
|---|---|---|
| Organization membership | [`anyscale_organization_user`](../resources/organization_user.md) | Whether the user is under Terraform management. It cannot create a member - people join by invitation - but declaring it adopts an existing member with no API call. Destroying it removes nobody: it cancels only a still-pending invitation this resource itself sent. |
| Organization role | [`anyscale_organization_user_role`](../resources/organization_user_role.md) | One user's `base_role` and `deny_roles` in the organization. Authoritative over that one user's role - not over who is a member. |
| Cloud membership, cloud role, and project role | [`anyscale_cloud_access`](../resources/cloud_access.md) | One cloud's **entire** member list, each member's `base_role`/`deny_roles` on that cloud, and their per-project roles under it. Authoritative from the first `apply` - see [`anyscale_cloud_access`: authoritative over one cloud's whole member list](#anyscale_cloud_access-authoritative-over-one-clouds-whole-member-list), below, before writing configuration against it. |

The two role resources above use the word "authoritative" to mean: authoritative over the one row
each manages, never over a whole population - see
[Authority](#authority-what-terraform-manages-here-actually-means), below. `anyscale_cloud_access`
means something categorically larger by the same word - see its own section below.

`anyscale_cloud_user_role`, which used to fill the single-user cloud-role gap, has been **removed**
with no coexistence period: it and `anyscale_cloud_access` manage the same collaborator surface
under incompatible authority models (one row vs. the whole set), and running both against the same
cloud would mean `anyscale_cloud_access` silently revoking members `anyscale_cloud_user_role` thinks
it still manages. There was never a version where declaring both was safe.

## Managing a team without a group resource

Anyscale groups exist (`/api/v2/user_groups`, org-scoped, with create/delete/set-roles) but cannot
yet be populated - group membership is written only by WorkOS directory-sync events, and directory
sync is not yet available to customers. A group nobody can join has nothing for a group resource to
usefully wrap, which is why there is no `anyscale_group` resource here today. The recommended
pattern is a `locals` map, keyed by email, that drives both the invitation loop and the role loop:

```hcl
locals {
  ml_team = {
    "dev1@example.com" = "collaborator"
    "dev2@example.com" = "collaborator"
    "lead@example.com" = "owner"
  }
}

resource "anyscale_organization_invitation" "team_members" {
  for_each = local.ml_team
  email    = each.key
}

resource "anyscale_organization_user_role" "team_member_roles" {
  for_each  = local.ml_team
  email     = each.key
  base_role = each.value
}
```

One map is the single source of truth for the whole team: adding a teammate is one line, in one
place, and every resource that reads the map follows. This is the idiomatic Terraform answer for
this shape, not a workaround for a missing primitive. See the full staged workflow - invitation,
then role, once the invitation is accepted - in
[`examples/resources/organization_user_workflow`](https://github.com/anyscale/terraform-provider-anyscale/tree/main/examples/resources/organization_user_workflow).

`anyscale_organization_user_role` requires an already-accepted member, so the role loop above
cannot apply in the same run as the invitation loop for someone who has not yet accepted - stage
it as a second `apply` once invitations are accepted, not a sign anything here is incomplete.

## The vocabulary problem

The organization, cloud, and project scopes don't just use different words for access levels - they
reuse the **same** words to mean **different** things. Four instances of this, and only the first is
explained anywhere else in this provider's documentation today:

1. **`collaborator` means three different things.** It's the non-owner organization role. It's also
   one of six possible cloud `base_role` values, a distinct concept from the organization one. And
   the Anyscale console itself uses "Cloud collaborator" as the display name for the unrelated
   legacy `write` permission level. Seeing "collaborator" tells you nothing about which of the three
   you're looking at without checking which resource's schema you're reading.
2. **Project `write` and cloud `writer` are not the same role**, despite being near-homographs. This
   is easy to typo in exactly one direction: the backend's own internal name for the cloud role is
   `writer`, but a project's valid value is the string `write` - sending `writer` to a project 422s.
3. **Project `readonly` and cloud `cloud_read_only` are not the same kind of thing**, not just
   different spellings of the same idea. Project `readonly` is a whole permission tier - one of
   three, on equal footing with `owner` and `write`. Cloud `cloud_read_only` is a **deny role**: a
   restriction layered on top of a separate `base_role`, never a tier by itself. A user with
   `base_role = "owner"` and `deny_roles = ["cloud_read_only"]` is not "read-only" the way a project
   collaborator with `permission_level = "readonly"` is - the cloud user still holds the underlying
   `owner` role, just with reads enforced on top of it.
4. **Organization `deny_roles` and cloud `deny_roles` are the same mechanism, and share the name
   deliberately.** Both are a restriction layered on top of a separate base role, never a tier by
   themselves - an organization member with `base_role = "collaborator"` and
   `deny_roles = ["image_reader_no_base_images"]` still holds the underlying `collaborator` role, just
   with container-image capability subtracted from it, the same shape as the cloud example above.
   Where the two genuinely differ is blast radius, not mechanism: an organization deny role overrides
   even an organization owner's implicit permissions, while a cloud deny role explicitly does **not**
   restrict organization or project owners. Same name, same restriction-on-a-base-role mechanism,
   different reach - check which scope you're granting at before assuming an owner is unaffected.

None of these are hypothetical. The first real hand-written configuration for the nested cloud/project
shape in this guide got two of them wrong on the first attempt - `permission_level = "read"` (not a
valid value; the project enum is `owner`, `write`, `readonly`) and a singular `deny_role` where the
schema expects the plural `deny_roles`, a list. If you remember one thing from this section, make it
this: **never assume a role, permission level, or deny role name from one scope is valid, or means
the same thing, on another.** Check the specific resource's schema every time.

## Authority: what "Terraform manages here" actually means

`anyscale_organization_user_role` uses the word "authoritative" to mean: authoritative over the one
row it manages, never over a whole population. It is authoritative over one user's organization role -
`base_role` and `deny_roles` for that person - and has no opinion about anyone else. It does not
discover or revoke anyone Terraform was not explicitly told to manage - a person nobody wrote a
resource for is invisible to it, not evicted by it.

This resource makes a deliberate design choice: `base_role` is **required**, with no default value. A
default would need to pick some role for a bare first `apply` against a person who already holds real
access - and if that default were ever lower than what someone actually holds, adopting them under
Terraform would silently demote them on the very first `apply`, with nothing unusual in the plan to
catch it. Stating the role explicitly every time trades a small amount of config brevity for making
sure a demotion, if it ever happens, is a value you typed and can see in the diff - never a fallback
you didn't choose.

**One sharp edge is worth flagging before the details below:** `anyscale_organization_user_role`'s
destroy can **leave a privilege behind** - an owner stays an owner - and can even **grant new
capability**, by lifting a declared `deny_roles` restriction. Neither is a bug; both follow from the
same rule explained next.

This resource's `destroy` can also fail to fully complete - see
["Destroying `anyscale_organization_user_role`"](#destroying-anyscale_organization_user_role-what-actually-happens)
below for the details.

## `anyscale_cloud_access`: authoritative over one cloud's whole member list

**Read this before you rely on "review the plan" as your safety net for this resource specifically
- it will not be one, for one particular failure mode.** `anyscale_cloud_access` is authoritative
over **one cloud's entire member list**, not over a single row the way the two role resources above
are. Anyone not declared in its `member` map is revoked - including someone granted access through
the console, and including on the cloud's very **first** `apply` under this resource, not only on
later ones. That first-apply revoke has no signal to detect: a member the operator never knew about
looks, to Terraform, identical to one they deliberately left out, so there is nothing a validator or
a guard can catch here. **The plan will not show it** - a member Terraform has never read appears
nowhere in plan output, so reviewing the plan before applying does not protect you against this
specific case the way it does for an accidental `for_each` typo (which *does* show up as a destroy
in the plan). Documentation is the only mitigation available, which is why it's stated here plainly
rather than left to be inferred.

**Only one Terraform state may manage a given `cloud_id`.** Two states managing the same cloud
revoke each other's members indefinitely - the first grants Alice and revokes Bob, the second
grants Bob and revokes Alice - and both report `apply` success on every run. Nothing about the
provider can detect this: it cannot see another state file. Treat it as the price of this
resource's authority, not a limitation to file a bug against.

Four consequences of the authority model are worth knowing in advance:

- **The caller's own identity is excluded from the authoritative set.** The backend already refuses
  self-removal outright (a 403, "You cannot remove yourself from the cloud"), so this exclusion is
  not what stands between you and locking yourself out - that can't happen through this resource
  regardless. What it actually prevents: without it, every apply where the caller is undeclared
  would attempt a revoke the backend refuses, permanently occupying the `unmanaged_grants` alarm
  this page tells you to watch. **This is a guard against noise on that alarm, not a
  protected-persons list - it follows the token.** Whoever runs a given `apply` is excluded during
  that apply; a colleague who ran last week's `apply` and is not declared in your configuration is
  still revoked, correctly. Declaring the caller in `member` is rejected at plan time.
- **Organization admins are excluded the same way, but the backend enforces it rather than this
  resource.** Declaring an org admin in `member` is rejected at plan time where the check can run,
  and the backend independently refuses the underlying role write outright ("You cannot modify an
  organization admin's cloud role"). **This plan-time check is a best-effort early warning, not a
  guarantee** - nothing available to the provider reliably predicts the backend's own signal ahead
  of time, so treat a clean plan as a courtesy, not proof that no admin is declared.
- **Removing a cloud member cascades:** the backend recursively revokes that member's project
  permissions on that cloud too, so this resource must not (and does not) attempt a separate
  project-scope revoke for a member it just removed at cloud scope - there is nothing left for that
  second revoke to reach.
- **Project authority is scoped to projects your configuration names, not to every project under
  the cloud.** A project role granted out of band on a project your configuration never mentions is
  neither reported by this resource nor revoked by it - that blindness is deliberate and disclosed,
  not a bug, but it means this resource's authority over projects is narrower than its authority
  over cloud membership itself.

`anyscale_cloud_access` also carries two typo guards worth knowing about ahead of time: a
case-insensitive duplicate-email check (Terraform's own map-key duplicate detection is
case-sensitive, so two spellings of one address look like two people to Terraform and are one
person to Anyscale), and a guard that refuses an apply that would empty a currently-populated
cloud's member list, naming how many people would lose access. Neither guard can catch a typo in
the `cloud_id` a `for_each` keys off of - a mistyped cloud ID is indistinguishable from a
deliberate removal, and destroying this resource against the wrong cloud correctly-per-its-own-logic
revokes that cloud's members. Mitigate that case the same way you would any other
too-easy-to-destroy resource: `lifecycle { prevent_destroy = true }` on production clouds.

**Three conditions block this resource from writing at all, checked before any mutation:**

- **`auto_add_user` enabled on the cloud.** Enabling it on a cloud that already has organization
  members retroactively adds every one of them as a collaborator immediately, not just future
  members - live-confirmed behavior, not a design assumption. An authoritative resource cannot
  converge against a cloud that keeps regranting access out from under it, so `Create` and `Update`
  refuse and name the setting as the remedy. `Delete` is the deliberate exception: refusing it would
  strand the resource in state with no exit but `terraform state rm`, so it attempts every revoke
  and records what it could not complete.
- **A group policy binding on the cloud.** Access granted to a *group* (rather than an individual)
  is reconciled into the same member rows this resource reads, with nothing marking a member as
  group-derived - so this resource cannot tell a group grant from a manual one and would revoke it
  outright, only for the group's own reconciler to silently re-add it later. `Create` and `Update`
  refuse and name the binding; `Delete` proceeds the same way it does for `auto_add_user`. This check
  only runs on an apply that would actually revoke someone, and it needs a permission tier the
  token running Terraform may not hold - if it can't get a clear answer (most commonly a
  permission error, not an outage), it warns loudly and proceeds rather than blocking, the same
  "warn, don't block" rule as the org-admin check above. **The absence of a warning is not proof
  that no binding exists** - it may mean the check could not run at all.
- **`base_role = "owner"` combined with `deny_roles = ["cloud_read_only"]`.** The backend rejects
  cloud owners being read-only outright; this resource catches it at plan time instead of partway
  through an apply.

### Drift detection has a real blind spot, and it is the dangerous direction

Once you've imported or created a cloud under this resource, `terraform plan` detects most - but
not all - out-of-band changes made through the Anyscale console or CLI. The three claims below are
independent; keep them separate rather than summarizing "drift is detected" or "drift isn't
detected" as one statement.

- **A change that turns a member's read-only restriction on or off is detected.** This shows up as a
  real diff, and applying repairs it.
- **A change to a member's underlying role - between `write` and `owner`, for example - is NOT
  detected, in either direction.** The console and CLI write through an older API that populates
  this resource's per-role data once, at the moment a member is first added, and never revisits it
  on a later change - so the role this resource reads is frozen at whatever it was when the member
  was created, and a later console change to it produces no difference `terraform plan` can see even
  though the member's actual role has changed. **This is a privilege-escalation blind spot: a
  member promoted to `owner` through the console stays `owner`, silently, while Terraform's state
  and every subsequent plan continue to say whatever role they held before.** There is no signal
  telling you to check.
- **The fix is not automatic, but it is simple: declare the role you want and apply.** Doing so
  corrects the member's role for real, not just in Terraform's state - but only when you explicitly
  declare it. Nothing about refreshing state repairs this on its own, because reading state never
  writes anything. If you suspect a cloud has been touched outside Terraform since its last apply,
  re-declare its full member set and apply rather than trusting a clean plan.

### A grant can fail without being confused for a revoke

Two computed attributes on this resource report partial failures, and they mean opposite things -
do not treat a nonzero length on either as interchangeable with the other:

- **`unmanaged_grants`** - a member the configuration does *not* want, that this resource could not
  finish revoking. Someone has access they shouldn't.
- **`ungranted_members`** - a member the configuration *does* want, that this resource could not
  finish granting. Someone lacks access they should have.

Either failure is recorded with a reason and a loud warning, and the apply still succeeds rather
than erroring - erroring after a partial write would mark the resource tainted and schedule a full
destroy-and-recreate on the next apply, which would revoke every member this one **did** manage to
grant. When a grant fails partway through an apply, this resource also skips the revokes that apply
would otherwise have attempted (recorded in `unmanaged_grants` with a reason naming the skip) rather
than removing access at the same moment it can't confirm who should have it - the next `apply`
retries everything still outstanding.

Project-role **writes** can fail for reasons that never show up on a project-role **read**, on the
very same project at the very same moment - a clean `plan` or a successful import is no guarantee
that granting or revoking on that project will succeed. When that happens it's `ungranted_members`
or `unmanaged_grants` that tells you, with the reason from the API attached, not a difference
you'd otherwise have any way to see coming.

Project roles nest inside each member (`member[*].projects`) rather than living in a sibling
resource, because the backend enforces two things only a single nesting resource can check at plan
time instead of surfacing as a confusing `apply`-time failure: a project role cannot exist without a
cloud role on that same cloud, and a member holding the `cloud_read_only` deny role may only hold
`readonly` on that cloud's projects - anything else is rejected.

## Destroying `anyscale_organization_user_role`: what actually happens

`terraform destroy` on this resource does **not** revert or remove the person's role the way destroying
most resources removes the thing they manage. Read this before your first `destroy` - the behavior is
asymmetric between the resource's two fields, and it is easy to assume wrong in either direction.

**`base_role` is left exactly as it is.** Every organization member always has some `base_role` - there
is no absent state for it to revert to, so this resource manages a value, not a grant, and destroy has
nothing to remove, only something it could change. Reverting to a fresh-member default on destroy was
considered and rejected: demoting an organization owner to collaborator strips their access to every
cloud in the organization, and every organization must have at least one owner - so a destroy that
silently reverted `base_role` could, on an org's last owner, either fail outright or leave the
organization with nobody able to administer it. A `destroy` whose plan says "destroy 1 resource" must
never be able to do that, so this resource does not attempt it. The person keeps whatever role they held
at the moment of destroy, indefinitely, until someone writes a different value some other way.

**`deny_roles` is different, because it genuinely has an absent state.** If your configuration declared
`deny_roles`, destroy clears them back to empty - this resource took authority over that value, so
destroying it releases that authority cleanly. If your configuration never declared `deny_roles`
(leaving it at its `Optional`+`Computed` default), destroy leaves it untouched too, the same as
`base_role` - this resource never took authority over a value it was never told to manage.

**Read that clearing carefully: it is not a reduction.** `deny_roles` are restrictions, so clearing a
declared set back to empty *increases* that person's access - the one destroy in this provider that
grants capability rather than removing it. Destroying this resource for someone whose configuration
declared `deny_roles = ["image_reader_no_base_images"]` lifts that restriction and lets them read
container images they could not read before. This is deliberate and documented, not a bug - it follows
from the same rule as everything else on this page, that destroy releases authority over what was
declared - but it is worth stating in these words, because nothing about the word "destroy" suggests a
grant of new access.

**Destroy is never silent about any of this.** It emits a warning diagnostic naming the person's email
and exactly what was left in place, so "nothing changed about this person's access" is always something
you can see in the `terraform destroy` output, not something you have to already know to expect.

Two more things worth knowing before you destroy this resource in practice: clearing a declared
`deny_roles` on destroy goes through the same gated endpoint a write to it always uses, so a destroy can
fail with the same feature-gate diagnostic a `deny_roles` apply can; and destroying your own role
resource hits the same self-modification restriction every scope in this provider enforces - expect a
clear error naming that restriction, not a silent no-op, if you try.

**A typo'd `email` has the same consequence, through a different path.** `email` is the value that
identifies which person this resource manages, and changing it - including fixing a typo - replaces the
resource: Terraform destroys the instance for the old address and creates a new one for the corrected
one. Destroying the old instance is exactly the state-only behavior described above, so the person at
the mistyped address keeps whatever role Terraform last gave them, indefinitely, with no automatic
cleanup. Read `terraform plan` before applying an `email` change for exactly this reason.

## Known values, since role names are not enforced by this provider's schema

The valid values for `base_role`, `deny_roles`, and `permission_level` are enforced by the Anyscale
API, not validated ahead of time by this provider's schema - the API adds new roles faster than a
provider release cycle could keep a hard-coded list current. Each attribute's own schema description
lists the values known at the time this was written; treat that list as a helpful starting point, not
an exhaustive one, and expect a role name your configuration sends to fail at `apply` time (with the
API's own error) rather than at `terraform plan` time if it's wrong or no longer current.
