# --- anyscale_system_cluster (Cloud B, gated by enable_system_cluster) ---------------------------
# Starts Cloud B's System Cluster, which backs the task and actor dashboards. Create waits for
# RUNNING. Off by default because it starts an instance. Destroying this resource only removes it
# from state; destroying Cloud B terminates the cluster's instance.

resource "anyscale_system_cluster" "b" {
  count = var.enable_system_cluster ? 1 : 0

  # From cc_b, so the cluster starts only once Cloud B can launch clusters.
  cloud_id = anyscale_compute_config.cc_b.cloud_id
}
