terraform {
  required_providers {
    anyscale = {
      source = "registry.terraform.io/anyscale/anyscale"
    }
  }
}

provider "anyscale" {
  # Configuration options
  # token can be set via ANYSCALE_CLI_TOKEN or ~/.anyscale/credentials.json
}

# Example 1: Look up an existing cloud by name
data "anyscale_cloud" "production" {
  name = "production-cloud"
}

output "production_cloud_id" {
  value       = data.anyscale_cloud.production.id
  description = "The ID of the production cloud"
}

output "production_cloud_provider" {
  value       = data.anyscale_cloud.production.cloud_provider
  description = "The cloud provider (AWS, GCP, etc.)"
}

output "production_cloud_region" {
  value       = data.anyscale_cloud.production.region
  description = "The region where the cloud is deployed"
}

# Example 2: Look up an existing cloud by ID
data "anyscale_cloud" "by_id" {
  id = "cld_abc123xyz"
}

# Example 3: Use data source to create compute config
resource "anyscale_compute_config" "example" {
  name     = "example-compute-config"
  cloud_id = data.anyscale_cloud.production.id

  head_node = {
    instance_type = "m5.large"
  }

  worker_nodes = [
    {
      name          = "workers"
      instance_type = "m5.xlarge"
      min_nodes     = 0
      max_nodes     = 10
    }
  ]
}

# Example 4: Look up an existing compute config by name
data "anyscale_compute_config" "standard" {
  name = "standard-config"
}

output "standard_config_details" {
  value = {
    id                       = data.anyscale_compute_config.standard.id
    cloud_id                 = data.anyscale_compute_config.standard.cloud_id
    region                   = data.anyscale_compute_config.standard.region
    idle_termination_minutes = data.anyscale_compute_config.standard.idle_termination_minutes
  }
  description = "Details of the standard compute config"
}

# Example 5: Use existing config as template for new config
data "anyscale_compute_config" "template" {
  name = "base-config"
}

resource "anyscale_compute_config" "customized" {
  name     = "customized-config"
  cloud_id = data.anyscale_compute_config.template.cloud_id

  head_node = {
    instance_type = "m5.2xlarge"
  }
}

# Example 6: Cross-reference data sources
data "anyscale_cloud" "dev" {
  name = "dev-cloud"
}

data "anyscale_compute_config" "dev_config" {
  name = "dev-compute-config"
}

# Verify the compute config is using the expected cloud
output "config_uses_correct_cloud" {
  value       = data.anyscale_compute_config.dev_config.cloud_id == data.anyscale_cloud.dev.id
  description = "Verify the compute config is using the dev cloud"
}

# Example 7: List all clouds
data "anyscale_clouds" "all" {
}

output "all_clouds_count" {
  value       = length(data.anyscale_clouds.all.clouds)
  description = "Total number of clouds in the organization"
}

output "all_cloud_names" {
  value       = [for cloud in data.anyscale_clouds.all.clouds : cloud.name]
  description = "List of all cloud names"
}

# Example 8: Filter clouds by provider
data "anyscale_clouds" "aws_clouds" {
  cloud_provider = "AWS"
}

output "aws_cloud_count" {
  value       = length(data.anyscale_clouds.aws_clouds.clouds)
  description = "Number of AWS clouds"
}

# Example 9: Filter clouds by region
data "anyscale_clouds" "us_east_clouds" {
  region = "us-east-2"
}

output "us_east_cloud_names" {
  value       = [for cloud in data.anyscale_clouds.us_east_clouds.clouds : cloud.name]
  description = "Clouds in us-east-2"
}

# Example 10: Filter by name pattern
data "anyscale_clouds" "production_clouds" {
  name_contains = "production"
}

output "production_clouds" {
  value = [
    for cloud in data.anyscale_clouds.production_clouds.clouds : {
      name           = cloud.name
      cloud_provider = cloud.cloud_provider
      region         = cloud.region
      status         = cloud.status
    }
  ]
  description = "Production clouds with key details"
}

# Example 11: Find default cloud
data "anyscale_clouds" "all_defaults" {
}

output "default_cloud" {
  value = [
    for cloud in data.anyscale_clouds.all_defaults.clouds : cloud.name
    if cloud.is_default
  ]
  description = "Default cloud name"
}

# Example 12: Get current user information
data "anyscale_user" "current" {
}

output "current_user_id" {
  value       = data.anyscale_user.current.id
  description = "The ID of the current authenticated user"
}

output "current_user_email" {
  value       = data.anyscale_user.current.email
  description = "The email of the current authenticated user"
}

output "current_user_permission_level" {
  value       = data.anyscale_user.current.organization_permission_level
  description = "The organization permission level of the current user"
}

output "current_user_organizations" {
  value = [
    for org in data.anyscale_user.current.organizations : {
      name             = org.name
      id               = org.id
      default_cloud_id = org.default_cloud_id
    }
  ]
  description = "Organizations the current user belongs to"
}

output "current_user_accessible_clouds" {
  value       = data.anyscale_user.current.cloud_ids
  description = "List of cloud IDs the current user has access to"
}

