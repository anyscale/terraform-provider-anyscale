# GCP Basic Test Scenario
# No Filestore, No Memorystore - Minimal GCP cloud configuration

resource "anyscale_cloud" "test" {
  # Common Fields
  name           = var.cloud_name
  cloud_provider = "GCP"
  region         = var.gcp_region
  compute_stack  = "VM"

  is_private_cloud = var.is_private_cloud
  auto_add_user    = var.auto_add_user

  # GCP Configuration
  gcp_config {
    project_id    = module.google_anyscale_v2.project_id
    provider_name = module.google_anyscale_v2.iam_workload_identity_provider_name
    vpc_name      = module.google_anyscale_v2.vpc_name
    subnet_names  = [module.google_anyscale_v2.public_subnet_name]

    controlplane_service_account_email = module.google_anyscale_v2.iam_anyscale_access_service_acct_email
    dataplane_service_account_email    = module.google_anyscale_v2.iam_anyscale_cluster_node_service_acct_email

    firewall_policy_names = [module.google_anyscale_v2.vpc_firewall_policy_name]
  }

  # Object Storage (GCS)
  object_storage {
    bucket_name = module.google_anyscale_v2.cloudstorage_bucket_name
    region      = var.gcp_region
  }

  # No file_storage block - Filestore disabled

  timeouts {
    create = "10m"
    update = "10m"
    delete = "10m"
  }
}
