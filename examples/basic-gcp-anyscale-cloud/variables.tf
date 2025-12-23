# Variables for Anyscale Cloud Configuration (GCP)
# ---------------------------------------------------------------------------------------------------------------------
# REQUIRED VARIABLES
# These variables must be set when using this module.
# ---------------------------------------------------------------------------------------------------------------------

variable "gcp_region" {
  description = <<-EOF
    The GCP region in which all resources will be created.
    ex:
    ```
    gcp_region = "us-central1"
    ```
  EOF
  type        = string
}

variable "gcp_zone" {
  description = <<-EOF
    The GCP zone for zonal resources.
    ex:
    ```
    gcp_zone = "us-central1-a"
    ```
  EOF
  type        = string
}

variable "billing_account_id" {
  description = <<-EOF
    The billing account ID to associate with the GCP project.
    ex:
    ```
    billing_account_id = "012345-ABCDEF-GHIJKL"
    ```
  EOF
  type        = string
}

variable "root_folder_number" {
  description = <<-EOF
    The folder number where the GCP project will be created.
    ex:
    ```
    root_folder_number = "123456789012"
    ```
  EOF
  type        = string
}

variable "customer_ingress_cidr_ranges" {
  description = <<-EOT
    The IPv4 CIDR blocks that are allowed to access the clusters.
    This provides the ability to lock down access to just the public IPs of a corporate network.
    This is added to the firewall rules and allows HTTPS and SSH access.

    While not recommended, you can set this to "0.0.0.0/0" to allow access from anywhere.
    ex:
    ```
    customer_ingress_cidr_ranges = "52.1.1.23/32,10.1.0.0/16"
    ```
  EOT
  type        = string
}

variable "anyscale_org_id" {
  description = <<-EOT
    (Required) Anyscale Organization ID.

    This is used to configure Workload Identity Federation for cross-project access.
    The Organization ID is unique to each customer, ensuring only the customer can access their resources.

    ex:
    ```
    anyscale_org_id = "org_abcdefghijklmn1234567890"
    ```
  EOT
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

# ------------------------------------------------------------------------------
# OPTIONAL PARAMETERS
# These variables have defaults, but may be overridden.
# ------------------------------------------------------------------------------

variable "anyscale_cloud_id" {
  description = <<-EOF
    (Optional) Anyscale Cloud ID.
    This is used to tag resources with the Cloud ID. The Cloud ID is not known until the
    Cloud is created, so this is an optional variable.
    ex:
    ```
    anyscale_cloud_id = "cld_abcdefghijklmnop1234567890"
    ```
  EOF
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
  description = <<-EOF
    (Optional) A map of labels.
    These labels will be added to all GCP resources that accept labels.
    ex:
    ```
    labels = {
      "environment" = "test",
      "team" = "anyscale"
    }
    ```
  EOF
  type        = map(string)
  default = {
    "test" : "true",
    "environment" : "test"
  }
}

variable "anyscale_deploy_env" {
  description = <<-EOF
    (Optional) Anyscale deployment environment. Used in resource names and labels.
    ex:
    ```
    anyscale_deploy_env = "production"
    ```
  EOF
  type        = string
  validation {
    condition = (
      var.anyscale_deploy_env == "production" || var.anyscale_deploy_env == "development" || var.anyscale_deploy_env == "test"
    )
    error_message = "The anyscale_deploy_env only allows `production`, `test`, or `development`"
  }
  default = "production"
}

variable "common_prefix" {
  description = <<-EOT
    (Optional) A common prefix for resource names.
    Default for this EXAMPLE is `anyscale-pfx-test-`
  EOT
  type        = string
  default     = "anyscale-pfx-test-"
  validation {
    condition     = var.common_prefix == null || try(length(var.common_prefix) <= 30, false)
    error_message = "common_prefix must either be `null` or less than 30 characters."
  }
}

# Basic Cloud Configuration
variable "cloud_name" {
  description = "The name of the Anyscale cloud"
  type        = string
}

variable "cloud_provider" {
  description = "The cloud provider (e.g., AWS, GCP, Azure, Generic)"
  type        = string
  default     = "GCP"
  validation {
    condition     = contains(["AWS", "GCP", "Azure", "Generic"], var.cloud_provider)
    error_message = "Invalid cloud provider. Must be one of: AWS, GCP, Azure, Generic."
  }
}

variable "compute_stack" {
  description = "The compute stack type (e.g., VM, K8s)"
  type        = string
  default     = "VM"
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

# Provider Configuration
variable "anyscale_token" {
  description = "Anyscale API token for authentication"
  type        = string
  sensitive   = true
  default     = null
}
