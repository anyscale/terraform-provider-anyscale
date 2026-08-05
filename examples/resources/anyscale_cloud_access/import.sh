# Import by cloud_id -- the same value the resource is keyed by. Do this before your first
# `apply` against any cloud you did not create through this resource: because Create is just as
# authoritative as Update, applying against a cloud with real, undeclared members revokes them
# exactly as an Update would. Import first, write your configuration to match what came back
# (`terraform plan` will show you the difference if it doesn't), and only then start changing it.
terraform import anyscale_cloud_access.production cld_00000000000000000000000000
