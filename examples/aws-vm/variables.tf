# Variables for AWS VM Test Scenario
# Consolidated example with optional EFS and MemoryDB
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

variable "tags" {
  description = "A map of tags to add to all resources."
  type        = map(string)
  default = {
    "test"        = "true"
    "environment" = "test"
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
  default     = "as-aws-vm-"
  validation {
    condition     = var.common_prefix == null || try(length(var.common_prefix) <= 30, false)
    error_message = "common_prefix must either be `null` or less than 30 characters."
  }
}

# Cloud Configuration
variable "cloud_name" {
  description = "The name of the Anyscale cloud"
  type        = string
  default     = "tf-aws-vm-test"
}

variable "is_private_cloud" {
  description = "Whether this is a private cloud"
  type        = bool
  default     = false
}

variable "auto_add_user" {
  description = "Whether to automatically add users"
  type        = bool
  default     = true
}

variable "anyscale_s3_force_destroy" {
  description = "Force destroy S3 bucket for testing"
  type        = bool
  default     = true
}

# ---------------------------------------------------------------------------------------------------------------------
# FEATURE TOGGLES
# Control which optional AWS resources to create
# ---------------------------------------------------------------------------------------------------------------------

variable "enable_efs" {
  description = "Enable EFS (Elastic File System) for shared storage."
  type        = bool
  default     = false
}

variable "enable_memorydb" {
  description = "Enable MemoryDB for Ray GCS fault tolerance."
  type        = bool
  default     = false
}

# ---------------------------------------------------------------------------------------------------------------------
# VPC CONFIGURATION
# ---------------------------------------------------------------------------------------------------------------------

variable "vpc_cidr_block" {
  description = "The CIDR block for the VPC."
  type        = string
  default     = "172.24.0.0/16"
}

variable "vpc_public_subnets" {
  description = "List of public subnet CIDR blocks."
  type        = list(string)
  default     = ["172.24.21.0/24", "172.24.22.0/24", "172.24.23.0/24"]
}
