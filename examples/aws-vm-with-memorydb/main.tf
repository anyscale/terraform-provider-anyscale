# AWS with MemoryDB Test Scenario
# No EFS, MemoryDB enabled

resource "anyscale_cloud" "test" {
  # Common Fields
  name           = var.cloud_name
  cloud_provider = var.cloud_provider
  region         = var.aws_region
  compute_stack  = var.compute_stack

  is_private_cloud = var.is_private_cloud
  auto_add_user    = var.auto_add_user

  # AWS Configuration
  aws_config {
    vpc_id           = module.aws_anyscale_v2.anyscale_vpc_id
    subnet_ids_to_az = module.aws_anyscale_v2.anyscale_vpc_public_subnet_ids_az_map

    security_group_ids = [module.aws_anyscale_v2.anyscale_security_group_id]

    controlplane_iam_role_arn = module.aws_anyscale_v2.anyscale_iam_role_arn
    dataplane_iam_role_arn    = module.aws_anyscale_v2.anyscale_iam_role_cluster_node_arn
    external_id               = module.aws_anyscale_v2.anyscale_iam_role_external_id

    # MemoryDB for Ray GCS fault tolerance
    memorydb_cluster_name = module.aws_anyscale_v2.anyscale_memorydb_cluster_id
  }

  # Object Storage (S3)
  object_storage {
    bucket_name = module.aws_anyscale_v2.anyscale_s3_bucket_id
    region      = var.aws_region
  }

  # EFS: DISABLED for this scenario
  create_efs_resources = false

  timeouts {
    create = "30m"
    update = "30m"
    delete = "30m"
  }
}
