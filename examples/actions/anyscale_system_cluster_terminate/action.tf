# Terminates the System Cluster for an Anyscale Cloud - an imperative side effect, not a
# declarative resource. This action does not alter anyscale_system_cluster's Terraform state;
# that resource will not refresh or update as a result of running this action.
#
# Invoke standalone with the -invoke flag on plan or apply, for example:
#   terraform apply -invoke=action.anyscale_system_cluster_terminate.example
action "anyscale_system_cluster_terminate" "example" {
  config {
    cloud_id = "cld_abc123"
  }
}
