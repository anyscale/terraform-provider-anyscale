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

# Project access is NOT managed here. This resource used to carry a
# `collaborator` block; it was removed because project roles cannot be granted
# independently of a cloud role - the backend requires a cloud grant on the
# parent cloud before a project grant, and revoking the cloud grant cascades to
# the projects under it. Managing the two separately meant a project role could
# be declared for someone with no cloud access, which only failed partway
# through an apply.
#
# There is currently NO in-provider replacement. Project collaborators must be
# managed outside Terraform for now - through the Anyscale console or API. A
# resource that manages a cloud's members together with their project roles is
# planned, but it is not part of this release, and this comment will point at it
# once it actually ships rather than before.
#
# To READ a project's current collaborators without managing them, the
# anyscale_project data source still exposes them.

# Outputs
output "project_id" {
  value       = anyscale_project.example.id
  description = "The unique identifier for the project"
}

output "project_directory_name" {
  value       = anyscale_project.example.directory_name
  description = "The storage directory name used by this project"
}
