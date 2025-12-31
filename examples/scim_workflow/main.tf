# Complete SCIM Provisioning Workflow Example
# This example demonstrates how to manage users, groups, and permissions using Anyscale's SCIM integration

terraform {
  required_providers {
    anyscale = {
      source = "anyscale/anyscale"
    }
  }
}

# ==============================================================================
# STEP 1: SCIM Setup (External)
# ==============================================================================
# Before using this configuration:
# 1. Enable SCIM in your IdP (Okta, Azure AD, etc.)
# 2. Configure SCIM endpoint in Anyscale
# 3. Create user groups in your IdP
# 4. Add users to groups in your IdP
# ==============================================================================

# ==============================================================================
# STEP 2: Reference existing resources
# ==============================================================================

# Reference your existing clouds
data "anyscale_cloud" "production" {
  name = "production-cloud"
}

data "anyscale_cloud" "development" {
  name = "development-cloud"
}

# Reference existing projects
data "anyscale_project" "ml_platform" {
  name = "ml-platform"
}

# Reference SCIM-synced user groups (these come from your IdP)
# Note: You'll need to know the group IDs from SCIM sync
# Use `anyscale user-group list` CLI command to find these

variable "engineering_group_id" {
  description = "User group ID for engineering team"
  type        = string
  default     = "ug_engineering123"
}

variable "data_science_group_id" {
  description = "User group ID for data science team"
  type        = string
  default     = "ug_datascience456"
}

variable "analysts_group_id" {
  description = "User group ID for analysts"
  type        = string
  default     = "ug_analysts789"
}

variable "admins_group_id" {
  description = "User group ID for organization admins"
  type        = string
  default     = "ug_admins000"
}

# ==============================================================================
# STEP 3: Set Organization Policies
# ==============================================================================

# Organization-level permissions
resource "anyscale_policy_binding" "organization" {
  resource_type = "organization"
  resource_id   = data.anyscale_organization.current.id

  bindings = [
    {
      # Organization admins get owner privileges
      role_name  = "owner"
      principals = [var.admins_group_id]
    },
    {
      # All other groups get collaborator (default) privileges
      role_name = "collaborator"
      principals = [
        var.engineering_group_id,
        var.data_science_group_id,
        var.analysts_group_id
      ]
    }
  ]
}

# ==============================================================================
# STEP 4: Set Cloud-Level Policies
# ==============================================================================

# Production cloud - limited access
resource "anyscale_policy_binding" "production_cloud" {
  resource_type = "cloud"
  resource_id   = data.anyscale_cloud.production.id

  bindings = [
    {
      # Engineering team has full write access
      role_name  = "collaborator"
      principals = [var.engineering_group_id]
    },
    {
      # Data scientists have readonly access
      role_name = "readonly"
      principals = [
        var.data_science_group_id,
        var.analysts_group_id
      ]
    }
  ]
}

# Development cloud - broader access
resource "anyscale_policy_binding" "development_cloud" {
  resource_type = "cloud"
  resource_id   = data.anyscale_cloud.development.id

  bindings = [
    {
      # Both engineering and data science get full access
      role_name = "collaborator"
      principals = [
        var.engineering_group_id,
        var.data_science_group_id
      ]
    },
    {
      # Analysts get readonly
      role_name  = "readonly"
      principals = [var.analysts_group_id]
    }
  ]
}

# ==============================================================================
# STEP 5: Set Project-Level Policies
# ==============================================================================

# ML Platform project - granular control
resource "anyscale_policy_binding" "ml_platform_project" {
  resource_type = "project"
  resource_id   = data.anyscale_project.ml_platform.id

  bindings = [
    {
      # Engineering owns the project
      role_name  = "owner"
      principals = [var.engineering_group_id]
    },
    {
      # Data scientists can run workloads
      role_name  = "write"
      principals = [var.data_science_group_id]
    },
    {
      # Analysts can view results
      role_name  = "readonly"
      principals = [var.analysts_group_id]
    }
  ]

  # Ensure cloud policy is set first
  depends_on = [anyscale_policy_binding.production_cloud]
}

# ==============================================================================
# STEP 6: Manage Individual Users (Optional)
# ==============================================================================

# For users not in groups or requiring special permissions,
# use the organization_collaborator resource

# Example: Find a specific user
data "anyscale_organization_user" "special_user" {
  email = "special.user@company.com"
}

# Import and manage their organization permissions
# terraform import anyscale_organization_collaborator.special_user <identity_id>
resource "anyscale_organization_collaborator" "special_user" {
  # Must be imported first
  permission_level = "owner"

  lifecycle {
    # Prevent accidental deletion
    prevent_destroy = true
  }
}

# ==============================================================================
# STEP 7: Monitor Policy Status
# ==============================================================================

# Output policy sync statuses
output "policy_sync_status" {
  description = "Synchronization status of all policies"
  value = {
    organization = anyscale_policy_binding.organization.sync_status
    prod_cloud   = anyscale_policy_binding.production_cloud.sync_status
    dev_cloud    = anyscale_policy_binding.development_cloud.sync_status
    ml_platform  = anyscale_policy_binding.ml_platform_project.sync_status
  }
}

# ==============================================================================
# STEP 8: Verify Access (Data Sources)
# ==============================================================================

# Check current policy bindings
data "anyscale_policy_binding" "production_check" {
  resource_type = "cloud"
  resource_id   = data.anyscale_cloud.production.id

  depends_on = [anyscale_policy_binding.production_cloud]
}

output "production_cloud_bindings" {
  description = "Current policy bindings for production cloud"
  value       = data.anyscale_policy_binding.production_check.bindings
}

# ==============================================================================
# NOTES
# ==============================================================================
#
# Important Constraints:
# 1. Groups must have cloud access before project access in that cloud
# 2. If a group has readonly on a cloud, they can only have readonly on projects
# 3. If a group has collaborator on a cloud, they can have any role on projects
# 4. Organization owners cannot be added to cloud/project policies (implicit access)
# 5. Policy bindings REPLACE all existing group permissions (not additive)
#
# Best Practices:
# 1. Use SCIM for user/group provisioning (don't create users manually)
# 2. Manage permissions at the group level (not individual users)
# 3. Set cloud policies before project policies (enforce dependency with depends_on)
# 4. Use lifecycle blocks to prevent accidental deletion of critical policies
# 5. Monitor sync_status outputs to ensure policies are applied correctly
#
# Workflow:
# 1. Enable SCIM in your IdP
# 2. Create groups in IdP and add users
# 3. Configure SCIM connection to Anyscale (syncs groups/users)
# 4. Apply Terraform configuration to set policies
# 5. Run `anyscale scim enforce-groups` to migrate existing user permissions to groups
#
# ==============================================================================

# Required data source for current organization
data "anyscale_organization" "current" {}
