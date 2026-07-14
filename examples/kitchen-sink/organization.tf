# --- anyscale_organization_invitation --------------------------------------------------------
# Applying this sends a REAL email to var.new_member_email. There is no default for that
# variable specifically so you can't apply this by accident with a placeholder address -- see
# variables.tf.
#
# There's no permission_level argument here: every invitation grants default collaborator access
# on acceptance, full stop -- the invitations API has no way to set a different level up front.
# var.new_member_permission_level is used below instead, once the invite is accepted, via the
# anyscale_organization_collaborator resource -- the only place a permission level can actually be
# chosen.
resource "anyscale_organization_invitation" "new_member" {
  email = var.new_member_email
}

# --- anyscale_organization_collaborator (commented out; import-only) --------------------------
# This resource manages an *existing* org member's permission level -- it has no Create, only
# Import, so it can't be part of a one-shot `terraform apply` the way everything else in this
# example is. It's included here, commented out, so the kitchen sink still shows every resource
# type this provider registers; uncomment and run the import once new_member has actually
# accepted the invitation above (which can take anywhere from seconds to days, and happens
# outside Terraform):
#
#   terraform import anyscale_organization_collaborator.new_member <identity_id>
#
# Find <identity_id> with the anyscale_organization_user data source once the invitation is
# accepted (see data_sources.tf's organization_user lookup for the pattern) -- or look it up
# directly:
#   data "anyscale_organization_user" "new_member" {
#     email = var.new_member_email
#   }
#
# See examples/resources/organization_user_workflow/main.tf for the full invite -> wait ->
# import -> manage lifecycle this resource is meant to slot into.
#
# resource "anyscale_organization_collaborator" "new_member" {
#   permission_level = var.new_member_permission_level
# }
