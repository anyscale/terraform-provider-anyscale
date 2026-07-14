# --- anyscale_compute_config -------------------------------------------------------------------
resource "anyscale_compute_config" "main" {
  name     = "${var.name_prefix}-compute-config"
  cloud_id = anyscale_cloud.main.id

  head_node = {
    instance_type = var.head_node_instance_type
  }

  worker_nodes = [
    {
      name          = "workers"
      instance_type = var.worker_instance_type
      min_nodes     = 0
      max_nodes     = 5
    }
  ]

  idle_termination_minutes = 30
}
