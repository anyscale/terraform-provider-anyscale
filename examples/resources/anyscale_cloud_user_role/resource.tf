# Grant a restricted Phase 2B role on a cloud. Two things worth knowing before
# your first apply, both explained fully on this resource's own doc page:
# email is the identifier you set even though the API underneath uses two
# different IDs for granting versus revoking, and this is the one resource in
# this provider where destroy can legitimately fail if the role was ever
# granted outside Terraform. base_role is the only field that is fully
# authoritative - it replaces whatever role and deny roles the user
# previously held on this cloud, it never adds to them.
resource "anyscale_cloud_user_role" "analyst" {
  cloud_id  = "cld_abc123"
  email     = "analyst@example.com"
  base_role = "compute_config_viewer"
}

# Grant an owner role with an additional restriction. Note that this cloud's
# legacy anyscale_organization_user / project collaborator vocabulary is NOT
# the same vocabulary as base_role here - never assume the two line up.
resource "anyscale_cloud_user_role" "restricted_owner" {
  cloud_id   = "cld_abc123"
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
