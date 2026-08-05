# Import by cloud_id -- the same value the resource is keyed by. This is the only supported
# entry point in this version: writes refuse, so import is how you get an existing cloud's member
# list into state at all.
terraform import anyscale_cloud_access.production cld_00000000000000000000000000
