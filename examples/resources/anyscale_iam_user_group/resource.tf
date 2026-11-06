# Beta. Creates an empty user group.
# Manage who belongs to it with anyscale_iam_user_group_members.
resource "anyscale_iam_user_group" "ml_platform" {
  # no leading or trailing whitespace; unique among the organization's
  # groups
  name = "ml-platform"
}
