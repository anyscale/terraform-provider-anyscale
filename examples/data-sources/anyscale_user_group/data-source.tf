# Alpha: the schema may change before Beta.
# Look up one group by name (or by id). Includes directory-synced (SCIM) groups, which
# Terraform cannot manage but can reference.
data "anyscale_user_group" "okta_admins" {
  name = "okta-admins"
}

output "okta_admins_group_id" {
  description = "ID of the directory-synced group"
  value       = data.anyscale_user_group.okta_admins.id
}

output "okta_admins_source" {
  description = "scim for directory-synced groups"
  value       = data.anyscale_user_group.okta_admins.source
}
