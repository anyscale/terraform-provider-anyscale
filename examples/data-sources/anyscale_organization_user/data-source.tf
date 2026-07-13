# Look up by email - the most common way to find a user before granting
# them project/cloud access or importing an anyscale_organization_collaborator
data "anyscale_organization_user" "by_email" {
  email = "user@example.com"
}

# Look up by user_id instead
data "anyscale_organization_user" "by_user_id" {
  user_id = "usr_abc123"
}

output "user_identity_id" {
  value       = data.anyscale_organization_user.by_email.id
  description = "The identity_id, used as the import ID and id for anyscale_organization_collaborator"
}

output "user_email_by_user_id" {
  value       = data.anyscale_organization_user.by_user_id.email
  description = "The user's email when looking up by user_id"
}

output "user_base_role" {
  value       = data.anyscale_organization_user.by_email.base_role
  description = "The user's base role - prefer this over permission_level, which the backend is moving away from"
}

output "user_additional_roles" {
  value       = data.anyscale_organization_user.by_email.additional_roles
  description = "Any additional roles granted beyond the base role; empty (not null) if none"
}
