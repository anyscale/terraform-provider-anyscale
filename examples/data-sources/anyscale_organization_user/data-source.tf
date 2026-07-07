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
