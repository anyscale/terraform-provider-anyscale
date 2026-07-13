# The organization the provider's token is connected to - takes no arguments
data "anyscale_organization" "current" {}

output "organization_id" {
  value       = data.anyscale_organization.current.id
  description = "ID of the connected organization"
}

output "organization_default_cloud_id" {
  value       = data.anyscale_organization.current.default_cloud_id
  description = "Default cloud ID for the organization, if one is configured"
}