output "current_user_cloud_count" {
  value       = length(data.anyscale_user.current.cloud_ids)
  description = "Number of clouds the current user has access to"
}

# Example 13: List all users in the organization (BETA - SCIM)
data "anyscale_organization_users" "all" {
}

output "organization_users_count" {
  value       = length(data.anyscale_organization_users.all.users)
  description = "Total number of users in the organization"
}

output "organization_users_list" {
  value = [for u in data.anyscale_organization_users.all.users : {
    name  = u.name
    email = u.email
    role  = u.permission_level
  }]
  description = "List of all organization users"
}

# Example 14: Look up a specific user by email (BETA - SCIM)
data "anyscale_organization_user" "specific" {
  email = "admin@example.com"
}

output "specific_user_id" {
  value       = data.anyscale_organization_user.specific.id
  description = "The identity ID of the specific user"
}

# Example 15: Look up a specific user by ID (BETA - SCIM)
data "anyscale_organization_user" "by_id" {
  id = "usr_abc123xyz"
}

# Example 16: Look up a specific user by user_id (BETA - SCIM)
data "anyscale_organization_user" "by_user_id" {
  user_id = "usr_xyz789abc"
}

# Example 17: Filter organization users by email (BETA - SCIM)
data "anyscale_organization_users" "filtered" {
  email = "admin@example.com"
}

# Example 18: Get service accounts only (BETA - SCIM)
data "anyscale_organization_users" "service_accounts" {
  is_service_account = true
}

output "service_accounts_count" {
  value       = length(data.anyscale_organization_users.service_accounts.users)
  description = "Number of service accounts in the organization"
}

# Example 19: List all user groups (BETA - SCIM)
data "anyscale_user_groups" "all" {
}

output "user_groups_count" {
  value       = length(data.anyscale_user_groups.all.groups)
  description = "Total number of user groups synced from IdP"
}

output "user_groups_list" {
  value = [for g in data.anyscale_user_groups.all.groups : {
    id   = g.id
    name = g.name
  }]
  description = "List of all user groups"
}

# Example 20: Look up a specific user group by name (BETA - SCIM)
data "anyscale_user_group" "admins" {
  name = "Admins"
}

output "admins_group_id" {
  value       = data.anyscale_user_group.admins.id
  description = "The ID of the Admins user group"
}

# Example 21: Look up a specific user group by ID (BETA - SCIM)
data "anyscale_user_group" "by_id" {
  id = "ug_abc123xyz"
}

# Example 22: List all policy bindings for clouds (BETA - SCIM)
data "anyscale_policy_bindings" "all_clouds" {
  resource_type = "clouds"
}

output "clouds_with_policies_count" {
  value       = length(data.anyscale_policy_bindings.all_clouds.policies)
  description = "Number of clouds with SCIM policy bindings"
}

output "clouds_policy_summary" {
  value = [for p in data.anyscale_policy_bindings.all_clouds.policies : {
    resource_id = p.resource_id
    sync_status = p.sync_status
    num_roles   = length(p.bindings)
  }]
  description = "Summary of cloud policy bindings"
}

# Example 23: List all policy bindings for projects (BETA - SCIM)
data "anyscale_policy_bindings" "all_projects" {
  resource_type = "projects"
}

# Example 24: Get policy binding for a specific cloud (BETA - SCIM)
data "anyscale_policy_binding" "production_cloud" {
  resource_type = "cloud"
  resource_id   = data.anyscale_cloud.production.id
}

output "production_cloud_policy" {
  value = {
    resource_id = data.anyscale_policy_binding.production_cloud.resource_id
    bindings = [for b in data.anyscale_policy_binding.production_cloud.bindings : {
      role       = b.role_name
      num_groups = length(b.principals)
    }]
  }
  description = "Policy bindings for the production cloud"
}

# Example 25: Get policy binding for a specific project (BETA - SCIM)
data "anyscale_project" "main" {
  name = "main-project"
}

data "anyscale_policy_binding" "main_project" {
  resource_type = "project"
  resource_id   = data.anyscale_project.main.id
}

# Example 26: Get organization-level policy binding (BETA - SCIM)
data "anyscale_policy_binding" "organization" {
  resource_type = "organization"
  resource_id   = data.anyscale_user.current.organizations[0].id
}

output "organization_roles" {
  value = [for b in data.anyscale_policy_binding.organization.bindings : {
    role   = b.role_name
    groups = b.principals
  }]
  description = "Organization-level role assignments"
}

# Example 27: Find which user groups have access to a cloud (BETA - SCIM)
data "anyscale_cloud" "shared" {
  name = "shared-cloud"
}

data "anyscale_policy_binding" "shared_cloud" {
  resource_type = "cloud"
  resource_id   = data.anyscale_cloud.shared.id
}

locals {
  # Extract all user group IDs that have any role on the shared cloud
  shared_cloud_group_ids = flatten([
    for binding in data.anyscale_policy_binding.shared_cloud.bindings : binding.principals
  ])
}

output "shared_cloud_access_groups" {
  value       = local.shared_cloud_group_ids
  description = "User groups with access to shared cloud"
}
