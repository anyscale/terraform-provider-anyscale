---
page_title: "Container Images: Build vs. Register, Identity, and Lifecycle"
subcategory: ""
description: |-
  Build vs. register workflows, the name_version identifier, digest-based pinning, id semantics, and lifecycle behavior for the Container Image resources and data sources that aren't obvious from the schema tables alone.
---

# Container Images: Build vs. Register, Identity, and Lifecycle

This guide covers cross-cutting behavior for the Container Image surface of the provider:
[`anyscale_container_image_build`](../resources/container_image_build.md) and
[`anyscale_container_image_registry`](../resources/container_image_registry.md) (resources), and
[`anyscale_container_image`](../data-sources/container_image.md) and
[`anyscale_container_images`](../data-sources/container_images.md) (data sources). It exists because
several of these behaviors aren't obvious from any single schema table.

## Two ways to get an image: build vs. register

`anyscale_container_image_build` and `anyscale_container_image_registry` solve the same underlying
problem — getting a container image Anyscale can run Ray workloads from — through two different
lifecycles, and they aren't interchangeable:

- **`anyscale_container_image_build`** builds a new image from a `containerfile` (inline, via
  Terraform's heredoc syntax) or `containerfile_path` (a file Terraform reads the contents of).
  Anyscale runs the build for you. Changing the Containerfile's contents on a later `apply` triggers
  a new build **revision** under the same name — this resource updates in place; nothing here is
  `RequiresReplace`.
- **`anyscale_container_image_registry`** registers an image that already exists in a container
  registry you control (`image_uri`), skipping the build step entirely. Anyscale validates the image
  and makes it available immediately. Because there's no build to revise, this resource is fully
  immutable: changing any optional attribute (`name`, `ray_version`, `registry_login_secret`)
  replaces the resource (`RequiresReplace`) rather than updating it in place.

Pick `..._build` when you want Anyscale to build the image for you from source. Pick `..._registry`
when your image already exists elsewhere and you just need Anyscale to know about it.

## `name_version` is the identifier to hand to Anyscale tooling

Both resources and both data sources expose a Computed `name_version` attribute, formatted as
`name:revision`. This — not `image_uri`, and not `id` — is the handle designed for `anyscale job
submit`, `anyscale service deploy`, and the Anyscale SDK: it pins a workload to the exact revision you
built or registered, the same way `anyscale_compute_config`'s own `name_version` pins a workload to an
exact compute config version.

`image_uri` is still useful on its own terms — it's the raw, pullable reference (what you'd hand to
`docker pull`) — but prefer `name_version` when submitting jobs or services. See the
[example below](#container-images-and-compute-configs-are-managed-independently) for both used side by
side.

## Digest-based image pinning

`anyscale_container_image_build`, `anyscale_container_image_registry`, and the
[`anyscale_container_image`](../data-sources/container_image.md) data source (the singular lookup, not
the plural [`anyscale_container_images`](../data-sources/container_images.md) list) all expose a
Computed `digest` attribute: the image's content digest (for example `sha256:...`) — a stronger pinning
guarantee than `name_version`, which pins to a named build revision rather than to the exact bytes of
that revision's contents.

```terraform
resource "anyscale_container_image_build" "training" {
  name          = "training-image"
  containerfile = <<-EOT
    FROM anyscale/ray:2.9.0-py310
  EOT
}

output "training_image_digest" {
  value       = anyscale_container_image_build.training.digest
  description = "Pin a job/service submission to these exact image bytes, not just this name:revision"
}
```

`digest` behaves differently across the three surfaces, and the difference is deliberate rather than an
inconsistency:

- **`anyscale_container_image_build`** rebuilds *in place* — changing `containerfile` /
  `containerfile_path` triggers a new build revision under `Update`, not a replacement — so `digest`
  legitimately changes on a rebuild. It has no plan modifiers: Terraform shows it as "known after apply"
  on any plan that changes the Containerfile, the same way `image_uri` and `build_id` do.
- **`anyscale_container_image_registry`** is fully immutable (every optional attribute change is
  `RequiresReplace`; see above), so its `digest` can't change without a full resource replacement. It
  carries `UseStateForUnknown`, pinning it to its last-known value between ordinary refreshes instead of
  showing a spurious "known after apply" on every plan.
- **The data source** has no plan-modifier concept at all — every `Read` is a fresh lookup, so `digest`
  is a plain Computed value that simply reflects whatever the latest successful build's digest is right
  now.

`digest` can be null on the data source: specifically when the underlying build lookup fails, or when
the image has no `latest_build` yet (a cluster environment that's never had a successful build). Don't
assume it's always populated — check for null in configurations that consume it conditionally.

## What `build_status` means

`build_status` (on `anyscale_container_image_build`, and read-only wherever else a build is surfaced)
is one of six values: `pending`, `in_progress`, `succeeded`, `failed`, `pending_cancellation`, or
`canceled` — one L. `pending_cancellation` means a cancel request was received but the build is still
tearing down; it is not yet terminal. Only `succeeded`, `failed`, and `canceled` are terminal.

## `id`, `build_id`, and `cluster_environment_id`

`id` is the **cluster environment ID** on both resources — a durable identifier for the underlying
entity that doesn't change across builds/revisions. `cluster_environment_id`, exposed separately on
both resources, is identical to `id`; it's there so a config that's already reading
`cluster_environment_id` off one of these resources doesn't need a second lookup to also get `id`, not
because the two values can diverge.

`build_id` is a different kind of handle: the ID of the *latest* build for this image. Unlike `id`, it
can change without the resource itself being replaced. On `anyscale_container_image_build`, it changes
every time `Update` creates a new revision. On `anyscale_container_image_registry`, it can also advance
between refreshes even though the resource is immutable — a registry's underlying build can be
superseded by a newer one without the Terraform resource changing, which is exactly why `id` had to mean
the cluster environment rather than "whichever build happened to be latest when the resource was
created."

### If you're upgrading from a version where `registry.id` was a build ID

Earlier provider versions used the build ID for `anyscale_container_image_registry.id`. A
`StateUpgrader` migrates existing state automatically the next time you run `plan` or `apply`: it
rewrites `id` to the `cluster_environment_id` value already in your state, and initializes the new
`digest` attribute (see above) to null so it can populate on the following refresh. There's no
re-import and no manual steps — Terraform's own view of the resource stays seamless across the upgrade.

The one real exception to "seamless": **anything outside Terraform that parsed `registry.id` and
expected a build ID** — a saved `terraform output`, a script, a CI variable — will see a different value
after the upgrade, since auto-migration covers Terraform's own state, not every external consumer of it.
If you have tooling like that, update it to use `cluster_environment_id` (or the migrated `id`, now the
same value) going forward.

The **import key** changed the same way: `terraform import anyscale_container_image_registry.<name>`
now takes a cluster environment ID, not a build ID. See this resource's own
[Import section](../resources/container_image_registry.md#import) for a copy-pasteable example.

## The default Ray version, if you don't set one

`anyscale_container_image_registry`'s `ray_version` is Optional and Computed. If you don't set it, the
provider still sends a concrete default value when registering the image — not "the latest available
Ray version." Whichever value ends up in state, whether it's the one you typed or the provider's
fallback, is resolved once, at creation, and left alone on every later refresh: it won't get silently
rewritten out from under you, and changing it yourself still replaces the resource (`ray_version` is
`RequiresReplace`, like every other optional attribute on this resource) rather than updating in place.

Don't rely on the fallback staying at any particular value across provider releases: if your workload
needs a specific Ray version, set `ray_version` explicitly rather than omitting it.

## Deleting a container image archives it — and defaults can't be archived

Neither resource supports permanent deletion. `terraform destroy` (or removing the resource block)
archives the underlying cluster environment via the Anyscale API; there is no way to hard-delete a
container image through this provider, matching how the Anyscale CLI and web console behave.

Anyscale-provided default cluster environments cannot be archived. If a resource under Terraform's
management happens to be one of these — for example, if you imported it — `Delete` treats the
backend's "cannot archive a default" error as a successful no-op: Terraform removes the resource from
state without actually deleting anything server-side. This is deliberate: the alternative is a
`destroy` that fails every time, for a resource the backend will never actually let you remove.

## Looking up images with data sources

Use [`anyscale_container_image`](../data-sources/container_image.md) to look up one image by `id` or
`name`. Use [`anyscale_container_images`](../data-sources/container_images.md) to list and filter
(`project_id`, `creator_id`, `name_contains`, `include_archived`) across many. The plural data source
returns a lighter-weight shape per item — enough to find the image you want (`name`, `name_version`,
`revision`, `latest_build_id`, `latest_build_status`) — not the full detail (`image_uri`, `ray_version`,
and so on) that the singular lookup returns once you know which image you're after.

## Container images and compute configs are managed independently

There is no attribute linking `anyscale_compute_config` to a container image, in either direction, and
no `depends_on` should be needed between them. Terraform manages each as an independent resource; a
job or service submission — via the Anyscale CLI, SDK, or web console, not a resource this provider
models — is what actually pairs a specific image with a specific compute config, and that pairing
happens outside Terraform, at submission time.

See [`container-image-compute-config`](../../examples/container-image-compute-config) for a complete
example that builds an image and defines a compute config side by side, and surfaces both resources'
`name_version` outputs — the two values you'd pass to a job or service submission command.

## Known limitations

- **Only Containerfile-based builds are supported.** The underlying Anyscale API also supports
  building from a structured configuration (a base image plus separate pip/conda/apt package lists,
  environment variables, and post-build commands) instead of a Containerfile. This provider does not
  expose that path yet; use `containerfile` / `containerfile_path` in the meantime.
