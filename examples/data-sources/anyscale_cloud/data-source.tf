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

output "cloud_is_k8s" {
  value       = data.anyscale_cloud.by_name.is_k8s
  description = "Whether this cloud uses Kubernetes"
}

output "cloud_availability_zones" {
  value       = data.anyscale_cloud.by_name.availability_zones
  description = "Availability zones considered for this cloud"
}

output "cloud_version" {
  value       = data.anyscale_cloud.by_name.version
  description = "Cluster management stack version (v1 or v2)"
}

output "cloud_external_id" {
  value       = data.anyscale_cloud.by_name.external_id
  description = "External ID for cross-account trust relationships; null if not set"
}
