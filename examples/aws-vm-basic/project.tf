# Anyscale Project Example
# This demonstrates creating projects with different configurations

# Basic project without collaborators
resource "anyscale_project" "basic" {
  name        = "${var.cloud_name}-basic-project"
  cloud_id    = anyscale_cloud.primary.id
  description = "Basic project for testing and development"
}

# Project access is NOT managed here. This resource used to carry a `collaborator`
# block; it was removed in v0.25.0 because project roles cannot be granted
# independently of a cloud role - see examples/resources/anyscale_project/resource.tf
# for the full explanation. There is currently NO in-provider replacement; manage
# project collaborators through the Anyscale console or API for now, or read them
# (without managing them) via the anyscale_project data source below.

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

  depends_on = [anyscale_project.basic]
}
