# Anyscale Project Example
# This demonstrates creating projects with different configurations

# Basic project without collaborators
resource "anyscale_project" "basic" {
  name        = "${var.cloud_name}-basic-project"
  cloud_id    = anyscale_cloud.primary.id
  description = "Basic project for testing and development"
}

# Project with collaborators (uncomment and provide valid email addresses to test)
# resource "anyscale_project" "with_collaborators" {
#   name        = "${var.cloud_name}-team-project"
#   cloud_id    = anyscale_cloud.primary.id
#   description = "Team project with collaborators"
#
#   collaborator {
#     email            = "alice@example.com"
#     permission_level = "owner"
#   }
#
#   collaborator {
#     email            = "bob@example.com"
#     permission_level = "writer"
#   }
#
#   collaborator {
#     email            = "charlie@example.com"
#     permission_level = "readonly"
#   }
# }

# Project using cloud_name instead of cloud_id
resource "anyscale_project" "by_cloud_name" {
  name        = "${var.cloud_name}-cloudname-project"
  cloud_name  = anyscale_cloud.primary.name
  description = "Project referencing cloud by name"
}

# Data source example: Look up a project by name
data "anyscale_project" "basic_lookup" {
  name     = anyscale_project.basic.name
  cloud_id = anyscale_cloud.primary.id

  depends_on = [anyscale_project.basic]
}

# Data source example: List all projects in this cloud
data "anyscale_projects" "all_in_cloud" {
  cloud_id         = anyscale_cloud.primary.id
  include_defaults = false

  depends_on = [
    anyscale_project.basic,
    anyscale_project.by_cloud_name,
  ]
}
