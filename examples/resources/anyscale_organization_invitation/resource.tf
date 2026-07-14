# Send an invitation to a new user. Every invitation grants default collaborator access once
# accepted -- there's no permission_level argument here, because the invitations API has no way
# to set one at invite time. To grant a different level (e.g. owner), invite the user, wait for
# them to accept, then manage their permission_level with the anyscale_organization_collaborator
# resource (import-only -- see its own example, or organization_user_workflow for the lifecycle).
resource "anyscale_organization_invitation" "new_user" {
  email = "newuser@example.com"
}

# Output the invitation status
output "invitation_status" {
  value       = anyscale_organization_invitation.new_user.status
  description = "Current status of the invitation (pending, accepted, or expired)"
}

# Output invitation ID for tracking
output "invitation_id" {
  value       = anyscale_organization_invitation.new_user.id
  description = "The unique invitation ID"
}

# Send a second invitation to a different address -- invitations behave identically regardless
# of who they're for, so this just demonstrates inviting more than one person.
resource "anyscale_organization_invitation" "second_user" {
  email = "admin@example.com"
}

# Check if the second invitation was accepted
output "second_user_accepted" {
  value       = anyscale_organization_invitation.second_user.accepted_at != null
  description = "Whether the second invitation has been accepted"
}
