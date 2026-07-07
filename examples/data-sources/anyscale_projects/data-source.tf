# List every non-default project in a cloud
data "anyscale_projects" "cloud_projects" {
  cloud_name       = "my-terraform-cloud"
  include_defaults = false
}

# Filter by a partial name match across all clouds
data "anyscale_projects" "research" {
  name_contains = "research"
}

output "project_ids" {
  value       = [for p in data.anyscale_projects.cloud_projects.projects : p.id]
  description = "IDs of every non-default project in the cloud"
}

output "research_project_ids" {
  value       = [for p in data.anyscale_projects.research.projects : p.id]
  description = "IDs of projects whose name contains \"research\""
}
