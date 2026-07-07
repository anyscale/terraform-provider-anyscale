# Read the current role bindings on a cloud
data "anyscale_policy_binding" "cloud_access" {
  resource_type = "cloud"
  resource_id   = "cld_abc123"
}

# Read the current role bindings on a project
data "anyscale_policy_binding" "project_access" {
  resource_type = "project"
  resource_id   = "prj_def456"
}

output "cloud_role_bindings" {
  value       = data.anyscale_policy_binding.cloud_access.bindings
  description = "List of {role_name, principals} bindings currently applied to the cloud"
}

output "project_role_bindings" {
  value       = data.anyscale_policy_binding.project_access.bindings
  description = "List of {role_name, principals} bindings currently applied to the project"
}
