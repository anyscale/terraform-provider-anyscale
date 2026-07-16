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
  name = var.cloud_name

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

  # Object Storage (GCS) - required for K8S. Deliberately uses the module's
  # bare bucket-name output, not its gs://-prefixed URL output: bucket_name
  # is scheme-tolerant (a bare name and its gs://-prefixed form are treated
  # as the same value - see the Cloud Resources guide), so either output
  # works here. Do not switch this to the gs://-prefixed output - not
  # because this example exercises the fix (it doesn't: this apply/destroy
  # scenario never imports, so it can't cover an import round-trip fix -
  # that regression guard lives in the acctest suite instead), simply
  # because there's no reason to change a value that already works.
  object_storage {
    bucket_name = module.anyscale_cloudstorage.cloudstorage_bucket_name
    region      = var.google_region
  }
}
