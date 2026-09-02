# Look up the latest version by name, scoped to a cloud
data "anyscale_compute_config" "by_name" {
  name       = "my-compute-config"
  cloud_name = "my-terraform-cloud"
}

# Look up a specific version by its version-specific config_id
data "anyscale_compute_config" "by_id" {
  id = "cpt_abc123"
}

output "compute_config_name_version" {
  value       = data.anyscale_compute_config.by_name.name_version
  description = "The config formatted as name:version, for use with Anyscale APIs"
}

output "compute_config_versions" {
  value       = data.anyscale_compute_config.by_name.versions
  description = "All available version numbers for this compute config"
}

output "compute_config_region_by_id" {
  value       = data.anyscale_compute_config.by_id.region
  description = "Region for the compute config looked up by its version-specific config_id"
}

# head_node, worker_nodes, and zones report exactly what the API has - unlike
# the resource, nothing here is masked based on what you did or didn't
# configure, since a data source has no plan to protect from drift.
output "compute_config_head_node_instance_type" {
  value       = data.anyscale_compute_config.by_name.head_node.instance_type
  description = "Instance type of the head node"
}

output "compute_config_worker_node_names" {
  value       = [for w in data.anyscale_compute_config.by_name.worker_nodes : w.name]
  description = "Names of all worker node groups"
}

output "compute_config_zones" {
  value       = data.anyscale_compute_config.by_name.zones
  description = "Availability zones considered for this cluster"
}
