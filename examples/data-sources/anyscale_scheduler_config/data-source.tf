# The organization's active Anyscale Scheduler config - takes no arguments.
# Read-only: this data source never writes the config and never becomes a
# second writer alongside an `anyscale_scheduler_config` resource. See the
# schema description for why a workspace that manages the resource still
# needs `depends_on` to read its own applied state back reliably.
data "anyscale_scheduler_config" "current" {}

output "scheduler_config_version" {
  value       = data.anyscale_scheduler_config.current.version
  description = "Version number of the currently active config"
}

output "scheduler_config_applied_at" {
  value       = data.anyscale_scheduler_config.current.created_at
  description = "When the active version was applied, RFC 3339"
}

output "scheduler_config_applied_by" {
  value       = data.anyscale_scheduler_config.current.creator_id
  description = "ID of the user that applied the active version, or null if applied by an automated process"
}

output "scheduler_config_flavor_names" {
  value       = [for f in data.anyscale_scheduler_config.current.resource_flavors : f.name]
  description = "Names of every resource flavor declared in the active config"
}

output "scheduler_config_queue_names" {
  value       = [for q in data.anyscale_scheduler_config.current.resource_queues : q.name]
  description = "Names of every resource queue declared in the active config, e.g. to validate a compute config targets one that exists"
}
