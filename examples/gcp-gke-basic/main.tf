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
  name = var.cloud_name


}

resource "anyscale_cloud_resource" "primary" {
  cloud_id       = anyscale_cloud.primary.id
  region         = var.google_region
  compute_stack  = "K8S"
  cloud_provider = "GCP"

  # Kubernetes Configuration (required for K8S compute_stack)
  kubernetes_config {
    # IAM role ARN for the Anyscale operator running in EKS (required)
    anyscale_operator_iam_identity = google_service_account.gke_nodes.email

    # Availability zones for the K8s cluster
    zones = module.gke.zones
  }

  # Object Storage (S3) - required for K8S
  object_storage {
    bucket_name = module.anyscale_cloudstorage.cloudstorage_bucket_name
    region      = var.google_region
  }
}
