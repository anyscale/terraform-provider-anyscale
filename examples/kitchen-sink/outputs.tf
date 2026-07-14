# --- Resources -------------------------------------------------------------------------------
output "cloud_id" {
  value       = anyscale_cloud.main.id
  description = "ID of the cloud created by this example."
}

output "cloud_resource_operator_status" {
  value       = anyscale_cloud_resource.main.operator_status
  description = "Health status reported by the Anyscale Operator for the attached compute stack; null until it has reported in."
}

output "compute_config_name_version" {
  value       = anyscale_compute_config.main.name_version
  description = "The name:revision handle for the compute config, for use in job/service submission."
}

output "training_image_name_version" {
  value       = anyscale_container_image_build.training.name_version
  description = "The name:revision handle for the built training image."
}

output "base_image_digest" {
  value       = anyscale_container_image_registry.base.digest
  description = "Content digest of the registered base image's latest successful build."
}

output "project_id" {
  value       = anyscale_project.main.id
  description = "ID of the project created by this example."
}

output "new_member_invitation_status" {
  value       = anyscale_organization_invitation.new_member.status
  description = "Status of the invitation sent to new_member_email: pending, accepted, or expired."
}

# --- Data sources ------------------------------------------------------------------------------
output "ds_cloud_provider" {
  value       = data.anyscale_cloud.lookup.cloud_provider
  description = "cloud_provider read back via the anyscale_cloud data source, confirming the create-then-read pattern works."
}

output "ds_aws_cloud_count" {
  value       = length(data.anyscale_clouds.aws_clouds.clouds)
  description = "Total AWS clouds visible to this token, including the one just created."
}

output "ds_compute_config_id" {
  value       = data.anyscale_compute_config.lookup.id
  description = "Compute config ID read back via the anyscale_compute_config data source."
}

output "ds_training_image_build_status" {
  value       = data.anyscale_container_image.training_lookup.build_status
  description = "Build status read back via the anyscale_container_image data source."
}

output "ds_matching_image_count" {
  value       = length(data.anyscale_container_images.all.container_images)
  description = "Number of container images whose name contains name_prefix, including both created above."
}

output "ds_project_directory_name" {
  value       = data.anyscale_project.lookup.directory_name
  description = "Storage directory name read back via the anyscale_project data source."
}

output "ds_projects_in_cloud_count" {
  value       = length(data.anyscale_projects.in_cloud.projects)
  description = "Number of projects in the cloud created above, including the one just created."
}

output "ds_current_user_email" {
  value       = data.anyscale_user.current.email
  description = "Email of the user whose token is authenticating this run."
}

output "ds_organization_name" {
  value       = data.anyscale_organization.current.name
  description = "Name of the connected organization."
}

output "ds_self_permission_level" {
  value       = data.anyscale_organization_user.self.permission_level
  description = "The current user's own organization permission level, looked up via anyscale_organization_user."
}

output "ds_organization_user_count" {
  value       = length(data.anyscale_organization_users.all.users)
  description = "Total number of users in the organization."
}
