# Most of this provider's data sources, reading back what the resource files above created.
# Two ordering patterns are in play:
#
# 1. Singular lookups below reference the resource's own name/id attribute directly (e.g.
#    `name = anyscale_cloud.a.name`), which gives Terraform an explicit dependency edge -- it defers
#    the read until after that resource exists. This is NOT the same as hardcoding the same literal
#    string in both places: a data source with no attribute reference has no dependency edge, so on
#    a first apply Terraform could read it before the matching resource exists and the lookup 404s.
#    Always prefer the attribute reference.
# 2. List lookups add an explicit `depends_on` where there's no natural attribute reference to
#    borrow, so the resources we just created are guaranteed to already be in the list on a first
#    apply, rather than only appearing after a second refresh.

# --- anyscale_cloud + anyscale_clouds ------------------------------------------------------------
data "anyscale_cloud" "lookup_a" {
  name = anyscale_cloud.a.name
}

data "anyscale_clouds" "aws_clouds" {
  cloud_provider = "AWS"

  # Scope to this scenario's clouds: every resource this example creates is
  # named "${var.name_prefix}-...", so a bare cloud_provider filter would
  # also count clouds created by any concurrent run in the same org and
  # break the scenario's empty-plan check.
  name_contains = var.name_prefix

  depends_on = [anyscale_cloud.a, anyscale_cloud.b]
}

# --- anyscale_compute_config -----------------------------------------------------------------
data "anyscale_compute_config" "lookup" {
  name     = anyscale_compute_config.cc_a_default.name
  cloud_id = anyscale_cloud.a.id
}

# --- anyscale_container_image + anyscale_container_images ----------------------------------------
data "anyscale_container_image" "training_lookup" {
  name = anyscale_container_image_build.training.name
}

data "anyscale_container_images" "all" {
  name_contains = var.name_prefix

  depends_on = [
    anyscale_container_image_build.training,
    anyscale_container_image_registry.base,
  ]
}

# --- anyscale_project + anyscale_projects ---------------------------------------------------------
data "anyscale_project" "lookup_a" {
  name     = anyscale_project.a.name
  cloud_id = anyscale_cloud.a.id
}

data "anyscale_projects" "in_cloud_a" {
  cloud_id = anyscale_cloud.a.id

  depends_on = [anyscale_project.a]
}

# --- anyscale_user + anyscale_organization -------------------------------------------------------
# Zero-argument connection-level singletons: the authenticated principal and the org that token is
# scoped to. No dependency on anything above -- always safe to read.
data "anyscale_user" "current" {}

data "anyscale_organization" "current" {}

# --- anyscale_iam_organization_user + anyscale_iam_organization_users -------------------------------------
# Look up the current user's own org-membership entry by chaining off anyscale_user.current above,
# rather than invite_email -- new_member is only invited, not yet an org member, until they accept
# (see organization.tf), so looking them up here would 404 on a fresh apply.
data "anyscale_iam_organization_user" "self" {
  email = data.anyscale_user.current.email
}

data "anyscale_iam_organization_users" "all" {}

# --- anyscale_services + anyscale_service ---------------------------------------------------------
data "anyscale_services" "in_project_b" {
  project_id = anyscale_project.b.id

  depends_on = [anyscale_service.hello]
}

# By id: a name lookup matches by substring.
data "anyscale_service" "hello" {
  id = anyscale_service.hello.id
}

# --- anyscale_system_cluster (gated by enable_system_cluster) ------------------------------------
data "anyscale_system_cluster" "b" {
  count = var.enable_system_cluster ? 1 : 0

  cloud_id = anyscale_system_cluster.b[0].cloud_id
}

# --- anyscale_cloud_iam_mapping ------------------------------------------------------------------
# Reads back the mapping cloud_iam_mapping.tf wrote. cloud_iam_mapping.b's cloud_id gives the
# read a dependency edge onto the write.
data "anyscale_cloud_iam_mapping" "b" {
  cloud_id = anyscale_cloud_iam_mapping.b.cloud_id
}

# --- anyscale_iam_role (gated by enable_role_binding) --------------------------------------------
# A built-in role by exact name, for organization.tf's role binding. Gated with it because the
# roles API only answers in organizations with role bindings enabled.
data "anyscale_iam_role" "project_viewer" {
  count = var.enable_role_binding ? 1 : 0

  name = "project_viewer"
}
