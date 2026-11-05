# Beta; Anyscale enables it per organization (contact
# support@anyscale.com). AUTHORITATIVE: every direct role binding on
# the scope that roles does not name is revoked, including the owner
# binding Anyscale grants whoever created the project. Do not also
# manage this scope with anyscale_role_binding.
variable "cloud_name" {
  description = "Name of an existing Anyscale cloud"
  type        = string
}

# Exact, case-sensitive role names; they can differ per environment.
data "anyscale_role" "project_owner" {
  name = "Project owner"
}

data "anyscale_role" "project_reader" {
  name = "Project reader"
}

data "anyscale_cloud" "this" {
  name = var.cloud_name
}

# The identity Terraform runs as, which becomes the project's owner
# when it creates the project below.
data "anyscale_user" "me" {}

resource "anyscale_project" "team" {
  name     = "team-project"
  cloud_id = data.anyscale_cloud.this.id
}

resource "anyscale_user_group" "analysts" {
  name = "analysts"
}

resource "anyscale_role_binding_policy" "project" {
  # exactly one of organization_id, cloud_id, project_id
  project_id = anyscale_project.team.id

  roles = {
    # Keep the owner binding this identity received by creating the
    # project. The API refuses to revoke it. Do not copy this entry
    # onto a project created by someone else: an identity cannot
    # grant itself a role, so the apply fails with a 403.
    (data.anyscale_role.project_owner.id) = {
      user_ids = [data.anyscale_user.me.id]
    }
    (data.anyscale_role.project_reader.id) = {
      user_group_ids = [anyscale_user_group.analysts.id]
    }
  }
}
