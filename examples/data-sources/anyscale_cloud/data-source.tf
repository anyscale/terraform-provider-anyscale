# Look up by name
data "anyscale_cloud" "by_name" {
  name = "my-terraform-cloud"
}

# Look up by ID
data "anyscale_cloud" "by_id" {
  id = "cld_abc123"
}

output "cloud_region" {
  value       = data.anyscale_cloud.by_name.region
  description = "The region the cloud is deployed in"
}

output "cloud_status" {
  value       = data.anyscale_cloud.by_name.status
  description = "Operational status of the cloud (ready, pending, failed)"
}

output "cloud_provider_by_id" {
  value       = data.anyscale_cloud.by_id.cloud_provider
  description = "Cloud provider (AWS, GCP, AZURE, GENERIC) when looking up by id"
}
