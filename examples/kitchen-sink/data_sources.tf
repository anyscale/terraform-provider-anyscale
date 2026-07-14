# Every data source this provider registers, reading back what the resource files above created
# (plus the two zero-argument connection-level singletons). Two ordering patterns are in play:
#
# 1. Singular lookups below reference the resource's own name/id attribute directly (e.g.
#    `name = anyscale_cloud.main.name`), which gives Terraform an explicit dependency edge -- it
#    defers the read until after that resource exists. This is NOT the same as hardcoding the same
#    literal string in both places: a data source with no attribute reference has no dependency
#    edge, so on a first apply Terraform could read it before the matching resource exists and
#    the lookup would 404. Always prefer the attribute reference.
# 2. List lookups add an explicit `depends_on` where there's no natural attribute reference to
#    borrow, so the resource we just created is guaranteed to already be in the list on a first
#    apply, rather than only appearing after a second refresh.

# --- anyscale_cloud + anyscale_clouds ------------------------------------------------------------
data "anyscale_cloud" "lookup" {
  name = anyscale_cloud.main.name
}

data "anyscale_clouds" "aws_clouds" {
  cloud_provider = "AWS"

  depends_on = [anyscale_cloud.main]
}

# --- anyscale_compute_config --------------------------------------------------------------------
data "anyscale_compute_config" "lookup" {
  name     = anyscale_compute_config.main.name
  cloud_id = anyscale_cloud.main.id
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
data "anyscale_project" "lookup" {
  name     = anyscale_project.main.name
  cloud_id = anyscale_cloud.main.id
}

data "anyscale_projects" "in_cloud" {
  cloud_id = anyscale_cloud.main.id

  depends_on = [anyscale_project.main]
}

# --- anyscale_user + anyscale_organization -------------------------------------------------------
# Zero-argument connection-level singletons: the authenticated principal and the org that token is
# scoped to. No dependency on anything above -- always safe to read.
data "anyscale_user" "current" {}

data "anyscale_organization" "current" {}

# --- anyscale_organization_user + anyscale_organization_users -------------------------------------
# Look up the current user's own org-membership entry by chaining off anyscale_user.current above,
# rather than new_member_email -- new_member is only invited, not yet an org member, until they
# accept (see organization.tf), so looking them up here would 404 on a fresh apply.
data "anyscale_organization_user" "self" {
  email = data.anyscale_user.current.email
}

data "anyscale_organization_users" "all" {}
