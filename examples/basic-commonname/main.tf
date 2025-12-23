# Create an Anyscale Cloud

locals {
  full_tags = merge(tomap({
    anyscale-cloud-id           = var.anyscale_cloud_id,
    anyscale-deploy-environment = var.anyscale_deploy_env
    }),
    var.tags
  )
}

module "aws_anyscale_v2_common_name" {
  source = "anyscale/anyscale-cloudfoundation-modules/aws"
  tags   = local.full_tags

  anyscale_deploy_env  = var.anyscale_deploy_env
  anyscale_cloud_id    = var.anyscale_cloud_id
  anyscale_org_id      = var.anyscale_org_id
  anyscale_external_id = var.anyscale_external_id

  # VPC Related
  anyscale_vpc_cidr_block     = "172.24.0.0/16"
  anyscale_vpc_public_subnets = ["172.24.21.0/24", "172.24.22.0/24", "172.24.23.0/24"]
  # anyscale_vpc_private_subnets = ["172.24.101.0/24", "172.24.102.0/24", "172.24.103.0/24"]

  # IAM Related
  anyscale_cluster_node_managed_policy_arns = [
    "arn:aws:iam::aws:policy/AmazonSQSReadOnlyAccess",
    "arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess"
  ]

  common_prefix   = var.common_prefix
  use_common_name = true

  # Security Group Related
  security_group_ingress_allow_access_from_cidr_range = var.customer_ingress_cidr_ranges

  # EFS Resource Related
  #   Disable EFS resources to avoid creating EFS resources.
  create_efs_resources = false

  # S3 Resource Related
  anyscale_s3_force_destroy = var.anyscale_s3_force_destroy
}

# Register the Anyscale Cloud using the resources created above
resource "anyscale_cloud" "example" {
  name            = var.cloud_name
  cloud_provider  = var.cloud_provider
  region          = var.aws_region
  compute_stack   = var.compute_stack
  networking_mode = var.is_private_cloud ? "PRIVATE" : "PUBLIC"

  deployment_name = "vm-${lower(var.cloud_provider)}-${var.aws_region}"

  aws_config {
    vpc_id                = module.aws_anyscale_v2_common_name.anyscale_vpc_id
    subnet_ids            = module.aws_anyscale_v2_common_name.anyscale_vpc_public_subnet_ids
    security_group_ids    = [module.aws_anyscale_v2_common_name.anyscale_security_group_id]
    s3_bucket_id          = module.aws_anyscale_v2_common_name.anyscale_s3_bucket_id
    anyscale_iam_role_id  = module.aws_anyscale_v2_common_name.anyscale_iam_role_arn
    instance_iam_role_id  = module.aws_anyscale_v2_common_name.anyscale_iam_role_cluster_node_arn
    external_id           = module.aws_anyscale_v2_common_name.anyscale_iam_role_external_id
  }
}
