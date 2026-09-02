# --- anyscale_organization_invitation (gated) --------------------------------------------------
# Gated behind var.invite_email, which defaults to "" -- count = 0 means this resource simply
# doesn't exist until you set it, so a heavy multi-cloud apply never has the side effect of emailing
# someone by accident. Set invite_email to a real address you own or control to include it.
#
# There's no role argument here: every invitation grants default collaborator access on
# acceptance, full stop -- the invitations API has no way to set a different role up front.
# var.new_member_base_role is used below instead, once the invite is accepted, via the
# anyscale_organization_user_role resource -- the only place an organization role can actually be
# chosen. See that resource's own guide page (RBAC) for why this is a separate resource from
# anyscale_organization_user below, rather than one combined resource.
resource "anyscale_organization_invitation" "new_member" {
  count = var.invite_email != "" ? 1 : 0

  email = var.invite_email
}

# --- anyscale_organization_user (commented out; needs an accepted member) ------------
# This resource manages only whether new_member is a member of the organization at all -- it has
# no other writable attributes. It's included here, commented out, so the kitchen sink still
# shows every resource type this provider registers; uncomment once new_member has actually
# accepted the invitation above (which can take anywhere from seconds to days, and happens
# outside Terraform). Keyed by email directly, same as anyscale_organization_user_role below --
# no identity_id or user_id lookup needed, though importing instead of declaring it works
# identically:
#
# See examples/resources/organization_user_workflow/main.tf for the full invite -> wait ->
# manage lifecycle this resource is meant to slot into.
#
# resource "anyscale_organization_user" "new_member" {
#   email = var.invite_email
# }

# --- anyscale_organization_user_role (commented out; needs an accepted member) --------
# Sets new_member's actual organization role, same email key as anyscale_organization_user
# above -- but it still needs a real, accepted member to point at, so the same "outside
# Terraform" wait applies before uncommenting:
#
# resource "anyscale_organization_user_role" "new_member" {
#   email     = var.invite_email
#   base_role = var.new_member_base_role
#
#   # deny_roles is omitted here, which leaves any existing container-image restrictions
#   # untouched and keeps this resource on the ungated endpoint that works in every
#   # organization. Set it -- including to [] -- only when you want Terraform to own that set.
# }
