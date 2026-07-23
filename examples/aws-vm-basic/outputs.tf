output "cloud_id" {
  description = "The ID of the created Anyscale cloud"
  value       = anyscale_cloud.primary.id
}

output "cloud_name" {
  description = "The name of the created Anyscale cloud"
  value       = anyscale_cloud.primary.name
}

# Project outputs
output "basic_project_id" {
  description = "The ID of the basic project"
  value       = anyscale_project.basic.id
}

output "basic_project_name" {
  description = "The name of the basic project"
  value       = anyscale_project.basic.name
}

output "basic_project_directory" {
  description = "The directory name for the basic project"
  value       = anyscale_project.basic.directory_name
}

output "basic_project_datasource_lookup_id" {
  description = "The ID of the basic project lookup"
  value       = data.anyscale_project.basic_lookup.id
}

output "all_project_ids" {
  description = "List of all project IDs in this cloud"
  value       = [for p in data.anyscale_projects.all_in_cloud.projects : p.id]
}

output "all_project_names" {
  description = "List of all project names in this cloud"
  value       = [for p in data.anyscale_projects.all_in_cloud.projects : p.name]
}

output "basic_compute_config_id" {
  description = "The ID of the created Anyscale compute config"
  value       = anyscale_compute_config.basic.id
}

output "basic_compute_config_name" {
  description = "The name of the created Anyscale compute config"
  value       = anyscale_compute_config.basic.name
}

output "basic_compute_config_version" {
  description = "The version of the created Anyscale compute config"
  value       = anyscale_compute_config.basic.version
}
