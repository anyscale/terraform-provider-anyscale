# Deprecated: see the RBAC guide, "Legacy RBAC deprecation".
# Manage an existing organization member's role. This resource does not
# add or remove members -- the person must already be in the
# organization (see anyscale_iam_organization_user for membership) -- it
# only sets base_role and, optionally, deny_roles for them.
resource "anyscale_organization_user_role" "analyst" {
  email     = "analyst@example.com"
  base_role = "collaborator"

  # deny_roles is omitted here. Omitting it leaves existing image
  # restrictions untouched. Declaring deny_roles requires a feature
  # flag that is not enabled in every organization.
}

# Set both base_role and deny_roles. Setting deny_roles routes through
# a gated API endpoint (see this resource's documentation). These deny
# roles also restrict organization owners.
resource "anyscale_organization_user_role" "restricted_owner" {
  email      = "owner@example.com"
  base_role  = "owner"
  deny_roles = ["image_reader_no_base_images"]
}

# Destroy leaves the member's base_role unchanged; changing email
# replaces the resource.
output "analyst_user_id" {
  value       = anyscale_organization_user_role.analyst.user_id
  description = "The resolved user_id backing this role assignment"
}

output "analyst_current_role" {
  value       = anyscale_organization_user_role.analyst.base_role
  description = "The member's current organization base role"
}
