# Step 5: own every grant on a scope with anyscale_iam_role_binding.
#
# AUTHORITATIVE: an apply revokes every direct grant on the scope that
# roles does not name, including grants made in the console and the
# Admin grant Anyscale gives a project's creator. Use it when one team
# owns all access to a scope. Never use it on a scope that
# anyscale_iam_role_grant also manages (step 3 uses the other project):
# each would undo the other on every apply.

data "anyscale_user" "me" {}

resource "anyscale_project" "owned" {
  name     = "${var.name_prefix}-owned"
  cloud_id = data.anyscale_cloud.this.id
}

resource "anyscale_iam_role_binding" "owned" {
  # Exactly one of organization_id, cloud_id, project_id.
  project_id = anyscale_project.owned.id

  # A map from role ID to the users and groups that hold it.
  roles = {
    # Creating the project gave the identity running Terraform the
    # Admin role on it. The API refuses to revoke your own grant, so
    # name it. Apply as the project's creator: anyone else cannot
    # grant themselves this role and the apply fails.
    (data.anyscale_iam_role.admin.id) = {
      user_ids = [data.anyscale_user.me.id]
    }
    (data.anyscale_iam_role.project_writer.id) = {
      user_group_ids = [anyscale_iam_user_group.team.id]
    }
  }
}
