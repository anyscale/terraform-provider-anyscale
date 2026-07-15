# --- Resources -------------------------------------------------------------------------------
output "cloud_a_id" {
  value       = anyscale_cloud.a.id
  description = "ID of Cloud A (BYOC/split pattern, hosting the VM and EKS resources)."
}

output "cloud_b_id" {
  value       = anyscale_cloud.b.id
  description = "ID of Cloud B (all-in-one VM pattern)."
}

output "cloud_a_vm_operator_status" {
  value       = anyscale_cloud_resource.a_vm.operator_status
  description = "Health status reported for Cloud A's VM resource; null until it has reported in."
}

output "cloud_a_eks_operator_status" {
  value       = anyscale_cloud_resource.a_eks.operator_status
  description = "Health status reported by the Anyscale Operator running in the EKS cluster for Cloud A's K8S resource; null until it has reported in."
}

output "compute_config_a_default_name_version" {
  value       = anyscale_compute_config.cc_a_default.name_version
  description = "name:revision handle for the compute config targeting Cloud A's default (VM) resource."
}

output "compute_config_a_eks_name_version" {
  value       = anyscale_compute_config.cc_a_eks.name_version
  description = "name:revision handle for the compute config targeting Cloud A's EKS resource by name."
}

output "compute_config_b_name_version" {
  value       = anyscale_compute_config.cc_b.name_version
  description = "name:revision handle for Cloud B's compute config."
}

output "training_image_name_version" {
  value       = anyscale_container_image_build.training.name_version
  description = "name:revision handle for the built training image."
}

output "base_image_digest" {
  value       = anyscale_container_image_registry.base.digest
  description = "Content digest of the registered base image's latest successful build."
}

output "project_a_id" {
  value       = anyscale_project.a.id
  description = "ID of the project created in Cloud A."
}

output "project_b_id" {
  value       = anyscale_project.b.id
  description = "ID of the project created in Cloud B."
}

output "new_member_invitation_status" {
  value       = length(anyscale_organization_invitation.new_member) > 0 ? anyscale_organization_invitation.new_member[0].status : null
  description = "Status of the invitation sent to invite_email: pending, accepted, or expired. Null when invite_email is left at its default (the resource doesn't exist)."
}

# --- Data sources ------------------------------------------------------------------------------
output "ds_cloud_a_provider" {
  value       = data.anyscale_cloud.lookup_a.cloud_provider
  description = "cloud_provider read back via the anyscale_cloud data source, confirming the create-then-read pattern works."
}

output "ds_aws_cloud_count" {
  value       = length(data.anyscale_clouds.aws_clouds.clouds)
  description = "Total AWS clouds visible to this token, including the two just created."
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

output "ds_project_a_directory_name" {
  value       = data.anyscale_project.lookup_a.directory_name
  description = "Storage directory name read back via the anyscale_project data source."
}

output "ds_projects_in_cloud_a_count" {
  value       = length(data.anyscale_projects.in_cloud_a.projects)
  description = "Number of projects in Cloud A, including the one just created."
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

output "ds_services_in_project_a_count" {
  value       = length(data.anyscale_services.in_project_a.services)
  description = "Number of services in project A. Expected to be 0 unless you've separately deployed a Ray Serve service there -- this provider has no service RESOURCE to create one."
}

output "ds_existing_service_state" {
  value       = length(data.anyscale_service.existing) > 0 ? data.anyscale_service.existing[0].current_state : null
  description = "Current lifecycle state of the service named by existing_service_name. Null when that variable is left at its default (the lookup is skipped)."
}
