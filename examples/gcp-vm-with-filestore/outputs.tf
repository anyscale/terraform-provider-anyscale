output "id" {
  description = "The ID of the created Anyscale cloud"
  value       = anyscale_cloud.primary.id
}

output "cloud_name" {
  description = "The name of the created Anyscale cloud"
  value       = anyscale_cloud.primary.name
}



output "filestore_name" {
  description = "The Filestore instance name"
  value       = module.google_anyscale_v2.filestore_name
}
