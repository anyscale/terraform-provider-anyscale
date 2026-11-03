# Organization-wide resources. Everything here is off by default (count = 0) and switched on by a
# variable, because each one either sends real email, needs a real member, or needs an
# organization feature that is not on everywhere. See README.md for what each gate needs.

# --- anyscale_organization_invitation (gated by invite_email) -----------------------------------
# Sends a real invitation email. Every invitation grants default collaborator access on
# acceptance; the invitations API cannot set another role up front.
resource "anyscale_organization_invitation" "new_member" {
  count = var.invite_email != "" ? 1 : 0

  email = var.invite_email
}

# --- anyscale_organization_user (gated by org_user_email) ---------------------------------------
# Manages membership only, keyed by email. Declaring an existing member adopts them with no API
# call, and destroying it removes nobody. An email that is not a member yet gets a real
# invitation, which destroy cancels if it is still pending.
resource "anyscale_organization_user" "member" {
  count = var.org_user_email != "" ? 1 : 0

  email = var.org_user_email
}

data "anyscale_organization_user" "member" {
  count = var.org_user_email != "" ? 1 : 0

  email = anyscale_organization_user.member[0].email
}

# --- anyscale_user_group + anyscale_user_group_members (gated by enable_user_groups) ------------
# Beta, and enabled per organization. The members list is authoritative: anyone in the group who
# is not listed is removed. Each email must belong to an accepted member.
resource "anyscale_user_group" "team" {
  count = var.enable_user_groups ? 1 : 0

  name = "${var.name_prefix}-team"
}

resource "anyscale_user_group_members" "team" {
  count = var.enable_user_groups ? 1 : 0

  user_group_id = anyscale_user_group.team[0].id
  members       = var.user_group_member_emails
}

data "anyscale_user_group" "team" {
  count = var.enable_user_groups ? 1 : 0

  # Read after the members are written, so the data source shows them.
  id = anyscale_user_group_members.team[0].user_group_id
}

data "anyscale_user_groups" "all" {
  count = var.enable_user_groups ? 1 : 0

  depends_on = [anyscale_user_group.team]
}

# --- anyscale_role_binding (gated by enable_role_binding) ---------------------------------------
# Beta. Only organizations converted to role bindings accept it; others answer 409. Gives every
# member of the group the built-in project_viewer role on Cloud B's project.
resource "anyscale_role_binding" "team_project_viewer" {
  count = var.enable_role_binding ? 1 : 0

  user_group_id = anyscale_user_group.team[0].id
  role_id       = data.anyscale_role.project_viewer[0].id
  project_id    = anyscale_project.b.id
}
