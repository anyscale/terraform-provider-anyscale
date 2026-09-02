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
  description = "The built image URI, for use in compute configs or job submissions"
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
  description = "The content digest of the latest build (e.g. sha256:...); null if the image has no successful build yet"
}

output "container_image_build_error" {
  value       = data.anyscale_container_image.by_name.build_error_message
  description = "The error message from the latest build, if it failed; null otherwise"
}

output "container_image_cloud_id" {
  value       = data.anyscale_container_image.by_name.cloud_id
  description = "The cloud this image is associated with; null if not associated with a specific cloud"
}

output "container_image_is_default" {
  value       = data.anyscale_container_image.by_name.is_default
  description = "Whether this is an Anyscale-provided base image, as opposed to one created in this organization"
}

output "container_image_is_experimental" {
  value       = data.anyscale_container_image.by_name.is_experimental
  description = "Whether this is an experimental container image"
}

output "container_image_last_modified_at" {
  value       = data.anyscale_container_image.by_name.last_modified_at
  description = "When the container image was last modified"
}
