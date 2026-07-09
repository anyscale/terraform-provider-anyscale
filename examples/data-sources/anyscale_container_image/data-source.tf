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
