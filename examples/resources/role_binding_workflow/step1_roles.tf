# Step 1: look up the roles to grant.
#
# A role is a named set of permissions, identified by an ID (rol_...).
# Grants take the ID, so look each one up by its exact, case-sensitive
# name. Built-in roles exist in every organization; see README.md for
# the full list. A misspelled or archived name fails the plan with
# "not found".

data "anyscale_iam_role" "project_reader" {
  name = "Project reader"
}

data "anyscale_iam_role" "project_writer" {
  name = "Project writer"
}

data "anyscale_iam_role" "cloud_collaborator" {
  name = "Cloud collaborator"
}

data "anyscale_iam_role" "billing_reader" {
  name = "Billing/consumption reader"
}

# The Admin role whoever creates a project or cloud receives on it.
# Step 5 needs it.
data "anyscale_iam_role" "admin" {
  name = "Admin"
}
