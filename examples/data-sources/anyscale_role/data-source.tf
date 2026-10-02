# Alpha, behind a feature flag (contact support@anyscale.com for access).
# Look up a role by exact, case-sensitive name; archived roles are not found.
data "anyscale_role" "project_viewer" {
  name = "project_viewer"
}

output "project_viewer_role_id" {
  description = "Pass to anyscale_role_binding.role_id"
  value       = data.anyscale_role.project_viewer.id
}
