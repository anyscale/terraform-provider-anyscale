# Create a minimal compute configuration in an existing Anyscale cloud.
# Omitting worker_nodes lets Anyscale auto-select workers for your workload.
resource "anyscale_compute_config" "example" {
  name       = "my-first-compute-config"
  cloud_name = "my-anyscale-cloud" # name of a cloud already registered in Anyscale

  head_node = {
    instance_type = "m5.2xlarge" # AWS; use e.g. n2-standard-8 on GCP
  }
}
