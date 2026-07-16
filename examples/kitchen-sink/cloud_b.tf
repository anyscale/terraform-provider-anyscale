# Cloud B: all-in-one VM pattern (compute_stack + aws_config embedded directly on anyscale_cloud,
# no separate anyscale_cloud_resource block) -- the simplest cloud shape, satisfying "multiple
# clouds" alongside Cloud A. Shares the same VPC and S3 bucket as Cloud A, but its own IAM role set
# (see infra_aws.tf's module.anyscale_iam_roles_b comment for why external_id can't be shared).

resource "anyscale_cloud" "b" {
  name           = "${var.name_prefix}-cloud-b"
  cloud_provider = "AWS"
  region         = var.aws_region
  compute_stack  = "VM"

  # Set explicitly here to demonstrate real enabling.
  enable_system_cluster = true

  aws_config {
    vpc_id             = module.anyscale_vpc.vpc_id
    subnet_ids_to_az   = module.anyscale_vpc.public_subnet_ids_az_map
    security_group_ids = [aws_security_group.shared.id]

    controlplane_iam_role_arn = module.anyscale_iam_roles_b.iam_anyscale_access_role_arn
    dataplane_iam_role_arn    = module.anyscale_iam_roles_b.iam_cluster_node_role_arn
    external_id               = module.anyscale_iam_roles_b.iam_anyscale_access_role_external_id
  }

  object_storage {
    bucket_name = module.anyscale_s3.s3_bucket_id
    region      = var.aws_region
  }
}
