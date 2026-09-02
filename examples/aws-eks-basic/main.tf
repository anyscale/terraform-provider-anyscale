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
    # IAM role ARN for the Anyscale operator running in EKS (required). NOT the node group's own
    # role (see anyscale_operator_iam.tf) -- that role can't be assumed by the Operator pod via EKS
    # Pod Identity.
    anyscale_operator_iam_identity = aws_iam_role.anyscale_operator.arn

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

}
