# Alpha: the schema may change before Beta.
# List every user group in the organization.
data "anyscale_user_groups" "all" {}

output "user_group_names" {
  description = "Names of all user groups in the organization"
  value       = [for g in data.anyscale_user_groups.all.groups : g.name]
}
