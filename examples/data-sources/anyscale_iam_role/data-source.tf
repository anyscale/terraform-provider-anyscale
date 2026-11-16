# Beta; Anyscale enables it per organization (contact
# support@anyscale.com). Look up a role by exact, case-sensitive
# name; archived roles are not found.
#
# Step-by-step walkthrough of every role-binding type:
# examples/resources/role_binding_workflow
data "anyscale_iam_role" "project_reader" {
  name = "Project reader"
}

output "project_reader_role_id" {
  description = "Pass to anyscale_iam_role_grant.role_id"
  value       = data.anyscale_iam_role.project_reader.id
}
