# Complete workflow: Invite -> Wait -> Import -> Manage -> Grant cloud access
#
# This is a walkthrough, not a single-shot apply: steps 2 and 3 depend on a real person
# accepting an email invitation (seconds to days, and it happens outside Terraform entirely),
# and anyscale_organization_user only supports import, never direct creation. Both are
# left commented out below so a fresh copy of this file applies cleanly (it only sends
# invitations) instead of failing on parts that cannot succeed yet. Uncomment and apply again as
# you move through each step. Step 4 (cloud roles) does not share this restriction -- see its
# own comment below for why it can be uncommented as soon as the invitation is accepted.

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
#   description = "Use this ID to import the organization_user resource in step 3"
# }

# Step 3: Manage the accepted member's permissions. anyscale_organization_user has no
# Create, only Import -- applying it fresh (as opposed to importing it first) always fails with a
# "Direct Creation Not Supported" error, by design. Once step 2 has given you the identity_id:
#
#   terraform import anyscale_organization_user.new_member <identity_id>
#
# Then uncomment the block below and apply again to manage their permission_level over time
# (e.g. change "collaborator" to "owner" to promote, or back to demote):
#
# resource "anyscale_organization_user" "new_member" {
#   permission_level = "collaborator"
#
#   lifecycle {
#     # Optional: Prevent accidental deletion
#     prevent_destroy = false
#   }
# }
#
# output "managed_user_email" {
#   value       = anyscale_organization_user.new_member.email
#   description = "Email of the managed member"
# }
#
# output "managed_user_permission" {
#   value       = anyscale_organization_user.new_member.permission_level
#   description = "Current permission level"
# }

# Step 4: Grant scoped access on a specific cloud. Unlike step 3,
# anyscale_cloud_user_role supports Create directly and is keyed by email, not
# identity_id -- no import, and no need to repeat the lookup from step 2. It
# can be uncommented as soon as the invitation is accepted, in the same apply
# that first manages their org-level permission_level if you like. Substitute
# a real cloud_id for the placeholder below. Two roles shown side by side so
# the set of possible values reads as a set, not a single example value --
# see the resource's own documentation for the full list, and for the two
# things worth reading before your first apply (why destroy can fail, and why
# this resource can't grant a role to its own calling identity).
#
# resource "anyscale_cloud_user_role" "new_member_compute_viewer" {
#   cloud_id  = "cld_abc123"
#   email     = "newmember@example.com"
#   base_role = "compute_config_viewer"
# }

# Example: Invite multiple users at once. Every invitation still grants only default
# collaborator access -- if "lead@example.com" should end up as an owner, that happens in step 3
# (via anyscale_organization_user), after they accept, same as any other promotion.
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
# list of emails granting a role per person is the actual answer. Each email gets its
# own role here to show the range: dev1 and dev2 get one of the three roles that have
# no console UI at all, lead gets the shared collaborator value. Uncomment once every
# invitation above is accepted and a real cloud_id is substituted for the placeholder.
#
# resource "anyscale_cloud_user_role" "team_member_roles" {
#   for_each = {
#     "dev1@example.com" = "compute_config_viewer"
#     "dev2@example.com" = "workload_operator"
#     "lead@example.com" = "collaborator"
#   }
#
#   cloud_id  = "cld_abc123"
#   email     = each.key
#   base_role = each.value
# }
#
# output "team_cloud_roles" {
#   value = {
#     for email, role in anyscale_cloud_user_role.team_member_roles : email => role.base_role
#   }
#   description = "Cloud role granted to each team member"
# }
