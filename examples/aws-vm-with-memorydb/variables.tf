# Variables for AWS with MemoryDB Test Scenario
# No EFS, MemoryDB enabled
# ---------------------------------------------------------------------------------------------------------------------

# ---------------------------------------------------------------------------------------------------------------------
# REQUIRED VARIABLES
# ---------------------------------------------------------------------------------------------------------------------

variable "aws_region" {
  description = "The AWS region in which all resources will be created."
  type        = string
}

variable "customer_ingress_cidr_ranges" {
  description = "The IPv4 CIDR block that is allowed to access the clusters."
  type        = string
}

variable "anyscale_external_id" {
  description = "A string that will be used for the IAM trust policy external ID."
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

variable "tags" {
  description = "A map of tags to add to all resources."
  type        = map(string)
  default = {
    "test"        = "true"
    "environment" = "test"
    "scenario"    = "aws-with-memorydb"
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
  default     = "as-aws-mdb-"
  validation {
    condition     = var.common_prefix == null || try(length(var.common_prefix) <= 30, false)
    error_message = "common_prefix must either be `null` or less than 30 characters."
  }
}

# Cloud Configuration
variable "cloud_name" {
  description = "The name of the Anyscale cloud"
  type        = string
  default     = "tf-aws-memorydb-test"
}

variable "cloud_provider" {
  description = "The cloud provider"
  type        = string
  default     = "AWS"
}

variable "compute_stack" {
  description = "The compute stack type"
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

variable "anyscale_s3_force_destroy" {
  description = "Force destroy S3 bucket for testing"
  type        = bool
  default     = true
}
