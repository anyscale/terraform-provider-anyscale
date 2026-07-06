# List all container images in a project, including archived ones
data "anyscale_container_images" "project_images" {
  project_id       = "prj_abc123"
  include_archived = true
}

# Filter by a partial name match
data "anyscale_container_images" "training" {
  name_contains = "training"
}

output "container_image_names" {
  value       = [for img in data.anyscale_container_images.project_images.container_images : img.name]
  description = "Names of every container image in the project"
}

output "training_image_ids" {
  value       = [for img in data.anyscale_container_images.training.container_images : img.id]
  description = "IDs of container images whose name contains \"training\""
}
