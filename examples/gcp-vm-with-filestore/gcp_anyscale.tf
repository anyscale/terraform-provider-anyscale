# GCP with Filestore Scenario - Cloud Foundation Module
# Filestore enabled, No Memorystore

locals {
  full_labels = merge(tomap({
    anyscale-cloud-id           = anyscale_cloud.primary.id,
    anyscale-deploy-environment = var.anyscale_deploy_env
    }),
    var.labels
  )
}

module "google_anyscale_v2" {
  source = "anyscale/anyscale-cloudfoundation-modules/google"

  labels = local.full_labels

  # Required: Anyscale Organization ID
  anyscale_organization_id = var.anyscale_org_id

  # Optional Anyscale identifiers
  anyscale_deploy_env = var.anyscale_deploy_env

  # GCP Project Configuration
  anyscale_project_billing_account = var.billing_account_id
  anyscale_project_folder_id       = var.root_folder_number

  # Resource Location Configuration
  anyscale_bucket_location    = "US"
  anyscale_filestore_location = var.gcp_zone

  # Networking Configuration
  anyscale_vpc_public_subnet_cidr               = "10.101.0.0/16"
  anyscale_vpc_firewall_allow_access_from_cidrs = var.customer_ingress_cidr_ranges

  # Resource Naming
  common_prefix   = var.common_prefix
  use_common_name = true

  # Filestore: ENABLED for this scenario
  enable_anyscale_filestore = true

  # Memorystore: DISABLED for this scenario
  enable_anyscale_memorystore = false
}
