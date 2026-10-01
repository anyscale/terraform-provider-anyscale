# Enable and start this cloud's System Cluster (task/actor observability dashboards).
# Creating this resource waits until it reaches RUNNING. Later applies do not restart a
# terminated cluster; use `terraform apply -replace` for that.
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
