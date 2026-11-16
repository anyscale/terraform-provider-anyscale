variable "cloud_name" {
  description = "Name of an existing Anyscale cloud to work in"
  type        = string
}

variable "name_prefix" {
  description = "Prefix for the group and projects this creates"
  type        = string
  default     = "rbac-demo"
}

# Every email must belong to someone who has already accepted their
# invitation to the organization. Invite people first with
# anyscale_iam_organization_invitation, then add them here.
variable "team_member_emails" {
  description = "Accepted organization members to put in the group"
  type        = list(string)
  default     = []
}

# One accepted member to grant a role directly, without a group. Leave
# empty to skip that step. It must not be the identity running
# Terraform: an identity cannot grant itself a role.
variable "direct_member_email" {
  description = "Accepted member to grant a role directly, or empty"
  type        = string
  default     = ""
}

# Off by default: an organization-scope grant applies across the whole
# organization.
variable "grant_organization_role" {
  description = "Also grant the group a role on the organization"
  type        = bool
  default     = false
}
