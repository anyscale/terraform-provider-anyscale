# Global Resource Scheduler with full configuration
resource "anyscale_global_resource_scheduler" "example" {
  name = "my-machine-pool"

  # Attach to one or more clouds
  cloud_attachment {
    cloud_id = "cld_abc123"
  }

  # Native HCL syntax for spec configuration
  spec {
    machine_type {
      name = "RES-8CPU-32GB"

      launch_template {
        instance_type = "m5.2xlarge"
        market_type   = "ON_DEMAND"
        zones         = ["us-west-2a", "us-west-2b"]
      }

      launch_template {
        instance_type = "m5.2xlarge"
        market_type   = "SPOT"
        zones         = ["us-west-2a", "us-west-2b"]
      }

      recycle_policy {
        max_workloads     = 100
        rotation_interval = "24h"
        max_idle_duration = "60m"
      }

      partition {
        name = "default"
        size = 10

        rule {
          selector = "workload-type in (job)"
          priority = 100
        }
      }

      partition {
        name = "production"
        size = 20

        rule {
          selector = "workload-type in (service)"
          priority = 200
          quota    = 15
        }

        rule {
          selector = "workload-type in (job)"
          priority = 50
          quota    = 5
        }
      }
    }

    machine_type {
      name = "RES-GPU-A10"

      launch_template {
        instance_type = "g5.2xlarge"
        market_type   = "ON_DEMAND"
      }

      partition {
        name = "gpu-partition"
        size = 5

        rule {
          selector = "workload-type in (job,service)"
          priority = 100
        }
      }
    }
  }
}

# Minimal global resource scheduler (empty pool, can be configured later)
resource "anyscale_global_resource_scheduler" "minimal" {
  name = "minimal-pool"
}

# Machine pool with cloud_name reference
resource "anyscale_global_resource_scheduler" "with_cloud_name" {
  name = "my-pool-by-cloud-name"

  cloud_attachment {
    cloud_name = "my-production-cloud"
  }

  spec {
    machine_type {
      name = "RES-4CPU-16GB"

      launch_template {
        instance_type = "m5.xlarge"
        market_type   = "SPOT"
      }

      partition {
        name = "default"
        size = 5

        rule {
          selector = "workload-type in (job)"
          priority = 100
        }
      }
    }
  }
}

# Machine pool with rootless dataplane config
resource "anyscale_global_resource_scheduler" "rootless" {
  name                             = "rootless-pool"
  enable_rootless_dataplane_config = true

  cloud_attachment {
    cloud_id = "cld_abc123"
  }

  spec {
    machine_type {
      name = "RES-8CPU-32GB"

      launch_template {
        instance_type = "m5.2xlarge"
        market_type   = "ON_DEMAND"
      }

      partition {
        name = "default"
        size = 10

        rule {
          selector = "workload-type in (job)"
          priority = 100
        }
      }
    }
  }
}

# Output
output "machine_pool_id" {
  value       = anyscale_global_resource_scheduler.example.id
  description = "The unique identifier for the global resource scheduler"
}

output "attached_cloud_ids" {
  value       = anyscale_global_resource_scheduler.example.cloud_ids
  description = "List of cloud IDs attached to the global resource scheduler"
}
