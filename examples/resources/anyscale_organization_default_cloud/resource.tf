# Ensure a specific cloud is the organization's default. Creating this resource sets the org
# default to cloud_id, and every plan re-asserts it if something else moves the default away
# out of band. Requires an organization-owner-scoped token - a lower-privileged token fails
# cleanly with a 403 at apply, not a silent no-op.
resource "anyscale_organization_default_cloud" "this" {
  cloud_id = "cld_abc123"
}

# Destroying this resource only stops Terraform from managing the pointer - the underlying API
# has no way to unset the org default, so destroy does not change which cloud is currently set.

output "organization_default_cloud_org_id" {
  value       = anyscale_organization_default_cloud.this.id
  description = "The ID of the organization whose default cloud this resource manages"
}
