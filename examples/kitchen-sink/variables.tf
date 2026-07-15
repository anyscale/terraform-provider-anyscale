# Kitchen Sink: variables grouped by what they configure (shared foundation, EKS, compute configs,
# container images, org, service data-source gating) rather than one flat list -- see README.md for
# how these groups map to the blocks each feeds.

# ---------------------------------------------------------------------------------------------------------------------
# REQUIRED VARIABLES
# ---------------------------------------------------------------------------------------------------------------------

variable "anyscale_org_id" {
  description = "Your Anyscale Organization ID (starts with \"org_\"). Feeds the IAM trust policy for the shared cross-account role. Find it at https://console.anyscale.com/ under Organization Settings."
  type        = string
  validation {
    condition     = length(var.anyscale_org_id) > 4 && substr(var.anyscale_org_id, 0, 4) == "org_"
    error_message = "The anyscale_org_id value must start with \"org_\"."
  }
}

variable "anyscale_external_id" {
  description = "Free-form string used as the IAM trust policy external ID for the shared cross-account role."
  type        = string
}

variable "customer_ingress_cidr_ranges" {
  description = "Comma-delimited IPv4 CIDR ranges allowed to reach the VM legs (opens 443/https and 22/ssh) in addition to all-traffic-within-the-VPC, which every node needs regardless. Replace with your own IP(s)/range(s) -- avoid \"0.0.0.0/0\" outside of throwaway testing."
  type        = string
}

# ---------------------------------------------------------------------------------------------------------------------
# OPTIONAL VARIABLES -- shared identity / naming
# ---------------------------------------------------------------------------------------------------------------------

variable "name_prefix" {
  description = "Prefix applied to every resource this example creates (both clouds, both cloud resources, projects, compute configs, images), so they're easy to spot and stay stable across reapplies -- no timestamp is mixed in, so re-running apply against the same state never collides. Destroy between runs or change this value to start a fresh set of resources."
  type        = string
  default     = "kitchen-sink"
}

variable "aws_region" {
  description = "AWS region for the shared VPC, EKS cluster, S3 bucket, and every compute config's instances."
  type        = string
  default     = "us-east-2"
}

variable "tags" {
  description = "A map of tags applied to every AWS resource this example creates."
  type        = map(string)
  default = {
    Test        = "true"
    Environment = "dev"
    Example     = "kitchen-sink"
  }
}

variable "anyscale_deploy_env" {
  description = "Anyscale deployment environment tag for the shared AWS foundation."
  type        = string
  default     = "test"
  validation {
    condition     = contains(["production", "development", "test"], var.anyscale_deploy_env)
    error_message = "anyscale_deploy_env only allows \"production\", \"test\", or \"development\"."
  }
}

variable "anyscale_s3_force_destroy" {
  description = "Force destroy the shared S3 bucket on terraform destroy, even if it isn't empty. Convenient for a kitchen-sink you tear down often; turn off for anything longer-lived."
  type        = bool
  default     = true
}

# ---------------------------------------------------------------------------------------------------------------------
# OPTIONAL VARIABLES -- EKS (Cloud A's K8S cloud resource)
# ---------------------------------------------------------------------------------------------------------------------

variable "eks_cluster_version" {
  description = "Kubernetes version of the EKS cluster built for Cloud A's K8S cloud resource."
  type        = string
  default     = "1.36"
}

# ---------------------------------------------------------------------------------------------------------------------
# OPTIONAL VARIABLES -- compute configs
# ---------------------------------------------------------------------------------------------------------------------

variable "head_node_instance_type" {
  description = "Instance type for every compute config's head node (VM legs and the EKS-targeted compute config alike -- see compute_config.tf for why the EKS one isn't guaranteed to actually schedule)."
  type        = string
  default     = "m5.2xlarge"
}

variable "worker_instance_type" {
  description = "Instance type for every compute config's autoscaling worker group."
  type        = string
  default     = "m5.4xlarge"
}

# ---------------------------------------------------------------------------------------------------------------------
# OPTIONAL VARIABLES -- container images
# ---------------------------------------------------------------------------------------------------------------------

variable "registry_image_uri" {
  description = "Publicly pullable image URI to register with anyscale_container_image_registry (default requires no credentials)."
  type        = string
  default     = "docker.io/anyscale/ray:2.9.0-py310"
}

# ---------------------------------------------------------------------------------------------------------------------
# OPTIONAL VARIABLES -- organization
# ---------------------------------------------------------------------------------------------------------------------

variable "invite_email" {
  description = <<-EOT
    Email address to send a real organization invitation to. Left empty by default so a heavy
    multi-cloud apply never has the side effect of emailing someone by accident -- set this to an
    address you own or control (e.g. a "+kitchen-sink" alias) to include the invitation this run.
  EOT
  type        = string
  default     = ""
}

# Only referenced inside organization.tf's commented-out anyscale_organization_collaborator block
# (import-only, so it can't be live code) -- not unused, just not wired into anything tflint can see
# until you uncomment it.
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

# ---------------------------------------------------------------------------------------------------------------------
# OPTIONAL VARIABLES -- service data sources
# ---------------------------------------------------------------------------------------------------------------------

variable "existing_service_name" {
  description = <<-EOT
    Name of an already-running Anyscale service to read back via the singular anyscale_service data
    source. Left empty by default (no anyscale_service resource exists for this example to create
    one for you) -- the singular lookup is skipped entirely until you point this at a real service.
    anyscale_services (the plural, filtered list) always runs regardless, since an empty result is
    still a meaningful, assertable state.
  EOT
  type        = string
  default     = ""
}
