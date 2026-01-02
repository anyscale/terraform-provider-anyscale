# List all global resource schedulers in the organization
data "anyscale_global_resource_schedulers" "all" {
}

# List global resource schedulers with name filter
data "anyscale_global_resource_schedulers" "production" {
  name_contains = "prod"
}

# Output all global resource schedulers
output "all_machine_pools" {
  value = [
    for pool in data.anyscale_global_resource_schedulers.all.machine_pools : {
      id   = pool.id
      name = pool.name
    }
  ]
  description = "List of all global resource schedulers"
}

# Output production global resource schedulers
output "production_pools" {
  value = [
    for pool in data.anyscale_global_resource_schedulers.production.machine_pools : {
      id        = pool.id
      name      = pool.name
      cloud_ids = pool.cloud_ids
    }
  ]
  description = "List of production global resource schedulers"
}

# Count of global resource schedulers
output "total_machine_pools" {
  value       = length(data.anyscale_global_resource_schedulers.all.machine_pools)
  description = "Total number of global resource schedulers in the organization"
}
