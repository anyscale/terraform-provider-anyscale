# Look up by name, scoped to a cloud
data "anyscale_project" "by_name" {
  name       = "my-team-project"
  cloud_name = "my-terraform-cloud"
}

# Look up by ID
data "anyscale_project" "by_id" {
  id = "prj_abc123"
}

output "project_directory_name" {
  value       = data.anyscale_project.by_name.directory_name
  description = "The storage directory name used by this project"
}

output "project_collaborators" {
  value       = data.anyscale_project.by_name.collaborators
  description = "Current collaborators on the project"
}

output "project_description_by_id" {
  value       = data.anyscale_project.by_id.description
  description = "The project's description when looking up by id"
}
