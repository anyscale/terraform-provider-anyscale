output "cloud_id" {
  description = "The ID of the created Anyscale cloud"
  value       = anyscale_cloud.primary.id
}

output "cloud_name" {
  description = "The name of the created Anyscale cloud"
  value       = anyscale_cloud.primary.name
}

output "primary_cloud_resource_id" {
  description = "The backend cloud_resource_id of the primary cloud resource"
  value       = anyscale_cloud_resource.primary.cloud_resource_id
}

output "secondary_cloud_resource_id" {
  description = "The backend cloud_resource_id of the secondary cloud resource"
  value       = anyscale_cloud_resource.secondary.cloud_resource_id
}
