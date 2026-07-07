# Look up by name - useful for feeding principals into anyscale_policy_binding
data "anyscale_user_group" "engineering" {
  name = "Engineering"
}

# Look up by ID
data "anyscale_user_group" "by_id" {
  id = "ug_abc123"
}

output "user_group_id" {
  value       = data.anyscale_user_group.engineering.id
  description = "The group ID (ug_*), for use as a principal in anyscale_policy_binding"
}

output "user_group_name_by_id" {
  value       = data.anyscale_user_group.by_id.name
  description = "The group's name when looking up by id"
}
