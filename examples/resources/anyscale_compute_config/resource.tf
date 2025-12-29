# Compute Config with Native HCL Syntax
resource "anyscale_compute_config" "example" {
  name     = "my-compute-config"
  cloud_id = "cld_abc123"

  # Native HCL syntax - no jsonencode needed!
  flags = {
    "ray-cluster-ray-version"          = "2.9.0"
    "ray-cluster-kubernetes-namespace" = "anyscale"
    "ray-cluster-autoscaler-enabled"   = "true"
  }

  # Native HCL for advanced configurations
  advanced_configurations_json = {
    ray_head_node = {
      instance_type = "m5.large"
      min_instances = 1
      max_instances = 1
    }
    ray_worker_nodes = [
      {
        instance_type = "m5.xlarge"
        min_instances = 0
        max_instances = 10
        resources = {
          CPU = 4
          memory = 16
        }
      }
    ]
  }
}

# Minimal compute config
resource "anyscale_compute_config" "minimal" {
  name     = "minimal-config"
  cloud_id = "cld_abc123"
}

# GPU compute config
resource "anyscale_compute_config" "gpu" {
  name     = "gpu-compute-config"
  cloud_id = "cld_abc123"

  flags = {
    "ray-cluster-ray-version" = "2.9.0"
  }

  advanced_configurations_json = {
    ray_head_node = {
      instance_type = "m5.large"
      min_instances = 1
      max_instances = 1
    }
    ray_worker_nodes = [
      {
        instance_type = "g4dn.xlarge"
        min_instances = 0
        max_instances = 5
        resources = {
          CPU    = 4
          memory = 16
          GPU    = 1
        }
      }
    ]
  }
}

# Output
output "compute_config_id" {
  value       = anyscale_compute_config.example.id
  description = "The unique identifier for the compute configuration"
}
