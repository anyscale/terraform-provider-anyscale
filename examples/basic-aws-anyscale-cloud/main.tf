# Example: Create an Anyscale Cloud on AWS with VM compute stack
resource "anyscale_cloud" "example_aws" {
  # ─── Common Fields (flat) ───────────────────────────
  name           = var.cloud_name
  cloud_provider = var.cloud_provider
  region         = var.aws_region
  compute_stack  = var.compute_stack

  # Optional common settings
  is_private_cloud = var.is_private_cloud
  auto_add_user    = var.auto_add_user

  # ─── AWS Configuration (nested) ─────────────────────
  aws_config {
    vpc_id           = module.aws_anyscale_v2_common_name.anyscale_vpc_id
    subnet_ids_to_az = module.aws_anyscale_v2_common_name.anyscale_vpc_public_subnet_ids_az_map

    security_group_ids = [module.aws_anyscale_v2_common_name.anyscale_security_group_id]

    controlplane_iam_role_arn = module.aws_anyscale_v2_common_name.anyscale_iam_role_arn
    dataplane_iam_role_arn    = module.aws_anyscale_v2_common_name.anyscale_iam_role_cluster_node_arn
    external_id               = module.aws_anyscale_v2_common_name.anyscale_iam_role_external_id
  }

  # ─── Object Storage (common abstraction) ────────────
  object_storage {
    bucket_name = module.aws_anyscale_v2_common_name.anyscale_s3_bucket_id
    region      = var.aws_region
  }

  # ─── File Storage (optional) ────────────────────────
  # Uncomment if using EFS
  # file_storage {
  #   file_system_id = module.aws_anyscale_v2_common_name.anyscale_efs_id
  #   mount_path     = "/mnt/shared"
  # }

  # ─── Timeout Configuration ──────────────────────────
  timeouts {
    create = "30m"
    update = "30m"
    delete = "30m"
  }
}
