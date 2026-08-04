# anyscale_organization_user manages MEMBERSHIP only - whether someone is in the
# organization. It has no writable attributes besides email. To manage what a
# member can DO, use anyscale_organization_user_role, which owns base_role and
# deny_roles.
#
# The split is deliberate: only one resource ever writes a member's role.

# Bring an existing member under management. This resource is keyed by email -
# not identity_id or user_id, which only exist once someone has accepted an
# invitation, while email identifies the same person before, during, and
# after. Declaring it and applying adopts the existing member with no API
# call; equivalently, you can import the same member instead:
#   terraform import anyscale_organization_user.existing_user user@example.com
#
# This resource cannot create members - people join by invitation (see
# anyscale_organization_invitation). It also cannot remove them: destroying
# this resource for an adopted member (both examples below) removes nothing
# from the organization - they keep full access, and a warning says so.
# Revoking a real member has to happen from the Anyscale console; this
# provider does not have that capability today.
resource "anyscale_organization_user" "existing_user" {
  email = "user@example.com"
}

# prevent_destroy here protects the STATE ENTRY, not the person - destroying
# this resource is harmless to their actual access (see above), but it does
# silently stop Terraform from tracking them, which is disruptive on its own
# if you rely on the outputs below.
resource "anyscale_organization_user" "admin" {
  email = "admin@example.com"

  lifecycle {
    prevent_destroy = true
  }
}

# Set the member's role with the companion resource:
resource "anyscale_organization_user_role" "existing_user" {
  email     = anyscale_organization_user.existing_user.email
  base_role = "collaborator"

  # deny_roles is omitted here, which leaves any existing container-image
  # restrictions untouched and keeps this resource on the ungated endpoint that
  # works in every organization. Set it - including to [] - only when you want
  # Terraform to own that set.
}

# Read a member's current role without managing it:
data "anyscale_organization_user" "existing_user" {
  email = anyscale_organization_user.existing_user.email
}

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
