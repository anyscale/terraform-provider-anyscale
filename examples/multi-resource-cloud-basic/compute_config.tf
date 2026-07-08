# Multi-resource compute config example (experimental)
# Note: This requires multiple cloud resources to be configured

# Uncomment if you have multiple cloud resources configured
# resource "anyscale_compute_config" "multi_resource" {
#   name     = "${var.cloud_name}-multi-resource-compute"
#   cloud_id = anyscale_cloud.primary.id
#   # project_id is optional - omit to use organization default
#
#   idle_termination_minutes = 30
#
#   head_node = {
#     instance_type = "m5.2xlarge"
#
#     # Target specific deployment
#     cloud_deployment = {
#       region   = "us-west-2"
#       provider = "aws"
#     }
#   }
#
#   worker_nodes = [
#     {
#       instance_type = "m5.4xlarge"
#       min_nodes     = 1
#       max_nodes     = 5
#
#       # Target specific deployment
#       cloud_deployment = {
#         region   = "us-west-2"
#         provider = "aws"
#       }
#     }
#   ]
# }
