# Import using the cloud ID
terraform import anyscale_cloud.example cld_abc123

# Import does not recover `credentials`. If your config sets it, the first plan after import
# records it in state with a warning and does not replace the cloud.
