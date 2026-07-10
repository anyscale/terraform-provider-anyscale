# Basic project, scoped to a cloud by name
resource "anyscale_project" "example" {
  name        = "my-team-project"
  cloud_name  = "my-terraform-cloud"
  description = "Workspaces and jobs for the data team"
}

# Project scoped by cloud_id instead, with collaborators managed in-place
resource "anyscale_project" "with_collaborators" {
  name        = "shared-research-project"
  cloud_id    = "cld_abc123"
  description = "Shared research workspaces"

  collaborator {
    email            = "owner@example.com"
    permission_level = "owner"
  }

  collaborator {
    email            = "developer@example.com"
    permission_level = "write"
  }

  collaborator {
    email            = "analyst@example.com"
    permission_level = "readonly"
  }
}

# Outputs
output "project_id" {
  value       = anyscale_project.example.id
  description = "The unique identifier for the project"
}

output "project_directory_name" {
  value       = anyscale_project.example.directory_name
  description = "The storage directory name used by this project"
}
