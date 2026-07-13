# List all container images in a project, including archived ones
data "anyscale_container_images" "project_images" {
  project_id       = "prj_abc123"
  include_archived = true
}

# Filter by a partial name match
data "anyscale_container_images" "training" {
  name_contains = "training"
}

# Filter by the underlying base/BYOD image name, and by cloud
data "anyscale_container_images" "pytorch_images" {
  image_name_contains = "pytorch"
  cloud_id            = "cld_abc123"
}

output "container_image_names" {
  value       = [for img in data.anyscale_container_images.project_images.container_images : img.name]
  description = "Names of every container image in the project"
}

output "training_image_ids" {
  value       = [for img in data.anyscale_container_images.training.container_images : img.id]
  description = "IDs of container images whose name contains \"training\""
}

output "pytorch_image_uris" {
  value = [
    for img in data.anyscale_container_images.pytorch_images.container_images : img.image_uri
  ]
  description = "Registry image URIs for pytorch-named images in the given cloud; null for images with no successful build"
}

output "default_images" {
  value = [
    for img in data.anyscale_container_images.project_images.container_images : img.name
    if img.is_default
  ]
  description = "Anyscale-provided base images among this project's images, as opposed to user-created ones"
}

output "container_image_last_modified" {
  value = {
    for img in data.anyscale_container_images.project_images.container_images : img.name => img.last_modified_at
  }
  description = "Last-modified timestamp for every image in the project, keyed by name"
}
