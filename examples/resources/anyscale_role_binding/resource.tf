# Alpha, behind a feature flag (contact support@anyscale.com for access). The schema may change before Beta.
# Grants one role to one user group on exactly one of a cloud, a project, or the organization.
# A binding cannot be edited: changing any argument replaces it.
variable "project_id" {
  description = "ID of an existing Anyscale project (prj_...)"
  type        = string
}

data "anyscale_role" "project_viewer" {
  name = "project_viewer" # exact, case-sensitive
}

resource "anyscale_user_group" "analysts" {
  name = "analysts"
}

resource "anyscale_role_binding" "analysts_project_viewer" {
  user_group_id = anyscale_user_group.analysts.id
  role_id       = data.anyscale_role.project_viewer.id
  project_id    = var.project_id # exactly one of cloud_id, project_id, organization_id

  # Avoid a window with no access when role_id changes.
  lifecycle {
    create_before_destroy = true
  }
}
