# Import by cloud_id, the same value the resource is keyed by. Create is
# as authoritative as Update, so applying against a cloud with real,
# undeclared members revokes them: import before your first apply
# against any cloud you did not create through this resource, write
# your configuration to match what came back, and only then start
# changing it.
terraform import anyscale_cloud_access.production cld_00000000000000000000000000
