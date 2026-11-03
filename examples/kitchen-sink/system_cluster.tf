# --- anyscale_system_cluster (Cloud B, gated by enable_system_cluster) ---------------------------
# Enables and starts Cloud B's System Cluster, which backs the task and actor observability
# dashboards. Create waits until it is RUNNING. Off by default: destroying this resource only
# removes it from state, and deleting Cloud B does not stop the cluster either, so its instance
# keeps running and blocks the VPC's teardown. Before destroying, stop the cluster from the
# console, or terminate its instance in EC2.

resource "anyscale_system_cluster" "b" {
  count = var.enable_system_cluster ? 1 : 0

  # From cc_b rather than anyscale_cloud.b, so the cluster starts only once Cloud B can launch
  # clusters.
  cloud_id = anyscale_compute_config.cc_b.cloud_id
}
