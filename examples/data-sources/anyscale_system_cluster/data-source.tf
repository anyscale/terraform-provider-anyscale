# Read-only lookup of a cloud's System Cluster status. Always side-effect-free, even if the
# System Cluster has never been created (cluster_id/state/is_enabled come back null instead of
# an error in that case).
data "anyscale_system_cluster" "primary" {
  cloud_id = "cld_abc123"
}

output "system_cluster_state" {
  value       = data.anyscale_system_cluster.primary.state
  description = "The System Cluster's current status, or null if it has never been created"
}
