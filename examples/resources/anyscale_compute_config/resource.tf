# Compute config with native HCL syntax for flags and advanced instance config
resource "anyscale_compute_config" "example" {
  name     = "my-compute-config"
  cloud_id = "cld_abc123"

  head_node = {
    instance_type = "m5.2xlarge"
  }

  worker_nodes = [
    {
      instance_type = "m5.4xlarge"
      min_nodes     = 0
      max_nodes     = 10
    }
  ]

  # Terminate idle clusters after 30 minutes, and cap total uptime at 8 hours
  idle_termination_minutes = 30
  maximum_uptime_minutes   = 480

  # Native HCL syntax - no jsonencode needed!
  flags = {
    "ray-cluster-ray-version"        = "2.9.0"
    "ray-cluster-autoscaler-enabled" = "true"
  }

  # Native HCL for advanced configurations passed through to the cloud provider
  advanced_instance_config = {
    BlockDeviceMappings = [
      {
        DeviceName = "/dev/sda1"
        Ebs = {
          VolumeSize = 100
          VolumeType = "gp3"
        }
      }
    ]
  }
}

# Minimal compute config - worker nodes are auto-selected based on workload
resource "anyscale_compute_config" "minimal" {
  name     = "minimal-config"
  cloud_id = "cld_abc123"

  head_node = {
    instance_type = "m5.xlarge"
  }
}

# GPU compute config
resource "anyscale_compute_config" "gpu" {
  name     = "gpu-compute-config"
  cloud_id = "cld_abc123"

  head_node = {
    instance_type = "m5.2xlarge"
  }

  worker_nodes = [
    {
      instance_type = "g4dn.xlarge"
      min_nodes     = 0
      max_nodes     = 5

      resources = {
        CPU    = 4
        memory = 16
        GPU    = 1
      }
    }
  ]
}

# Output
output "compute_config_id" {
  value       = anyscale_compute_config.example.id
  description = "The unique identifier for the compute configuration"
}
