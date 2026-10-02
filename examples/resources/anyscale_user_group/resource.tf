# Alpha: the schema may change before Beta.
# Creates an empty user group. Manage who belongs to it with anyscale_user_group_members.
resource "anyscale_user_group" "ml_platform" {
  name = "ml-platform" # no leading or trailing whitespace; unique among the organization's groups
}
