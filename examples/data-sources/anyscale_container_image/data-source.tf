# Look up by name (returns the latest build)
data "anyscale_container_image" "by_name" {
  name = "my-custom-image"
}

# Look up by ID
data "anyscale_container_image" "by_id" {
  id = "cenv_abc123"
}

output "container_image_uri" {
  value       = data.anyscale_container_image.by_name.image_uri
  description = "Built image URI, for compute configs and jobs"
}

output "container_image_build_status" {
  value       = data.anyscale_container_image.by_name.build_status
  description = "The status of the latest build"
}

output "container_image_uri_by_id" {
  value       = data.anyscale_container_image.by_id.image_uri
  description = "The image URI when looking up by id"
}

output "container_image_digest" {
  value       = data.anyscale_container_image.by_name.digest
  description = "Digest of the latest build; null if none succeeded"
}

output "container_image_build_error" {
  value = (
    data.anyscale_container_image.by_name.build_error_message
  )
  description = "Error message from the latest build; null if none"
}

output "container_image_cloud_id" {
  value       = data.anyscale_container_image.by_name.cloud_id
  description = "Associated cloud; null if not cloud-specific"
}

output "container_image_is_default" {
  value       = data.anyscale_container_image.by_name.is_default
  description = "True for Anyscale-provided base images"
}

output "container_image_is_experimental" {
  value       = data.anyscale_container_image.by_name.is_experimental
  description = "Whether this is an experimental container image"
}

output "container_image_last_modified_at" {
  value       = data.anyscale_container_image.by_name.last_modified_at
  description = "When the container image was last modified"
}
