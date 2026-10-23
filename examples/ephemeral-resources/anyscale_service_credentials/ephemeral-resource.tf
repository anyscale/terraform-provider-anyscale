# Fetches live authentication credentials for a running
# anyscale_service without ever writing them to Terraform state or plan
# output; the value is fetched fresh every time this configuration runs.
# auth_token and secondary_auth_token are null when the service does not
# have bearer authentication enabled; secondary_auth_token is otherwise
# null except during a token rotation.
#
# Use the value immediately after this block (for example in a provider
# block, a provisioner, or a write-only resource argument) rather than
# storing it yourself. See
# https://developer.hashicorp.com/terraform/language/manage-sensitive-data
# for every supported consumption context.
ephemeral "anyscale_service_credentials" "example" {
  service_id = "service2_abc123"
}
