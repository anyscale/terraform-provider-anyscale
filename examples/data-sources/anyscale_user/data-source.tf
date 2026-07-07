# The current authenticated user (based on the provider's token) - takes no arguments
data "anyscale_user" "current" {}

output "current_user_email" {
  value       = data.anyscale_user.current.email
  description = "Email of the user the provider is currently authenticated as"
}

output "current_user_cloud_ids" {
  value       = data.anyscale_user.current.cloud_ids
  description = "IDs of clouds this user can access"
}
