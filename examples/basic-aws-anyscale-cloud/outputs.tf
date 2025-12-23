output "cloud_id" {
  description = "The ID of the created Anyscale cloud"
  value       = anyscale_cloud.example_aws.cloud_id
}

output "cloud_name" {
  description = "The name of the created Anyscale cloud"
  value       = anyscale_cloud.example_aws.name
}

output "cloud_state" {
  description = "The current state of the Anyscale cloud"
  value       = anyscale_cloud.example_aws.state
}

output "cloud_status" {
  description = "The current status of the Anyscale cloud"
  value       = anyscale_cloud.example_aws.status
}


output "anyscale_register_command" {
  description = <<-EOF
    Anyscale register command.
    This output can be used with the Anyscale CLI to register a new Anyscale Cloud.
    You will need to replace `<CUSTOMER_DEFINED_NAME>` with a name of your choosing before running the Anyscale CLI command.
  EOF
  value       = <<-EOT
    anyscale cloud register --provider aws \
    --name <CUSTOMER_DEFINED_NAME> \
    --region ${var.aws_region} \
    --vpc-id ${module.aws_anyscale_v2_common_name.anyscale_vpc_id} \
    --subnet-ids ${join(",", module.aws_anyscale_v2_common_name.anyscale_vpc_public_subnet_ids)} \
    --security-group-ids ${module.aws_anyscale_v2_common_name.anyscale_security_group_id} \
    --s3-bucket-id ${module.aws_anyscale_v2_common_name.anyscale_s3_bucket_id} \
    --anyscale-iam-role-id ${module.aws_anyscale_v2_common_name.anyscale_iam_role_arn} \
    --instance-iam-role-id ${module.aws_anyscale_v2_common_name.anyscale_iam_role_cluster_node_arn} \
    --external-id ${module.aws_anyscale_v2_common_name.anyscale_iam_role_external_id} \
    --functional-verify workspace
  EOT
}
