# Variables for GCP with Memorystore Test Scenario
# No Filestore, Memorystore enabled
# ---------------------------------------------------------------------------------------------------------------------

# ---------------------------------------------------------------------------------------------------------------------
# REQUIRED VARIABLES
# ---------------------------------------------------------------------------------------------------------------------

variable "gcp_region" {
  description = "The GCP region in which all resources will be created."
  type        = string
}

variable "gcp_zone" {
  description = "The GCP zone for zonal resources."
  type        = string
}

variable "billing_account_id" {
  description = "The billing account ID to associate with the GCP project."
  type        = string
}

variable "root_folder_number" {
  description = "The folder number where the GCP project will be created."
  type        = string
}

variable "customer_ingress_cidr_ranges" {
  description = "The IPv4 CIDR blocks that are allowed to access the clusters."
  type        = string
}

variable "anyscale_org_id" {
  description = "Anyscale Organization ID."
  type        = string
  validation {
    condition = (
      var.anyscale_org_id == null ? true : (
        length(var.anyscale_org_id) > 4 &&
        substr(var.anyscale_org_id, 0, 4) == "org_"
      )
    )
    error_message = "The anyscale_org_id value must start with \"org_\"."
  }
}

# ---------------------------------------------------------------------------------------------------------------------
# OPTIONAL VARIABLES
# ---------------------------------------------------------------------------------------------------------------------

variable "anyscale_cloud_id" {
  description = "Anyscale Cloud ID (optional, not known until cloud is created)."
  type        = string
  default     = null
  validation {
    condition = (
      var.anyscale_cloud_id == null ? true : (
        length(var.anyscale_cloud_id) > 4 &&
        substr(var.anyscale_cloud_id, 0, 4) == "cld_"
      )
    )
    error_message = "The anyscale_cloud_id value must start with \"cld_\"."
  }
}

variable "labels" {
  description = "A map of labels to add to all GCP resources."
  type        = map(string)
  default = {
    "test"        = "true"
    "environment" = "test"
    "scenario"    = "gcp-with-memorystore"
  }
}

variable "anyscale_deploy_env" {
  description = "Anyscale deployment environment."
  type        = string
  default     = "test"
  validation {
    condition     = contains(["production", "development", "test"], var.anyscale_deploy_env)
    error_message = "The anyscale_deploy_env only allows `production`, `test`, or `development`"
  }
}

variable "common_prefix" {
  description = "Common prefix for resource names. Must be unique per scenario."
  type        = string
  default     = "as-gcp-ms-"
  validation {
    condition     = var.common_prefix == null || try(length(var.common_prefix) <= 30, false)
    error_message = "common_prefix must either be `null` or less than 30 characters."
  }
}

# Cloud Configuration
variable "cloud_name" {
  description = "The name of the Anyscale cloud"
  type        = string
  default     = "tf-gcp-memorystore-test"
}

variable "is_private_cloud" {
  description = "Whether this is a private cloud"
  type        = bool
  default     = false
}

variable "auto_add_user" {
  description = "Whether to automatically add users"
  type        = bool
  default     = false
}
