output "cloud_id" {
  description = "The ID of the created Anyscale cloud"
  value       = anyscale_cloud.test.cloud_id
}

output "cloud_name" {
  description = "The name of the created Anyscale cloud"
  value       = anyscale_cloud.test.name
}

output "cloud_resource_id" {
  description = "The ID of the created Anyscale cloud resource"
  value       = anyscale_cloud_resource.primary.id
}
