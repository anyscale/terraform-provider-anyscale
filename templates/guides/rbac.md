---
page_title: "RBAC: Roles Across Organizations, Clouds, and Projects"
subcategory: "Behavior & Limitations"
description: |-
  How access control is split across anyscale_organization_user, anyscale_organization_user_role, and anyscale_cloud_access - the vocabulary differences between scopes, what "authoritative" means for each resource, the two rules the backend enforces across a cloud grant and its nested project grants, and migrating from a per-user access-management script.
---

# RBAC: Roles Across Organizations, Clouds, and Projects

Anyscale access control spans three scopes - organization, cloud, and project - and this provider
manages them through more than one resource because the backend itself treats them as genuinely
different concepts, not just different levels of the same one. This guide is the one place that
explains the whole picture: which resource manages what, why the same word means different things
depending on which resource you're reading, and the two rules the backend enforces that neither
resource's own documentation can explain on its own.

Read this before writing configuration that spans more than one scope, and especially before
migrating from a script-based user-management workflow onto this provider.

## Which resource manages what

| Scope | Resource | Manages |
|---|---|---|
| Organization membership | [`anyscale_organization_user`](../resources/organization_user.md) | Whether the user exists in the organization at all. Import-only - it cannot create a member, and destroying it evicts them. |
| Organization role | [`anyscale_organization_user_role`](../resources/organization_user_role.md) | One user's `base_role` and `additional_roles` in the organization. Authoritative over that one user's role - not over who is a member. |
| Cloud and project role | [`anyscale_cloud_access`](../resources/cloud_access.md) | Every user's access to one cloud, including their access to projects inside that cloud. Authoritative over the cloud's **whole** member list. |
| Everything, read-only | [`anyscale_organization_user_access`](../data-sources/organization_user_access.md) | A single lookup for one user's role across every scope at once. |

