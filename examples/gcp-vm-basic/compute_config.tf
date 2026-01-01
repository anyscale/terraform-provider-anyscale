# Basic GCP Compute Config
# This example demonstrates a simple compute config with head node and worker nodes

resource "anyscale_compute_config" "basic" {
  name     = "${var.cloud_name}-basic-compute"
  cloud_id = anyscale_cloud.primary.id
  # project_id is optional - omit to use organization default

  idle_termination_minutes  = 30
  enable_cross_zone_scaling = false

  head_node = {
    instance_type = "n2-standard-8"
  }

  worker_nodes = [
    {
      instance_type = "n2-standard-16"
      min_nodes     = 1
      max_nodes     = 5
      market_type   = "ON_DEMAND"
    },
    {
      instance_type = "n2-standard-32"
      min_nodes     = 0
      max_nodes     = 3
      market_type   = "PREFER_SPOT"
    }
  ]
}

# Advanced GCP Compute Config with custom resources and advanced configurations
# Demonstrates resource constraints, advanced instance configs, and flags

resource "anyscale_compute_config" "advanced" {
  name     = "${var.cloud_name}-advanced-compute"
  cloud_id = anyscale_cloud.primary.id
  # project_id is optional - omit to use organization default

  idle_termination_minutes  = 60
  maximum_uptime_minutes    = 480
  enable_cross_zone_scaling = true

  # Resource constraints
  min_resources = {
    "CPU" = 4
    "GPU" = 0
  }

  max_resources = {
    "CPU"    = 100
    "GPU"    = 8
    "memory" = 1024
  }

  # Cluster-level flags
  flags = {
    "idle_termination_seconds"    = 300
    "workload_starting_timeout"   = "30m"
    "workload_recovering_timeout" = "20m"
    "instance_selection_strategy" = "relaxed"
  }

  # Advanced configurations for GCP
  advanced_configurations_json = {
    instance_properties = {
      disks = [
        {
          boot        = true
          auto_delete = true
          initialize_params = {
            disk_size_gb = 100
            disk_type    = "pd-standard"
          }
        }
      ]
    }
  }

  head_node = {
    instance_type = "n2-standard-16"

    # Custom resources for head node
    resources = {
      "CPU"    = 16
      "memory" = 64
    }

    # Labels for head node scheduling
    labels = {
      "node_type" = "head"
      "workload"  = "control"
    }
  }

  worker_nodes = [
    {
      name          = "general-compute"
      instance_type = "n2-standard-16"
      min_nodes     = 2
      max_nodes     = 10
      market_type   = "ON_DEMAND"

      # Custom resources
      resources = {
        "CPU"    = 16
        "memory" = 64
      }

      # Labels for worker node scheduling
      labels = {
        "node_type" = "worker"
        "workload"  = "general"
      }
    },
    {
      name          = "gpu-workers"
      instance_type = "n1-standard-8"
      min_nodes     = 0
      max_nodes     = 5
      market_type   = "SPOT"

      # Custom resources for GPU nodes
      resources = {
        "CPU"    = 8
        "GPU"    = 1
        "memory" = 32
      }

      # Labels for GPU scheduling
      labels = {
        "node_type"   = "worker"
        "accelerator" = "nvidia-tesla-t4"
        "workload"    = "ml-training"
      }

      required_labels = {
        "ray.io/node-type" = "worker"
      }

      # Note: GCP GPU configuration (guest_accelerators) cannot be set via advanced_instance_config
      # The API derives GPU configuration from instance type and other sources
      # For GPU instances, use appropriate GPU-capable instance types
    },
    {
      name          = "spot-workers"
      instance_type = "n2-standard-32"
      min_nodes     = 0
      max_nodes     = 20
      market_type   = "PREFER_SPOT"

      resources = {
        "CPU"    = 32
        "memory" = 128
      }

      labels = {
        "node_type" = "worker"
        "workload"  = "batch"
      }

      # Node-level flags
      flags = jsonencode({
        "replacement_threshold" = "15m"
      })
    }
  ]
}

# Simple compute config example
resource "anyscale_compute_config" "simple" {
  name     = "tf-example-simple-config"
  cloud_id = anyscale_cloud.primary.id

  head_node = {
    instance_type = "n2-standard-4"
  }

  worker_nodes = [
    {
      instance_type = "n2-standard-8"
      min_nodes     = 0
      max_nodes     = 3
    }
  ]
}

# Compute config with custom instance type (free pod shapes for GKE)
# Uncomment for GKE deployments

# resource "anyscale_compute_config" "custom_instance" {
#   name       = "${var.cloud_name}-custom-compute"
#   cloud_id   = anyscale_cloud.primary.id
#   project_id = var.anyscale_project_id
#
#   head_node {
#     instance_type = "custom"
#
#     required_resources {
#       cpu    = 4
#       memory = "16Gi"
#     }
#   }
#
#   worker_nodes {
#     instance_type = "custom"
#     min_nodes     = 1
#     max_nodes     = 10
#
#     required_resources {
#       cpu    = 8
#       memory = "32Gi"
#       gpu    = 1
#       accelerator = "T4"
#     }
#
#     labels = {
#       "ray.io/accelerator-type" = "T4"
#     }
#   }
# }

# TPU compute config example for GKE
# Uncomment for GKE deployments with TPU support

# resource "anyscale_compute_config" "tpu" {
#   name       = "${var.cloud_name}-tpu-compute"
#   cloud_id   = anyscale_cloud.primary.id
#   project_id = var.anyscale_project_id
#
#   head_node {
#     instance_type = "n2-standard-8"
#   }
#
#   worker_nodes {
#     name          = "tpu-workers"
#     instance_type = "custom"
#     min_nodes     = 0
#     max_nodes     = 4
#
#     required_resources {
#       cpu       = 7
#       memory    = "12Gi"
#       tpu       = 4
#       tpu_hosts = 4
#       accelerator = "TPU-V6E"
#     }
#
#     # TPU labels for node selector derivation
#     labels = {
#       "ray.io/accelerator-type" = "TPU-V6E"
#       "ray.io/tpu-topology"     = "2x2"
#     }
#
#     # Alternative: explicit node selectors
#     # advanced_instance_config = ({
#     #   spec = {
#     #     nodeSelector = {
#     #       "cloud.google.com/gke-tpu-topology"     = "2x2"
#     #       "cloud.google.com/gke-tpu-accelerator" = "tpu-v6e-podslice"
#     #     }
#     #   }
#     # })
#   }
# }
