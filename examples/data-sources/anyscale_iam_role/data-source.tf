# Beta; Anyscale enables it per organization (contact
# support@anyscale.com). Look up a role by exact, case-sensitive
# name; archived roles are not found.
data "anyscale_iam_role" "project_viewer" {
  name = "project_viewer"
}

output "project_viewer_role_id" {
  description = "Pass to anyscale_iam_role_grant.role_id"
  value       = data.anyscale_iam_role.project_viewer.id
}
