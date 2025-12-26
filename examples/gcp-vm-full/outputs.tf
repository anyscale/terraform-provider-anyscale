output "cloud_id" {
  description = "The ID of the created Anyscale cloud"
  value       = anyscale_cloud.test.cloud_id
}

output "cloud_name" {
  description = "The name of the created Anyscale cloud"
  value       = anyscale_cloud.test.name
}



output "filestore_name" {
  description = "The Filestore instance name"
  value       = module.google_anyscale_v2.filestore_name
}

output "memorystore_id" {
  description = "The Memorystore instance ID"
  value       = module.google_anyscale_v2.memorystore_id
}
