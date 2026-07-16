# ---------------------------------------------------------------------------------------------------------------------
# REQUIRED VARIABLES
# These variables must be set when using this module.
# ---------------------------------------------------------------------------------------------------------------------

variable "azure_subscription_id" {
  description = <<-EOT
    (Required) The Azure subscription ID to deploy into.

    ex:
    ```
    azure_subscription_id = "00000000-0000-0000-0000-000000000000"
    ```
  EOT
  type        = string
}

variable "azure_location" {
  description = <<-EOT
    (Required) The Azure region in which all resources will be created.

    ex:
    ```
    azure_location = "eastus2"
    ```
  EOT
  type        = string
}

# ---------------------------------------------------------------------------------------------------------------------
# OPTIONAL VARIABLES
# These variables have defaults, but may be overridden.
# ---------------------------------------------------------------------------------------------------------------------

variable "aks_cluster_name" {
  description = <<-EOT
    (Optional) AKS Cluster Name

    The name of the AKS cluster to create.

    ex:
    ```
    aks_cluster_name = "anyscale-cluster"
    ```
  EOT
  type        = string
  default     = "anyscale-aks"
  validation {
    condition     = can(regex("^[a-zA-Z][a-zA-Z0-9_-]{0,62}$", var.aks_cluster_name))
    error_message = "Cluster name must start with a letter, be 1-63 characters, and contain only letters, digits, underscores, and hyphens."
  }
}

variable "gpu_node_pool_configs" {
  description = <<-EOT
    (Optional) GPU node pool configurations for the AKS cluster.

    ex:
    ```
    gpu_node_pool_configs = {
      "T4" = {
        vm_size    = "Standard_NC4as_T4_v3"
        node_label = "nvidia-tesla-t4"
      }
    }
    ```
  EOT
  type = map(object({
    vm_size    = string
    node_label = string
  }))
  default = {
    "T4" = {
      vm_size    = "Standard_NC4as_T4_v3"
      node_label = "nvidia-tesla-t4"
    }
  }
}

variable "ingress_cidr_ranges" {
  description = <<-EOT
    (Optional) The IPv4 CIDR blocks that allow access to Anyscale clusters.

    These are added to the network security group and allow port 443 (https) access.

    ex:
    ```
    ingress_cidr_ranges = ["52.1.1.23/32", "10.1.0.0/16"]
    ```
  EOT
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

variable "anyscale_k8s_namespace" {
  description = <<-EOT
    (Optional) The Kubernetes namespace the Anyscale Operator deploys into.

    Must match the namespace used in the federated identity credential's
    subject (system:serviceaccount:<namespace>:anyscale-operator).

    ex:
    ```
    anyscale_k8s_namespace = "anyscale-operator"
    ```
  EOT
  type        = string
  default     = "anyscale-operator"
}

variable "tags" {
  description = <<-EOT
    (Optional) A map of tags to apply to all resources that accept them.

    ex:
    ```
    tags = {
      "example"     = "true"
      "environment" = "example"
    }
    ```
  EOT
  type        = map(string)
  default = {
    "example"     = "true"
    "environment" = "example"
  }
}

variable "cloud_name" {
  description = <<-EOT
    (Optional) The name of the Anyscale cloud.

    ex:
    ```
    cloud_name = "my-aks-cloud"
    ```
  EOT
  type        = string
  default     = "tf-aks-test"
}
