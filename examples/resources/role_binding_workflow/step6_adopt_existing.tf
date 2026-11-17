# Step 6 (reference): adopt grants that already exist.
#
# Declaring a grant that already exists fails with "Role Grant Already
# Exists". Import it instead. The import ID names what the grant gives:
#   <scope_type>/<scope_id>/<principal_type>/<principal_id>/<role_id>
# scope_type is organization, cloud or project; principal_type is user
# or user_group. The grant's own ID (rb_...) also works.
#
# Converting an organization to role bindings creates grants for the
# access people already had, so this is the usual first step after
# conversion. Uncomment, fill in the IDs, and run terraform plan to see
# what would be imported.
#
# locals {
#   existing = {
#     analysts = {
#       project = "prj_..."
#       group   = "ug_..."
#     }
#   }
# }
#
# resource "anyscale_iam_role_grant" "existing" {
#   for_each = local.existing
#
#   user_group_id = each.value.group
#   role_id       = data.anyscale_iam_role.project_reader.id
#   project_id    = each.value.project
# }
#
# import {
#   for_each = local.existing
#   to       = anyscale_iam_role_grant.existing[each.key]
#   id = join("/", [
#     "project", each.value.project,
#     "user_group", each.value.group,
#     data.anyscale_iam_role.project_reader.id,
#   ])
# }
#
# To adopt every grant on one scope at once, import an
# anyscale_iam_role_binding by "<scope_type>/<scope_id>" instead:
#   terraform import anyscale_iam_role_binding.owned project/prj_...
