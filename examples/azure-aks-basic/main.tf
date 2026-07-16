# ---------------------------------------------------------------------------------------------------------------------
# Example Anyscale K8s Resources - AKS
#   This template creates AKS resources for Anyscale + Anyscale Cloud
#   It creates:
#     - Resource group, VNet, subnet
#     - AKS cluster with CPU/GPU node pools and Microsoft Entra Workload ID
#     - ADLS Gen2 storage account (for the abfss:// object storage anyscale_cloud expects)
#     - The Anyscale Operator's managed identity + federated credential
# ---------------------------------------------------------------------------------------------------------------------

resource "anyscale_cloud" "primary" {
  # Common Fields
  name           = var.cloud_name
  cloud_provider = "AZURE"
  region         = var.azure_location
  compute_stack  = "K8S"

  azure_config {
    tenant_id = data.azurerm_client_config.current.tenant_id
  }

  # Kubernetes Configuration (required for K8S compute_stack)
  kubernetes_config {
    # Principal ID (object ID) of the operator's managed identity, NOT its
    # client ID - see the anyscale_operator_iam_identity schema description.
    anyscale_operator_iam_identity = azurerm_user_assigned_identity.anyscale_operator.principal_id
  }

  # Object Storage - required for K8S. Azure uses its own abfss:// URI,
  # passed through verbatim - never s3:// or gs://.
  object_storage {
    bucket_name = "abfss://${azurerm_storage_container.ray_storage.name}@${azurerm_storage_account.anyscale.name}.dfs.core.windows.net"
  }
}
