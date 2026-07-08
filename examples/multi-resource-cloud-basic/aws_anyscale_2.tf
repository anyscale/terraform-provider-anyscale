# AWS Basic Scenario - Cloud Foundation Module
# No EFS, No MemoryDB
#
# local.full_tags is defined once in aws_anyscale_1.tf and shared here -
# both scenarios compute it identically, and locals share one namespace
# per module regardless of which file declares them.

module "aws_anyscale_2" {
  source = "anyscale/anyscale-cloudfoundation-modules/aws"
  tags   = local.full_tags

  anyscale_deploy_env  = var.anyscale_deploy_env
  anyscale_cloud_id    = var.anyscale_cloud_id
  anyscale_org_id      = var.anyscale_org_id
  anyscale_external_id = var.anyscale_external_id

  # VPC Configuration
  anyscale_vpc_cidr_block     = "172.25.0.0/16"
  anyscale_vpc_public_subnets = ["172.25.21.0/24", "172.25.22.0/24", "172.25.23.0/24"]

  common_prefix   = var.common_prefix_2
  use_common_name = true

  # Security Group Configuration
  security_group_ingress_allow_access_from_cidr_range = var.customer_ingress_cidr_ranges

  # EFS: DISABLED for this scenario
  create_efs_resources = false

  # S3 Configuration
  anyscale_s3_force_destroy = var.anyscale_s3_force_destroy
}
