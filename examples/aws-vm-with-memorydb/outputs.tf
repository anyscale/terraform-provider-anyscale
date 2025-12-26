output "cloud_id" {
  description = "The ID of the created Anyscale cloud"
  value       = anyscale_cloud.test.cloud_id
}

output "cloud_name" {
  description = "The name of the created Anyscale cloud"
  value       = anyscale_cloud.test.name
}



output "memorydb_cluster_id" {
  description = "The MemoryDB cluster ID"
  value       = module.aws_anyscale_v2.anyscale_memorydb_cluster_id
}
