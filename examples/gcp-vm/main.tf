# GCP VM Test Scenario
# Consolidated example with optional Filestore and Memorystore
# Uses multi-resource cloud pattern: empty cloud + cloud_resource

# Step 1: Create empty cloud shell
resource "anyscale_cloud" "primary" {
  # Destroy the cloud before the module. The credentials below order
  # only the outputs they read, not the module's IAM bindings.
  depends_on = [module.google_anyscale_v2]

  name = var.cloud_name

  # Match the cloud resource below. Left unset, the empty cloud is
  # recorded as AWS in us-east-1 with a placeholder role, and a GCP
  # service fails to create its load balancer.
  cloud_provider = "GCP"
  region         = var.gcp_region
  credentials = jsonencode({
    provider_id           = module.google_anyscale_v2.iam_workload_identity_provider_name
    project_id            = module.google_anyscale_v2.project_id
    service_account_email = module.google_anyscale_v2.iam_anyscale_access_service_acct_email
  })

  is_private_cloud = var.is_private_cloud
  auto_add_user    = var.auto_add_user

  # Cloud-level settings (optional). lineage_tracking_enabled is also
  # available but is gated per organization, so this example leaves it off.
  aggregated_logs_enabled = true

  # No gcp_config, object_storage, or file_storage blocks
  # This creates an "empty" cloud - resources attached via anyscale_cloud_resource

}

# Step 2: Attach cloud resource with configuration
resource "anyscale_cloud_resource" "primary" {
  # Keep the IAM roles and their policies until everything under the
  # cloud is destroyed: Anyscale needs them to terminate clusters and
  # tear down load balancers. A role ARN reference alone does not
  # order the policy attachments.
  depends_on = [module.google_anyscale_v2]

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
    # subnet_names is VM-only - rejected at plan time on K8S, so this drops
    # automatically if compute_stack is switched to K8S above.
    subnet_names = var.compute_stack == "VM" ? [module.google_anyscale_v2.public_subnet_name] : null

    controlplane_service_account_email = module.google_anyscale_v2.iam_anyscale_access_service_acct_email
    dataplane_service_account_email    = module.google_anyscale_v2.iam_anyscale_cluster_node_service_acct_email

    firewall_policy_names = [module.google_anyscale_v2.vpc_firewall_policy_name]

    # Memorystore for Ray GCS fault tolerance (only if enabled)
    memorystore_instance_name = var.enable_memorystore ? module.google_anyscale_v2.memorystore_id : null
    # The backend expects "host:port"; the module exports them separately.
    memorystore_endpoint = var.enable_memorystore ? (
      "${module.google_anyscale_v2.memorystore_host}:${module.google_anyscale_v2.memorystore_port}"
    ) : null
  }

  # Object Storage (GCS)
  object_storage {
    bucket_name = module.google_anyscale_v2.cloudstorage_bucket_name
  }

  # File Storage (Filestore) - only if enabled
  dynamic "file_storage" {
    for_each = var.enable_filestore ? [1] : []
    content {
      file_storage_id = module.google_anyscale_v2.filestore_name
      mount_path      = "/mnt/shared"
      mount_targets = [{
        address = module.google_anyscale_v2.filestore_ip_addresses[0]
        zone    = var.gcp_zone
      }]
    }
  }

}
