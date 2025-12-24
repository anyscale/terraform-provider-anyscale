# AWS Full Scenario - Cloud Foundation Module
# EFS + MemoryDB enabled

locals {
  full_tags = merge(tomap({
    anyscale-cloud-id           = var.anyscale_cloud_id,
    anyscale-deploy-environment = var.anyscale_deploy_env
    }),
    var.tags
  )
}

module "aws_anyscale_v2" {
  source = "anyscale/anyscale-cloudfoundation-modules/aws"
  tags   = local.full_tags

  anyscale_deploy_env  = var.anyscale_deploy_env
  anyscale_cloud_id    = var.anyscale_cloud_id
  anyscale_org_id      = var.anyscale_org_id
  anyscale_external_id = var.anyscale_external_id

  # VPC Configuration
  anyscale_vpc_cidr_block     = "172.27.0.0/16"
  anyscale_vpc_public_subnets = ["172.27.21.0/24", "172.27.22.0/24", "172.27.23.0/24"]

  # IAM Configuration
  anyscale_cluster_node_managed_policy_arns = [
    "arn:aws:iam::aws:policy/AmazonSQSReadOnlyAccess",
    "arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess"
  ]

  common_prefix   = var.common_prefix
  use_common_name = true

  # Security Group Configuration
  security_group_ingress_allow_access_from_cidr_range = var.customer_ingress_cidr_ranges

  # EFS: ENABLED for this scenario
  create_efs_resources = true

  # MemoryDB: ENABLED for this scenario
  create_memorydb_resources = true

  # S3 Configuration
  anyscale_s3_force_destroy = var.anyscale_s3_force_destroy
}
