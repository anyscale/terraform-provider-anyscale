# Ensure this cloud's System Cluster (task/actor observability dashboards) is enabled and
# running. Creating this resource enables the System Cluster if needed, starts it if it is
# terminated, and waits until it reaches RUNNING.
resource "anyscale_system_cluster" "primary" {
  cloud_id = "cld_abc123"
}

# Destroying this resource only removes it from Terraform state - it does not stop, disable, or
# terminate the running System Cluster. See the resource docs for how to do that directly.

output "system_cluster_state" {
  value       = anyscale_system_cluster.primary.state
  description = "The System Cluster's current status (e.g. Running, StartingUp, Terminated)"
}

output "system_cluster_workload_service_url" {
  value       = anyscale_system_cluster.primary.workload_service_url
  description = "URL the task/actor observability dashboards use to reach this System Cluster"
}
