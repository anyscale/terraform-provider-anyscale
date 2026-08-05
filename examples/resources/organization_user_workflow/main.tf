# Complete workflow: Invite -> Wait -> Manage role -> Manage membership
#
# This is a walkthrough, not a single-shot apply: steps 2 and 3 depend on a real person
# accepting an email invitation (seconds to days, and it happens outside Terraform entirely). Both
# are left commented out below so a fresh copy of this file applies cleanly (it only sends
# invitations) instead of failing on parts that cannot succeed yet. Uncomment and apply again as
# you move through each step. Steps 2 and 3 are both keyed by email alone -- neither needs an
# identity_id or user_id lookup -- and can be uncommented together as soon as the invitation is
# accepted.
#
# Granting scoped access on a specific cloud is not included here: there is currently no
# resource that can do it. anyscale_cloud_user_role, which used to fill this role, has been
# removed with no direct replacement yet - see the RBAC guide for the current state of that
# surface.

terraform {
  required_providers {
    anyscale = {
      source = "anyscale/anyscale"
    }
  }
}

# Step 1: Send an invitation to a new user. There's no role argument here -- every invitation
# grants default collaborator access on acceptance; the API has no way to set a different role
# at invite time. Step 2 below is where a different role actually gets set.
resource "anyscale_organization_invitation" "new_member" {
  email = "newmember@example.com"
}

# Output invitation details for manual follow-up
output "invitation_id" {
  value       = anyscale_organization_invitation.new_member.id
  description = "Share this invitation ID if the user needs to check status"
}

output "invitation_status" {
  value       = anyscale_organization_invitation.new_member.status
  description = "Current status: pending, accepted, or expired"
}

output "invitation_expires_at" {
  value       = anyscale_organization_invitation.new_member.expires_at
  description = "When this invitation will expire"
}

# Step 2: Once you have confirmed the invitation above was accepted (check invitation_status),
# manage the member's organization role directly by email -- no identity_id or user_id lookup
# needed. Applying this any earlier than acceptance fails with a real "not found" error, since
# the person does not exist as an org member yet.
#
# resource "anyscale_organization_user_role" "new_member" {
#   email     = "newmember@example.com"
#   base_role = "collaborator"
#
#   # deny_roles is omitted here, which leaves any existing container-image restrictions
#   # untouched and keeps this resource on the ungated endpoint that works in every
#   # organization. Set it -- including to [] -- only when you want Terraform to own that set.
# }
#
# output "managed_user_role" {
#   value       = anyscale_organization_user_role.new_member.base_role
#   description = "The role Terraform is now managing for this member"
# }

# Step 3: Bring the member's organization MEMBERSHIP itself under Terraform management -- a
# separate concern from their role in step 2 (see the RBAC guide for why these are two
# resources rather than one). Keyed by email, same as step 2 -- declaring it and applying
# adopts the existing member with no API call, since Terraform is just declaring a fact that
# is already true:
#
# resource "anyscale_organization_user" "new_member" {
#   email = "newmember@example.com"
#
#   # Importing this resource instead of declaring it works identically - either way,
#   # destroying it later removes nothing from the organization; the person keeps full
#   # access and a warning says so. This resource can only stop tracking them, never
#   # revoke them -- do that from the Anyscale console:
#   #   terraform import anyscale_organization_user.new_member newmember@example.com
# }
#
# output "managed_user_email" {
#   value       = anyscale_organization_user.new_member.email
#   description = "Email of the managed member"
# }

# Example: Invite multiple users at once. Every invitation still grants only default
# collaborator access -- if "lead@example.com" should end up as an owner, that happens in step 2
# (via anyscale_organization_user_role), after they accept, same as any other promotion.
resource "anyscale_organization_invitation" "team_members" {
  for_each = toset([
    "dev1@example.com",
    "dev2@example.com",
    "lead@example.com",
  ])

  email = each.key
}

# Output the status of all invitations
output "team_invitations" {
  value = {
    for email, invitation in anyscale_organization_invitation.team_members :
    email => {
      id         = invitation.id
      status     = invitation.status
      expires_at = invitation.expires_at
    }
  }
  description = "Status of all team member invitations"
}

# Once each team member above has accepted, this is the closest thing to managing a
# "group" this provider has today -- there is no group resource, so a for_each over a
# list of emails granting a role per person is the actual answer (see Step 2 above for
# the single-member version of this same resource). Uncomment once every invitation
# above is accepted.
#
# resource "anyscale_organization_user_role" "team_member_roles" {
#   for_each = {
#     "dev1@example.com" = "collaborator"
#     "dev2@example.com" = "collaborator"
#     "lead@example.com" = "owner"
#   }
#
#   email     = each.key
#   base_role = each.value
# }
#
# output "team_roles" {
#   value = {
#     for email, role in anyscale_organization_user_role.team_member_roles : email => role.base_role
#   }
#   description = "Organization role granted to each team member"
# }
