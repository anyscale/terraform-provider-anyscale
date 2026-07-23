# AWS VM Test Scenario
# Consolidated example with optional EFS and MemoryDB
# Uses multi-resource cloud pattern: empty cloud + cloud_resource

# Step 1: Create empty cloud shell
resource "anyscale_cloud" "primary" {
  name = var.cloud_name

  is_private_cloud = var.is_private_cloud
  auto_add_user    = var.auto_add_user

  # Cloud-level settings (optional)
  lineage_tracking_enabled = true
  aggregated_logs_enabled  = true

  # No aws_config, object_storage, or file_storage blocks
  # This creates an "empty" cloud - resources attached via anyscale_cloud_resource

}

# Step 2: Attach cloud resource with configuration
resource "anyscale_cloud_resource" "primary" {
  name = var.cloud_name

  cloud_id      = anyscale_cloud.primary.id
  region        = var.aws_region
  compute_stack = "VM"
  is_private    = var.is_private_cloud

  # AWS Configuration
  aws_config {
    vpc_id           = module.aws_anyscale_v2.anyscale_vpc_id
    subnet_ids_to_az = module.aws_anyscale_v2.anyscale_vpc_public_subnet_ids_az_map

    security_group_ids = [module.aws_anyscale_v2.anyscale_security_group_id]

    controlplane_iam_role_arn = module.aws_anyscale_v2.anyscale_iam_role_arn
    dataplane_iam_role_arn    = module.aws_anyscale_v2.anyscale_iam_role_cluster_node_arn
    external_id               = module.aws_anyscale_v2.anyscale_iam_role_external_id

    # MemoryDB for Ray GCS fault tolerance (only if enabled)
    memorydb_cluster_name     = var.enable_memorydb ? module.aws_anyscale_v2.anyscale_memorydb_cluster_id : null
    memorydb_cluster_arn      = var.enable_memorydb ? module.aws_anyscale_v2.anyscale_memorydb_cluster_arn : null
    memorydb_cluster_endpoint = var.enable_memorydb ? module.aws_anyscale_v2.anyscale_memorydb_cluster_endpoint_address : null
  }

  # Object Storage (S3)
  object_storage {
    bucket_name = module.aws_anyscale_v2.anyscale_s3_bucket_id
    region      = var.aws_region
  }

  # File Storage (EFS) - only if enabled
  dynamic "file_storage" {
    for_each = var.enable_efs ? [1] : []
    content {
      file_storage_id = module.aws_anyscale_v2.anyscale_efs_id

      mount_targets = [{
        address = module.aws_anyscale_v2.anyscale_efs_mount_target_ips[0]
      }]
    }
  }

}
