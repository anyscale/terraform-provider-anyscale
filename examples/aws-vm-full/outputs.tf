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

output "efs_id" {
  description = "The EFS file system ID"
  value       = module.aws_anyscale_v2.anyscale_efs_id
}

output "memorydb_cluster_id" {
  description = "The MemoryDB cluster ID"
  value       = module.aws_anyscale_v2.anyscale_memorydb_cluster_id
}
