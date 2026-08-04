# --- anyscale_project: one per cloud ------------------------------------------------------------
# Project access (collaborators/roles) is NOT managed here. The `collaborator` block was removed
# from this resource in v0.25.0 - project roles cannot be granted independently of a cloud role.
# There is currently NO in-provider replacement; see examples/resources/anyscale_project/resource.tf
# for the full explanation. The anyscale_project data source (data_sources.tf) still reads existing
# collaborators without managing them.

resource "anyscale_project" "a" {
  name        = "${var.name_prefix}-project-a"
  cloud_id    = anyscale_cloud.a.id
  description = "Created by the kitchen-sink example (Cloud A)."
}

resource "anyscale_project" "b" {
  name        = "${var.name_prefix}-project-b"
  cloud_id    = anyscale_cloud.b.id
  description = "Created by the kitchen-sink example (Cloud B)."
}
