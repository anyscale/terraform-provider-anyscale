---
page_title: "RBAC: Roles Across Organizations, Clouds, and Projects"
subcategory: "Concepts & Reference"
description: |-
  Managing access with this provider, for a first-time administrator and for reference: the grant model and prerequisites, baseline access for everyone who joins, team groups, project-scoped access, onboarding and offboarding, adopting existing access, organization access mode, user groups, role grants and role bindings, legacy RBAC deprecation, and which resource manages what.
---

# RBAC: Roles Across Organizations, Clouds, and Projects

Anyscale access control spans three scopes: organization, cloud, and project. This guide starts
with a progressive walkthrough - understand the model, give joining members a baseline, give a
team scoped access, keep access correct as people join and leave, adopt access that already
exists - and then covers each resource, its constraints, and its error messages in detail.

## The model

Access is a set of **grants**. Each grant gives one **role** - a named set of permissions, such as
`Project reader` - to one **principal** on one **scope**:

| Term | What it is | ID |
|---|---|---|
| **Role** | A named set of permissions, such as `Project reader` | `rol_...` |
| **Principal** | Who holds it: a **user group** or a single **user** | `ug_...` / `usr_...` |
| **Scope** | Where it applies: one **project**, one **cloud**, or the **organization** | `prj_...` / `cld_...` / `org_...` |
| **Grant** | One role, for one principal, on one scope. The API calls it a *role binding* | `rb_...` |

Two resources write grants, and you never use both on the same scope:

| Resource | Manages | Removes grants it did not create? |
|---|---|---|
| [`anyscale_iam_role_grant`](../resources/iam_role_grant.md) | One grant | **No.** Additive. |
| [`anyscale_iam_role_binding`](../resources/iam_role_binding.md) | **Every** direct grant on one scope | **Yes.** Anything not listed is revoked. |

