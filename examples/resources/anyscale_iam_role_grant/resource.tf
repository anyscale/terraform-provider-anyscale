# Beta; Anyscale enables it per organization (contact
# support@anyscale.com). Beta resources can change incompatibly
# between provider versions. Grants one role to one user group or one
# user on exactly one of a cloud, a project, or the organization. A
# binding cannot be edited: changing any argument replaces it.
#
# Prefer groups: grant a role to a group once, then change who has
# access by editing the group's members.
variable "project_id" {
  description = "ID of an existing Anyscale project (prj_...)"
  type        = string
}

variable "org_role_name" {
  description = "Exact, case-sensitive name of a grantable role"
  type        = string
}

data "anyscale_iam_role" "project_viewer" {
  name = "project_viewer" # exact, case-sensitive
}

resource "anyscale_iam_user_group" "analysts" {
  name = "analysts"
}

# Who is in the group. AUTHORITATIVE: anyone not listed is removed from
# the group. Each email must already belong to your organization.
resource "anyscale_iam_user_group_members" "analysts" {
  user_group_id = anyscale_iam_user_group.analysts.id
  members = [
    "alice@example.com",
    "bob@example.com",
  ]
}

# What the group may do: every member gets project_viewer on this
# project.
resource "anyscale_iam_role_grant" "analysts_project_viewer" {
  user_group_id = anyscale_iam_user_group.analysts.id
  role_id       = data.anyscale_iam_role.project_viewer.id
  # exactly one of cloud_id, project_id, organization_id
  project_id = var.project_id

  # Avoid a window with no access when role_id changes.
  lifecycle {
    create_before_destroy = true
  }
}

# Directory-synced (SCIM) groups can't be managed by Terraform, but they
# can hold a role binding. Here every member of the group gets the role
# across the whole organization.
data "anyscale_iam_user_group" "okta_admins" {
  name = "okta-admins"
}

data "anyscale_organization" "current" {}

data "anyscale_iam_role" "org_role" {
  name = var.org_role_name
}

resource "anyscale_iam_role_grant" "okta_admins_org" {
  user_group_id   = data.anyscale_iam_user_group.okta_admins.id
  role_id         = data.anyscale_iam_role.org_role.id
  organization_id = data.anyscale_organization.current.id
}

# Alternative: bind a single user or service account (usr_...) with
# user_id instead of user_group_id. Use the data source's user_id, not
# its id.
data "anyscale_organization_user" "carol" {
  email = "carol@example.com"
}

resource "anyscale_iam_role_grant" "carol_project_viewer" {
  user_id    = data.anyscale_organization_user.carol.user_id
  role_id    = data.anyscale_iam_role.project_viewer.id
  project_id = var.project_id
}
