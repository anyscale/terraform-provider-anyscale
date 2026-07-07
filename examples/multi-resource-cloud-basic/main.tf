# Multi-Resource Cloud Basic Test Scenario
# AWS based multi-resource cloud configuration

resource "anyscale_cloud" "primary" {
  # Common Fields
  name          = var.cloud_name
  auto_add_user = var.auto_add_user
}

resource "anyscale_cloud_resource" "primary" {
  cloud_id      = anyscale_cloud.primary.id
  region        = var.aws_region
  compute_stack = "VM"
  is_private    = var.is_private_cloud

  # AWS Configuration
  aws_config {
    vpc_id           = module.aws_anyscale_1.anyscale_vpc_id
    subnet_ids_to_az = module.aws_anyscale_1.anyscale_vpc_public_subnet_ids_az_map

    security_group_ids = [module.aws_anyscale_1.anyscale_security_group_id]

    controlplane_iam_role_arn = module.aws_anyscale_1.anyscale_iam_role_arn
    dataplane_iam_role_arn    = module.aws_anyscale_1.anyscale_iam_role_cluster_node_arn
    external_id               = module.aws_anyscale_1.anyscale_iam_role_external_id
  }

  # Object Storage (S3)
  object_storage {
    bucket_name = module.aws_anyscale_1.anyscale_s3_bucket_id
    region      = var.aws_region
  }

}
