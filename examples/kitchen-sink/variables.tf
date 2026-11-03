# Kitchen Sink: variables grouped by what they configure (shared foundation, EKS, compute configs,
# container images, organization gates) rather than one flat list -- see README.md for
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
  default     = "docker.io/anyscale/ray:2.59.0-py313"
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

variable "org_user_email" {
  description = "Email of an existing, accepted organization member to manage with anyscale_organization_user. Empty (the default) skips it. Destroy never removes the member."
  type        = string
  default     = ""
}

variable "enable_user_groups" {
  description = "Create a user group and manage its members. User groups are a beta feature that must be enabled for your organization."
  type        = bool
  default     = false
}

variable "user_group_member_emails" {
  description = "Members of the kitchen-sink user group when enable_user_groups is set. Each must be an accepted organization member. The list is authoritative."
  type        = list(string)
  default     = []
}

variable "enable_role_binding" {
  description = "Grant the user group the built-in project_viewer role on Cloud B's project. Needs enable_user_groups and an organization converted to role bindings; other organizations answer 409."
  type        = bool
  default     = false
  validation {
    condition     = !var.enable_role_binding || var.enable_user_groups
    error_message = "enable_role_binding needs enable_user_groups: the binding is granted to the kitchen-sink user group."
  }
}
