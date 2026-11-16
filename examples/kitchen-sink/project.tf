# --- anyscale_project: one per cloud ------------------------------------------------------------
# Project access is managed with role bindings, not on this resource: organization.tf grants roles
# on project.b and manages every binding on project.a (gated by enable_role_binding).

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
