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
