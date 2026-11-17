# Step 2: create a user group and set its members.
#
# A principal is who holds a role: a user group (ug_...) or a single
# user (usr_...). Grant roles to groups, then change who has access by
# editing the member list; every member gets every role the group
# holds.

resource "anyscale_iam_user_group" "team" {
  name = "${var.name_prefix}-team"
}

# AUTHORITATIVE: anyone in the group who is not listed is removed,
# including people added in the Anyscale console. An empty list
# empties the group.
resource "anyscale_iam_user_group_members" "team" {
  user_group_id = anyscale_iam_user_group.team.id
  members       = var.team_member_emails
}
