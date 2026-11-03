# --- anyscale_system_cluster (Cloud B) -----------------------------------------------------------
# Enables and starts Cloud B's System Cluster, which backs the task and actor observability
# dashboards. Create waits until it is RUNNING. Destroying this resource only removes it from
# state: the cluster keeps running until Cloud B itself is destroyed.

resource "anyscale_system_cluster" "b" {
  # From cc_b rather than anyscale_cloud.b, so the cluster starts only once Cloud B can launch
  # clusters.
  cloud_id = anyscale_compute_config.cc_b.cloud_id
}
