# GCP with Memorystore Test Scenario
# No Filestore, Memorystore enabled
# Uses split pattern: empty cloud + cloud_resource

# Step 1: Create empty cloud shell
# cloud_provider and region are optional - will use placeholders for empty clouds
resource "anyscale_cloud" "test" {
  name = var.cloud_name

  is_private_cloud = var.is_private_cloud
  auto_add_user    = var.auto_add_user

  timeouts {
    create = "30m"
    update = "30m"
    delete = "30m"
  }
}

# Step 2: Attach cloud resource with Memorystore configuration
resource "anyscale_cloud_resource" "primary" {
  cloud_id      = anyscale_cloud.test.cloud_id
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

    # Memorystore for Ray GCS fault tolerance
    memorystore_instance_name = module.google_anyscale_v2.memorystore_id
    memorystore_endpoint      = module.google_anyscale_v2.memorystore_endpoint
  }

  # Object Storage (GCS)
  object_storage {
    bucket_name = module.google_anyscale_v2.cloudstorage_bucket_name
    region      = var.gcp_region
  }

  # No file_storage block - Filestore disabled

  timeouts {
    create = "30m"
    update = "30m"
    delete = "30m"
  }
}
