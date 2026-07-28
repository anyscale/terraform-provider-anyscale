# Grant a restricted Phase 2B role on a cloud. base_role is the only field
# that is fully authoritative - it replaces whatever role and deny roles the
# user previously held on this cloud, it never adds to them.
resource "anyscale_cloud_user_role" "analyst" {
  cloud_id  = anyscale_cloud.main.id
  email     = "analyst@example.com"
  base_role = "compute_config_viewer"
}

# Grant an owner role with an additional restriction. Note that this cloud's
# legacy anyscale_organization_collaborator / project collaborator vocabulary
# is NOT the same vocabulary as base_role here - never assume the two line up.
resource "anyscale_cloud_user_role" "restricted_owner" {
  cloud_id   = anyscale_cloud.main.id
  email      = "owner@example.com"
  base_role  = "owner"
  deny_roles = ["cloud_read_only"]
}

# user_id and identity_id are resolved automatically from email - you never
# need to look either of them up yourself.
output "analyst_user_id" {
  value       = anyscale_cloud_user_role.analyst.user_id
  description = "The resolved user_id backing this role grant"
}
