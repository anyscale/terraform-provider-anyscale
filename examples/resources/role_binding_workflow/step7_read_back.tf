# Step 7: read back who holds what.
#
# The anyscale_iam_role_binding data source reads every direct grant on
# one scope, whoever made it. Reading needs permission to manage access
# on that scope, which a project's creator has. Referencing the grants
# makes Terraform read after they are written.

data "anyscale_iam_role_binding" "shared" {
  project_id = anyscale_iam_role_grant.team_reads_project.project_id

  depends_on = [anyscale_iam_role_grant.direct_writes_project]
}

output "shared_project_grants" {
  description = "Role ID => holders, on the shared project"
  value       = data.anyscale_iam_role_binding.shared.roles
}

output "owned_project_grants" {
  description = "Role ID => holders, on the owned project"
  value       = anyscale_iam_role_binding.owned.roles
}

output "role_ids" {
  description = "Role name => role ID, for reading the outputs above"
  value = {
    for r in [
      data.anyscale_iam_role.project_reader,
      data.anyscale_iam_role.project_writer,
      data.anyscale_iam_role.cloud_collaborator,
      data.anyscale_iam_role.admin,
    ] : r.name => r.id
  }
}
