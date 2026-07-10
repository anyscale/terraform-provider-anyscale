# A minimal compute config with a single head node, in an existing Anyscale
# cloud. Add a worker_nodes block to define autoscaling worker groups.
resource "anyscale_compute_config" "example" {
  name       = "my-first-compute-config"
  cloud_name = "my-anyscale-cloud" # name of a cloud already registered in Anyscale

  head_node = {
    instance_type = "m5.2xlarge" # AWS; use e.g. n2-standard-8 on GCP
  }
}
