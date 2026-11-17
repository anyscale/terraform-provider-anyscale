# Step 3: grant roles with anyscale_iam_role_grant.
#
# A scope is where a role applies: one project, one cloud, or the
# organization. Each grant is one role, for one principal, on one
# scope. Grants are ADDITIVE: Terraform manages only the grants you
# declare and never removes anyone else's.

data "anyscale_cloud" "this" {
  name = var.cloud_name
}

# A project for the grants. Creating it gives the identity running
# Terraform the Admin role on it.
resource "anyscale_project" "shared" {
  name     = "${var.name_prefix}-shared"
  cloud_id = data.anyscale_cloud.this.id
}

# Project scope: every group member can read the project.
resource "anyscale_iam_role_grant" "team_reads_project" {
  user_group_id = anyscale_iam_user_group.team.id
  role_id       = data.anyscale_iam_role.project_reader.id
  project_id    = anyscale_project.shared.id

  # A grant cannot be edited: changing any argument replaces it. This
  # creates the new grant before removing the old one, so the group is
  # never left without access while role_id changes.
  lifecycle {
    create_before_destroy = true
  }
}

# Cloud scope: every group member can use the cloud.
resource "anyscale_iam_role_grant" "team_uses_cloud" {
  user_group_id = anyscale_iam_user_group.team.id
  role_id       = data.anyscale_iam_role.cloud_collaborator.id
  cloud_id      = data.anyscale_cloud.this.id
}

# Organization scope, off by default: the role applies across the
# whole organization.
data "anyscale_organization" "current" {}

resource "anyscale_iam_role_grant" "team_reads_billing" {
  count = var.grant_organization_role ? 1 : 0

  user_group_id   = anyscale_iam_user_group.team.id
  role_id         = data.anyscale_iam_role.billing_reader.id
  organization_id = data.anyscale_organization.current.id
}
