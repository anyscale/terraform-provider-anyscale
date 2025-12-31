# Basic cloud policy binding with readonly access
resource "anyscale_policy_binding" "cloud_readonly" {
  resource_type = "cloud"
  resource_id   = "cld_abc123"

  bindings = [
    {
      role_name = "readonly"
      principals = [
        "ug_team1",
        "ug_team2"
      ]
    }
  ]
}

# Cloud policy with multiple roles
resource "anyscale_policy_binding" "cloud_multi_role" {
  resource_type = "cloud"
  resource_id   = "cld_xyz789"

  bindings = [
    {
      role_name = "collaborator"
      principals = [
        "ug_engineering"
      ]
    },
    {
      role_name = "readonly"
      principals = [
        "ug_analysts",
        "ug_viewers"
      ]
    }
  ]
}

# Project policy binding
resource "anyscale_policy_binding" "project_permissions" {
  resource_type = "project"
  resource_id   = "prj_def456"

  bindings = [
    {
      role_name = "owner"
      principals = [
        "ug_project_owners"
      ]
    },
    {
      role_name = "write"
      principals = [
        "ug_developers"
      ]
    },
    {
      role_name = "readonly"
      principals = [
        "ug_stakeholders"
      ]
    }
  ]
}

# Organization policy binding
resource "anyscale_policy_binding" "organization" {
  resource_type = "organization"
  resource_id   = "org_ghi789"

  bindings = [
    {
      role_name = "owner"
      principals = [
        "ug_admins"
      ]
    },
    {
      role_name = "collaborator"
      principals = [
        "ug_all_users"
      ]
    }
  ]
}

# Empty bindings - removes all group-based permissions
resource "anyscale_policy_binding" "no_group_access" {
  resource_type = "cloud"
  resource_id   = "cld_private"
  bindings      = []
}

# Using data sources to reference user groups
data "anyscale_user_group" "engineering" {
  name = "Engineering"
}

data "anyscale_cloud" "production" {
  name = "production-cloud"
}

resource "anyscale_policy_binding" "prod_cloud_access" {
  resource_type = "cloud"
  resource_id   = data.anyscale_cloud.production.id

  bindings = [
    {
      role_name  = "collaborator"
      principals = [data.anyscale_user_group.engineering.id]
    }
  ]
}

# Output the sync status
output "policy_sync_status" {
  value       = anyscale_policy_binding.cloud_readonly.sync_status
  description = "Status of policy synchronization"
}
