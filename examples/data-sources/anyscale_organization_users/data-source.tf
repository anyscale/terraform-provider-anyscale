# List all human users in the organization (excludes service accounts by default)
data "anyscale_organization_users" "humans" {}

# Filter by a partial name or email match
data "anyscale_organization_users" "engineering" {
  email = "@eng.example.com"
}

# Include service accounts
data "anyscale_organization_users" "service_accounts" {
  is_service_account = true
}

output "organization_user_emails" {
  value       = [for u in data.anyscale_organization_users.humans.users : u.email]
  description = "Emails of every human user in the organization"
}

output "engineering_user_emails" {
  value       = [for u in data.anyscale_organization_users.engineering.users : u.email]
  description = "Emails matching the @eng.example.com filter"
}

output "service_account_emails" {
  value       = [for u in data.anyscale_organization_users.service_accounts.users : u.email]
  description = "Emails of every service account in the organization"
}

output "users_by_base_role" {
  value = {
    for u in data.anyscale_organization_users.humans.users : u.email => u.base_role
  }
  description = "Base role for every human user, keyed by email - prefer base_role over permission_level"
}

output "users_with_additional_roles" {
  value = [
    for u in data.anyscale_organization_users.humans.users : u.email
    # additional_roles is null (not empty) for a user with no user_id, rather than an
    # empty list - coalesce it first so this doesn't error out on that case.
    if length(coalesce(u.additional_roles, [])) > 0
  ]
  description = "Emails of users who have at least one additional restriction role beyond their base role"
}
