# A minimal compute config with a single head node, in an existing Anyscale
# cloud. Add a worker_nodes block to define autoscaling worker groups.
#
# This looks up the cloud by name, for a cloud that already exists outside
# this configuration. If you manage the cloud with Terraform too, reference
# it directly instead: cloud_id = anyscale_cloud.primary.id
data "anyscale_cloud" "example" {
  name = "my-anyscale-cloud" # name of a cloud already registered in Anyscale
}

resource "anyscale_compute_config" "example" {
  name     = "my-first-compute-config"
  cloud_id = data.anyscale_cloud.example.id

  head_node = {
    instance_type = "m5.2xlarge" # AWS; use e.g. n2-standard-8 on GCP
  }
}
