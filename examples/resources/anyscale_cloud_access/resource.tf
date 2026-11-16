# Deprecated: see the RBAC guide, "Legacy RBAC deprecation".
# anyscale_cloud_access is AUTHORITATIVE over this cloud's entire member
# list. Anyone with access to the cloud who is not declared in `member`
# below is REVOKED, including on the very first apply -- including
# people granted access through the console. A member you forget to
# declare is revoked silently and never appears in `terraform plan`'s
# output. Before the first apply against an existing cloud, import it
# (see import.sh) and declare every current member here.
#
# lifecycle.prevent_destroy: destroying or replacing this resource
# revokes every member of whichever cloud it is pointed at.
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

      # A member with the cloud_read_only deny role may only hold
      # "readonly" on this cloud's projects -- anything else is rejected
      # at plan time.
      projects = {
        "prj_00000000000000000000000000" = "readonly"
      }
    }

    "engineer@example.com" = {
      base_role = "writer"

      # Project vocabulary is "write"; "writer" is a cloud role.
      projects = {
        "prj_00000000000000000000000000" = "write"
      }
    }
  }
}

# A non-empty unmanaged_grants means someone still has access this
# configuration does not declare; a non-empty ungranted_members means
# someone lacks access it does declare. Alert on both.
#   length(anyscale_cloud_access.production.unmanaged_grants) > 0
#   length(anyscale_cloud_access.production.ungranted_members) > 0
output "production_unmanaged_grants" {
  value       = anyscale_cloud_access.production.unmanaged_grants
  description = "Grants that could not be revoked, and why"
}

output "production_ungranted_members" {
  value       = anyscale_cloud_access.production.ungranted_members
  description = "Grants that could not be established, and why"
}
