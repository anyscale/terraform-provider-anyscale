output "cloud_id" {
  description = "The ID of the created Anyscale cloud"
  value       = anyscale_cloud.example_gcp.cloud_id
}

output "cloud_name" {
  description = "The name of the created Anyscale cloud"
  value       = anyscale_cloud.example_gcp.name
}

output "cloud_state" {
  description = "The current state of the Anyscale cloud"
  value       = anyscale_cloud.example_gcp.state
}

output "cloud_status" {
  description = "The current status of the Anyscale cloud"
  value       = anyscale_cloud.example_gcp.status
}

# output "anyscale_register_command" {
#   description = <<-EOF
#     Anyscale register command.
#     This output can be used with the Anyscale CLI to register a new Anyscale Cloud.
#     You will need to replace `<CUSTOMER_DEFINED_NAME>` with a name of your choosing before running the Anyscale CLI command.
#   EOF
#   value       = <<-EOT
#     anyscale cloud register --provider gcp \
#     --name <CUSTOMER_DEFINED_NAME> \
#     --region ${var.gcp_region} \
#     --project-id ${module.google_anyscale_v2_commonname.project_id} \
#     --vpc-name ${module.google_anyscale_v2_commonname.vpc_name} \
#     --subnet-names ${module.google_anyscale_v2_commonname.public_subnet_name} \
#     --cloud-storage-bucket-name ${module.google_anyscale_v2_commonname.cloudstorage_bucket_name} \
#     --anyscale-service-account-email ${module.google_anyscale_v2_commonname.iam_anyscale_access_service_acct_email} \
#     --instance-service-account-email ${module.google_anyscale_v2_commonname.iam_anyscale_cluster_node_service_acct_email} \
#     --provider-name ${module.google_anyscale_v2_commonname.iam_workload_identity_provider_name} \
#     --firewall-policy-names ${module.google_anyscale_v2_commonname.vpc_firewall_policy_name} \
#     --functional-verify workspace
#   EOT
# }
