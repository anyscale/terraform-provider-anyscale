# Look up an existing cloud by name, then create a project scoped to it -
# for a cloud you don't also manage in this configuration
data "anyscale_cloud" "by_name" {
  name = "my-terraform-cloud"
}

resource "anyscale_project" "example" {
  name        = "my-team-project"
  cloud_id    = data.anyscale_cloud.by_name.id
  description = "Workspaces and jobs for the data team"
}

# Project scoped by a literal cloud_id
resource "anyscale_project" "shared_research" {
  name        = "shared-research-project"
  cloud_id    = "cld_abc123"
  description = "Shared research workspaces"
}

# Project access is not managed here. Use anyscale_cloud_access to manage a cloud's members
# and their project roles (see the RBAC guide). The anyscale_project data source reads a
# project's current collaborators without managing them.

# Outputs
output "project_id" {
  value       = anyscale_project.example.id
  description = "The unique identifier for the project"
}

output "project_directory_name" {
  value       = anyscale_project.example.directory_name
  description = "The storage directory name used by this project"
}
