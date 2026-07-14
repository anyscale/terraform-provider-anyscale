# Complete workflow: Invite → Wait → Import → Manage
# This example shows the full lifecycle of adding and managing organization users

terraform {
  required_providers {
    anyscale = {
      source = "anyscale/anyscale"
    }
  }
}

# Step 1: Send an invitation to a new user. There's no permission_level argument -- every
# invitation grants default collaborator access on acceptance; the API has no way to set a
# different level at invite time. Step 4 below is where a different level actually gets set.
resource "anyscale_organization_invitation" "new_member" {
  email = "newmember@company.com"
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

# Step 2: After user accepts the invitation, find their identity_id
# This data source will only succeed after the invitation is accepted
data "anyscale_organization_user" "accepted_user" {
  email = "newmember@company.com"

  # Wait for invitation to be accepted
  depends_on = [anyscale_organization_invitation.new_member]
}

output "user_identity_id" {
  value       = data.anyscale_organization_user.accepted_user.id
  description = "Use this ID to import the collaborator resource"
}

# Step 3: Import the collaborator resource manually
# Run this command after the invitation is accepted:
# terraform import anyscale_organization_collaborator.new_member <identity_id>

resource "anyscale_organization_collaborator" "new_member" {
  # This resource must be imported - it cannot be created directly
  # The id field will be populated during import
  permission_level = "collaborator"

  lifecycle {
    # Optional: Prevent accidental deletion
    prevent_destroy = false
  }
}

# Step 4: Manage permissions over time
# You can update the permission_level as needed:
# - Change from "collaborator" to "owner" to promote
# - Change from "owner" to "collaborator" to demote

output "managed_user_email" {
  value       = try(anyscale_organization_collaborator.new_member.email, "not yet imported")
  description = "Email of the managed collaborator"
}

output "managed_user_permission" {
  value       = try(anyscale_organization_collaborator.new_member.permission_level, "not yet imported")
  description = "Current permission level"
}

# Example: Invite multiple users at once. Every invitation still grants only default
# collaborator access -- if "lead@company.com" should end up as an owner, that happens in step 4
# (via anyscale_organization_collaborator), after they accept, same as any other promotion.
resource "anyscale_organization_invitation" "team_members" {
  for_each = toset([
    "dev1@company.com",
    "dev2@company.com",
    "lead@company.com",
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
