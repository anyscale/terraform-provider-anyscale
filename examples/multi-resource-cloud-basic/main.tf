# Multi-Resource Cloud Basic Test Scenario
# AWS based multi-resource cloud configuration

resource "anyscale_cloud" "primary" {
  # Common Fields
  name          = var.cloud_name
  auto_add_user = var.auto_add_user
}

resource "anyscale_cloud_resource" "primary" {
  # Spelled out from the region var (rather than a bare literal) so it still
  # matches this example's documented default -- "vm-aws-us-east-2" -- while
  # staying correct if you override aws_region.
  name = "vm-aws-${var.aws_region}"

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

resource "anyscale_cloud_resource" "secondary" {
  # is_default is assigned by the backend to whichever cloud resource was
  # created first (not a settable field), so this ordering keeps "primary"
  # the one that's actually default.
  depends_on = [anyscale_cloud_resource.primary]

  # name is required and must be unique among cloud resources on the same
  # cloud -- give any resource beyond the first its own distinct value.
  name = "${var.cloud_name}-secondary"

  cloud_id      = anyscale_cloud.primary.id
  region        = var.aws_region
  compute_stack = "VM"
  is_private    = var.is_private_cloud

  # AWS Configuration
  aws_config {
    vpc_id           = module.aws_anyscale_2.anyscale_vpc_id
    subnet_ids_to_az = module.aws_anyscale_2.anyscale_vpc_public_subnet_ids_az_map

    security_group_ids = [module.aws_anyscale_2.anyscale_security_group_id]

    controlplane_iam_role_arn = module.aws_anyscale_2.anyscale_iam_role_arn
    dataplane_iam_role_arn    = module.aws_anyscale_2.anyscale_iam_role_cluster_node_arn
    external_id               = module.aws_anyscale_2.anyscale_iam_role_external_id
  }

  # Object Storage (S3)
  object_storage {
    bucket_name = module.aws_anyscale_2.anyscale_s3_bucket_id
    region      = var.aws_region
  }

}
