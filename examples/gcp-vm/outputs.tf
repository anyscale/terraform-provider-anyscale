output "cloud_id" {
  description = "The ID of the created Anyscale cloud"
  value       = anyscale_cloud.primary.id
}

output "cloud_name" {
  description = "The name of the created Anyscale cloud"
  value       = anyscale_cloud.primary.name
}

output "is_empty_cloud" {
  description = "Whether the cloud was created as an empty shell"
  value       = anyscale_cloud.primary.is_empty_cloud
}

output "cloud_resource_id" {
  description = "The ID of the attached cloud resource"
  value       = anyscale_cloud_resource.primary.cloud_resource_id
}

output "cloud_resource_name" {
  description = "The name of the attached cloud resource"
  value       = anyscale_cloud_resource.primary.name
}

# Conditional outputs based on feature toggles

output "filestore_name" {
  description = "The Filestore instance name (if enabled)"
  value       = var.enable_filestore ? module.google_anyscale_v2.filestore_name : null
}

output "memorystore_id" {
  description = "The Memorystore instance ID (if enabled)"
  value       = var.enable_memorystore ? module.google_anyscale_v2.memorystore_id : null
}
