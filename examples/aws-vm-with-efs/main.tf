# AWS with EFS Test Scenario
# EFS enabled, No MemoryDB
# Uses split pattern: empty cloud + cloud_resource

# Step 1: Create empty cloud shell
# cloud_provider and region are optional - will use placeholders for empty clouds
resource "anyscale_cloud" "test" {
  name = var.cloud_name

  is_private_cloud = false
  auto_add_user    = true

  timeouts {
    create = "10m"
    update = "10m"
    delete = "10m"
  }
}

# Step 2: Attach cloud resource with EFS configuration
resource "anyscale_cloud_resource" "primary" {
  cloud_id      = anyscale_cloud.test.cloud_id
  region        = var.aws_region
  compute_stack = "VM"
  is_private    = false

  # AWS Configuration
  aws_config {
    vpc_id           = module.aws_anyscale_v2.anyscale_vpc_id
    subnet_ids_to_az = module.aws_anyscale_v2.anyscale_vpc_public_subnet_ids_az_map

    security_group_ids = [module.aws_anyscale_v2.anyscale_security_group_id]

    controlplane_iam_role_arn = module.aws_anyscale_v2.anyscale_iam_role_arn
    dataplane_iam_role_arn    = module.aws_anyscale_v2.anyscale_iam_role_cluster_node_arn
    external_id               = module.aws_anyscale_v2.anyscale_iam_role_external_id
  }

  # Object Storage (S3)
  object_storage {
    bucket_name = module.aws_anyscale_v2.anyscale_s3_bucket_id
    region      = var.aws_region
  }

  # File Storage (EFS) - API expects single mount target
  file_storage {
    file_storage_id = module.aws_anyscale_v2.anyscale_efs_id
    mount_path      = "/mnt/shared"

    mount_targets {
      address = module.aws_anyscale_v2.anyscale_efs_mount_target_ips[0]
    }
  }

  timeouts {
    create = "10m"
    update = "10m"
    delete = "10m"
  }
}
