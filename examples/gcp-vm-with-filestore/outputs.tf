output "cloud_id" {
  description = "The ID of the created Anyscale cloud"
  value       = anyscale_cloud.test.cloud_id
}

output "cloud_name" {
  description = "The name of the created Anyscale cloud"
  value       = anyscale_cloud.test.name
}

output "cloud_state" {
  description = "The current state of the Anyscale cloud"
  value       = anyscale_cloud.test.state
}

output "cloud_status" {
  description = "The current status of the Anyscale cloud"
  value       = anyscale_cloud.test.status
}

output "filestore_name" {
  description = "The Filestore instance name"
  value       = module.google_anyscale_v2.filestore_name
}
