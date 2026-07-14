# --- anyscale_organization_invitation (gated) --------------------------------------------------
# Gated behind var.invite_email, which defaults to "" -- count = 0 means this resource simply
# doesn't exist until you set it, so a heavy multi-cloud apply never has the side effect of emailing
# someone by accident. Set invite_email to a real address you own or control to include it.
#
# There's no permission_level argument here: every invitation grants default collaborator access
# on acceptance, full stop -- the invitations API has no way to set a different level up front.
# var.new_member_permission_level is used below instead, once the invite is accepted, via the
# anyscale_organization_collaborator resource -- the only place a permission level can actually be
# chosen.
resource "anyscale_organization_invitation" "new_member" {
  count = var.invite_email != "" ? 1 : 0

  email = var.invite_email
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
#     email = var.invite_email
#   }
#
# See examples/resources/organization_user_workflow/main.tf for the full invite -> wait ->
# import -> manage lifecycle this resource is meant to slot into.
#
# resource "anyscale_organization_collaborator" "new_member" {
#   permission_level = var.new_member_permission_level
# }
