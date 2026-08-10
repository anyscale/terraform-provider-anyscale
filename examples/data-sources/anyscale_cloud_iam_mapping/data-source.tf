# Reads the cloud's primary deployment (is_default) when cloud_resource_id is omitted.
data "anyscale_cloud_iam_mapping" "production" {
  cloud_id = "cld_00000000000000000000000000"
}

output "production_rules" {
  value       = data.anyscale_cloud_iam_mapping.production.rules
  description = "The mapping's rules, in evaluation order -- the first matching rule wins"
}

output "production_fallback_rule" {
  value       = data.anyscale_cloud_iam_mapping.production.fallback_rule
  description = "CLOUD_DEFAULT or FAIL when a mapping exists; null when the cloud has no mapping configured"
}

# Look up a specific, non-default deployment by naming its cloud_resource_id explicitly.
data "anyscale_cloud_iam_mapping" "secondary_deployment" {
  cloud_id          = "cld_00000000000000000000000000"
  cloud_resource_id = "cldrsrc_00000000000000000000000000"
}

output "secondary_deployment_rules" {
  value       = data.anyscale_cloud_iam_mapping.secondary_deployment.rules
  description = "Rules for the explicitly named cloud_resource_id, rather than the cloud's primary deployment"
}
