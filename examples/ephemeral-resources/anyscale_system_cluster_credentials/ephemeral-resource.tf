# Fetches live System Cluster credentials for a cloud without ever writing them to Terraform
# state or plan output - the value is fetched fresh every time this configuration runs. Reading
# this never creates, starts, or otherwise provisions a System Cluster as a side effect:
# workload_service_url_auth comes back null (with an explanatory warning) unless the System
# Cluster already exists and is currently Running.
#
# Use the fetched value immediately after this block - for example in a provider block, a
# provisioner, or a write-only resource argument - rather than storing it anywhere yourself. See
# https://developer.hashicorp.com/terraform/language/manage-sensitive-data for every supported
# consumption context.
ephemeral "anyscale_system_cluster_credentials" "example" {
  cloud_id = "cld_abc123"
}
