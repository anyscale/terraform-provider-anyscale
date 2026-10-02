# Alpha: the schema may change before Beta.
# AUTHORITATIVE: anyone in the group who is not listed in `members` is removed, including people
# added in the Anyscale console. Declare exactly one anyscale_user_group_members per group.
resource "anyscale_user_group" "ml_platform" {
  name = "ml-platform"
}

resource "anyscale_user_group_members" "ml_platform" {
  group_id = anyscale_user_group.ml_platform.id

  # Each email must already belong to your organization; invite people first with
  # anyscale_organization_invitation and wait for them to accept. Matching is case-insensitive.
  members = [
    "dev1@example.com",
    "dev2@example.com",
  ]
}
