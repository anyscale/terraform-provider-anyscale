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
| Cloud membership, cloud role, and project role | [`anyscale_cloud_access`](../resources/cloud_access.md) | One cloud's **entire** member list, each member's `base_role`/`deny_roles` on that cloud, and their per-project roles under it. **Read and import only in this version** - see [`anyscale_cloud_access`: read-only today, authoritative by design](#anyscale_cloud_access-read-only-today-authoritative-by-design), below, before writing configuration against it. |

The two role resources above use the word "authoritative" to mean: authoritative over the one row
each manages, never over a whole population - see
[Authority](#authority-what-terraform-manages-here-actually-means), below. `anyscale_cloud_access`
means something categorically larger by the same word - see its own section below.

`anyscale_cloud_user_role`, which used to fill the single-user cloud-role gap, has been **removed**
with no coexistence period: it and `anyscale_cloud_access` manage the same collaborator surface
under incompatible authority models (one row vs. the whole set), and running both against the same
cloud would mean `anyscale_cloud_access` silently revoking members `anyscale_cloud_user_role` thinks
it still manages. There was never a version where declaring both was safe.

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

## `anyscale_cloud_access`: read-only today, authoritative by design

**This section describes two different things, and the version you're running only does the
first.** `anyscale_cloud_access` is registered as **read and import only**: it can populate state
with a cloud's members, their `base_role`/`deny_roles`, and their per-project roles, but `Create`,
`Update`, and `Destroy` all refuse. You cannot yet add, change, or revoke access through it - do
that through the console or the API directly, the same as before this resource existed. Everything
below the first paragraph describes what this resource is *designed* to do once its write path
ships in a future version, so that reading it now doesn't mean re-reading it later.

**Read this before you rely on "review the plan" as your safety net for this resource specifically
- it will not be one, for one particular failure mode, once writes are enabled.** Once writes ship,
`anyscale_cloud_access` is authoritative over **one cloud's entire member list**, not over a single
row the way the two role resources above are. Anyone not declared in its `member` map is revoked -
including someone granted access through the console, and including on the cloud's very **first**
`apply` under this resource, not only on later ones. That first-apply revoke has no signal to
detect: a member the operator never knew about looks, to Terraform, identical to one they
deliberately left out, so there is nothing a validator or a guard can catch here. **The plan will
not show it** - a member Terraform has never read appears nowhere in plan output, so reviewing the
plan before applying does not protect you against this specific case the way it does for an
accidental `for_each` typo (which *does* show up as a destroy in the plan). Documentation is the
only mitigation available, which is why it's stated here plainly rather than left to be inferred.

Once writes are enabled, three consequences of that authority model are worth knowing in advance:

- **The caller's own identity is excluded from the authoritative set.** The token running Terraform
  is usually a collaborator on the cloud it manages, often its owner - without this exclusion, the
  first authoritative `apply` could revoke the operator's own access. This is handled for you; it's
  listed here so the design is visible, not because it's something you need to configure.
- **A cloud owner who is not an organization admin can be revoked on a first apply that omits
  them.** Organization admins are incidentally safe (they're invisible to the endpoint this
  resource reads, so it can't revoke what it can't see), but a non-admin owner has no such
  accidental protection. Declare every current member before your first `apply` against a
  production cloud.
- **Removing a cloud member cascades:** the backend recursively revokes that member's project
  permissions on that cloud too. You do not need to also drop their entries from other
  project-scoped configuration for the same cloud - the backend has already done it.

Once writes are enabled, `anyscale_cloud_access` also carries two typo guards worth knowing about
ahead of time: a
case-insensitive duplicate-email check (Terraform's own map-key duplicate detection is
case-sensitive, so two spellings of one address look like two people to Terraform and are one
person to Anyscale), and a guard that refuses an apply that would empty a currently-populated
cloud's member list, naming how many people would lose access. Neither guard can catch a typo in
the `cloud_id` a `for_each` keys off of - a mistyped cloud ID is indistinguishable from a
deliberate removal, and destroying this resource against the wrong cloud correctly-per-its-own-logic
revokes that cloud's members. Mitigate that case the same way you would any other
too-easy-to-destroy resource: `lifecycle { prevent_destroy = true }` on production clouds.

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
