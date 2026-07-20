# Basic AWS Compute Config
# This example demonstrates a simple compute config with head node and worker nodes

resource "anyscale_compute_config" "basic" {
  name     = "${var.cloud_name}-basic-compute"
  cloud_id = anyscale_cloud.primary.id
  # project_id is optional - omit to use organization default

  enable_cross_zone_scaling = false

  head_node = {
    instance_type = "m7a.2xlarge"
  }

  worker_nodes = [
    {
      instance_type = "m8a.4xlarge"
      min_nodes     = 1
      max_nodes     = 5
      market_type   = "ON_DEMAND"
    },
    {
      instance_type = "m5.8xlarge"
      min_nodes     = 0
      max_nodes     = 3
      market_type   = "PREFER_SPOT"
    }
  ]
}

# Advanced AWS Compute Config with custom resources and advanced configurations
# Demonstrates resource constraints, advanced instance configs, and flags
resource "anyscale_compute_config" "advanced" {
  name     = "${var.cloud_name}-advanced-compute"
  cloud_id = anyscale_cloud.primary.id
  # project_id is optional - omit to use organization default

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
    workload_starting_timeout   = "30m"
    workload_recovering_timeout = "20m"
    instance_selection_strategy = "relaxed"
  }


  # Advanced configurations for AWS
  advanced_instance_config = {
    BlockDeviceMappings = [
      {
        DeviceName = "/dev/sda1"
        Ebs = {
          VolumeSize          = 100
          VolumeType          = "gp3"
          DeleteOnTermination = true
        }
      }
    ]
  }

  head_node = {
    instance_type = "m5.4xlarge"

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
      instance_type = "m5.4xlarge"
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

      # Node-level advanced configuration
      advanced_instance_config = jsonencode({
        IamInstanceProfile = {
          Arn = module.aws_anyscale_v2.anyscale_iam_instance_profile_role_arn
        }
      })
    },
    {
      name          = "gpu-workers"
      instance_type = "g5.2xlarge"
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
        "accelerator" = "nvidia-a10g"
        "workload"    = "ml-training"
      }
    },
    {
      name          = "spot-workers"
      instance_type = "m5.8xlarge"
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
        replacement_threshold = "15m"
      })
    }
  ]
}

# Multi-resource compute config example (experimental)
# Note: This requires multiple cloud resources to be configured

# Uncomment if you have multiple cloud resources configured
# resource "anyscale_compute_config" "multi_resource" {
#   name     = "${var.cloud_name}-multi-resource-compute"
#   cloud_id = anyscale_cloud.primary.id
#   # project_id is optional - omit to use organization default
#
#   head_node = {
#     instance_type = "m5.2xlarge"
#
#     # Target specific deployment
#     cloud_deployment = {
#       region   = "us-west-2"
#       provider = "aws"
#     }
#   }
#
#   worker_nodes = [
#     {
#       instance_type = "m5.4xlarge"
#       min_nodes     = 1
#       max_nodes     = 5
#
#       # Target specific deployment
#       cloud_deployment = {
#         region   = "us-west-2"
#         provider = "aws"
#       }
#     }
#   ]
# }

# Simple compute config example
resource "anyscale_compute_config" "simple" {
  name     = "tf-example-simple-config"
  cloud_id = anyscale_cloud.primary.id

  head_node = {
    instance_type = "m5.xlarge"
  }

  worker_nodes = [
    {
      instance_type = "m5.2xlarge"
      min_nodes     = 0
      max_nodes     = 3
    }
  ]
}
