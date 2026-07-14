# --- anyscale_project: one per cloud ------------------------------------------------------------
# No inline `collaborator` block here on purpose: the API requires the collaborator's email to
# already be an existing org identity with cloud-level access before it can be added (a fresh
# invitee, like invite_email below, has neither yet -- the API 404s "user not found"). That needs a
# second, already-provisioned org member to demo safely, which isn't a fair thing to require just to
# copy-paste this example. See examples/resources/anyscale_project/resource.tf for the collaborator
# block itself, with real owner/write/readonly entries.

resource "anyscale_project" "a" {
  name        = "${var.name_prefix}-project-a"
  cloud_id    = anyscale_cloud.a.id
  description = "Created by the kitchen-sink example (Cloud A)."
}

resource "anyscale_project" "b" {
  name        = "${var.name_prefix}-project-b"
  cloud_id    = anyscale_cloud.b.id
  description = "Created by the kitchen-sink example (Cloud B)."
}