**Membership is separate from access.** In a converted organization, joining grants a person
nothing: a new member holds no role until an admin grants one, usually to a group the person
belongs to - most broadly the All Users group every member joins automatically. The
`owner`/`collaborator` base roles belong to the legacy model; see
[Organization access mode](#organization-access-mode).

For a runnable, end-to-end tutorial of every RBAC resource, see
[`examples/resources/role_binding_workflow`](https://github.com/anyscale/terraform-provider-anyscale/tree/main/examples/resources/role_binding_workflow);
for the unconverted-organization equivalent,
[`examples/resources/organization_user_workflow`](https://github.com/anyscale/terraform-provider-anyscale/tree/main/examples/resources/organization_user_workflow).

## Which resource manages what

| Scope | Resource | Manages |
|---|---|---|
| Organization membership | [`anyscale_iam_organization_user`](../resources/iam_organization_user.md) | Whether the user is under Terraform management. It cannot create a member - people join by invitation - but declaring it adopts an existing member with no API call. Destroying it removes nobody: it cancels only a still-pending invitation this resource itself sent. |
| Organization role | [`anyscale_organization_user_role`](../resources/organization_user_role.md) | One user's `base_role` and `deny_roles` in the organization. Authoritative over that one user's role - not over who is a member. |
| User groups (Beta) | [`anyscale_iam_user_group`](../resources/iam_user_group.md), [`anyscale_iam_user_group_members`](../resources/iam_user_group_members.md) | A group's name, and its **entire** member list. Not what the group may access - see role bindings, next row. See [User groups (Beta)](#user-groups-beta). |
| Role grants (Beta) | [`anyscale_iam_role_grant`](../resources/iam_role_grant.md), [`anyscale_iam_role_binding`](../resources/iam_role_binding.md), [`anyscale_iam_role`](../data-sources/iam_role.md) and [`anyscale_iam_role_binding`](../data-sources/iam_role_binding.md) data sources | One role held by one user group or one user on one cloud, project, or the organization (`anyscale_iam_role_grant`), or every direct role binding on a scope (`anyscale_iam_role_binding`). **Converted organizations only** - see [Organization access mode](#organization-access-mode). See [Role bindings (Beta)](#role-bindings-beta). |

## Before you start

1. **Know your organization's access mode.** Writes that work depend on it - role grants in a
   converted organization, `anyscale_organization_user_role` in an unconverted one. An admin
   sees and converts the mode (one-way) in **Users & IAM**. See
   [Organization access mode](#organization-access-mode).
2. **Beta resources and feature flag.** User groups and role grants/bindings are Beta, and role
   bindings are enabled per organization: without the flag every call fails with "Role bindings
   are not enabled". Contact support@anyscale.com. See
   [User groups (Beta)](#user-groups-beta) and [Role bindings (Beta)](#role-bindings-beta).
3. **Apply as an identity that can manage access** - an organization owner, or a holder of the
   Admin role on each scope you change. Otherwise writes fail with "Not Allowed to Manage Access".
4. **People must already be members.** Group members must have accepted an invitation; invite
   first with [`anyscale_iam_organization_invitation`](../resources/iam_organization_invitation.md).
5. **Terraform 1.10 or later**, with credentials from `anyscale login` or `ANYSCALE_CLI_TOKEN`.
   The token decides which organization a run changes.
6. **Not an Anyscale control plane in Azure.** There, access is managed with Azure RBAC and the
   write resources here fail at plan with "RBAC Is Managed in Azure" - see
   [Organization access mode](#organization-access-mode).

## Start here: baseline access, then a team

This configuration gives everyone who joins the organization read-only visibility of it, then
gives one team write access to one project. Replace the names and emails with your own; every
resource and argument below is real, and the configuration then applies as written.

```hcl
terraform {
  required_providers {
    anyscale = {
      source = "anyscale/anyscale"
    }
  }
}

# --- 1. Baseline: everyone who joins can read the organization. ---
#
# A converted organization grants a new member nothing until an admin
# grants a role. The "All Users" group holds every member automatically,
# so grant the baseline to it. The user group data source matches `name`
# exactly; the platform group is named "All Users" unless a group of yours
# already held that name when the organization converted, in which case
# find the actual name with the anyscale_iam_user_groups data source.
data "anyscale_iam_user_group" "all_users" {
  name = "All Users"
}

data "anyscale_organization" "current" {}

# The organization-scope read-only role, for "everyone who joins can at
# least see the clouds and projects". Check what each role allows in the
# console (Users & IAM); grant the least-privileged role that fits every
# member. A narrower baseline is also possible: skip the organization
# grant and grant per-cloud roles instead, such as "Cloud read-only" or
# "Cloud project viewer" on individual clouds.
data "anyscale_iam_role" "reader" {
  name = "Reader"
}

resource "anyscale_iam_role_grant" "all_users_baseline" {
  user_group_id   = data.anyscale_iam_user_group.all_users.id
  role_id         = data.anyscale_iam_role.reader.id
  organization_id = data.anyscale_organization.current.id
}

# The cloud the team works in. An organization-scope grant reaches every
# cloud and project in the organization; this data source is for the
# narrower, team-specific grants below.
data "anyscale_cloud" "this" {
  name = "my-cloud"
}

# --- 2. The team: a group and its full member list. ---
resource "anyscale_iam_user_group" "ml_platform" {
  name = "ml-platform"
}

# AUTHORITATIVE: anyone in the group but not in this list is removed,
# including people added in the Anyscale console, on every apply.
# Everyone here must already be an organization member - invite people
# first; see "Adding and removing people" below.
resource "anyscale_iam_user_group_members" "ml_platform" {
  user_group_id = anyscale_iam_user_group.ml_platform.id

  members = [
    "dev1@example.com",
    "dev2@example.com",
  ]
}

# --- 3. What the team may access: writer on one project, nothing wider. ---
resource "anyscale_project" "shared" {
  name     = "ml-platform-shared"
  cloud_id = data.anyscale_cloud.this.id
}

data "anyscale_iam_role" "project_writer" {
  name = "Project writer"
}

# Additive: this grants exactly one role on one project and never removes
# other grants - the baseline above and any grant made elsewhere are safe.
resource "anyscale_iam_role_grant" "ml_platform_writes_shared" {
  user_group_id = anyscale_iam_user_group.ml_platform.id
  role_id       = data.anyscale_iam_role.project_writer.id
  project_id    = anyscale_project.shared.id

  # A grant cannot be edited: changing any argument replaces it. This
  # creates the replacement before removing the old one, so the group is
  # never briefly without access.
  lifecycle {
    create_before_destroy = true
  }
}
```

Three properties of this shape are deliberate:

- **Grants go to groups, not persons.** Team membership changes by editing `members`; no grant
  changes when a person joins or leaves.
- **Scope per grant.** A project role the group holds on one project gives it nothing on other
  projects, other clouds, or the organization. Grant the narrowest scope the access needs.
- **One resource, one grant, additive.** `terraform destroy` of everything above revokes exactly
  the two declared grants, empties the group, deletes it and the project - and nobody's other
  access, in this configuration or outside it, is touched.

To read back who currently holds what, the
[`anyscale_iam_role_binding`](../data-sources/iam_role_binding.md) data source lists every direct
grant on a scope, whoever made it:

```hcl
data "anyscale_iam_role_binding" "shared" {
  project_id = anyscale_iam_role_grant.ml_platform_writes_shared.project_id
}
```

## Adding and removing people

**Add a person** (a converted organization):

1. Invite them with [`anyscale_iam_organization_invitation`](../resources/iam_organization_invitation.md)
   - one resource per person; see
   [Inviting people](#inviting-people-limits-single-sign-on-directory-sync) for rate limits,
   single sign-on, and directory sync, which decide whether Terraform can invite at all.
2. Wait for the emailed invitation to be accepted. The invitation's `status` attribute (`pending`,
   `accepted`, `expired`) shows where the person is; a plan while it is pending shows no diff.
3. Add their email to the team group's `members`. The plan shows one in-place update; after the
   apply the person holds every role the group holds, plus the All Users baseline. Nothing else
   in the configuration changes.

**Remove a person** from a team: delete their email from `members`. They leave the group on the
next apply and lose every role that group held - assuming nothing else grants it, which is
[additive grants](#start-here-baseline-access-then-a-team) being a property of what you declare,
not a guarantee about the world out of band. Removing a person from the **organization** is not
possible with this provider: destroying any membership resource never evicts anyone - see
[Inviting people](#inviting-people-limits-single-sign-on-directory-sync).

**Change what someone may do:** edit the grants, not the people. A new role for the group is a new
`anyscale_iam_role_grant` (or a second role ID in an `anyscale_iam_role_binding` policy);
a narrower one replaces the old grant with `create_before_destroy`, as in the example above.
An individual's organization-level base role is the one role you can give a person directly in
an unconverted organization - see
[Managing a team without groups](#managing-a-team-without-groups).

**Verify.** `terraform plan` reports grants changed outside Terraform (an additive grant shows
as a create; a grant revoked out of band shows as a create; an authoritative
`anyscale_iam_role_binding` shows an update that would revoke the interloper). Read current
access with the [`anyscale_iam_role_binding`](../data-sources/iam_role_binding.md) data source, or
`anyscale role-binding policy generate` from the CLI.

## Adopting access that already exists

Do not create a duplicate of a grant that already exists - the API refuses with "Role Grant
Already Exists", and the message names the import ID to adopt it instead:

```text
<scope_type>/<scope_id>/<principal_type>/<principal_id>/<role_id>
```

where `scope_type` is `organization`, `cloud` or `project` and `principal_type` is `user` or
`user_group`. The binding's own ID (`rb_...`) works too, and an `import` block with `for_each`
adopts many at once (Terraform 1.7 or later):

```hcl
import {
  to = anyscale_iam_role_grant.ml_platform_writes_shared
  id = "project/prj_.../user_group/ug_.../rol_..."
}
```

The common case is an organization just converted to role bindings: conversion creates bindings
for the access people already had, so import those rather than re-creating them. The full
conversion sequence, and which bindings conversion creates, is in
[Organization access mode](#organization-access-mode).

The alternative is to take over **every** direct grant on one scope with
[`anyscale_iam_role_binding`](../resources/iam_role_binding.md), imported by
`<scope_type>/<scope_id>` - but its first apply revokes every grant the policy does not name,
including grants made in the console. Read
[Managing every grant on a scope](#managing-every-grant-on-a-scope-anyscale_iam_role_binding)
before doing that.

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
Role bindings are also behind a feature flag (see [Role bindings (Beta)](#role-bindings-beta)). A
new organization starts converted; an existing one stays unconverted until an admin converts it.

**Azure control planes.** On an Anyscale control plane in Azure (`console.azure.anyscale.com`), access
is managed with Azure role-based access control in the Azure portal, in either mode. When the
provider's API URL is one of these hosts, creating, changing or importing `anyscale_iam_role_grant`,
`anyscale_iam_role_binding`, `anyscale_iam_user_group`, `anyscale_iam_user_group_members` or
`anyscale_organization_user_role`, reading the `anyscale_iam_role`, `anyscale_iam_role_binding`,
`anyscale_iam_user_group` or `anyscale_iam_user_groups` data sources, and setting
`anyscale_cloud.auto_add_user` fail at plan with "RBAC Is Managed in Azure". Destroying one, or a
plan with no changes, still works. A custom or proxied URL in front of an Azure control plane is not
detected.

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
   existing binding instead of creating one. Creating a grant that already exists fails with "Role Grant
   Already Exists". You do not need to look up the binding ID: import by what the binding grants,
   `<scope_type>/<scope_id>/<principal_type>/<principal_id>/<role_id>`, where `scope_type` is `organization`,
   `cloud` or `project`, and `principal_type` is `user` or `user_group`. Import fails, and creates nothing,
   if no such binding exists. An `import` block with `for_each` imports many at once and needs Terraform 1.7
   or later.

```hcl
locals {
  grants = {
    analysts = {
      group = anyscale_iam_user_group.analysts.id
      role  = data.anyscale_iam_role.project_reader.id
    }
  }
}

resource "anyscale_iam_role_grant" "project_reader" {
  for_each      = local.grants
  user_group_id = each.value.group
  role_id       = each.value.role
  project_id    = var.project_id
}

import {
  for_each = local.grants
  to       = anyscale_iam_role_grant.project_reader[each.key]
  id       = "project/${var.project_id}/user_group/${each.value.group}/${each.value.role}"
}
```

The binding ID (`rb_...`) also works as the import ID.

Which bindings conversion creates depends on the option chosen in the migration wizard, so expect to
find some grants missing and create those normally.

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
  one. Neither resource can remove someone's access.

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
| [`anyscale_iam_user_group`](../data-sources/iam_user_group.md) data source | Reads one group by `id` or `name`, including directory-synced groups and the All Users group (the All Service Accounts group is hidden and cannot be read). |
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

Anyscale manages these groups, and refuses changes to them whether or not the organization is
[converted](#organization-access-mode):

- **All Users** holds every member: Anyscale adds each one as they join, and members cannot be
  added or removed by hand. Do not manage it with `anyscale_iam_user_group_members`: it would show
  a perpetual diff, then fail with a 409. Granting it roles is how you give everyone who joins a
  baseline - see [Start here](#start-here-baseline-access-then-a-team). If a group of yours
  already held this name, Anyscale's group takes a number suffix, such as `All Users 2`.
- **All Service Accounts** holds every service account. Its members cannot be added or removed
  either, and the group cannot be renamed or deleted. It is hidden: the groups data sources do not
  list it and it cannot be looked up or named in configuration. Do not manage it with
  `anyscale_iam_user_group_members` either.
- The **organization admin group**, named **Super Admins**, cannot be deleted or renamed, its last
  member cannot be removed, and service accounts cannot join it. Anyscale owns its grants: see
  [Keeping your own access](#keeping-your-own-access).

### Directory-synced groups are read-only here

Groups created by directory sync (SCIM) are owned by your identity provider. The resources refuse to
manage them: importing one, or declaring members for it, fails with an error. Read them with the
`anyscale_iam_user_group` data source (its `source` attribute is `scim`) and reference their IDs from
other configuration. Groups created before April 2026 may report no `source`; those are treated as
managed and can be imported.

### Destroying

Destroying `anyscale_iam_user_group_members` removes every member and leaves the group. Destroying
`anyscale_iam_user_group` removes the members first, then deletes the group; deleting a group alone
does not revoke access that was derived from its membership.

### Cost of a refresh

Reading membership fetches the whole organization's group memberships once per
`anyscale_iam_user_group_members`, so refreshes scale with the number of groups you manage.

See [`examples/resources/anyscale_iam_user_group_members`](https://github.com/anyscale/terraform-provider-anyscale/tree/main/examples/resources/anyscale_iam_user_group_members)
for a runnable configuration.

### Managing a team without groups

Groups are one way to model a team. When you only need each person's *organization role*, which
groups do not manage, a `locals` map keyed by email can drive both the invitation loop and the role
loop. `anyscale_organization_user_role` works in
[unconverted organizations](#organization-access-mode) only:

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

`anyscale_organization_user_role` requires an already-accepted member, so the role loop above
cannot apply in the same run as the invitation loop for someone who has not yet accepted - apply
it in a second run once invitations are accepted. See the full staged workflow in
[`examples/resources/organization_user_workflow`](https://github.com/anyscale/terraform-provider-anyscale/tree/main/examples/resources/organization_user_workflow).

## Legacy RBAC deprecation

The resources and attributes that manage or report access through collaborator and policy records
are deprecated. They keep working in an unconverted organization, a converted organization refuses
or ignores them, and they will be removed in a future release once every organization can convert.
None is available on an Azure control plane (see [Organization access mode](#organization-access-mode)).

| Deprecated | In a converted organization | Use instead |
|---|---|---|
| `anyscale_organization_user_role` | Writes fail with "Organization Uses Role Bindings" | `anyscale_iam_role_grant` |
| `anyscale_cloud.auto_add_user` | Setting it fails | An `anyscale_iam_role_grant` of the `Cloud collaborator` role on the cloud for the All Users group - in a converted organization a joining member gets this from nowhere |
| `auto_add_user` on the `anyscale_cloud` and `anyscale_clouds` data sources | No longer applies | Same |
| `collaborators` on the `anyscale_project` data source | Null | [`anyscale_iam_role_binding`](../data-sources/iam_role_binding.md) data source with `project_id`; reading needs IAM management on the project |
| `permission_level`, `base_role` and `additional_roles` on the `anyscale_iam_organization_user(s)` data sources; `organization_permission_level` on the `anyscale_user` data source | A snapshot from before conversion | [`anyscale_iam_role_binding`](../data-sources/iam_role_binding.md) data source with `organization_id`; reading needs IAM management on the organization |

Terraform warns when a configuration uses one: setting a deprecated resource or attribute, or
referencing a deprecated read-only attribute, for example in an output. For a referenced read-only
attribute, Terraform 1.15 prints the provider's message and Terraform 1.10 a generic "The attribute
... is deprecated". To move a converted organization off them, follow the steps in
[Organization access mode](#organization-access-mode).

## Role bindings (Beta)

~> **Beta, behind a feature flag.** Role bindings are a Beta feature. Beta resources can change
incompatibly between provider versions, so pin the provider version, and read the changelog before
upgrading; every breaking change is listed there. Anyscale enables them per organization while
customers migrate: contact support@anyscale.com for access. Without the flag, every call fails with
"Role bindings are not enabled". The organization must also be [converted](#organization-access-mode).

`anyscale_iam_role_grant` is additive: it grants only what you declare and never removes other
grants. `anyscale_iam_role_binding` is authoritative over every direct grant on one scope. Each API
object is a role binding with an `rb_` ID, and one `anyscale_iam_role_grant` creates exactly one, so
an `rb_` ID traced from the console or API corresponds to one grant resource.

`anyscale_iam_role_grant` grants **one role to one user group or one user on one scope** - exactly one of
`user_group_id` or `user_id`, and exactly one of `cloud_id`,
`project_id`, or `organization_id`. Look up the role with the [`anyscale_iam_role`](../data-sources/iam_role.md)
data source, which resolves a role by its exact, case-sensitive `name`. Built-in role names include,
for example, `Project reader`, `Project writer`, `Project owner`, `Cloud writer`, `Cloud owner`,
`Admin` and `Organization collaborator`. Some organizations still list `Admin` under its former name,
`Organization owner`; the data source resolves either name to the same role.

```hcl
data "anyscale_iam_role" "project_reader" {
  name = "Project reader" # exact, case-sensitive; archived roles are not found
}

resource "anyscale_iam_user_group" "analysts" {
  name = "analysts"
}

resource "anyscale_iam_user_group_members" "analysts" {
  user_group_id = anyscale_iam_user_group.analysts.id
  members       = ["alice@example.com", "bob@example.com"] # must already be organization members
}

resource "anyscale_iam_role_grant" "analysts_project_reader" {
  user_group_id = anyscale_iam_user_group.analysts.id
  role_id       = data.anyscale_iam_role.project_reader.id
  project_id    = var.project_id # exactly one of cloud_id / project_id / organization_id
}
```

Prefer groups: bind the role to the group once, then add or remove people by editing `members`; every
member gets the role.

New to role bindings? [`examples/resources/role_binding_workflow`](https://github.com/anyscale/terraform-provider-anyscale/tree/main/examples/resources/role_binding_workflow) is a runnable, step-by-step
walkthrough of every role-binding resource and data source, with the expected output.

### Granting a user, not a group

Binding a user takes the user's `usr_...` ID, not their email. Look it up with the
[`anyscale_iam_organization_user`](../data-sources/iam_organization_user.md) data source and use its **`user_id`**
attribute, not `id` (that is the identity ID, a different value):

```hcl
data "anyscale_iam_organization_user" "alice" {
  email = "alice@example.com"
}

resource "anyscale_iam_role_grant" "alice_project_reader" {
  user_id    = data.anyscale_iam_organization_user.alice.user_id
  role_id    = data.anyscale_iam_role.project_reader.id
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

`terraform destroy` plans the configuration first, and that plan reads every data source in it under
the identity running the destroy - so destroy with an identity that can read each data source the
configuration uses, such as role lookups.

### Managing every grant on a scope: `anyscale_iam_role_binding`

`anyscale_iam_role_binding` is the authoritative alternative. It owns **every direct binding on one
organization, cloud or project**, as a map from role ID to the users and user groups that hold it. It is
the Terraform form of `anyscale role-binding policy generate` and `apply`. Use it when one team owns all
access to a scope; use `anyscale_iam_role_grant` when several owners share it.

- **An apply revokes every direct binding the policy does not name**, including ones Anyscale grants
  itself, such as the Admin binding a project or cloud creator receives. The first apply on a scope
  that already has bindings is no exception. When the scope ID and every role and principal are known
  at plan time, the plan dry-runs the policy: a refusal an apply would report fails the plan instead,
  and a create warns naming each grant the first apply revokes — read it before you apply. A policy
  aimed at a project or cloud this same configuration creates is first checked at apply, not plan.
- **Bindings on a parent scope are not part of the policy** and are never touched.
- **Name your own binding.** Anyscale refuses a policy that revokes the identity Terraform runs as, so if
  that identity holds a direct binding on the scope, list it under its role (see the
  [example](../resources/iam_role_binding.md#example-usage)). Name the role the identity actually holds
  on that scope, not a fixed one: today a project or cloud creator holds the `Admin` role on it;
  scopes created earlier may carry
  `Project owner` or `Cloud owner` instead. Check with `anyscale role-binding policy generate` when unsure. An identity cannot grant
  itself a role, so one that reaches the scope through a group needs nothing named.
- **Never manage one scope with this resource and `anyscale_iam_role_grant` or a CLI `policy apply`.** Each
  revokes or re-creates the other's bindings on every apply.
- **Destroy revokes everything Anyscale allows.** It keeps the identity's own bindings and any frozen
  binding, and warns naming what it kept; the destroy plan lists what it will revoke and keep. An
  organization policy can hold group bindings other members rely on, so read that plan before
  destroying one.
- **Import** with `<scope_type>/<scope_id>`, for example `project/prj_...`.
- Like `anyscale_iam_role_grant`, it needs a [converted organization](#organization-access-mode).

### Directory-synced groups

Directory-synced (SCIM) groups can be named in grants and `anyscale_iam_role_binding` policies. The
identity provider keeps owning the group's membership.

### Keeping your own access

~> **Do not revoke your own way in.** Anyscale refuses adding a role to the organization admin
group and refuses revoking any of its bindings. A converted organization freezes those bindings: an
`anyscale_iam_role_binding` apply that would revoke one fails with "Policy Would Revoke a Frozen
Role Binding" (name the binding in `roles` to keep it), and a destroy keeps it; the destroy plan
lists what it keeps. Revoking the provider's own direct binding is refused too. Removing a *group*
binding that gives the Terraform identity its permission to manage access is not refused: if the
identity depends on a group binding you destroy, later applies fail until someone restores it in the
console. Keep the grant the Terraform identity relies on out of any configuration you might destroy,
or bind that identity directly.

### Role and scope must make sense together

The API does not check that a role suits the scope: binding an organization-only role to a cloud is
accepted and has no effect. Roles are referenced by ID, so renaming a role never replaces a binding.

### Diagnostics

| Message | Meaning |
|---|---|
| "Role bindings are not enabled" | The feature flag is off for your organization; contact support@anyscale.com. During a refresh, state is kept unchanged. |
| "Role Grant Already Exists" | The principal already holds that role on that scope; the message names the existing binding's import ID. |
| "Organization Not Converted to Role Bindings" | The organization is not converted. See [Organization access mode](#organization-access-mode). |
| "Role or User Group Not Found" | An `anyscale_iam_role_binding` policy names a role or group that does not exist (a deleted group is reported this way). A group that exists but that this identity cannot see is reported as "Not Allowed to Manage Access", not as missing. |
| "User Group Name Already Exists" | Group names match ignoring letter case and spacing, so the message may quote your spelling while the existing group is spelled differently. List the groups to find it, then import it. |
| "Role Is Archived" | The role cannot be newly assigned; pick another role. |
| "Not Allowed to Manage Access" | This identity does not manage IAM on the scope the write targeted. Apply with an identity that does - for example one holding the Admin role there - or ask an admin. |
| "Organization Admin Group Is Managed by Anyscale" | A role grant named the organization admin group, tried to remove one of its bindings, or a policy tried to add a role to it. The platform owns its bindings; the error says which change to make. |
| "Policy Would Revoke a Frozen Role Binding" | An `anyscale_iam_role_binding` policy would remove a binding Anyscale froze, including the organization admin group's. The error names the binding; add it to `roles` to keep it. |
| "Policy Would Revoke Your Own Role Binding" | The policy omits a direct binding this identity holds; the API never lets an identity revoke its own. Name it in `roles` to keep it, or apply with a different identity. |
| "Cannot Manage User Group" | The group exists but this identity cannot change it: it does not manage IAM on at least one scope the group's bindings reach. Nothing was changed. Use an organization admin token, or manage the group outside Terraform. |
| "Organization Is Converting to Role Bindings" | The conversion is still running and legacy writes are refused. Retry after it finishes. |
| "Cannot Grant a Role to the Provider's Own Identity" | Bind a user group the identity belongs to, or apply with a different identity. |
| "Cannot Revoke the Provider's Own Role Grant" | Revoke it in the console or with a different identity, or run `terraform state rm`. |
| Binding disappears from state | It was revoked outside Terraform, or its group, cloud or project was deleted. The next plan recreates it; for a deleted cloud or project, remove the grant from configuration instead. |
| "Role Grant Not Readable", "Role Binding Not Readable" | This identity does not manage IAM on the cloud or project, which still exists. State is kept: restore that access (or run the destroy as an identity that has it), or run `terraform state rm` for the binding. The `anyscale_iam_role_binding` data source also reports a deleted scope this way. |

## `anyscale_organization_user_role`: authority and destroy

`anyscale_organization_user_role` manages one member's `base_role` and `deny_roles` and no other
member's. `base_role` is required, with no default. A `terraform destroy` of this resource does not
revert the role:

- `base_role` is left exactly as it is, and the destroy warns, naming the member's email and the role.
- A declared `deny_roles` is cleared to empty; `deny_roles` are restrictions, so this **increases**
  that member's access. An undeclared `deny_roles` is left untouched.
- Clearing a declared `deny_roles` uses the same gated endpoint an apply to it uses, so the destroy
  can fail the same way the apply can.
- You cannot destroy your own role resource: self-modification is refused with an error.
- Changing `email` - including fixing a typo - replaces the resource; the person at the old address
  keeps the role Terraform last gave them, with no automatic cleanup.
