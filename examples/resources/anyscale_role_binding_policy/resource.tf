# Beta; Anyscale enables it per organization (contact
# support@anyscale.com). AUTHORITATIVE: every direct role binding on
# the scope that roles does not name is revoked, including the owner
# binding Anyscale grants whoever created the project. Do not also
# manage this scope with anyscale_role_binding.
variable "project_id" {
  description = "ID of an existing Anyscale project (prj_...)"
  type        = string
}

# Exact, case-sensitive role names; they can differ per environment.
data "anyscale_role" "project_owner" {
  name = "Project owner"
}

data "anyscale_role" "project_reader" {
  name = "Project reader"
}

# The identity Terraform runs as.
data "anyscale_user" "me" {}

resource "anyscale_user_group" "analysts" {
  name = "analysts"
}

resource "anyscale_role_binding_policy" "project" {
  # exactly one of organization_id, cloud_id, project_id
  project_id = var.project_id

  roles = {
    # Keep this identity's own owner binding: the API refuses to
    # revoke it, and an identity cannot grant itself a role.
    (data.anyscale_role.project_owner.id) = {
      user_ids = [data.anyscale_user.me.id]
    }
    (data.anyscale_role.project_reader.id) = {
      user_group_ids = [anyscale_user_group.analysts.id]
    }
  }
}
