# anyscale_organization_user manages MEMBERSHIP only - whether someone is in the
# organization. It has no writable attributes. To manage what a member can DO,
# use anyscale_organization_user_role, which owns base_role and deny_roles.
#
# The split is deliberate: only one resource ever writes a member's role.

# Find an existing member's identity_id, which is the import ID:
data "anyscale_organization_user" "existing_user" {
  email = "user@example.com"
}

output "existing_user_identity_id" {
  value       = data.anyscale_organization_user.existing_user.id
  description = "The identity_id to use below with terraform import"
}

# Bring an existing member under management:
#   terraform import anyscale_organization_user.existing_user <identity_id>
#
# This resource cannot create members - people join by invitation (see
# anyscale_organization_invitation). Importing one gives you eviction-as-code:
# removing this resource from your configuration removes them from the
# organization.
resource "anyscale_organization_user" "existing_user" {
  # All attributes are set on import; there is nothing to configure.
}

# Guard members whose removal would be disruptive. Destroying this resource
# evicts a real person from the organization, and Terraform cannot tell an
# intentional removal from an accidental one.
resource "anyscale_organization_user" "admin" {
  # terraform import anyscale_organization_user.admin <identity_id>

  lifecycle {
    prevent_destroy = true
  }
}

# Set the member's role with the companion resource:
resource "anyscale_organization_user_role" "existing_user" {
  email     = data.anyscale_organization_user.existing_user.email
  base_role = "collaborator"

  # deny_roles is omitted here, which leaves any existing container-image
  # restrictions untouched and keeps this resource on the ungated endpoint that
  # works in every organization. Set it - including to [] - only when you want
  # Terraform to own that set.
}

# Read a member's current role without managing it:
output "user_email" {
  value       = anyscale_organization_user.existing_user.email
  description = "Email address of the member"
}

output "user_base_role" {
  value       = data.anyscale_organization_user.existing_user.base_role
  description = "The member's organization base role, read from the data source"
}

output "user_additional_roles" {
  value       = data.anyscale_organization_user.existing_user.additional_roles
  description = "Container image deny roles restricting the base role, if any. The API names this field additional_roles; the anyscale_organization_user_role resource calls it deny_roles, which is what it does."
}
