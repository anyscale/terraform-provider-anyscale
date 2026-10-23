# Look up by email - the most common way to find a user before granting
# them project/cloud access or importing an anyscale_organization_user
data "anyscale_organization_user" "by_email" {
  email = "user@example.com"
}

# Look up by user_id instead
data "anyscale_organization_user" "by_user_id" {
  user_id = "usr_abc123"
}

output "user_identity_id" {
  value       = data.anyscale_organization_user.by_email.id
  description = "Read-only identity_id; not the import key (email is)"
}

output "user_email_by_user_id" {
  value       = data.anyscale_organization_user.by_user_id.email
  description = "The user's email when looking up by user_id"
}

# base_role and additional_roles are deprecated: see the RBAC guide,
# "Legacy RBAC deprecation".
output "user_base_role" {
  value       = data.anyscale_organization_user.by_email.base_role
  description = "Base role"
}

output "user_additional_roles" {
  value = (
    data.anyscale_organization_user.by_email.additional_roles
  )
  description = "Extra restriction roles; empty if none"
}
