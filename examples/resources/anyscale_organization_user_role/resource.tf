# Manage an existing organization member's role. This resource does not add or remove members --
# the person must already be in the organization (see anyscale_organization_user for membership)
# -- it only sets base_role and, optionally, deny_roles for them.
resource "anyscale_organization_user_role" "analyst" {
  email     = "analyst@example.com"
  base_role = "collaborator"

  # deny_roles is omitted here. Omitting it leaves any existing container-image restrictions
  # untouched and keeps this resource on the endpoint that works in every organization -- setting
  # deny_roles at all requires a feature that is not enabled in every organization, and never on
  # Azure.
}

# Set both base_role and deny_roles. This goes through a different, gated API endpoint than
# base_role alone -- see this resource's own documentation before relying on it in an
# organization you do not control. Note that these deny roles restrict organization OWNERS too,
# unlike the similarly named deny_roles on anyscale_cloud_user_role, which do not.
resource "anyscale_organization_user_role" "restricted_owner" {
  email      = "owner@example.com"
  base_role  = "owner"
  deny_roles = ["image_reader_no_base_images"]
}

# Two things worth knowing before your first apply, both explained fully on this resource's own
# doc page and in the RBAC guide: destroying this resource does not revert base_role -- an
# organization member always has some role, so there is nothing to revert to, and reverting could
# demote or lock out an owner -- and changing email (including fixing a typo) replaces the
# resource, leaving whoever was at the old address with whatever role they last had.

# user_id and identity_id are resolved automatically from email -- you never need to look either
# of them up yourself.
output "analyst_user_id" {
  value       = anyscale_organization_user_role.analyst.user_id
  description = "The resolved user_id backing this role assignment"
}

output "analyst_current_role" {
  value       = anyscale_organization_user_role.analyst.base_role
  description = "The member's current organization base role"
}
