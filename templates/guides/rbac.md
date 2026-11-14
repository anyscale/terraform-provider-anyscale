---
page_title: "RBAC: Roles Across Organizations, Clouds, and Projects"
subcategory: "Concepts & Reference"
description: |-
  How access control is split across anyscale_iam_organization_user and anyscale_organization_user_role - which resource manages what, what "authoritative" means for a role resource, and why destroying anyscale_organization_user_role does not revert a person's role.
---

# RBAC: Roles Across Organizations, Clouds, and Projects

Anyscale access control spans three scopes - organization, cloud, and project - and this provider
manages them through more than one resource because the backend itself treats them as genuinely
different concepts, not just different levels of the same one. This guide is the one place that
explains the whole picture: which resource manages what, and what "authoritative" means for each.

Read this before writing configuration that spans more than one scope.

## Which resource manages what

| Scope | Resource | Manages |
|---|---|---|
| Organization membership | [`anyscale_iam_organization_user`](../resources/iam_organization_user.md) | Whether the user is under Terraform management. It cannot create a member - people join by invitation - but declaring it adopts an existing member with no API call. Destroying it removes nobody: it cancels only a still-pending invitation this resource itself sent. |
| Organization role | [`anyscale_organization_user_role`](../resources/organization_user_role.md) | One user's `base_role` and `deny_roles` in the organization. Authoritative over that one user's role - not over who is a member. |
| User groups (Beta) | [`anyscale_iam_user_group`](../resources/iam_user_group.md), [`anyscale_iam_user_group_members`](../resources/iam_user_group_members.md) | A group's name, and its **entire** member list. Not what the group may access - see role bindings, next row. See [User groups (Beta)](#user-groups-beta). |
| Role grants (Beta) | [`anyscale_iam_role_grant`](../resources/iam_role_grant.md), [`anyscale_iam_role_binding`](../resources/iam_role_binding.md), [`anyscale_iam_role`](../data-sources/iam_role.md) data source | One role held by one user group or one user on one cloud, project, or the organization (`anyscale_iam_role_grant`), or every direct role binding on a scope (`anyscale_iam_role_binding`). **Converted organizations only** - see [Organization access mode](#organization-access-mode). See [Role bindings (Beta)](#role-bindings-beta). |

`anyscale_organization_user_role` uses the word "authoritative" to mean: authoritative over the one row
it manages, never over a whole population - see
[Authority](#authority-what-terraform-manages-here-actually-means), below.

## Inviting people: limits, single sign-on, directory sync

`anyscale_iam_organization_user` and `anyscale_iam_organization_invitation` both send invitations, and both
are subject to the same constraints.

- **Apply success is not access.** A person who is not yet a member has only been *invited*; they
  have access once they accept the emailed link. Read the `anyscale_iam_organization_user` data source
  to see where they are.
- **Rate limits are per organization**, shared with the Anyscale console and not per configuration:
  by default at most 20 pending invitations at once and 20 sent per 24 hours. Anyscale can change
  both per organization. A `for_each` that invites more people than the limit fails partway
  through the apply, leaving earlier invitations sent. Invite in batches, or ask Anyscale to raise
  the limit.
- **Single sign-on `required`:** creating an invitation is refused, because the emailed link would be
  rejected on use while still spending one of the 20 daily invitations. Bring the person in through
  your identity provider; once they have logged in, `anyscale_iam_organization_user` adopts them. If the
  provider cannot read the organization's SSO mode, it sends the invitation and attaches a warning.
- **Directory sync (SCIM) with the Policy API:** if the organization has both enabled, the backend
  refuses to create invitations. Both must be true; SCIM alone does not trigger it. Existing members
  can still be adopted and read. Manage membership through your identity provider and
  [`anyscale policy set`](https://docs.anyscale.com/reference/cli/policy#policy-cli).
- **Re-inviting a pending address** silently invalidates the earlier invitation and creates a new one
  with a new `id`; letter case does not matter. A tracked earlier invitation then reads `expired`
  on its next refresh.
- **Destroying never removes a member.** Destroying `anyscale_iam_organization_user` cancels only a
  still-pending invitation it sent, and never evicts anyone who had joined or was adopted. Destroying
  `anyscale_iam_organization_invitation` revokes a pending invitation and has no effect on an accepted
  one. Removing someone's access is a deliberate act neither resource can do.

## User groups (Beta)

~> **Beta.** User groups are a Beta feature. Beta resources may change incompatibly; every breaking
change is listed in the changelog. Pin the provider version if you depend on these resources, and
read the changelog before upgrading.

An Anyscale **user group** is a named set of organization members. This provider manages groups in
four types:

| Type | Does |
|---|---|
| [`anyscale_iam_user_group`](../resources/iam_user_group.md) | Creates, renames, and deletes one group. Does not manage who is in it. |
| [`anyscale_iam_user_group_members`](../resources/iam_user_group_members.md) | The **complete** member list of one group, keyed by email. |
| [`anyscale_iam_user_group`](../data-sources/iam_user_group.md) data source | Reads one group by `id` or `name`, including directory-synced groups. |
| [`anyscale_iam_user_groups`](../data-sources/iam_user_groups.md) data source | Lists the organization's groups. |

Managing groups needs the organization-level permission to manage IAM (an organization admin).

### What is in scope, and what is not

In scope: creating and renaming groups, and managing who belongs to them. **Not managed here:** what a
group may access. Grant a group a role on a cloud, project, or the organization with
[`anyscale_iam_role_grant`](#role-bindings-beta). A group may also already hold grants set in the
Anyscale console or API, so adding or removing members can grant or revoke real access. A group's
organization roles (`base_role`) are not managed by this provider.

### Members must already be in the organization

`anyscale_iam_user_group_members` takes **email addresses** and resolves each to a user in your
organization. An email that is not an organization member fails the apply; invite the person first
with [`anyscale_iam_organization_invitation`](../resources/iam_organization_invitation.md) and wait for them to
accept. Emails match case-insensitively, and state keeps the spelling from your configuration. After `terraform import`, a configuration that spells an
email with uppercase letters shows a one-time in-place update to `members`; it makes no API change.

Anyscale support staff accounts cannot be added to a group: the apply fails with an error that names the
email, and nothing is changed.

### `anyscale_iam_user_group_members` is authoritative

Anyone in the group who is not in `members` is **removed** - including people added through the
Anyscale console, and including on the first apply. `members = []` is allowed and empties the group.
Declare exactly one `anyscale_iam_user_group_members` per group; two configurations managing the same
group remove each other's members on every apply, and nothing can detect that from either state. A
person who leaves the organization drops out of the group; the next plan proposes re-adding them and
the apply fails, because they are no longer an organization member.

In a [converted organization](#organization-access-mode), the "All users" group adds every joining member
automatically and refuses removals, so do not manage it with `anyscale_iam_user_group_members`: it would show a
perpetual diff, then fail with a 409.

### Directory-synced groups are read-only here

Groups created by directory sync (SCIM) are owned by your identity provider. The resources refuse to
manage them: importing one, or declaring members for it, fails with an error. Read them with the
`anyscale_iam_user_group` data source (its `source` attribute is `scim`) and reference their IDs from
other configuration. Groups created before April 2026 may report no `source`; those are treated as
managed and can be imported.

### Destroying

Destroying `anyscale_iam_user_group_members` removes every member and leaves the group. Destroying
`anyscale_iam_user_group` removes the members first, then deletes the group. The members are removed
first deliberately: deleting a group does not, on its own, revoke everything that was derived from
its membership.

### Cost of a refresh

Reading membership fetches the whole organization's group memberships once per
`anyscale_iam_user_group_members`. That is fine for typical organizations; if you manage many groups,
expect refreshes to scale with the number of groups.

### Example

```hcl
# Prerequisite: both people are already organization members. If not, invite them first with
# anyscale_iam_organization_invitation, wait for them to accept, then apply this configuration.
resource "anyscale_iam_user_group" "ml_platform" {
  name = "ml-platform"
}

resource "anyscale_iam_user_group_members" "ml_platform" {
  user_group_id = anyscale_iam_user_group.ml_platform.id
  members       = ["dev1@example.com", "dev2@example.com"] # must already be organization members
}
```

See [`examples/resources/anyscale_iam_user_group_members`](https://github.com/anyscale/terraform-provider-anyscale/tree/main/examples/resources/anyscale_iam_user_group_members)
for a runnable configuration.

### Managing a team without groups

Groups are one way to model a team. When you only need each person's *organization role*, which
groups do not manage, a `locals` map keyed by email can drive both the invitation loop and the role
loop:

```hcl
locals {
  ml_team = {
    "dev1@example.com" = "collaborator"
    "dev2@example.com" = "collaborator"
    "lead@example.com" = "owner"
  }
}

resource "anyscale_iam_organization_invitation" "team_members" {
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
this shape. See the full staged workflow - invitation,
then role, once the invitation is accepted - in
[`examples/resources/organization_user_workflow`](https://github.com/anyscale/terraform-provider-anyscale/tree/main/examples/resources/organization_user_workflow).

`anyscale_organization_user_role` requires an already-accepted member, so the role loop above
cannot apply in the same run as the invitation loop for someone who has not yet accepted - stage
it as a second `apply` once invitations are accepted, not a sign anything here is incomplete.

## Organization access mode

An Anyscale organization manages access in one of two modes, and the mode decides which resources can
write. You cannot choose the mode from Terraform; an organization admin converts it in **Users & IAM**,
and conversion is one-way.

| | Unconverted organization | Converted organization |
|---|---|---|
| Access is held by | Collaborator and policy records | Role bindings |
| Writes that work | [`anyscale_organization_user_role`](../resources/organization_user_role.md), `anyscale_cloud.auto_add_user` | [`anyscale_iam_role_grant`](../resources/iam_role_grant.md) |
| Writes the API refuses (HTTP 409) | `anyscale_iam_role_grant` | The resources in the Unconverted column |
| Not affected by mode | [`anyscale_iam_user_group`](../resources/iam_user_group.md), [`anyscale_iam_user_group_members`](../resources/iam_user_group_members.md), [`anyscale_iam_organization_user`](../resources/iam_organization_user.md), [`anyscale_project`](../resources/project.md), [`anyscale_cloud`](../resources/cloud.md) (except `auto_add_user`) | same |

The provider does not detect the mode in advance; the API refuses the write and the provider reports it.
Role bindings are also behind a feature flag (see [Role bindings (Beta)](#role-bindings-beta)). An
organization is unconverted until an admin converts it.

**Azure control planes.** On an Anyscale control plane in Azure (`console.azure.anyscale.com`), access
is managed with Azure role-based access control in the Azure portal, in either mode. When the
provider's API URL is one of these hosts, creating, changing or importing `anyscale_iam_role_grant`,
`anyscale_iam_user_group`, `anyscale_iam_user_group_members` or
`anyscale_organization_user_role`, reading the `anyscale_iam_role`, `anyscale_iam_user_group` or
`anyscale_iam_user_groups` data sources, and setting `anyscale_cloud.auto_add_user` fail at plan with "RBAC
Is Managed in Azure". Destroying one, or a plan with no changes, still works. A custom or proxied URL in
front of an Azure control plane is not detected.

| Error | Mode it means | What to do |
|---|---|---|
| "Organization Not Converted to Role Bindings" | Unconverted | Convert the organization in Users & IAM, or manage organization roles with `anyscale_organization_user_role`. |
| "Organization Uses Role Bindings" | Converted | Stop managing that access with this resource; use `anyscale_iam_role_grant` on a user group or a user. |

In a converted organization, destroying `anyscale_organization_user_role` warns "Organization Role Left In
Place" and removes it from state; nothing is reverted, and the existing role is left in place.

Terraform **cannot detect** a conversion. After it, a refresh of `anyscale_organization_user_role` still
returns the record from before conversion, so `plan` shows no changes while the access that matters now
lives in role bindings. The next write is what fails.

Role-binding writes are refused until the organization converts, so the move happens in this order:

1. Convert in Users & IAM. Conversion writes role bindings for your existing access, so most grants you
   would declare already exist.
2. Run `terraform state rm` for `anyscale_organization_user_role` (or destroy it, which only warns), and remove it
   from your configuration.
3. Declare an `anyscale_iam_role_grant` for each grant you want Terraform to manage, and **import** the
   existing binding instead of creating one. Creating a grant that already exists fails with "Role Binding
   Already Exists". You do not need to look up the binding ID: import by what the binding grants,
   `<scope_type>/<scope_id>/<principal_type>/<principal_id>/<role_id>`, where `scope_type` is `organization`,
   `cloud` or `project`, and `principal_type` is `user` or `user_group`. Import fails, and creates nothing,
   if no such binding exists. An `import` block with `for_each` imports many at once and needs Terraform 1.7
   or later.

```hcl
locals {
  grants = {
    analysts = { group = anyscale_iam_user_group.analysts.id, role = data.anyscale_iam_role.viewer.id }
  }
}

resource "anyscale_iam_role_grant" "viewer" {
  for_each      = local.grants
  user_group_id = each.value.group
  role_id       = each.value.role
  project_id    = var.project_id
}

import {
  for_each = local.grants
  to       = anyscale_iam_role_grant.viewer[each.key]
  id       = "project/${var.project_id}/user_group/${each.value.group}/${each.value.role}"
}
```

The binding ID (`rb_...`) also works as the import ID.

Which bindings conversion creates depends on the option chosen in the migration wizard, so expect to
find some grants missing and create those normally.

## Legacy RBAC deprecation

The resources and attributes that manage or report access through collaborator and policy records
are deprecated. They keep working in an unconverted organization, a converted organization refuses
or ignores them, and they will be removed in a future release once every organization can convert.
None is available on an Azure control plane (see [Organization access mode](#organization-access-mode)).

| Deprecated | In a converted organization | Use instead |
|---|---|---|
| `anyscale_organization_user_role` | Writes fail with "Organization Uses Role Bindings" | `anyscale_iam_role_grant` |
| `anyscale_cloud.auto_add_user` | Setting it fails | An `anyscale_iam_role_grant` on the cloud for the All users group |
| `auto_add_user` on the `anyscale_cloud` and `anyscale_clouds` data sources | No longer applies | Same |
| `collaborators` on the `anyscale_project` data source | Null | No replacement yet |
| `permission_level`, `base_role` and `additional_roles` on the `anyscale_iam_organization_user(s)` data sources; `organization_permission_level` on the `anyscale_user` data source | A snapshot from before conversion | No replacement yet |

Terraform warns when a configuration uses one: setting a deprecated resource or attribute, or
referencing a deprecated read-only attribute, for example in an output. For a referenced read-only
attribute, Terraform 1.15 prints the provider's message and Terraform 1.10 a generic "The attribute
... is deprecated". To move a converted organization off them, follow the steps in
[Organization access mode](#organization-access-mode).

## Role bindings (Beta)

~> **Beta, behind a feature flag.** Role bindings are a Beta feature. Beta resources can change
incompatibly between provider versions - 0.37.0 renamed both of them, with no alias for the old
names - so pin the provider version, and read the changelog before upgrading; every breaking
change is listed there. Anyscale enables them per organization while customers migrate: contact
support@anyscale.com for access. Without it, every call fails with "Role bindings are not enabled".
The organization must also be [converted](#organization-access-mode).

Vocabulary, if you know other IAM systems: these names follow the Anyscale CLI, not anybody
else's. `anyscale_iam_role_grant` is **additive** and non-authoritative, like
`google_project_iam_member` - it grants only what you declare and never removes other grants.
`anyscale_iam_role_binding` owns **every** grant on one scope: more than `google_project_iam_binding`
(which owns one role), and unlike a Kubernetes `RoleBinding`, which is additive. Each API object
is still a "role binding" with an `rb_` ID - one `anyscale_iam_role_grant` creates exactly one,
so someone tracing an `rb_` ID from the console or API lands on the grant resource.

`anyscale_iam_role_grant` grants **one role to one user group or one user on one scope** - exactly one of
`user_group_id` or `user_id`, and exactly one of `cloud_id`,
`project_id`, or `organization_id`. Look up the role with the [`anyscale_iam_role`](../data-sources/iam_role.md)
data source, which resolves a role by its exact, case-sensitive `name`. Which role names exist depends on
your organization; ask support@anyscale.com which built-in roles are enabled.

```hcl
data "anyscale_iam_role" "viewer" {
  name = "project_viewer" # exact, case-sensitive; archived roles are not found
}

resource "anyscale_iam_user_group" "analysts" {
  name = "analysts"
}

resource "anyscale_iam_user_group_members" "analysts" {
  user_group_id = anyscale_iam_user_group.analysts.id
  members       = ["alice@example.com", "bob@example.com"] # must already be organization members
}

resource "anyscale_iam_role_grant" "analysts_viewer" {
  user_group_id = anyscale_iam_user_group.analysts.id
  role_id       = data.anyscale_iam_role.viewer.id
  project_id    = var.project_id # exactly one of cloud_id / project_id / organization_id
}
```

Prefer groups: bind the role to the group once, then add or remove people by editing `members`; every
member gets the role.

### Granting a user, not a group

Binding a user takes the user's `usr_...` ID, not their email. Look it up with the
[`anyscale_iam_organization_user`](../data-sources/iam_organization_user.md) data source and use its **`user_id`**
attribute, not `id` (that is the identity ID, a different value):

```hcl
data "anyscale_iam_organization_user" "alice" {
  email = "alice@example.com"
}

resource "anyscale_iam_role_grant" "alice_viewer" {
  user_id    = data.anyscale_iam_organization_user.alice.user_id
  role_id    = data.anyscale_iam_role.viewer.id
  project_id = var.project_id
}
```

`user_id` can be null for a user who has no user ID yet; apply the binding once it is set.
Resources that take an email (`anyscale_iam_user_group_members`) and this one that takes
an ID cannot share a variable; keep emails and IDs in separate places.

### Each binding is one grant, and cannot be edited

The API has no update, so changing any argument replaces the binding. Set `lifecycle { create_before_destroy = true }` when changing `role_id` so the
group is never briefly without access.

Bindings are **not authoritative**: Terraform manages only the bindings you declare, never the full set
on a cloud, project, or organization. Declaring a binding that already exists fails with a conflict;
`terraform import anyscale_iam_role_grant.<name> <binding id>` to adopt it.

### Managing every grant on a scope: `anyscale_iam_role_binding`

`anyscale_iam_role_binding` is the authoritative alternative. It owns **every direct binding on one
organization, cloud or project**, as a map from role ID to the users and user groups that hold it. It is
the Terraform form of `anyscale role-binding policy generate` and `apply`. Use it when one team owns all
access to a scope; use `anyscale_iam_role_grant` when several owners share it.

- **An apply revokes every direct binding the policy does not name**, including ones Anyscale grants
  itself, such as the owner binding a project or cloud creator receives. The first apply on a scope
  that already has bindings is no exception. When the scope ID and every role and principal are known
  at plan time, the plan dry-runs the policy: a refusal an apply would report fails the plan instead,
  and a create warns naming each grant the first apply revokes — read it before you apply. A policy
  aimed at a project or cloud this same configuration creates is first checked at apply, not plan.
- **Bindings on a parent scope are not part of the policy** and are never touched.
- **Name your own binding.** Anyscale refuses a policy that revokes the identity Terraform runs as, so if
  that identity holds a direct binding on the scope, list it under its role (see the
  [example](../resources/iam_role_binding.md#example-usage)). An identity cannot grant itself a role,
  so one that reaches the scope through a group needs nothing named.
- **Never manage one scope with this resource and `anyscale_iam_role_grant` or a CLI `policy apply`.** Each
  revokes or re-creates the other's bindings on every apply.
- **Destroy revokes everything Anyscale allows.** It keeps the identity's own bindings and any frozen
  binding, and warns naming what it kept; the destroy plan lists what it will revoke and keep. An
  organization policy can hold owner groups, so read that plan before destroying one.
- **Import** with `<scope_type>/<scope_id>`, for example `project/prj_...`.
- Like `anyscale_iam_role_grant`, it needs a [converted organization](#organization-access-mode).

### Directory-synced groups

Directory-synced (SCIM) groups can be bound; unlike membership, nothing here fights the identity
provider.

~> **Do not revoke your own way in.** Anyscale refuses every role-binding write that names the
organization admin group - the platform owns its bindings - and refuses revoking the provider's own
direct binding. It does **not** stop a
configuration from removing a *group* binding that gives the Terraform identity its permission to manage
access. If the identity depends on a group binding you destroy, later applies fail until someone restores
it in the console. Keep the grant that the Terraform identity relies on out of any configuration you might
destroy, or bind that identity directly.

### Role and scope must make sense together

The API does not check that a role suits the scope: binding an organization-only role to a cloud is
accepted and has no effect. Roles are referenced by ID, so renaming a role never replaces a binding.
Whether the roles you bind are enforced depends on Anyscale's rollout of them.

### Diagnostics

| Message | Meaning |
|---|---|
| "Role bindings are not enabled" | The feature flag is off for your organization; contact support@anyscale.com. During a refresh, state is kept unchanged. |
| "Role Grant Already Exists" | The principal already holds that role on that scope; the message names the existing binding's import ID. |
| "Organization Not Converted to Role Bindings" | The organization is not converted. See [Organization access mode](#organization-access-mode). |
| "Role Is Archived" | The role cannot be newly assigned; pick another role. |
| "Organization Admin Group Is Managed by Anyscale" | The platform owns the organization admin group's role bindings; remove the group (or the binding) from the grant or role-binding configuration. |
| "Cannot Grant a Role to the Provider's Own Identity" | Bind a user group the identity belongs to, or apply with a different identity. |
| "Cannot Revoke the Provider's Own Role Binding" | Revoke it in the console or with a different identity, or run `terraform state rm`. |
| Binding disappears from state | It was revoked outside Terraform, or its group was deleted; the next plan recreates it. |
| "Role Binding Not Readable" | The cloud or project was deleted outside Terraform, or access to it was lost. State is kept; run `terraform state rm` for the binding if the scope is gone. |

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
