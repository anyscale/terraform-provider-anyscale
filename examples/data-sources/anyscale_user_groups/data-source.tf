# List every user group in the organization - takes no arguments
data "anyscale_user_groups" "all" {}

output "user_group_names" {
  value       = [for g in data.anyscale_user_groups.all.groups : g.name]
  description = "Names of every user group in the organization"
}
