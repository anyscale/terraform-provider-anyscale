# Role bindings, step by step

A runnable walkthrough of Anyscale role bindings with Terraform, for someone using them for the
first time. Each `stepN_*.tf` file is one step and is commented line by line. Read them in order,
then apply the whole directory. The [RBAC guide](../../../docs/guides/rbac.md) is the reference;
this directory is the tutorial.

## The model in one minute

Access is a set of **grants**. Each grant says *who* holds *which role* *where*:

| Term | What it is | ID |
|---|---|---|
| **Role** | A named set of permissions, such as `Project reader` | `rol_...` |
| **Principal** (who) | A **user group** or a single **user** (service accounts are users) | `ug_...` / `usr_...` |
| **Scope** (where) | One **project**, one **cloud**, or the **organization** | `prj_...` / `cld_...` / `org_...` |
| **Grant** | One role, for one principal, on one scope. The API calls it a *role binding*. | `rb_...` |

Two resources write grants. Pick one per scope, never both on the same scope:

| Resource | Manages | Removes grants it did not create? | Use when |
|---|---|---|---|
| [`anyscale_iam_role_grant`](../../../docs/resources/iam_role_grant.md) | One grant | **No.** Additive. | Several teams or tools share access to a scope |
| [`anyscale_iam_role_binding`](../../../docs/resources/iam_role_binding.md) | **Every** direct grant on one scope | **Yes.** Authoritative: anything not listed is revoked. | One team owns all access to a scope |

Two data sources read: [`anyscale_iam_role`](../../../docs/data-sources/iam_role.md) turns a role
name into its ID, and [`anyscale_iam_role_binding`](../../../docs/data-sources/iam_role_binding.md)
lists every direct grant on a scope.

`anyscale_iam_role_binding` covers the grants made directly on its scope. Grants on a parent scope
(the cloud above a project, the organization above a cloud) are not part of it and are never
touched.

## Before you start

1. **Your organization uses role bindings.** New organizations do. An existing organization uses
   them once an admin converts it in **Users & IAM** (one-way). Until then every grant fails with
   "Organization Not Converted to Role Bindings".
2. **Role bindings are enabled for your organization.** They are in Beta and enabled per
   organization; without them every call fails with "Role bindings are not enabled". Contact
   support@anyscale.com.
3. **You can manage access.** Apply as an organization owner, or as an identity holding the Admin
   role on each scope you change. Otherwise writes fail with "Not Allowed to Manage Access".
4. **The people already belong to the organization.** Group members and direct grantees must have
   accepted their invitation. Invite them first with
   [`anyscale_iam_organization_invitation`](../../../docs/resources/iam_organization_invitation.md).
5. **Terraform 1.10 or later**, and credentials from `anyscale login` or `ANYSCALE_CLI_TOKEN`. The
   token decides which organization this changes.

Not on an Anyscale control plane in Azure: there, access is managed with Azure RBAC and these
resources fail at plan with "RBAC Is Managed in Azure".

## Built-in roles

Look roles up by exact, case-sensitive name with `data "anyscale_iam_role"`. A converted
organization listed these built-in roles in November 2026; your organization may also define its
own, and the list can grow.

- `Organization owner`, `Organization collaborator`. `Organization owner` is also the name of the
  Admin role a project's or cloud's creator holds on it.
- `Cloud owner`, `Cloud writer`, `Cloud collaborator`, `Cloud read-only`, `Cloud project viewer`,
  `Cloud compute config viewer`, `Cloud workload operator`
- `Project owner`, `Project writer`, `Project reader`
- `Reader`, `Viewer`, `Workspace operator`, `Job operator`, `Service operator`,
  `Compute config deployer`, `Base image deployer`, `Custom image deployer`, `Cluster data viewer`
- `Image reader`, `Image reader, no base images`, `Billing/consumption reader`

To list them yourself: `curl -H "Authorization: Bearer $ANYSCALE_CLI_TOKEN"
"https://console.anyscale.com/api/v2/roles/?include_built_in=true&count=50"`.

The API does not check that a role suits the scope: a role with only organization permissions,
granted on a cloud, is accepted and has no effect. Archived roles cannot be looked up or granted.

## The steps

| File | Step | Resources |
|---|---|---|
| [`step1_roles.tf`](step1_roles.tf) | Look up role IDs by name | `data.anyscale_iam_role` |
| [`step2_group.tf`](step2_group.tf) | Create a group and set its members | `anyscale_iam_user_group`, `anyscale_iam_user_group_members` |
| [`step3_grants.tf`](step3_grants.tf) | Grant the group roles on a project, a cloud and (optionally) the organization | `anyscale_iam_role_grant` |
| [`step4_direct_user.tf`](step4_direct_user.tf) | Grant one user a role directly (optional) | `data.anyscale_iam_organization_user`, `anyscale_iam_role_grant` |
| [`step5_binding.tf`](step5_binding.tf) | Own every grant on a second project | `anyscale_iam_role_binding` |
| [`step6_adopt_existing.tf`](step6_adopt_existing.tf) | Import grants that already exist (reference, commented out) | `import` blocks |
| [`step7_read_back.tf`](step7_read_back.tf) | List who holds what | `data.anyscale_iam_role_binding`, outputs |

