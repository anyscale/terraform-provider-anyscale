# anyscale_cloud_access is AUTHORITATIVE over this cloud's entire member list. Anyone with access
# to the cloud who is not declared in `member` below is REVOKED, including on the very first
# apply -- including people granted access through the console. Before running this against a
# real cloud for the first time, list its current members (console or `terraform import`, see
# import.sh) and declare every one of them here; a member you forget to declare is revoked
# silently and does not appear anywhere in `terraform plan`'s output.
#
# lifecycle.prevent_destroy guards against the sharpest failure mode: destroying this resource, or
# replacing it because of a typo in cloud_id, correctly-per-its-own-logic revokes every member of
# whichever cloud_id it was pointed at. Keep this on for any cloud you cannot afford to depopulate
# by accident.
resource "anyscale_cloud_access" "production" {
  cloud_id = "cld_00000000000000000000000000"

  lifecycle {
    prevent_destroy = true
  }

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

# unmanaged_grants and ungranted_members report opposite failures -- alert on both rather than
# trusting a clean plan alone. A nonzero unmanaged_grants means someone has access this
# configuration says they shouldn't; a nonzero ungranted_members means someone lacks access this
# configuration says they should have.
#   length(anyscale_cloud_access.production.unmanaged_grants) > 0
#   length(anyscale_cloud_access.production.ungranted_members) > 0
output "production_unmanaged_grants" {
  value       = anyscale_cloud_access.production.unmanaged_grants
  description = "Grants this resource could not revoke to match configuration, and why"
}

output "production_ungranted_members" {
  value       = anyscale_cloud_access.production.ungranted_members
  description = "Grants this resource could not establish to match configuration, and why"
}
