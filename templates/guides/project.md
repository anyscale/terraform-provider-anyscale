---
page_title: "Project: Collaborator Removal and Known Limitations"
subcategory: "Behavior & Limitations"
description: |-
  Why anyscale_project's collaborator block was removed in v0.25.0, what to do with a configuration or state that still has one, where project access is managed instead, and the brief retry delay you may see on terraform destroy shortly after apply.
---

# Project: Collaborator Removal and Known Limitations

This guide covers [`anyscale_project`](../resources/project.md) behavior that isn't obvious from
its schema table alone: what happened to the `collaborator` block it used to have, and a timing
limitation on `terraform destroy` shortly after `terraform apply`. To *read* a project's current
collaborators without managing them, see the [`anyscale_project` data
source](../data-sources/project.md), which still exposes them — the plural
[`anyscale_projects`](../data-sources/projects.md) data source does not, for performance.

For a project created as part of a full cloud setup, see the [Create a VM
Cloud](./create-a-vm-cloud.md) getting-started walkthrough.

## The `collaborator` block was removed in v0.25.0

`anyscale_project` no longer has a `collaborator` block, and this resource no longer manages project
access at all. It was removed because a project role cannot be granted independently of a cloud
role: the backend requires a cloud grant on the parent cloud before a project grant on a project
under it, and revoking the cloud grant cascades to every project beneath it. Managing the two in
separate resources let a project role be declared for someone with no cloud access, which only ever
failed partway through an `apply` rather than at `plan` time.

**There is currently no in-provider replacement.** Project collaborators must be managed outside
Terraform for now, through the Anyscale console or API. A resource that manages a cloud's members
together with their project roles is planned, but it is not part of this release — this guide will
point at it once it actually ships, not before.

## Upgrading a configuration or state that still has a `collaborator` block

If your configuration has one or more `collaborator` blocks on an `anyscale_project` resource,
remove them before upgrading to v0.25.0 or later. The schema no longer accepts this block, so
Terraform rejects an un-updated configuration at `plan`/`validate` time with a schema error naming
`collaborator`, before ever reaching the API.

Your **state** does not need the same manual cleanup. Every `anyscale_project` ever created carries
a `collaborator` key in state — an empty list if you never used it — and upgrading the provider
silently migrates that key away the first time state is read. No `terraform state rm` or re-import
is needed, and this touches only the local state record, never real collaborator access on the
backend.

## Known limitation: brief delay on `terraform destroy` shortly after `terraform apply`

Deleting a project you just created can occasionally retry for a few seconds — up to a minute and a
half in rare cases — before succeeding. This targets a known backend timing race in the delete-time
permission check, not a provider bug — a project's create-time permission grant can take a moment to
become visible to that check, so a `destroy` within about 5 minutes of the matching `apply` can hit
a `403 Permission denied` that would not reproduce a moment later. The provider retries automatically
with a capped-exponential backoff — starting at 1 second and holding at a short cap up to a 90 second
total ceiling — only for a project this recently created; a project that has existed longer than
that surfaces any real `403` immediately, exactly as before this behavior was added, so a genuine
permission problem is never masked.

With `TF_LOG` unset (the default), this is invisible — the operation just takes a few extra seconds
before completing normally. Set `TF_LOG` to `WARN` or higher to see the retry logged explicitly. If
the retry is exhausted, the error is identical to what you would see without this behavior, so a
persistent, real permission problem still surfaces exactly as it always has.

Deleting a project that still has active jobs or services is not affected by this retry — the
provider recognizes that specific error and surfaces the friendly "Project Has Active Resources"
message immediately, the same way it already does when the API reports active resources with a
different status code.
