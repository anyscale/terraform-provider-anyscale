---
page_title: "Project: Collaborator Access and Permission Levels"
subcategory: "Behavior & Limitations"
description: |-
  Collaborator access model and upgrading permission_level from a prior release, for the Project resource and data sources that aren't obvious from the schema tables alone.
---

# Project: Collaborator Access and Permission Levels

This guide covers cross-cutting behavior for [`anyscale_project`](../resources/project.md)
(resource), and the [`anyscale_project`](../data-sources/project.md) and
[`anyscale_projects`](../data-sources/projects.md) data sources. It exists because these behaviors
aren't obvious from any single schema table alone.

## Importing an existing project

`terraform import anyscale_project.example <project_id>` recovers the project's full, current
`collaborator` set from the API — every real collaborator, not just the ones you've declared in
configuration so far. This includes collaborators added outside Terraform, and the collaborator the
API automatically adds for the project's creator.

Every project has this creator-owner collaborator whether or not you ever write a `collaborator`
block for it: the API adds it automatically at creation time. A project managed with no
`collaborator` blocks at all never shows this collaborator in state during ordinary use — the
provider only fetches collaborators when your configuration declares at least one, specifically to
avoid a perpetual diff against a collaborator nothing in your config is managing — but import has no
prior configuration to protect, so it surfaces every real collaborator, creator-owner included.

**After importing a project with existing collaborators, add a matching `collaborator` block for
every collaborator that import brought into state — the creator-owner one too.** The `collaborator`
list is authoritative: on the next `apply`, any real collaborator missing from your configuration is
treated as one to remove, and the provider calls the API to remove their access — for the
creator-owner collaborator, that can mean revoking the very access that created the project.
Reconcile your configuration with the imported state before your next `apply`, not after.

If you already imported a project with real collaborators using a provider version older than
v0.4.0, upgrading alone does not fix it: the empty `collaborator` list from that import is already
saved in your state, and ordinary refreshes only re-fetch collaborators when state already has some
— once it's empty, it stays empty on its own, so neither upgrading in place nor
`terraform apply -refresh-only` recovers it. Re-import that resource to pick up the real
collaborator set: `terraform state rm anyscale_project.<name>` followed by
`terraform import anyscale_project.<name> <project_id>`.

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

## Brief delay on `terraform destroy` or adding a `collaborator` shortly after `terraform apply`

Deleting a project you just created, or adding a `collaborator` to it, can occasionally retry for a
few seconds — up to a minute in rare cases — before succeeding. This targets a known backend timing
race in the delete-time and collaborator-add permission checks, not a provider bug — a project's
create-time permission grant can take a moment to become visible to those checks, so an operation
within about 5 minutes of the matching apply can hit a `403 Permission denied` that would not
reproduce a moment later. The provider retries automatically with a capped-exponential backoff —
starting at 1 second and holding at a short cap up to a 60 second total ceiling — only for a project
this recently created; a project that has existed longer than that surfaces any real `403`
immediately, exactly as before this behavior was added, so a genuine permission problem is never
masked.

With `TF_LOG` unset (the default), this is invisible — the operation just takes a few extra seconds
before completing normally. Set `TF_LOG` to `WARN` or higher to see the retry logged explicitly. If
the retry is exhausted, the error is identical to what you would see without this behavior, so a
persistent, real permission problem still surfaces exactly as it always has.

Deleting a project that still has active jobs or services is not affected by this retry — the
provider recognizes that specific error and surfaces the friendly "Project Has Active Resources"
message immediately, the same way it already does when the API reports active resources with a
different status code.
