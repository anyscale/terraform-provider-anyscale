# All-in-one: config blocks are set on this resource
terraform import anyscale_cloud.example cld_abc123

# Split: config blocks live on anyscale_cloud_resource
terraform import anyscale_cloud.example cld_abc123:empty

# Import does not recover `credentials`. If your config sets it, the
# first plan after import records it in state with a warning and does
# not replace the cloud.
