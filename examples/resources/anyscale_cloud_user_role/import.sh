# Import using the cloud_id and the member's email, separated by a slash.
# email is required (not user_id) - see the resource documentation for why.
terraform import anyscale_cloud_user_role.analyst cld_abc123/analyst@example.com
