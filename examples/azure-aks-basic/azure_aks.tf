####################################################################################################
# Terraform configuration to create a new Azure AKS cluster
#
# This example hand-rolls the AKS cluster and its supporting resources (rather than wrapping a
# community module, unlike the AWS/GCP examples) because the Azure-specific piece that matters most
# for Anyscale - Microsoft Entra Workload ID federation - is a handful of resources, not a module's
# worth. The overall shape (resource group, network, cluster with a system pool, additional CPU/GPU
# node pools, object storage, operator identity) follows Anyscale's own published reference, see
# https://github.com/anyscale/terraform-kubernetes-anyscale-foundation-modules
# (examples/azure/aks-new_cluster), adapted here to register the cloud via this provider's own
# anyscale_cloud resource instead of that repo's separate `anyscale cloud register` CLI step.
#
# It demonstrates:
# - on-demand CPU node pool
# - spot CPU node pool
# - GPU node pools (one per entry in gpu_node_pool_configs, on-demand)
# - Microsoft Entra Workload ID federation for the Anyscale Operator (no long-lived secrets)
# - an ADLS Gen2 (hierarchical namespace) storage account, the abfss:// scheme anyscale_cloud expects
####################################################################################################

data "azurerm_client_config" "current" {}

resource "azurerm_resource_group" "anyscale" {
  name     = "${var.aks_cluster_name}-rg"
  location = var.azure_location
  tags     = var.tags
}

resource "azurerm_virtual_network" "anyscale" {
  name                = "${var.aks_cluster_name}-vnet"
  location            = azurerm_resource_group.anyscale.location
  resource_group_name = azurerm_resource_group.anyscale.name
  address_space       = ["10.0.0.0/16"]
  tags                = var.tags
}

resource "azurerm_subnet" "nodes" {
  name                 = "${var.aks_cluster_name}-nodes"
  resource_group_name  = azurerm_resource_group.anyscale.name
  virtual_network_name = azurerm_virtual_network.anyscale.name
  address_prefixes     = ["10.0.0.0/20"]
}

# ─── AKS Cluster ────────────────────────────────────────────────────────────
resource "azurerm_kubernetes_cluster" "aks" {
  name                = var.aks_cluster_name
  location            = azurerm_resource_group.anyscale.location
  resource_group_name = azurerm_resource_group.anyscale.name
  dns_prefix          = var.aks_cluster_name

  # Required for Microsoft Entra Workload ID - the mechanism the Anyscale
  # Operator uses to reach Azure Blob Storage without a long-lived secret.
  oidc_issuer_enabled       = true
  workload_identity_enabled = true

  default_node_pool {
    name           = "default"
    vm_size        = "Standard_D4s_v5"
    vnet_subnet_id = azurerm_subnet.nodes.id
    node_count     = 2
    # Small, fixed pool for cluster components (CoreDNS, the Anyscale
    # Operator) - Ray workloads run on the node pools below.
  }

  identity {
    type = "SystemAssigned"
  }

  network_profile {
    network_plugin = "azure"
  }

  # Restrict API server access to the given CIDR ranges rather than leaving
  # the control plane open to the internet.
  api_server_access_profile {
    authorized_ip_ranges = var.ingress_cidr_ranges
  }

  tags = var.tags
}

locals {
  capacity_type_taints = {
    on_demand = "node.anyscale.com/capacity-type=ON_DEMAND:NoSchedule"
    spot      = "node.anyscale.com/capacity-type=SPOT:NoSchedule"
  }
}

resource "azurerm_kubernetes_cluster_node_pool" "ondemand_cpu" {
  name                  = "ondemandcpu"
  kubernetes_cluster_id = azurerm_kubernetes_cluster.aks.id
  vm_size               = "Standard_D8s_v5"
  vnet_subnet_id        = azurerm_subnet.nodes.id
  min_count             = 0
  max_count             = 10
  node_count            = 0
  node_taints           = [local.capacity_type_taints.on_demand]
  tags                  = var.tags
}

