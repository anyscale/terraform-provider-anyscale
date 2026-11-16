# Step 4 (optional): grant a role to one user instead of a group.
#
# A user principal takes the user's user ID (usr_...), not their email.
# Look it up with the anyscale_iam_organization_user data source and
# use its user_id attribute; its id attribute is a different value.

data "anyscale_iam_organization_user" "direct" {
  count = var.direct_member_email != "" ? 1 : 0

  email = var.direct_member_email
}

resource "anyscale_iam_role_grant" "direct_writes_project" {
  count = var.direct_member_email != "" ? 1 : 0

  user_id    = data.anyscale_iam_organization_user.direct[0].user_id
  role_id    = data.anyscale_iam_role.project_writer.id
  project_id = anyscale_project.shared.id
}
