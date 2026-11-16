# Organization-wide resources, all off by default (count = 0) and enabled by a variable: each
# sends real email, needs a real member, or needs an organization feature. See README.md.

# --- anyscale_iam_organization_invitation (gated by invite_email) -----------------------------------
# Sends a real invitation email. Every invitation grants default collaborator access on
# acceptance; the invitations API cannot set another role up front.
resource "anyscale_iam_organization_invitation" "new_member" {
  count = var.invite_email != "" ? 1 : 0

  email = var.invite_email
}

# --- anyscale_iam_organization_user (gated by org_user_email) ---------------------------------------
# Manages membership only, keyed by email. Declaring an existing member adopts them with no API
# call, and destroying it removes nobody. An email that is not a member yet gets a real
# invitation, which destroy cancels if it is still pending.
resource "anyscale_iam_organization_user" "member" {
  count = var.org_user_email != "" ? 1 : 0

  email = var.org_user_email
}

data "anyscale_iam_organization_user" "member" {
  count = var.org_user_email != "" ? 1 : 0

  email = anyscale_iam_organization_user.member[0].email
}

# --- anyscale_iam_user_group + anyscale_iam_user_group_members (gated by enable_user_groups) --------
# Beta, and enabled per organization. The members list is authoritative: anyone in the group who
# is not listed is removed. Each email must belong to an accepted member.
resource "anyscale_iam_user_group" "team" {
  count = var.enable_user_groups ? 1 : 0

  name = "${var.name_prefix}-team"
}

resource "anyscale_iam_user_group_members" "team" {
  count = var.enable_user_groups ? 1 : 0

  user_group_id = anyscale_iam_user_group.team[0].id
  members       = var.user_group_member_emails
}

data "anyscale_iam_user_group" "team" {
  count = var.enable_user_groups ? 1 : 0

  # Read after the members are written, so the data source shows them.
  id = anyscale_iam_user_group_members.team[0].user_group_id
}

data "anyscale_iam_user_groups" "all" {
  count = var.enable_user_groups ? 1 : 0

  depends_on = [anyscale_iam_user_group.team]
}

# --- anyscale_iam_role_grant (gated by enable_role_binding) ---------------------------------------
# Beta. Only organizations converted to role bindings accept it; others answer 409. Additive: each
# grant adds one role for one principal on one scope and never removes other grants.
# Every member of the group gets Project reader on Cloud B's project...
resource "anyscale_iam_role_grant" "team_project_reader" {
  count = var.enable_role_binding ? 1 : 0

  user_group_id = anyscale_iam_user_group.team[0].id
  role_id       = data.anyscale_iam_role.project_reader[0].id
  project_id    = anyscale_project.b.id

  # A grant cannot be edited; this keeps the group's access while role_id changes.
  lifecycle {
    create_before_destroy = true
  }
}

# ...and Cloud collaborator on Cloud B itself.
resource "anyscale_iam_role_grant" "team_cloud_b" {
  count = var.enable_role_binding ? 1 : 0

  user_group_id = anyscale_iam_user_group.team[0].id
  role_id       = data.anyscale_iam_role.cloud_collaborator[0].id
  cloud_id      = anyscale_cloud.b.id
}

# A single user instead of a group (also gated by org_user_email). Takes the data source's user_id
# (usr_...), not its id. The member must not be the identity running Terraform: an identity cannot
# grant itself a role.
resource "anyscale_iam_role_grant" "member_project_reader" {
  count = var.enable_role_binding && var.org_user_email != "" ? 1 : 0

  user_id    = data.anyscale_iam_organization_user.member[0].user_id
  role_id    = data.anyscale_iam_role.project_reader[0].id
  project_id = anyscale_project.b.id
}

# --- anyscale_iam_role_binding (gated by enable_role_binding) -------------------------------------
# Beta. Authoritative over every direct binding on Cloud A's project: an apply revokes any binding
# that roles does not name. Never manage the same scope with anyscale_iam_role_grant too.
resource "anyscale_iam_role_binding" "project_a" {
  count = var.enable_role_binding ? 1 : 0

  project_id = anyscale_project.a.id

  roles = {
    # Creating project.a gave the identity running Terraform the Admin role on it. The API refuses
    # to revoke an identity's own binding, so the policy names it.
    (data.anyscale_iam_role.admin[0].id) = {
      user_ids = [data.anyscale_user.current.id]
    }
    (data.anyscale_iam_role.project_writer[0].id) = {
      user_group_ids = [anyscale_iam_user_group.team[0].id]
    }
  }
}
