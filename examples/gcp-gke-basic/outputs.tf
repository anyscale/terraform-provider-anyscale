output "anyscale_operator_service_account_email" {
  description = "The Anyscale operator service account email."
  value       = google_service_account.gke_nodes.email
}

output "cloud_id" {
  description = "The ID of the created Anyscale cloud"
  value       = anyscale_cloud.primary.id
}

output "cloud_name" {
  description = "The name of the created Anyscale cloud"
  value       = anyscale_cloud.primary.name
}

output "cloud_deployment_id" {
  description = "The cloud deployment ID. Pass this to the Anyscale operator during installation."
  value       = anyscale_cloud_resource.primary.cloud_deployment_id
}
