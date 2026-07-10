---
page_title: "Project: Collaborator Access and Permission Levels"
subcategory: ""
description: |-
  Collaborator access model versus anyscale_policy_binding, and upgrading permission_level from a prior release, for the Project resource and data sources that aren't obvious from the schema tables alone.
---

# Project: Collaborator Access and Permission Levels

This guide covers cross-cutting behavior for [`anyscale_project`](../resources/project.md)
(resource), and the [`anyscale_project`](../data-sources/project.md) and
[`anyscale_projects`](../data-sources/projects.md) data sources. It exists because these behaviors
aren't obvious from any single schema table alone.

## Two ways to grant project access: `collaborator` versus `anyscale_policy_binding`

`anyscale_project`'s `collaborator` block and the separate
[`anyscale_policy_binding`](../resources/policy_binding.md) resource both grant access to a
project, but they operate on different principals with different Terraform semantics — picking
the wrong one for your use case leads to surprises:

- **`collaborator` grants access to individual users**, by email address
  (`email = "person@example.com"`). Each block is one user; adding, removing, or changing the
  `permission_level` for one collaborator doesn't touch any other collaborator on the project.
  Valid `permission_level` values are `owner`, `write`, and `readonly`.
- **`anyscale_policy_binding`** (a **beta feature**, built for SCIM-provisioned organizations)
  **grants access to user groups** (`ug_*` IDs), not individual users, via `principals` on a
  `bindings` list scoped to `resource_type = "project"`. Valid `role_name` values for project scope
  are the same three (`owner`, `write`, `readonly`), but the semantics differ sharply from
  `collaborator`: **`bindings` replaces the entire set of group-based role bindings for that
  project on every apply.** A group left out of `bindings` loses its project access, even if
  something outside Terraform granted it. `anyscale_policy_binding` also has cross-resource
  requirements — a group needs cloud-level access before it can be granted project-level access;
  see [its own resource page](../resources/policy_binding.md) for the exact rules.

Use `collaborator` for individual users. Use `anyscale_policy_binding` when you're managing access
for user groups at scale (typically alongside SCIM provisioning) and want Terraform to own the
complete, authoritative set of group bindings for that project. The two mechanisms don't conflict
with each other — a project can have both individual collaborators and group policy bindings at
once — but don't use both to manage the *same* group's access, since only `anyscale_policy_binding`
will ever remove a group binding on apply.

## Upgrading `permission_level` from `writer` to `write`

The only valid `permission_level` values on `anyscale_project`'s `collaborator` block are `owner`,
`write`, and `readonly`. `writer` is not a valid value — it was replaced by `write` in v0.4.0 (see
below for why this is not a disruptive change in practice).

This isn't a value that ever worked: the Anyscale API has always rejected `writer` for a project
collaborator (`owner` and `readonly` happen to match the API and always worked, which is what made
this easy to miss). Any `terraform apply` that tried to grant `permission_level = "writer"` either
failed outright with an API error, or — if it somehow reached state — read back as `write` on the
next refresh and produced a permanent plan diff. There is no working configuration this change
disrupts; update the literal string in your configuration and re-run `terraform plan`:

```terraform
collaborator {
  email            = "developer@example.com"
  permission_level = "write" # was "writer"
}
```

`write` also matches the value [`anyscale_policy_binding`](../resources/policy_binding.md) already
uses for the identical project-level permission (see above) — the two resources now speak the same
vocabulary for the same concept.
