output "cloud_id" {
  description = "The ID of the created Anyscale cloud"
  value       = anyscale_cloud.primary.id
}

output "cloud_name" {
  description = "The name of the created Anyscale cloud"
  value       = anyscale_cloud.primary.name
}

output "aks_cluster_name" {
  description = "The name of the AKS cluster"
  value       = azurerm_kubernetes_cluster.aks.name
}

output "aks_oidc_issuer_url" {
  description = "The AKS cluster's OIDC issuer URL, useful when debugging Workload ID federation"
  value       = azurerm_kubernetes_cluster.aks.oidc_issuer_url
}

output "anyscale_operator_client_id" {
  description = "Client ID of the Anyscale Operator's managed identity - annotate the Kubernetes ServiceAccount with this (azure.workload.identity/client-id) when installing the operator"
  value       = azurerm_user_assigned_identity.anyscale_operator.client_id
}

output "anyscale_operator_principal_id" {
  description = "Principal (object) ID of the Anyscale Operator's managed identity - this is what anyscale_cloud.kubernetes_config.anyscale_operator_iam_identity expects, not the client ID above"
  value       = azurerm_user_assigned_identity.anyscale_operator.principal_id
}

output "storage_account_name" {
  description = "The ADLS Gen2 storage account backing the abfss:// object storage URI"
  value       = azurerm_storage_account.anyscale.name
}
