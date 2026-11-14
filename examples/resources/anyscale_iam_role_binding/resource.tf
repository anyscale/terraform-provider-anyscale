# Beta; Anyscale enables it per organization (contact
# support@anyscale.com). AUTHORITATIVE: every direct role binding on
# the scope that roles does not name is revoked, including the Admin
# binding Anyscale grants whoever created the project. Do not also
# manage this scope with anyscale_iam_role_grant.
variable "cloud_name" {
  description = "Name of an existing Anyscale cloud"
  type        = string
}

# Exact, case-sensitive role names; they can differ per environment.
# Newer projects grant their creator the Admin role, named
# "Organization owner" here. Name the role the creator actually
# holds; see the RBAC guide before assuming which one that is.
data "anyscale_iam_role" "admin" {
  name = "Organization owner"
}

data "anyscale_iam_role" "project_reader" {
  name = "Project reader"
}

data "anyscale_cloud" "this" {
  name = var.cloud_name
}

# The identity Terraform runs as, which holds the Admin role directly
# on the project it creates below.
data "anyscale_user" "me" {}

resource "anyscale_project" "team" {
  name     = "team-project"
  cloud_id = data.anyscale_cloud.this.id
}

resource "anyscale_iam_user_group" "analysts" {
  name = "analysts"
}

resource "anyscale_iam_role_binding" "project" {
  # exactly one of organization_id, cloud_id, project_id
  project_id = anyscale_project.team.id

  roles = {
    # Keep the binding this identity received by creating the
    # project, under the role it actually holds (Admin on projects
    # created since November 2026; Project owner on older ones).
    # The API refuses to revoke it. Do not copy this entry onto a
    # project created by someone else: an identity cannot grant
    # itself a role, so the apply fails with a 403.
    (data.anyscale_iam_role.admin.id) = {
      user_ids = [data.anyscale_user.me.id]
    }
    (data.anyscale_iam_role.project_reader.id) = {
      user_group_ids = [anyscale_iam_user_group.analysts.id]
    }
  }
}
