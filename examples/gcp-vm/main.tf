# GCP VM Test Scenario
# Consolidated example with optional Filestore and Memorystore
# Uses split pattern: empty cloud + cloud_resource

# Step 1: Create empty cloud shell
resource "anyscale_cloud" "primary" {
  name = var.cloud_name

  is_private_cloud = var.is_private_cloud
  auto_add_user    = var.auto_add_user

  # Cloud-level settings (optional)
  enable_lineage_tracking = true
  enable_log_ingestion    = true

  # No gcp_config, object_storage, or file_storage blocks
  # This creates an "empty" cloud - resources attached via anyscale_cloud_resource

}

# Step 2: Attach cloud resource with configuration
resource "anyscale_cloud_resource" "primary" {
  name = var.cloud_name

  cloud_id      = anyscale_cloud.primary.id
  region        = var.gcp_region
  compute_stack = var.compute_stack
  is_private    = var.is_private_cloud

  # GCP Configuration
  gcp_config {
    project_id    = module.google_anyscale_v2.project_id
    provider_name = module.google_anyscale_v2.iam_workload_identity_provider_name
    vpc_name      = module.google_anyscale_v2.vpc_name
    subnet_names  = [module.google_anyscale_v2.public_subnet_name]

    controlplane_service_account_email = module.google_anyscale_v2.iam_anyscale_access_service_acct_email
    dataplane_service_account_email    = module.google_anyscale_v2.iam_anyscale_cluster_node_service_acct_email

    firewall_policy_names = [module.google_anyscale_v2.vpc_firewall_policy_name]

    # Memorystore for Ray GCS fault tolerance (only if enabled)
    memorystore_instance_name = var.enable_memorystore ? module.google_anyscale_v2.memorystore_id : null
    memorystore_endpoint      = var.enable_memorystore ? module.google_anyscale_v2.memorystore_endpoint : null
  }

  # Object Storage (GCS)
  object_storage {
    bucket_name = module.google_anyscale_v2.cloudstorage_bucket_name
    region      = var.gcp_region
  }

  # File Storage (Filestore) - only if enabled
  dynamic "file_storage" {
    for_each = var.enable_filestore ? [1] : []
    content {
      file_storage_id = module.google_anyscale_v2.filestore_name
      mount_path      = "/mnt/shared"
      mount_targets {
        address = module.google_anyscale_v2.filestore_ip_address
        zone    = var.gcp_zone
      }
    }
  }

}
