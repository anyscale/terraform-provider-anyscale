---
page_title: "Container Images: Build vs. Register, Identity, and Lifecycle"
subcategory: ""
description: |-
  Build vs. register workflows, the name_version identifier, id semantics, and lifecycle behavior for the Container Image resources and data sources that aren't obvious from the schema tables alone.
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

## Digest-based image pinning is planned, not yet available

A future release will add a Computed `digest` attribute to `anyscale_container_image_build`,
`anyscale_container_image_registry`, and the
[`anyscale_container_image`](../data-sources/container_image.md) data source. `digest` will hold the
image's content hash (for example `sha256:...`) — a stronger pinning guarantee than `name_version`,
which pins to a named build revision rather than to the exact bytes of that revision's contents.

**This attribute does not exist yet.** Referencing `digest` in your configuration today fails with a
schema error ("Unsupported attribute"), not a null value — there's nothing to read yet, so don't add it
until it ships. Once it's available, this section will be updated with the resource and data source
attribute references and an example showing digest-based pinning alongside `name_version`.

## What `build_status` means

`build_status` (on `anyscale_container_image_build`, and read-only wherever else a build is surfaced)
is one of six values: `pending`, `in_progress`, `succeeded`, `failed`, `pending_cancellation`, or
`canceled` — one L. `pending_cancellation` means a cancel request was received but the build is still
tearing down; it is not yet terminal. Only `succeeded`, `failed`, and `canceled` are terminal.

## `id` means something different on each resource today

`anyscale_container_image_build.id` is the cluster environment ID — a durable identifier for the
underlying entity that doesn't change across builds/revisions.

`anyscale_container_image_registry.id`, today, is the **build ID** of the registration, not the
cluster environment ID — even though both `build_id` and `cluster_environment_id` are separately
exposed as read-only attributes on the same resource. In other words, `id` is not necessarily the same
kind of handle across the two resources in this family.

> **TODO / known open item:** this inconsistency is under active review and may change in a future
> release (tracked internally; not yet decided as of this writing). If it changes, `id` semantics and
> the import key for `anyscale_container_image_registry` would both be affected, alongside an
> automatic state upgrade and an upgrade note — not a silent behavior change. **Don't treat the
> registry resource's current import key as a stable long-term contract.** Always check this page's
> own Import section and the resource's schema reference for the current, authoritative key before
> scripting or automating around it.

## The default Ray version, if you don't set one

`anyscale_container_image_registry`'s `ray_version` is optional. If you don't set it, the provider
does not resolve "the latest available Ray version" — it falls back to a specific version pinned
inside the provider itself. Don't rely on that fallback staying at any particular value across
provider releases: if your workload needs a specific Ray version, set `ray_version` explicitly rather
than omitting it.

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

See also [Digest-based image pinning is planned, not yet available](#digest-based-image-pinning-is-planned-not-yet-available)
above for the other attribute that's planned but not yet available.
