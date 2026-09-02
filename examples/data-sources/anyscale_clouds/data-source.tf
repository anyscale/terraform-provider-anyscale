# List all clouds visible to the authenticated user/token
data "anyscale_clouds" "all" {}

# Filter to a specific provider and region
data "anyscale_clouds" "aws_us_east" {
  cloud_provider = "AWS"
  region         = "us-east-2"
}

# Filter by a partial name match
data "anyscale_clouds" "staging" {
  name_contains = "staging"
}

output "all_cloud_names" {
  value       = [for c in data.anyscale_clouds.all.clouds : c.name]
  description = "Names of every cloud visible to this token"
}

output "aws_us_east_cloud_ids" {
  value       = [for c in data.anyscale_clouds.aws_us_east.clouds : c.id]
  description = "IDs of AWS clouds in us-east-2"
}

output "staging_cloud_ids" {
  value       = [for c in data.anyscale_clouds.staging.clouds : c.id]
  description = "IDs of clouds whose name contains \"staging\""
}

output "kubernetes_clouds" {
  value       = [for c in data.anyscale_clouds.all.clouds : c.name if c.is_k8s]
  description = "Names of clouds that use Kubernetes"
}

output "cloud_availability_zones_by_name" {
  value = {
    for c in data.anyscale_clouds.all.clouds : c.name => c.availability_zones
  }
  description = "Availability zones for every visible cloud, keyed by name"
}

output "cloud_versions_and_external_ids" {
  value = [
    for c in data.anyscale_clouds.all.clouds : {
      name        = c.name
      version     = c.version
      external_id = c.external_id
    }
  ]
  description = "Cluster management stack version and external ID (null if not set) for every visible cloud"
}
