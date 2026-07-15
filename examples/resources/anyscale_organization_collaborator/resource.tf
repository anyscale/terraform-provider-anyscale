# Import an existing collaborator to manage their permissions
# First, find their identity_id using the data source:
data "anyscale_organization_user" "existing_user" {
  email = "user@example.com"
}

output "existing_user_identity_id" {
  value       = data.anyscale_organization_user.existing_user.id
  description = "The identity_id to use below with terraform import"
}

# Then import the collaborator using:
# terraform import anyscale_organization_collaborator.existing_user <identity_id>

resource "anyscale_organization_collaborator" "existing_user" {
  # The id field is set during import
  # Only permission_level can be managed
  permission_level = "collaborator"
}

# Manage an owner's permissions
resource "anyscale_organization_collaborator" "admin" {
  # Import with: terraform import anyscale_organization_collaborator.admin <identity_id>
  permission_level = "owner"

  lifecycle {
    # Prevent accidental deletion of important users
    prevent_destroy = true
  }
}

# Output collaborator details
output "user_email" {
  value       = anyscale_organization_collaborator.existing_user.email
  description = "Email address of the collaborator"
}

output "user_permission" {
  value       = anyscale_organization_collaborator.existing_user.permission_level
  description = "Current permission level"
}

output "user_base_role" {
  value       = anyscale_organization_collaborator.existing_user.base_role
  description = "The collaborator's base role - always agrees with permission_level, since permission_level is the field you set to change it"
}

output "user_additional_roles" {
  value       = anyscale_organization_collaborator.existing_user.additional_roles
  description = "Additional restriction (deny) roles beyond the base role, if any - read-only, not manageable through Terraform"
}
