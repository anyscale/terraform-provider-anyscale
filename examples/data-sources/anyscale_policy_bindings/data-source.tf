# Read policy bindings across every cloud in the organization
data "anyscale_policy_bindings" "all_clouds" {
  resource_type = "clouds"
}

# Read policy bindings across every project
data "anyscale_policy_bindings" "all_projects" {
  resource_type = "projects"
}

output "clouds_out_of_sync" {
  value = [
    for p in data.anyscale_policy_bindings.all_clouds.policies : p.resource_id
    if p.sync_status != "success"
  ]
  description = "Resource IDs whose policy bindings have not synced successfully"
}

output "project_policy_count" {
  value       = length(data.anyscale_policy_bindings.all_projects.policies)
  description = "Number of projects that have at least one policy binding"
}
