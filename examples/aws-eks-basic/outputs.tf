output "cloud_id" {
  description = "The ID of the created Anyscale cloud"
  value       = anyscale_cloud.primary.id
}

output "cloud_name" {
  description = "The name of the created Anyscale cloud"
  value       = anyscale_cloud.primary.name
}

output "cloud_resource_id" {
  description = "The cloud resource ID. Pass this to the Anyscale operator during installation."
  value       = anyscale_cloud.primary.cloud_resource_id
}

output "eks_cluster_name" {
  description = "The name of the EKS cluster"
  value       = module.eks.cluster_name
}

# Guards the count=0 case (before the second apply / install_gateway_resources = true) so this
# output never errors on an index into an empty list.
output "gateway_address" {
  description = "The Envoy Gateway's external load balancer address, once the second apply (install_gateway_resources = true) has run and the Gateway reached Programmed. Null before then."
  value       = length(data.kubernetes_resource.gateway_status) > 0 ? data.kubernetes_resource.gateway_status[0].object.status.addresses[0].value : null
}
