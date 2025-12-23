# Example: Create an Anyscale Cloud on GCP with VM compute stack
resource "anyscale_cloud" "example_gcp" {
  # --- Common Fields (flat) ---
  name           = var.cloud_name
  cloud_provider = var.cloud_provider
  region         = var.gcp_region
  compute_stack  = var.compute_stack

  # Optional common settings
  is_private_cloud = var.is_private_cloud
  auto_add_user    = var.auto_add_user

  # --- GCP Configuration (nested) ---
  gcp_config {
    project_id    = module.google_anyscale_v2_commonname.project_id
    provider_name = module.google_anyscale_v2_commonname.iam_workload_identity_provider_name
    vpc_name      = module.google_anyscale_v2_commonname.vpc_name
    subnet_names  = [module.google_anyscale_v2_commonname.public_subnet_name]

    controlplane_service_account_email = module.google_anyscale_v2_commonname.iam_anyscale_access_service_acct_email
    dataplane_service_account_email    = module.google_anyscale_v2_commonname.iam_anyscale_cluster_node_service_acct_email

    firewall_policy_names = [module.google_anyscale_v2_commonname.vpc_firewall_policy_name]
  }

  # --- Object Storage (common abstraction) ---
  object_storage {
    bucket_name = module.google_anyscale_v2_commonname.cloudstorage_bucket_name
    region      = var.gcp_region
  }

  # --- Timeout Configuration ---
  timeouts {
    create = "30m"
    update = "30m"
    delete = "30m"
  }
}
