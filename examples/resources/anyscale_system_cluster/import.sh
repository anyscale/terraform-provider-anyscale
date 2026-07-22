# Import using the cloud ID - a System Cluster is a one-per-cloud singleton, so cloud_id alone
# is a complete, unambiguous identifier (no compound ID needed, unlike anyscale_cloud_resource).
terraform import anyscale_system_cluster.example cld_abc123
