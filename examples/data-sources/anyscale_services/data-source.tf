# List every service in a project
data "anyscale_services" "by_project" {
  project_id = "prj_abc123"
}

# Filter by partial name match, narrowed to a cloud by name
data "anyscale_services" "matching" {
  name_contains = "training"
  cloud_name    = "my-terraform-cloud"
}

output "project_service_count" {
  value       = length(data.anyscale_services.by_project.services)
  description = "How many services exist in this project"
}

output "project_service_names" {
  value       = [for s in data.anyscale_services.by_project.services : s.name]
  description = "Names of every service in this project"
}

# Each entry carries the same full detail as anyscale_service (nested
# primary_version, service_observability_urls, etc.) - not a trimmed summary -
# since the backend's list response already includes it at no extra API-call
# cost, unlike anyscale_projects which trims collaborators to avoid an N+1.
output "unhealthy_services" {
  value = [
    for s in data.anyscale_services.by_project.services : s.name
    if s.current_state != "RUNNING" && s.current_state != "TERMINATED"
  ]
  description = "Names of services in a state other than RUNNING or TERMINATED"
}

output "matching_service_names" {
  value       = [for s in data.anyscale_services.matching.services : s.name]
  description = "Names of every service matching the name_contains + cloud_name filters above"
}
