# Send an invitation to a new user with collaborator permissions
resource "anyscale_organization_invitation" "new_user" {
  email            = "newuser@example.com"
  permission_level = "collaborator"
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

# Send invitation to an owner
resource "anyscale_organization_invitation" "new_owner" {
  email            = "admin@example.com"
  permission_level = "owner"
}

# Check if invitation was accepted
output "new_owner_accepted" {
  value       = anyscale_organization_invitation.new_owner.accepted_at != null
  description = "Whether the owner invitation has been accepted"
}
