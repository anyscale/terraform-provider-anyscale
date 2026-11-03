# --- anyscale_system_cluster (Cloud B, gated by enable_system_cluster) ---------------------------
# Starts Cloud B's System Cluster, which backs the task and actor dashboards. Create waits for
# RUNNING. Off by default because neither destroying this resource (state only) nor deleting
# Cloud B stops the cluster: its instance keeps running and blocks VPC teardown. Before destroying,
# stop the cluster in the console or terminate its instance in EC2.

resource "anyscale_system_cluster" "b" {
  count = var.enable_system_cluster ? 1 : 0

  # From cc_b, so the cluster starts only once Cloud B can launch clusters.
  cloud_id = anyscale_compute_config.cc_b.cloud_id
}
