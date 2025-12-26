output "cloud_id" {
  description = "The ID of the created Anyscale cloud"
  value       = anyscale_cloud.primary.id
}

output "cloud_name" {
  description = "The name of the created Anyscale cloud"
  value       = anyscale_cloud.primary.name
}

output "cloud_resource_id" {
  description = "The ID of the created Anyscale cloud resource"
  value       = anyscale_cloud_resource.primary.id
}
