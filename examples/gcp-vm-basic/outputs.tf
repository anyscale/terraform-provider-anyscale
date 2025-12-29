output "cloud_id" {
  description = "The ID of the created Anyscale cloud"
  value       = anyscale_cloud.primary.id
}

output "cloud_name" {
  description = "The name of the created Anyscale cloud"
  value       = anyscale_cloud.primary.name
}


output "compute_config_id" {
  description = "The ID of the created Anyscale compute config"
  value       = anyscale_compute_config.basic.id
}
