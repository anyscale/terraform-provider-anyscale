# Complete workflow: Invite -> Wait -> Import -> Manage
#
# This is a walkthrough, not a single-shot apply: steps 2 and 3 depend on a real person
# accepting an email invitation (seconds to days, and it happens outside Terraform entirely),
# and anyscale_organization_collaborator only supports import, never direct creation. Both are
# left commented out below so a fresh copy of this file applies cleanly (it only sends
# invitations) instead of failing on parts that cannot succeed yet. Uncomment and apply again as
# you move through each step.

terraform {
  required_providers {
    anyscale = {
      source = "anyscale/anyscale"
    }
  }
}

# Step 1: Send an invitation to a new user. There's no permission_level argument -- every
# invitation grants default collaborator access on acceptance; the API has no way to set a
# different level at invite time. Step 3 below is where a different level actually gets set.
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
# find the new member's identity_id. Uncomment and apply again -- applying this any earlier fails
# with a "User Not Found" error, since the user does not exist as an org member until they accept.
#
# data "anyscale_organization_user" "accepted_user" {
#   email = "newmember@example.com"
# }
#
# output "user_identity_id" {
#   value       = data.anyscale_organization_user.accepted_user.id
#   description = "Use this ID to import the collaborator resource in step 3"
# }

# Step 3: Manage the accepted member's permissions. anyscale_organization_collaborator has no
# Create, only Import -- applying it fresh (as opposed to importing it first) always fails with a
# "Direct Creation Not Supported" error, by design. Once step 2 has given you the identity_id:
#
#   terraform import anyscale_organization_collaborator.new_member <identity_id>
#
# Then uncomment the block below and apply again to manage their permission_level over time
# (e.g. change "collaborator" to "owner" to promote, or back to demote):
#
# resource "anyscale_organization_collaborator" "new_member" {
#   permission_level = "collaborator"
#
#   lifecycle {
#     # Optional: Prevent accidental deletion
#     prevent_destroy = false
#   }
# }
#
# output "managed_user_email" {
#   value       = anyscale_organization_collaborator.new_member.email
#   description = "Email of the managed collaborator"
# }
#
# output "managed_user_permission" {
#   value       = anyscale_organization_collaborator.new_member.permission_level
#   description = "Current permission level"
# }

# Example: Invite multiple users at once. Every invitation still grants only default
# collaborator access -- if "lead@example.com" should end up as an owner, that happens in step 3
# (via anyscale_organization_collaborator), after they accept, same as any other promotion.
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