## Run it

```bash
export TF_VAR_cloud_name="my-cloud"
export TF_VAR_team_member_emails='["alice@example.com","bob@example.com"]'
export TF_VAR_direct_member_email="carol@example.com"   # optional
terraform init
terraform plan
terraform apply
```

This creates two projects (`<name_prefix>-shared` and `<name_prefix>-owned`) and one group
(`<name_prefix>-team`). It changes access on the cloud you name: every group member gets
`Cloud collaborator` there. Set `TF_VAR_grant_organization_role=true` to also grant the group
`Billing/consumption reader` on the whole organization.

The first plan and apply print one warning, **"Role Binding Not Checked at Plan"**, for
`anyscale_iam_role_binding.owned`. It is expected: the project is created in the same apply, so its
ID is unknown at plan time and the plan cannot dry-run the policy. Later plans check it.

The `shared_project_grants` and `owned_project_grants` outputs map each role ID to its holders; the
`role_ids` output maps the names back. On the shared project expect the group under
`Project reader`, the direct member (if set) under `Project writer`, and you under the Admin role
(`Organization owner`), which you received by creating the project. On the owned project expect
exactly what `step5_binding.tf` lists. A real run's output (IDs shortened):

```text
role_ids = {
  "Cloud collaborator" = "rol_2nn8..."
  "Organization owner" = "rol_zdti..."
  "Project reader"     = "rol_r2sc..."
  "Project writer"     = "rol_9k8u..."
}
shared_project_grants = {
  "rol_9k8u..." = { user_group_ids = null,          user_ids = ["usr_d5g9..."] }  # direct member
  "rol_r2sc..." = { user_group_ids = ["ug_3xj8..."], user_ids = null }           # the group
  "rol_zdti..." = { user_group_ids = null,          user_ids = ["usr_bxjg..."] }  # you, the creator
}
```

The same check from the CLI: `anyscale role-binding policy generate` prints every direct grant on a
scope.

## Day-2 changes

| To | Do | What the plan shows |
|---|---|---|
| Add or remove a person | Edit `team_member_emails` | An in-place update of `anyscale_iam_user_group_members`; no grant changes |
| Change a group's role | Edit the grant's `role_id` | Replace. `create_before_destroy` adds the new grant before removing the old one |
| Move a grant to another scope or principal | Edit `project_id`, `cloud_id`, `user_group_id`... | Replace |
| Revoke a grant | Delete its `anyscale_iam_role_grant` block | Destroy; only that grant is revoked |
| Change who holds what on the owned project | Edit `roles` in `step5_binding.tf` | An in-place update; anything removed from `roles` is revoked |

A grant revoked outside Terraform shows up as a create on the next plan. On the owned project, a
grant added outside Terraform shows up as an update that revokes it.

## Things that surprise people

- **`anyscale_iam_role_binding` revokes on its first apply.** Adopting a scope that already has
  grants revokes every one you did not list, including grants made in the console. When every ID
  is known at plan time, the plan dry-runs the policy and warns naming each grant it would revoke.
  For a project created in the same apply, as here, the only existing grant is your own Admin grant,
  which the policy names.
- **Name your own grant in `anyscale_iam_role_binding`.** The API never lets an identity revoke its
  own grant, so a policy that omits it fails with "Policy Would Revoke Your Own Role Binding".
- **You cannot grant yourself a role.** `direct_member_email` and every `user_ids` entry other than
  your own existing grant must be someone else; otherwise the apply fails with "Cannot Grant a
  Role to the Provider's Own Identity". Grant a group you belong to instead.
- **Do not lock yourself out.** Removing a *group* grant that gives the Terraform identity its
  access is allowed. If you do, later applies fail with "Not Allowed to Manage Access" until an
  admin restores it.
- **A user grant takes `user_id`, not `id`.** On the `anyscale_iam_organization_user` data source,
  `id` is the identity ID; grants need `user_id` (`usr_...`).
- **Group members must have accepted their invitation.** An email that is not yet a member fails the
  apply.
- **Directory-synced (SCIM) groups** can be granted roles but not edited: read one with
  `data "anyscale_iam_user_group"` and pass its `id` as `user_group_id`.
- **The organization admin group's grants belong to Anyscale.** Granting it a role, or revoking one
  of its grants, is refused.

For every error message and what to do about it, see the guide's
[Diagnostics](../../../docs/guides/rbac.md#diagnostics) table.

## Clean up

`terraform destroy` revokes the grants, deletes the group, and deletes both projects. Expect two
warnings from `anyscale_iam_role_binding.owned`: **"Destroy Revokes Role Bindings"** at plan, listing
what it will revoke, and **"Role Bindings Kept on Destroy"**, naming your own Admin grant, which the
API never lets you revoke. The project is deleted right after, so nothing is left. Destroying a
grant on a scope you keep revokes only that grant.

This directory is tested end to end against a converted organization with
`make test-role-binding-workflow` (apply, an empty re-plan, destroy).
