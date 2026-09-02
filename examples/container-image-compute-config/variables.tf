# ---------------------------------------------------------------------------------------------------------------------
# REQUIRED VARIABLES
# ---------------------------------------------------------------------------------------------------------------------

variable "cloud_id" {
  description = "The ID of an existing Anyscale cloud to attach the compute config to."
  type        = string
  validation {
    condition     = length(var.cloud_id) > 4 && substr(var.cloud_id, 0, 4) == "cld_"
    error_message = "The cloud_id value must start with \"cld_\"."
  }
}

# ---------------------------------------------------------------------------------------------------------------------
# OPTIONAL VARIABLES
# ---------------------------------------------------------------------------------------------------------------------

variable "project_id" {
  description = "Anyscale project ID to scope the container image build to. Leave null to use the default project."
  type        = string
  default     = null
}

variable "image_name" {
  description = "Name for the container image build."
  type        = string
  default     = "container-image-compute-config-example"
}

variable "compute_config_name" {
  description = "Name for the compute config."
  type        = string
  default     = "container-image-compute-config-example"
}

variable "head_node_instance_type" {
  description = "Instance type for the head node."
  type        = string
  default     = "m5.2xlarge"
}

variable "worker_instance_type" {
  description = "Instance type for worker nodes."
  type        = string
  default     = "m5.4xlarge"
}

variable "worker_min_nodes" {
  description = "Minimum number of worker nodes."
  type        = number
  default     = 0
}

variable "worker_max_nodes" {
  description = "Maximum number of worker nodes."
  type        = number
  default     = 5
}