resource "azurerm_kubernetes_cluster_node_pool" "spot_cpu" {
  name                  = "spotcpu"
  kubernetes_cluster_id = azurerm_kubernetes_cluster.aks.id
  vm_size               = "Standard_D8s_v5"
  vnet_subnet_id        = azurerm_subnet.nodes.id
  priority              = "Spot"
  eviction_policy       = "Delete"
  spot_max_price        = -1 # pay up to the on-demand price, never evicted purely on price
  min_count             = 0
  max_count             = 10
  node_count            = 0
  node_taints           = [local.capacity_type_taints.spot]
  tags                  = var.tags
}

# One on-demand GPU node pool per entry in gpu_node_pool_configs (a single T4
# pool by default - see variables.tf for the full shape).
resource "azurerm_kubernetes_cluster_node_pool" "gpu_ondemand" {
  for_each = var.gpu_node_pool_configs

  name                  = "gpu${lower(each.key)}"
  kubernetes_cluster_id = azurerm_kubernetes_cluster.aks.id
  vm_size               = each.value.vm_size
  vnet_subnet_id        = azurerm_subnet.nodes.id
  min_count             = 0
  max_count             = 10
  node_count            = 0
  node_labels = {
    "nvidia.com/gpu.product" = each.value.node_label
  }
  node_taints = [
    local.capacity_type_taints.on_demand,
    "nvidia.com/gpu=present:NoSchedule",
    "node.anyscale.com/accelerator-type=GPU:NoSchedule",
  ]
  tags = var.tags
}

# ─── Object Storage (ADLS Gen2, for abfss://) ──────────────────────────────
resource "azurerm_storage_account" "anyscale" {
  # Storage account names must be globally unique, 3-24 chars, lowercase
  # alphanumeric only - no hyphens or underscores.
  name                     = substr(replace("${var.aks_cluster_name}sa${data.azurerm_client_config.current.subscription_id}", "-", ""), 0, 24)
  resource_group_name      = azurerm_resource_group.anyscale.name
  location                 = azurerm_resource_group.anyscale.location
  account_tier             = "Standard"
  account_replication_type = "LRS"
  account_kind             = "StorageV2"
  # Hierarchical namespace is what makes this an ADLS Gen2 account and is
  # required for the abfss:// scheme anyscale_cloud expects - it can only be
  # set at creation time, never toggled on an existing storage account.
  is_hns_enabled = true
  tags           = var.tags
}

resource "azurerm_storage_container" "ray_storage" {
  name                  = "ray-storage"
  storage_account_id    = azurerm_storage_account.anyscale.id
  container_access_type = "private"
}

# ─── Anyscale Operator Identity (Microsoft Entra Workload ID) ──────────────
resource "azurerm_user_assigned_identity" "anyscale_operator" {
  name                = "${var.aks_cluster_name}-operator"
  resource_group_name = azurerm_resource_group.anyscale.name
  location            = azurerm_resource_group.anyscale.location
  tags                = var.tags
}

# Links the Kubernetes ServiceAccount the Anyscale Operator's Helm chart
# creates (system:serviceaccount:<namespace>:anyscale-operator - the chart,
# not this Terraform config, creates the actual ServiceAccount) to this
# managed identity via the cluster's OIDC issuer, so the operator can
# authenticate to Azure with no stored secret.
resource "azurerm_federated_identity_credential" "anyscale_operator" {
  name                = "${var.aks_cluster_name}-operator-fic"
  resource_group_name = azurerm_resource_group.anyscale.name
  parent_id           = azurerm_user_assigned_identity.anyscale_operator.id
  issuer              = azurerm_kubernetes_cluster.aks.oidc_issuer_url
  subject             = "system:serviceaccount:${var.anyscale_k8s_namespace}:anyscale-operator"
  audience            = ["api://AzureADTokenExchange"]
}

resource "azurerm_role_assignment" "anyscale_operator_blob" {
  scope                = azurerm_storage_account.anyscale.id
  role_definition_name = "Storage Blob Data Contributor"
  principal_id         = azurerm_user_assigned_identity.anyscale_operator.principal_id
}