Two resources use the word "authoritative" and it means different things for each, which is worth
understanding before you write either one - see [Authority](#authority-what-terraform-manages-here-actually-means),
below.

If you used `anyscale_cloud_user_role` before, it has been replaced by `anyscale_cloud_access`, not
renamed alongside it - the schema genuinely changed shape, from one resource per user per cloud to one
resource per cloud managing every user on it. See that resource's own migration notes for moving
existing state over.

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
4. **Organization `additional_roles` and cloud `deny_roles` are not the same mechanism, and do not even
   point the same direction internally.** Cloud `deny_roles` is unambiguously a restriction - its only
   value, `cloud_read_only`, narrows what a base role can do. Organization `additional_roles` is less
   settled: `image_reader` reads as a capability grant, while `image_reader_no_base_images` reads as a
   restriction on that same grant - two values in one field that do not agree on a direction between
   themselves. That is exactly why this provider keeps the attribute named `additional_roles`, the
   API's own neutral name, rather than folding it into `deny_roles` alongside the cloud attribute: a
   name that asserts a single direction would already be wrong for one of its own two values. Check
   this attribute's own schema description for the current, precise account of what each value does.

None of these are hypothetical. The first real hand-written configuration for the nested cloud/project
shape in this guide got two of them wrong on the first attempt - `permission_level = "read"` (not a
valid value; the project enum is `owner`, `write`, `readonly`) and a singular `deny_role` where the
schema expects the plural `deny_roles`, a list. If you remember one thing from this section, make it
this: **never assume a role, permission level, or deny role name from one scope is valid, or means
the same thing, on another.** Check the specific resource's schema every time.

## Authority: what "Terraform manages here" actually means

`anyscale_cloud_access` is **authoritative over a cloud's entire member list.** Declare three people
with access to a cloud, and a fourth person who was granted access some other way - through the
console, through a script, by another team - gets **revoked** on your next `apply`. This is
deliberate: the whole reason this resource exists, rather than a script comparing a JSON file to
reality by hand, is to make Terraform the source of truth for who has access to a cloud and to catch
drift automatically.

`anyscale_organization_user_role` is **not** authoritative in that sense. It manages the role of the
one user it names - `base_role` and `additional_roles` for that person - and has no opinion about
anyone else. A person nobody wrote a resource for is invisible to it, not revoked by it. Organization
membership follows the same rule for the same reason: Terraform evicting every organization member it
wasn't told about is a much larger blast radius than most practitioners want by default, and the
underlying script this provider is meant to replace only does that behind an explicit, separate flag.

One field on this resource is deliberately stricter than its cloud counterpart: `base_role` is
**required**, with no default value. A default would need to pick some role for a bare first `apply`
against a person who already holds real access - and if that default were ever lower than what someone
actually holds, adopting them under Terraform would silently demote them on the very first `apply`,
with nothing unusual in the plan to catch it. Stating the role explicitly every time trades a small
amount of config brevity for making sure a demotion, if it ever happens, is a value you typed and can
see in the diff - never a fallback you didn't choose.

Because `anyscale_cloud_access` is authoritative, and because the backend has no `DELETE` that
removes a role without also removing the underlying cloud membership entirely, a revoke can fail in a
way that has no clean retry: the person was granted access outside Terraform in a way that left no
membership record for Terraform's revoke call to find. Two different kinds of failure follow two
different rules, and the difference matters:

- **A failed write of something your configuration declares is a hard error.** If Terraform cannot
  grant or update a role you asked for, the apply fails. There is no best-effort here - a silently
  unapplied grant would be a worse outcome than a loud failure.
- **A failed revoke of something your configuration does not declare converges anyway**, with the
  grant surfaced as an exception rather than silently dropped or endlessly retried. Check this
  resource's own schema for exactly which attribute lists these - it is a real, `Computed` value you
  can read in `terraform plan` output and reference from other configuration (for example, alerting
  if it is ever non-empty), not just a warning printed to the console and forgotten.

This asymmetry is intentional: Terraform must never silently fail to apply something you asked for,
but it may - and here, must - tell you plainly when it cannot undo something it never did.

## Two rules the backend enforces across a cloud grant and its project grants

`anyscale_cloud_access` nests a member's project grants inside their cloud grant rather than managing
projects as a separate resource, and the reason is two real backend rules that span both halves of a
single grant - rules a practitioner cannot learn from either half's documentation alone, because
neither half's schema mentions the other:

1. **A project grant cannot exist without a cloud grant on that project's parent cloud.** The backend
   rejects a project-level grant for someone who does not already hold cloud-level access to that
   project's cloud. Nesting reflects this directly: there is no way to write a project grant in your
   configuration without it living inside some cloud's member entry, so the invalid configuration
   simply cannot be expressed.
2. **A `cloud_read_only` deny role forces every nested project grant to `readonly`.** If a member's
   cloud-level `deny_roles` includes `cloud_read_only`, every project `permission_level` under that
   same member must also be `readonly` - the backend rejects anything else. Because both the cloud
   deny role and the project permission levels live in the same resource, this is caught by Terraform
   at `terraform plan`, before any API call - not as a failure partway through an `apply`, after other
   grants in the same configuration have already been written.

If you see a plan-time validation error naming either of these rules, it is describing a real backend
constraint, not a provider bug - the fix is to change your configuration to match one of the two rules
above, not to work around the error.

## Migrating from a script-based access-management workflow

If you currently manage cloud access with a script that reconciles a JSON file of users, groups, and
roles against the Anyscale API, `anyscale_cloud_access` is meant to replace it, with a few differences
worth knowing before your first `apply`:

- **Groups are not a Terraform concept.** Anyscale has no API to grant a role to a group, so if your
  source file expresses access in terms of groups, expand group membership into individual
  per-person, per-cloud entries in your Terraform configuration (a `locals` transform, done once)
  before writing the resource. This is the same expansion your script already does internally; moving
  it into `locals` keeps it visible and version-controlled instead of hidden in shell logic.
- **Email keys are matched case-insensitively at plan time.** `Bob@example.com` and `bob@example.com`
  are treated as the same person before any API call is made, specifically so this provider does not
  reproduce a known failure mode of comparing emails as opaque strings: silently treating the same
  person as two different people and repeatedly removing and re-adding them.
- **Your first `apply` against a cloud that already has real members adopts them.** You do not need
  to grant everyone's access again from scratch; declaring the members your script already granted
  and applying should bring existing access under Terraform's management rather than revoking and
  recreating it. If you see unexpected revoke-then-recreate behavior on a first apply, that is worth
  reporting - it is not the intended behavior.

## Typo protection: what is guarded, and what genuinely is not

Because `anyscale_cloud_access` is authoritative, a typo can mean the difference between managing
access and silently revoking it. That risk is guarded against, but not the same way in every case -
worth understanding precisely rather than assuming blanket protection, because one of the three shapes
below cannot be guarded at all:

- **An empty member map - for example, from a group reference that resolved to nobody because the
  group's name was misspelled - requires an explicit, deliberate acknowledgment before Terraform will
  apply it.** Without that acknowledgment, an apply that would reduce a cloud's member map to empty
  fails instead of silently removing everyone. Declaring an intentionally empty cloud is still
  possible - it just cannot happen by accident.
- **A typo'd email address is caught by operation ordering, not by validation.** Every add and update
  in a member map is applied before any removal. A misspelled or nonexistent email among the additions
  or updates fails the apply - before any existing member of that cloud is removed - rather than
  after. This is a safety property of the order operations happen in, not a check on the email itself;
  Terraform cannot know an address is wrong until the API rejects it.
- **A typo'd `cloud_id` cannot be guarded against inside this resource at all.** The `for_each` key is
  what selects which cloud a resource instance manages; if that key is wrong, you are authoritatively
  managing the wrong cloud, and nothing inside the resource can distinguish a wrong-but-valid cloud ID
  from the right one. The real protection here is the same as for any other authoritative resource:
  read `terraform plan` before every `apply`, and consider `lifecycle { prevent_destroy = true }` on a
  cloud you cannot afford to have emptied by mistake.

## Known values, since role names are not enforced by this provider's schema

The valid values for `base_role`, `deny_roles`, and `permission_level` are enforced by the Anyscale
API, not validated ahead of time by this provider's schema - the API adds new roles faster than a
provider release cycle could keep a hard-coded list current. Each attribute's own schema description
lists the values known at the time this was written; treat that list as a helpful starting point, not
an exhaustive one, and expect a role name your configuration sends to fail at `apply` time (with the
API's own error) rather than at `terraform plan` time if it's wrong or no longer current.
