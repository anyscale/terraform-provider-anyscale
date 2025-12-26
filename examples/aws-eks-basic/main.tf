# ---------------------------------------------------------------------------------------------------------------------
# Example Anyscale K8s Resources - Public Networking
#   This template creates EKS resources for Anyscale + Anyscale Cloud
#   It creates:
#     - VPC
#     - EFS (optional)
#     - S3 Bucket
#     - IAM policies
# ---------------------------------------------------------------------------------------------------------------------


resource "anyscale_cloud" "primary" {
  # Common Fields
  name           = var.cloud_name
  cloud_provider = "AWS"
  region         = var.aws_region
  compute_stack  = "K8S"

  # Kubernetes Configuration (required for K8S compute_stack)
  kubernetes_config {
    # IAM role ARN for the Anyscale operator running in EKS (required)
    anyscale_operator_iam_identity = module.eks.eks_managed_node_groups["default"].iam_role_arn

    # Availability zones for the K8s cluster
    zones = module.anyscale_vpc.availability_zones
  }

  # Object Storage (S3) - required for K8S
  object_storage {
    bucket_name = module.anyscale_s3.s3_bucket_id
    region      = var.aws_region
  }

  # Optional: File storage (EFS)
  # file_storage {
  #   file_storage_id = module.anyscale_efs.efs_id
  #   mount_path      = "/mnt/shared"
  # }

  timeouts {
    create = "10m"
    update = "10m"
    delete = "10m"
  }
}
