# Look up a global resource scheduler by name
data "anyscale_global_resource_scheduler" "example" {
  name = "my-machine-pool"
}

# Output the global resource scheduler details
output "machine_pool_id" {
  value       = data.anyscale_global_resource_scheduler.example.id
  description = "The unique identifier of the global resource scheduler"
}

output "organization_id" {
  value       = data.anyscale_global_resource_scheduler.example.organization_id
  description = "The organization that owns the global resource scheduler"
}

output "attached_clouds" {
  value       = data.anyscale_global_resource_scheduler.example.cloud_ids
  description = "List of cloud IDs attached to the global resource scheduler"
}

output "rootless_enabled" {
  value       = data.anyscale_global_resource_scheduler.example.enable_rootless_dataplane_config
  description = "Whether rootless dataplane is enabled"
}

# Use the global resource scheduler in other resources
resource "anyscale_compute_config" "example" {
  name     = "config-using-machine-pool"
  cloud_id = data.anyscale_global_resource_scheduler.example.cloud_ids[0]

  # Reference the global resource scheduler in cloud_deployment
  cloud_deployment {
    provider     = "AWS"
    region       = "us-west-2"
    machine_pool = data.anyscale_global_resource_scheduler.example.name
  }
}
