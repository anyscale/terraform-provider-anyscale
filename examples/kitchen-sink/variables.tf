# ---------------------------------------------------------------------------------------------------------------------
# REQUIRED VARIABLES
#
# There's no module here building fresh AWS infrastructure (see the full aws-vm/gcp-vm examples for
# that) -- these all point at AWS infrastructure that already exists in your account, the same way
# examples/resources/anyscale_cloud/resource.tf does.
# ---------------------------------------------------------------------------------------------------------------------

variable "aws_region" {
  description = "AWS region for the cloud, its object storage bucket, and the compute config's instances."
  type        = string
}

variable "aws_vpc_id" {
  description = "ID of an existing VPC to register with Anyscale."
  type        = string
}

variable "aws_subnet_ids_to_az" {
  description = "Map of existing subnet IDs to their availability zone, e.g. { \"subnet-abc\" = \"us-east-2a\" }."
  type        = map(string)
}

variable "aws_security_group_ids" {
  description = "IDs of existing security groups that allow the traffic Anyscale clusters need."
  type        = list(string)
}

variable "aws_controlplane_iam_role_arn" {
  description = "ARN of the existing cross-account IAM role the Anyscale control plane assumes."
  type        = string
}

variable "aws_dataplane_iam_role_arn" {
  description = "ARN of the existing IAM role attached to cluster node instances."
  type        = string
}

variable "aws_external_id" {
  description = "External ID configured in the controlplane IAM role's trust policy."
  type        = string
}

variable "object_storage_bucket_name" {
  description = "Name of an existing S3 bucket Anyscale will use for object storage (bare name, no s3:// prefix needed)."
  type        = string
}

variable "new_member_email" {
  description = <<-EOT
    Email address to send a real organization invitation to. Applying this configuration WILL send
    a real email -- use an address you own or control (e.g. a `+kitchen-sink` alias of your own
    address), not a placeholder. There is deliberately no default: forgetting to set this should
    fail plan, not silently invite whatever example address happened to be hardcoded here.
  EOT
  type        = string
}

# ---------------------------------------------------------------------------------------------------------------------
# OPTIONAL VARIABLES
# ---------------------------------------------------------------------------------------------------------------------

variable "name_prefix" {
  description = "Prefix applied to every resource this example creates, so they're easy to spot and to distinguish from other examples applied in the same org."
  type        = string
  default     = "kitchen-sink"
}

# Only referenced inside organization.tf's commented-out anyscale_organization_collaborator
# block (import-only, so it can't be live code) -- not unused, just not wired into anything
# tflint can see until you uncomment it.
# tflint-ignore: terraform_unused_declarations
variable "new_member_permission_level" {
  description = "Organization permission level for the invited member: \"collaborator\" or \"owner\"."
  type        = string
  default     = "collaborator"
  validation {
    condition     = contains(["collaborator", "owner"], var.new_member_permission_level)
    error_message = "new_member_permission_level must be \"collaborator\" or \"owner\"."
  }
}

variable "head_node_instance_type" {
  description = "Instance type for the compute config's head node."
  type        = string
  default     = "m5.2xlarge"
}

variable "worker_instance_type" {
  description = "Instance type for the compute config's autoscaling worker group."
  type        = string
  default     = "m5.4xlarge"
}

variable "registry_image_uri" {
  description = "Publicly pullable image URI to register with anyscale_container_image_registry (default requires no credentials)."
  type        = string
  default     = "docker.io/anyscale/ray:2.9.0-py310"
}
