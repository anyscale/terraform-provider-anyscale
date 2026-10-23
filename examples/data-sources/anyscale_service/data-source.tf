# Look up by ID
data "anyscale_service" "by_id" {
  id = "service2_abc123"
}

# Look up by name. Service names are unique only WITHIN A PROJECT, not
# organization-wide, so if two different projects each have a service
# called "my-service" this lookup errors out asking you to set
# project_id (and/or cloud_id) to disambiguate - it never silently
# guesses which one you meant the way this provider's other by-name
# lookups (cloud, project, compute config, container image) pick the
# most recently created match.
data "anyscale_service" "by_name" {
  name = "my-service"
  # only required if "my-service" isn't unique org-wide
  project_id = "prj_abc123"
}

output "service_state" {
  value       = data.anyscale_service.by_id.current_state
  description = "Lifecycle state, e.g. RUNNING, UNHEALTHY, TERMINATED"
}

output "service_base_url" {
  value       = data.anyscale_service.by_id.base_url
  description = "The base URL clients use to reach this service"
}

# canary_version is null except while a rollout is actively in progress
# - check for null before reading fields off it.
output "service_is_rolling_out" {
  value       = data.anyscale_service.by_id.canary_version != null
  description = "Whether a canary rollout is currently in progress"
}

# ray_serve_config is a dynamic, open-ended JSON blob upstream (no fixed
# schema - it can hold arbitrary Ray Serve deployment settings), always
# present on a version (never null). Decode it with jsondecode() to
# reach specific keys instead of treating it as an opaque string, the
# same pattern used for anyscale_compute_config's
# advanced_instance_config/flags.
output "service_ray_serve_config" {
  value = jsondecode(
    data.anyscale_service.by_id.primary_version.ray_serve_config
  )
  description = "Primary version's Ray Serve config, decoded from JSON"
}

output "service_status_checklist" {
  value       = data.anyscale_service.by_id.service_status_checklist
  description = "Per-component status; null if terminated or new"
}

output "service_by_name_id" {
  value       = data.anyscale_service.by_name.id
  description = "The id resolved from the by-name lookup above"
}
