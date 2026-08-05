# anyscale_cloud_access is READ AND IMPORT ONLY in this version. Create, Update, and Destroy all
# refuse -- you cannot add, change, or revoke a cloud's members through this resource yet. Manage
# actual membership through the console or the API directly, then use `terraform import` (see
# import.sh) to bring the current state under Terraform for visibility.
#
# The block below is what your configuration should look like to MATCH an imported cloud's real
# members -- write it to describe reality, not to change it. A config that doesn't match what
# `terraform import` recovers will show a diff on every plan, since Terraform still computes one
# even though applying it errors.
resource "anyscale_cloud_access" "production" {
  cloud_id = "cld_00000000000000000000000000"

  member = {
    "owner@example.com" = {
      base_role = "owner"
    }

    "analyst@example.com" = {
      base_role  = "collaborator"
      deny_roles = ["cloud_read_only"]

      # A member with the cloud_read_only deny role may only hold "readonly" on this cloud's
      # projects -- anything else is rejected at plan time.
      projects = {
        "prj_00000000000000000000000000" = "readonly"
      }
    }

    "engineer@example.com" = {
      base_role = "writer"

      # Project vocabulary is "write", not "writer" -- the two scopes genuinely differ.
      projects = {
        "prj_00000000000000000000000000" = "write"
      }
    }
  }
}

# unmanaged_grants surfaces any member this resource could not manage as declared -- once writes
# are enabled, alert on it directly rather than trusting a clean plan alone:
#   length(anyscale_cloud_access.production.unmanaged_grants) > 0
output "production_unmanaged_grants" {
  value       = anyscale_cloud_access.production.unmanaged_grants
  description = "Grants this resource could not reconcile to match configuration, and why"
}
