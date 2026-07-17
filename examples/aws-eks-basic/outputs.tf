output "cloud_id" {
  description = "The ID of the created Anyscale cloud"
  value       = anyscale_cloud.primary.id
}

output "cloud_name" {
  description = "The name of the created Anyscale cloud"
  value       = anyscale_cloud.primary.name
}

output "cloud_deployment_id" {
  description = "The cloud deployment ID - deprecated by the Anyscale API and always null on this all-in-one pattern (there is no separate anyscale_cloud_resource here to read cloud_resource_id from instead)."
  value       = anyscale_cloud.primary.cloud_deployment_id
}

output "eks_cluster_name" {
  description = "The name of the EKS cluster"
  value       = module.eks.cluster_name
}
