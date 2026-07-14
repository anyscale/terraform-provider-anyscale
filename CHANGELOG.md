# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.8.0] - 2026-07-14

### New Data Sources

- data-source/anyscale_service: adds a data source to look up a single Anyscale Service by ID or name.
- data-source/anyscale_services: adds a data source to list and filter Anyscale Services.

### Changed

- data-source/anyscale_container_image: Document that looking up by `name` returns the most recently modified match when multiple container images share a name (no behavior change, docs only - the resolution rule already worked this way).
- data-source/anyscale_compute_config: Document that looking up by `name` returns the most recently created match when multiple compute configs share a name (no behavior change, docs only - the resolution rule already worked this way).
- data-source/anyscale_compute_config: Look up compute configs by ID via `api/v2/compute_templates` instead of the deprecated `ext/v0/cluster_computes` endpoint; behavior is unchanged.
- data-source/anyscale_compute_config: Search compute configs by name/version via `api/v2/compute_templates/search` instead of the deprecated `ext/v0/cluster_computes/search` endpoint; behavior is unchanged, including still returning archived configs.

### Fixed

- resource/anyscale_cloud: Fix the by-name adopt check (used to recover from an interrupted create) to scan every page of existing clouds instead of only the first, and to error - naming the conflicting cloud ids and how to resolve them via `terraform import` - instead of silently adopting an arbitrary one when multiple clouds already share the same name.
- resource/anyscale_organization_invitation: Remove the fictional `permission_level` argument from the resource's documentation and examples; it was never a real schema attribute, so copying the previous example verbatim into a real configuration failed `terraform validate` with an unsupported argument error.

## [0.7.0] - 2026-07-13

### Added

- data-source/anyscale_cloud: adds `is_k8s`, `availability_zones`, `version`, and `external_id` (`external_id` is `null` when not set).
- data-source/anyscale_clouds: adds `availability_zones`, `version`, and `external_id` (`external_id` is `null` when not set).
- data-source/anyscale_organization_user: adds `base_role` and `additional_roles`, the current source of role information on the backend; `permission_level` is being deprecated in their favor.
- data-source/anyscale_organization_users: adds `base_role` and `additional_roles`, matching `anyscale_organization_user`.
- data-source/anyscale_container_images: adds `image_uri`, available at no extra cost per item since it comes from the same build summary each item already carries.
- data-source/anyscale_container_images: adds `image_name_contains` and `cloud_id` filter arguments. `image_name_contains` matches the underlying base or BYOD image name, distinct from the existing `name_contains`, which matches the user-given template name.
- data-source/anyscale_container_image: adds `cloud_id`, `is_default`, `is_experimental`, `last_modified_at`, and `build_error_message`. `build_error_message` is singular-only; it comes from the full per-build lookup that only this data source makes.
- data-source/anyscale_container_images: adds `cloud_id`, `is_default`, `is_experimental`, and `last_modified_at`.

### Changed

- data-source/anyscale_cloud: the error shown when a by-name lookup fails due to an API error now uses the title "API Request Failed" instead of "Cloud Lookup Failed", matching the wording used elsewhere in the provider; the error detail message is unchanged.
- data-source/anyscale_organization: the error shown when fetching organization info fails now uses the title "API Request Failed" instead of "Organization Lookup Failed", matching the wording used elsewhere in the provider.
- data-source/anyscale_organization_user: the error shown when looking up a user fails now uses the title "API Request Failed" instead of "User Lookup Failed", matching the wording used elsewhere in the provider.
- data-source/anyscale_user: four previously distinct error titles for a failed lookup (a fetch failure, a response-read failure, a bad status code, and a JSON parse failure) now all use the single "API Request Failed" title, matching the wording used elsewhere in the provider.
- data-source/anyscale_user: a failure to fetch `user_group_ids` (including a genuine network failure, not just a bad response) now leaves it `null` instead of failing the entire read; previously, only a bad response or unparseable data degraded gracefully to an empty list, while a network-level failure failed the whole data source. `user_group_ids` is `null` when it could not be determined, and empty only when the user genuinely belongs to no groups.

### Removed

- provider: the example directories `examples/data-sources/anyscale_global_resource_scheduler`, `examples/data-sources/anyscale_global_resource_schedulers`, and `examples/resources/anyscale_global_resource_scheduler` are deleted. The underlying resource and data sources have been disabled since PR #34 and are not in the compiled provider schema, so these examples errored with "Invalid resource/data source type" on `terraform plan` for anyone regardless of credentials; removing them has no effect on a working configuration.

### Fixed

- data-source/anyscale_clouds: the `name_contains`, `cloud_provider`, and `region` filter arguments were silent no-ops because the backend endpoint ignored all three; `name_contains` is now sent to the API as a substring filter and `cloud_provider`/`region` are now applied as client-side filters, so all three actually narrow the result set.
- data-source/anyscale_cloud: looking up a cloud by `name` only checked the first page of results, so a valid name could incorrectly resolve to "not found" once an organization has more clouds than fit on one page; the lookup now pages through all results. This also fixes the "multiple clouds found" warning, which previously compared against the total number of clouds fetched instead of the number actually matching the given name.
- data-source/anyscale_clouds: `status` and `state` now report `null` instead of an empty string when the API returns no value, matching `anyscale_cloud`'s existing behavior.
- data-source/anyscale_user: the nested `organizations[].default_cloud_id` collapsed a null value to an empty string instead of Terraform `null`; now mapped the same way `anyscale_organization` already does.
- data-source/anyscale_user: `organization_permission_level` collapsed a null value (no permission level assigned) to an empty string instead of `null`; fixed the same way. Its description also listed the wrong example values (`owner`, `admin`, `member`); the real enum is `owner`/`collaborator`.
- data-source/anyscale_user: `user_group_ids` only read the first page of `GET /api/v2/user_groups`, silently truncating the list for an organization with more groups than fit on one page; now paginates through all results.
- data-source/anyscale_organization_user: `name` collapsed a null value to an empty string instead of Terraform `null`; the adjacent `user_id` field already handled this correctly, and `name` now uses the same mapping.
- data-source/anyscale_organization_users: `name` collapsed a null value to an empty string instead of Terraform `null`, matching the same fix as `anyscale_organization_user`.
- data-source/anyscale_project: `cloud_id` collapsed a null `parent_cloud_id` to an empty string instead of Terraform `null`; now mapped via the same nullable-pointer pattern used elsewhere in the provider.
- data-source/anyscale_projects: `cloud_id` collapsed a null `parent_cloud_id` to an empty string instead of Terraform `null`, matching the same fix as `anyscale_project`.
- resource/anyscale_project: on read, `cloud_id` collapsed a null `parent_cloud_id` to an empty string instead of Terraform `null`. This only affects an already-anomalous case (a `cloud_id`-configured project whose backend cloud association is unexpectedly absent) that cannot be produced through Terraform today; a healthy project's `cloud_id` is unaffected and does not trigger a plan diff or a replacement.
- data-source/anyscale_container_image: `ray_version` reported `null` for a BYOD (Bring Your Own Docker) image whose Ray version is only available in the build's `byod_ray_version` field; now resolves from `byod_ray_version` when the standard field is absent.
- data-source/anyscale_container_image: `image_uri` used to depend on a second, per-build API call succeeding; now reads from the same build summary already available on the initial lookup, so it stays populated even when that second call fails.
- data-source/anyscale_compute_config: `versions` could never return more than the latest version, since the underlying search sent no version filter at all, which the backend treats as latest-only; now sends the documented `-2` sentinel to fetch every version.
- data-source/anyscale_compute_config: both the by-name lookup and the versions search only read the first page (10 results) of matches, so a name with more than 10 versions or anonymous variants could silently miss the real newest match or the full version history; both now page through every result.

## [0.6.0] - 2026-07-13

### New Data Sources

- data-source/anyscale_organization: Look up the connected Anyscale organization's id, name, public_identifier, and default_cloud_id.

## [0.5.1] - 2026-07-11

### Changed

- resource/anyscale_cloud: the `kubernetes_config`/`object_storage`-required validation errors for a K8S `compute_stack` now name the `cloud_provider` explicitly (e.g. "kubernetes_config is required when cloud_provider is AWS and compute_stack is K8S"), instead of the previous provider-agnostic wording; and errors expanding `aws_config`/`gcp_config`/`kubernetes_config`/`object_storage`/`file_storage` are now wrapped with a "failed to expand <field>:" prefix instead of surfacing the underlying error unwrapped.
- resource/anyscale_cloud_resource: several plan-time validation error messages were reworded to match `anyscale_cloud`'s existing phrasing as part of consolidating shared provider-config logic between the two resources: `aws_config`/`gcp_config`-required messages now say "required when cloud_provider is X and compute_stack is Y" (previously "required when using X provider with Y compute_stack"); the `kubernetes_config`/`object_storage`-required messages for K8S now name the provider explicitly, same as `anyscale_cloud`; and the azure-unsupported message now also states that `azure_config` cannot be applied, matching `anyscale_cloud`'s wording.
- resource/anyscale_organization_invitation: on a failed create, the error title/detail changed from "Error creating invitation"/"Error reading response"/"Error parsing response" (three different titles depending on the failure point) to a single consistent "API Request Failed" title with a "Failed to create invitation: <detail>" message.
- resource/anyscale_organization_collaborator: on a failed update, the error title/detail changed from "Error updating collaborator"/"Error reading response" (two different titles depending on the failure point) to a single consistent "API Request Failed" title with a "Failed to update collaborator <identity_id>: <detail>" message.
- resource/anyscale_project: the delete-time 403 retry for a recently-created project now uses a capped-exponential backoff, retrying for up to 60 seconds total, since real-world backend permission-check lag can exceed a short fixed window.

### Fixed

- data-source/anyscale_clouds: `status` and `state` attribute descriptions now include example values (e.g., ready/pending/failed and ACTIVE/CREATING/FAILED), matching `anyscale_cloud`.
- data-source/anyscale_container_image: Attribute descriptions no longer refer to "cluster environment" (terminology left over from before this data source was renamed) and consistently say "container image" instead.
- data-source/anyscale_container_images: The `id` attribute description no longer refers to "cluster environment"; it now says "container image", matching `anyscale_container_image`.
- resource/anyscale_cloud_resource: `cloud_provider` values that aren't canonical uppercase (e.g. `aws` instead of `AWS`) no longer silently produce an incomplete apply with the provider-specific config block (`aws_config`, `gcp_config`, etc.) left unpopulated and no error; the value is now case-normalized before matching, consistent with `anyscale_cloud`.
- resource/anyscale_project: `terraform destroy` immediately after `terraform apply` no longer intermittently fails with a spurious 403 Permission denied on delete; the provider now retries a bounded number of times for a project created in the last few minutes, since this specific error was a known backend permission-check consistency race rather than a real permission problem.
- resource/anyscale_project: deleting a project that still has active jobs or services now surfaces the friendly Project Has Active Resources error immediately, instead of being misidentified as the delete-time permission-check race and retried for up to a minute before showing the same message.
- resource/anyscale_project: adding a collaborator immediately after creating the project no longer intermittently fails with a spurious 403 Permission denied; the provider now retries this call the same way it already does for delete, since it hits the identical backend permission-check consistency race.

## [0.5.0] - 2026-07-10

### Breaking Changes

- resource/anyscale_policy_binding: This resource managed an alpha SCIM-provisioning feature that was never fully functional and has been removed; before upgrading, remove any `anyscale_policy_binding` blocks from your configuration and apply (or run `terraform destroy -target`) with your current provider version so the underlying bindings are actually cleared from Anyscale, since a plain `terraform state rm` only forgets the resource locally and leaves any real bindings in place.
- data-source/anyscale_policy_binding: This data source belonged to the same alpha SCIM-provisioning feature as the removed `anyscale_policy_binding` resource and has been removed; delete any matching `data` blocks from your configuration.
- data-source/anyscale_policy_bindings: This data source belonged to the same alpha SCIM-provisioning feature as the removed `anyscale_policy_binding` resource and has been removed; delete any matching `data` blocks from your configuration.
- data-source/anyscale_user_group: This data source surfaced SCIM-synced user groups, an alpha feature that has been removed; delete any matching `data` blocks from your configuration.
- data-source/anyscale_user_groups: This data source surfaced SCIM-synced user groups, an alpha feature that has been removed; delete any matching `data` blocks from your configuration.

## [0.4.0] - 2026-07-10

### Breaking Changes

- resource/anyscale_project: collaborator `permission_level` value `"writer"` — previously accepted by the provider schema but always rejected by the Anyscale API (HTTP 422) — is replaced by `"write"`; valid values are now `"owner"`, `"write"`, `"readonly"`. Any configuration using `"writer"` never applied successfully; update it to `"write"`.

### Changed

- resource/anyscale_project: `name` values of `"-"` or `"default"` (case-insensitive) now fail at plan time with a clear error instead of failing later at apply with a generic API error; the Anyscale API has always rejected these reserved names.
- resource/anyscale_project: deleting a project that still has running clusters or workspaces now returns a clearer "Project Has Active Resources" error instead of a generic API error; it still fails rather than being silently ignored.

### Fixed

- data-source/anyscale_project: `collaborator` now returns every collaborator across all pages of API results instead of only the first page.
- resource/anyscale_project: `terraform import` now recovers the project's real collaborator list (including the API's auto-added creator-owner collaborator) instead of always importing an empty `collaborator` block. This only applies to imports performed with this version or later; a project already imported under a prior version keeps its empty collaborator state and must be re-imported (`terraform state rm` then `terraform import` again) to pick up the fix — upgrading the provider in place or running `terraform apply -refresh-only` will not recover it.

## [0.3.4] - 2026-07-10

### Changed

- provider: Delete-failure diagnostics on anyscale_cloud, anyscale_organization_collaborator, anyscale_organization_invitation, and anyscale_policy_binding now use a shared error helpers title and wording instead of each resources own custom phrasing, with the underlying status and body unchanged and only visible on a genuine delete failure; cloud-creation polling also makes one fewer API call per iteration, a debug-only resources lookup that previously fed a log line and nothing else.

### Removed

- provider: Remove seven unused internal helper functions with no remaining callers; no user-visible behavior change.

### Fixed

- data-source/anyscale_policy_binding: Correct the `role_name` description for Cloud-scoped bindings; it previously listed `write` as a valid value, but the enforced set is `collaborator`/`readonly`.
- data-source/anyscale_policy_bindings: Correct the generic `role_name` description to list the actual valid values per resource type (`collaborator`/`readonly` for Cloud, `owner`/`write`/`readonly` for Project).

## [0.3.3] - 2026-07-10

### Fixed

- resource/anyscale_cloud: Retry the transient "still being applied, try again" 409 on `auto_add_user` updates instead of failing `terraform apply` outright; the underlying reconciliation check is organization-wide, so a pending change on an unrelated cloud could previously block an update to this one.

## [0.3.2] - 2026-07-10

### Added

- provider: Add a quick-start example (creating a minimal Compute Config) and an authentication precedence summary to the Registry index page.

### Fixed

- provider: Surface the beta-status warning on the Terraform Registry index page; previously it only appeared in the repository README, whose GitHub-style alert syntax does not render on the Registry.
- resource/anyscale_compute_config: Fix the `worker_nodes` and `auto_select_worker_config` descriptions, which incorrectly claimed omitting `worker_nodes` or enabling `auto_select_worker_config` automatically provisions workload-appropriate workers.

## [0.3.1] - 2026-07-10

### Fixed

- resource/anyscale_container_image_registry: corrected documentation that claimed `digest` is "stable across refreshes" and pinned because the resource is immutable — `digest` is actually a latest-build-derived value, like `build_id`, `revision`, and `name_version`, and can change on a later refresh if a new build supersedes this one outside Terraform; no behavior changed, only the docs.
- resource/anyscale_container_image_build: `digest` could come back empty from `Create` or a Containerfile-triggered `Update` even though the build succeeded, because the backend can report status "succeeded" a few seconds before the digest itself finishes populating. Create/Update now wait (up to 30s) for the digest to settle before returning; a still-unsettled digest after that window no longer fails the apply, it proceeds with digest left null plus a warning, and self-heals on a later refresh.
- resource/anyscale_container_image_registry: same fix as `anyscale_container_image_build` for the identical race on `Create` — `digest` could come back empty even though the underlying build succeeded. `Update` is unaffected (every optional attribute already forces replacement).

## [0.3.0] - 2026-07-09

### Breaking Changes

- resource/anyscale_container_image_registry: id now identifies the cluster environment instead of the latest build, and the redundant cluster_environment_id attribute has been removed; existing state is upgraded automatically, but external tooling reading cluster_environment_id or expecting a build ID from id (e.g. a saved terraform output) must be updated to read id instead, and terraform import now takes a cluster environment ID rather than a build ID.

### Added

- resource/anyscale_container_image_build: gains digest, the built image's content digest (e.g. sha256:...), for pinning a workload to exact image bytes rather than just a name:revision.
- resource/anyscale_container_image_registry: gains digest, the registered image's content digest (e.g. sha256:...); pinned to its last-known value between refreshes since this resource is immutable.
- data-source/anyscale_container_image: gains digest, the image's content digest (e.g. sha256:...); may be null if the image has no successful build yet.

### Fixed

- resource/anyscale_container_image_build: a cancelled build now surfaces a clear "build was cancelled" error during apply instead of "unknown build status: canceled". The backend reports the cancelled state as "canceled" (one L); the provider only recognized the two-L spelling.
- resource/anyscale_container_image_registry: ray_version left unset now correctly reflects the provider-resolved default in state instead of remaining null forever; it still resolves once at creation and won't change on later refreshes.
- data-source/anyscale_container_image: looking up an image by name now searches every page of results instead of only the first, so a match beyond the first page is no longer missed.

## [0.2.0] - 2026-07-09

### Breaking Changes

- resource/anyscale_compute_config: physical_resources is renamed to required_resources on head_node and worker_nodes to match what the API actually accepts. The backend has always rejected the old attribute name on any non-empty value, so this affects configuration text but not any resource that ever successfully applied; update the attribute name and re-run terraform plan. Existing state is upgraded automatically.
- resource/anyscale_compute_config: changing name now replaces the resource instead of silently leaving an orphaned, unmanaged duplicate compute config behind. Review the plan before applying if you are renaming an existing resource.
- resource/anyscale_cloud_resource: `name` is now a required attribute instead of an optional one the provider could compute a default for; set an explicit `name` on every `anyscale_cloud_resource` block before upgrading, since a configuration that omits it now fails `terraform plan` instead of receiving a computed value.

### Added

- resource/anyscale_compute_config: idle_termination_minutes and maximum_uptime_minutes are now settable; previously they were only readable through the data source.
- resource/anyscale_compute_config: required_resources gains cpu_architecture for selecting x86_64 or arm64.
- data-source/anyscale_compute_config: gains zones, head_node, and worker_nodes for parity with the resource.

### Deprecated

- resource/anyscale_cloud_resource: `cloud_deployment_id` is deprecated in favor of `cloud_resource_id`; the backend no longer populates it. It will be removed in a future major version.
- resource/anyscale_cloud: `cloud_deployment_id` is deprecated in favor of `anyscale_cloud_resource`'s `cloud_resource_id`; the backend no longer populates it. It will be removed in a future major version.
- data-source/anyscale_cloud: `cloud_deployment_id` is deprecated in favor of `anyscale_cloud_resource`'s `cloud_resource_id`; the backend no longer populates it. It will be removed in a future major version.
- resource/anyscale_cloud_resource: `status` is deprecated in favor of `operator_status` (identical value, and always null for VM cloud resources); it will be removed in a future major version.

### Fixed

- resource/anyscale_compute_config: changing cloud_id or cloud_name to a different cloud is now rejected with a clear error at apply time instead of silently creating an orphaned, unmanaged duplicate compute config in the new cloud. To move a compute config to a different cloud, use terraform apply -replace or taint instead of editing cloud_id or cloud_name in place. Switching between cloud_id and cloud_name for the same cloud is unaffected.
- data-source/anyscale_compute_config: enable_cross_zone_scaling now correctly reflects the actual configured value instead of always reading as false.
- resource/anyscale_compute_config: a compute config archived outside Terraform is now correctly detected on the next refresh and removed from state instead of lingering forever.
- resource/anyscale_compute_config: importing a compute config now recovers its flags and advanced_instance_config values from the backend into state instead of leaving them null; a config that already matches plans cleanly, one that omits these values now shows a clear diff instead of silently dropping them on the next apply.
- resource/anyscale_cloud_resource: Fix `terraform apply` failing when adding a second cloud resource to a cloud that already has one; Create no longer renames the new resource to match an existing resource before calling the backend, which always rejected that duplicate name.
- resource/anyscale_cloud_resource: Fix spurious `Provider returned invalid result object after apply` errors (4 diagnostics) when Create fails; `is_default`, `operator_status`, `operator_version`, and `reported_at` are now given a concrete value before Create's defensive early state save instead of being left unknown.

## [0.1.2] - 2026-07-07

### Added

- data-source/anyscale_cloud: Add compute_stack, created_at, creator_id, is_default, is_aioa, is_bring_your_own_resource, is_private_cloud, and is_private_service_cloud, matching the anyscale_clouds data source.
- resource/anyscale_cloud: Add aws_config.cluster_instance_profile_id, file_storage.persistent_volume_claim, and file_storage.csi_ephemeral_volume_driver.
- resource/anyscale_cloud_resource: Add aws_config.cluster_instance_profile_id, file_storage.persistent_volume_claim, and file_storage.csi_ephemeral_volume_driver.

### Changed

- provider: GitHub release notes are now curated from CHANGELOG.md via per-PR changelog fragments instead of auto-generated from commit messages.

### Deprecated

- resource/anyscale_cloud: kubernetes_config's namespace, ingress_host, cluster_name, context, and kubeconfig_path are deprecated; none of them are sent to the Anyscale API and they have no effect. They will be removed in a future major version - remove them from your configuration.
- resource/anyscale_cloud_resource: kubernetes_config's namespace, ingress_host, cluster_name, context, and kubeconfig_path are deprecated; none of them are sent to the Anyscale API and they have no effect. They will be removed in a future major version - remove them from your configuration.

### Fixed

- data-source/anyscale_cloud: auto_add_user, enable_lineage_tracking, enable_log_ingestion, is_empty_cloud, and cloud_deployment_id now reflect the actual cloud instead of always reading as false/null.
- resource/anyscale_cloud: terraform import no longer forces replacement of a live cloud on the next plan; aws_config, gcp_config, or kubernetes_config plus object_storage are recovered automatically for the compute stack the cloud actually uses.
- resource/anyscale_cloud_resource: terraform import no longer forces replacement of a live cloud resource on the next plan; aws_config, gcp_config, or kubernetes_config plus object_storage are recovered automatically for the compute stack the resource actually uses.
- resource/anyscale_cloud_resource: Fix a read failure for Kubernetes cloud resources once the Anyscale Operator has reported status; also adds operator_status, operator_version, and reported_at as computed attributes.
- resource/anyscale_cloud: Warn when credentials could not be determined from the configured aws_config, gcp_config, or azure_config block and a placeholder was generated, instead of silently applying with a non-functional credential.
- resource/anyscale_cloud: Fix auto_add_user, enable_lineage_tracking, and enable_log_ingestion updates, which previously failed against the real API on every apply.
- resource/anyscale_cloud: name is now immutable after creation; changing it produces a clear plan-time error and never triggers a destructive replace.
- resource/anyscale_cloud: Fix Kubernetes clouds configured only with kubernetes_config (no aws_config or gcp_config) being silently misclassified as empty and never actually provisioned.
- resource/anyscale_cloud: Fail clearly at apply time when region cannot be determined for a Kubernetes cloud, instead of submitting an invalid empty region to the API.
- resource/anyscale_cloud: Fail clearly at plan time when compute_stack is set to a non-VM value on an empty cloud (the split pattern's parent, with no embedded resource configuration), since that combination can never be honored.
- resource/anyscale_cloud_resource: Fix the import ID error message and documentation, which incorrectly referred to the name attribute as resource_name.

## [0.1.1] - 2026-07-06

### 🎉 Major Framework Migration

**Migrated from Terraform Plugin SDK v2 to Terraform Plugin Framework** for improved developer experience and native HCL support.

### ✨ Added

#### Resources

- **`anyscale_cloud`**: Complete cloud infrastructure management
  - All-in-one pattern (embedded configuration)
  - Empty cloud pattern (split deployment)
  - Support for AWS, GCP, Azure providers
  - Support for VM and K8S compute stacks
  - Automatic cloud provider and region detection
  - Two-phase create with polling (create → add_resource → wait for ready)
  - Computed fields: `is_empty_cloud`, `cloud_deployment_id`

- **`anyscale_cloud_resource`**: Separate cloud resource deployment management
  - Split deployment pattern support
  - Custom import format: `cloud_id:resource_name`
  - Support for all provider configs (AWS, GCP, Azure, K8S)
  - Object storage and file storage configurations

- **`anyscale_compute_config`**: Cluster template management (migrated)
  - **Native HCL support** for `flags` and `advanced_configurations_json` (no more `jsonencode`!)
  - Same functionality as SDK v2 version with improved type safety

#### Features

- **Native HCL Syntax**: Top-level complex fields now support native Terraform syntax
  ```hcl
  # Before (SDK v2) - required jsonencode
  flags = jsonencode({
    "ray-cluster-ray-version" = "2.9.0"
  })

  # After (Framework) - native HCL!
  flags = {
    "ray-cluster-ray-version" = "2.9.0"
  }
  ```

- **Auto-Detection**: Automatic detection of:
  - Cloud provider from config blocks (aws_config → AWS, gcp_config → GCP)
  - Region from subnet zones or explicit configuration
  - Credentials from config blocks or generated placeholders

- **Flexible Deployment Patterns**:
  - **All-in-one**: Cloud + embedded config in single resource
  - **Split**: Empty cloud + separate `anyscale_cloud_resource`

- **Comprehensive Validation**:
  - Compute stack × provider compatibility validation
  - Required field validation based on deployment pattern
  - Automatic bucket prefix normalization (s3://, gs://)

#### Testing

- **Unit Tests**: 88 total tests
  - Cloud helper functions: 43 tests
  - Cloud resource expand helpers: 27 tests
  - Compute config helpers: 18 tests

- **Acceptance Tests**: 8 test scenarios
  - AWS VM basic (all-in-one)
  - AWS VM empty cloud
  - GCP VM basic
  - AWS K8S basic
  - Cloud resource AWS VM
  - Cloud resource GCP VM
  - Cloud resource K8S
  - Cloud resource with file storage

- **Integration Tests**: Full end-to-end AWS provisioning

### 🔄 Changed

#### Schema Updates

**anyscale_cloud** (previously SDK v2 version):
- Renamed fields (backward compatible in state):
  - `anyscale_iam_role_id` → `controlplane_iam_role_arn`
  - `instance_iam_role_id` → `dataplane_iam_role_arn`
  - `s3_bucket_id` → Moved to `object_storage.bucket_name`

- Restructured nested blocks:
  - `aws_config`: Now uses `subnet_ids_to_az` map instead of separate lists
  - Added `object_storage` and `file_storage` blocks
  - Added `kubernetes_config` for K8S deployments

- New computed fields:
  - `is_empty_cloud`: Boolean indicating if cloud has embedded config
  - `cloud_deployment_id`: Deployment ID (may be null for empty clouds)

**anyscale_compute_config**:
- `flags`: Now supports native HCL (previously required `jsonencode`)
- `advanced_configurations_json`: Now supports native HCL
- Type safety improved with stronger typing

#### Provider Behavior

- **Two-Phase Create**: All-in-one clouds now automatically:
  1. Create minimal cloud (POST /api/v2/clouds)
  2. Add resource deployment (PUT /api/v2/clouds/{id}/add_resource)
  3. Poll until state=ACTIVE and status=ready (up to 30 minutes)
  4. Read final state

- **Authentication**: Order remains the same (token → env var → credentials file)

- **API Client**: Enhanced logging with debug-level request/response tracking

### 🗑️ Removed

- **timeouts blocks**: No longer supported (SDK v2 specific)
  ```hcl
  # Remove this:
  timeouts {
    create = "30m"
    update = "10m"
    delete = "10m"
  }
  ```
  Framework uses internal timeout management instead.

### 🐛 Fixed

- **Compute Stack Apply Drift**: Fixed "Provider produced inconsistent result after apply" on `anyscale_cloud`
  - `compute_stack` is now `Optional` + `Computed` with `UseStateForUnknown`, matching the existing `cloud_provider`/`region` pattern
  - Configs that omit `compute_stack` now apply cleanly instead of erroring; the server-derived value stays stable across subsequent plans

- **Cloud Resource Region Apply Drift**: Fixed the same "inconsistent result after apply" pattern on `anyscale_cloud_resource`
  - `region` is now `Optional` + `Computed` with `UseStateForUnknown`

- **Compute Config Lookup by Name**: Fixed `anyscale_compute_config` data source lookups by name always failing with "unexpected status 422"
  - Removed an `archive_status` field from the search request that the API has never accepted
  - Affected any lookup by `name` (with or without `versions`); lookups by `id` were unaffected

- **Compute Config Apply Drift**: Fixed "Provider returned invalid result object after apply" on `anyscale_compute_config`
  - `head_node.resources` and `worker_nodes[].resources` were already correctly schema'd, but `Create` never populated them from the API response, leaving them unknown; `Create` now populates them like `Read` does
  - `cloud_resource` was incorrectly `Optional` + `Computed`, but the API never echoes it back when omitted; changed to `Optional`-only so it no longer waits on a value that never arrives

- **CloudDeploymentID State**: Fixed "unknown value after apply" error
  - CloudDeploymentID now properly initialized to known null value
  - Updated during add_resource if deployment succeeds

- **Schema Validation**: Fixed Required vs Optional in nested blocks
  - SingleNestedBlock attributes changed from Required to Optional
  - Block-level validation moved to runtime checks

- **API Request Validation**: Removed invalid fields from add_resource request
  - `auto_add_user`, `lineage_tracking_enabled`, `is_aggregated_logs_enabled` moved to cloud creation only

- **Bucket Prefix Normalization**: Automatic addition of s3:// and gs:// prefixes

### 📦 Dependencies

- Added: `github.com/hashicorp/terraform-plugin-framework` v1.14.0+
- Added: `github.com/hashicorp/terraform-plugin-testing` v1.14.0
- Removed: `github.com/hashicorp/terraform-plugin-sdk/v2` (fully migrated)

### 🔐 Security

- Credential handling improved with unique placeholder generation
- External IDs properly validated and generated
- Sensitive fields (credentials, tokens) marked appropriately in schema

### 📚 Documentation

- Complete README rewrite with framework migration guide
- Examples updated to remove SDK v2 syntax (timeouts blocks)
- Added examples for all deployment patterns:
  - AWS VM (all-in-one)
  - GCP VM
  - AWS EKS (K8S)
  - Empty cloud + cloud_resource (split pattern)

### ⚡ Performance

- Framework provides better plan performance with type-safe operations
- Reduced JSON marshaling overhead with native types
- Improved error messages with structured diagnostics

### 🔧 Breaking Changes

#### State Migration

**No state migration required!** The framework provider can read SDK v2 state. Existing resources continue working.

#### Configuration Changes

1. **Remove timeouts blocks**:
   ```hcl
   # OLD (SDK v2) - remove this
   resource "anyscale_cloud" "example" {
     # ...
     timeouts {
       create = "30m"
     }
   }

   # NEW (Framework) - no timeouts block
   resource "anyscale_cloud" "example" {
     # ...
   }
   ```

2. **Update native HCL syntax** (optional but recommended):
   ```hcl
   # OLD (SDK v2) - still works but deprecated
   flags = jsonencode({
     "ray-cluster-ray-version" = "2.9.0"
   })

   # NEW (Framework) - native HCL syntax
   flags = {
     "ray-cluster-ray-version" = "2.9.0"
   }
   ```

3. **Update field names for anyscale_cloud** (if using new version):
   ```hcl
   # OLD field names (SDK v2)
   aws_config {
     anyscale_iam_role_id = "arn:..."
     instance_iam_role_id = "arn:..."
     s3_bucket_id         = "my-bucket"
   }

   # NEW field names (Framework)
   aws_config {
     controlplane_iam_role_arn = "arn:..."
     dataplane_iam_role_arn    = "arn:..."
   }
   object_storage {
     bucket_name = "my-bucket"
   }
   ```

### 📊 Migration Path

1. **Update provider configuration** in `.terraformrc` (for local dev)
2. **Remove timeouts blocks** from all resources
3. **Optional**: Update to native HCL syntax for `flags` and `advanced_configurations_json`
4. **Run `terraform plan`** to verify no unexpected changes
5. **Test**: Run `terraform apply` on non-production first

### 🧪 Testing

To run the full test suite:

```bash
# Unit tests
make test

# Acceptance tests (requires AWS/GCP credentials)
TF_ACC=1 go test ./... -v

# Integration test
make test-aws-vm-basic
```

### 🎯 Highlights

**Native HCL is the star of this release!** No more `jsonencode()` for `flags` and `advanced_configurations_json`. This was the #1 requested feature and improves the developer experience significantly.

### Since the framework migration

- **Added**: `redis_endpoint` on `kubernetes_config` (`anyscale_cloud`, `anyscale_cloud_resource`) for Ray GCS fault tolerance on K8s clouds.
- **Added**: Terraform Registry publishing pipeline (GoReleaser config + `terraform-registry-manifest.json`) so tagged releases can be published to the Registry.
- **Removed**: `anyscale_global_resource_scheduler` resource and data sources are temporarily disabled (not registered with the provider) pending a backend API rework. Existing configurations referencing them will fail to plan until they're reinstated.
- **Fixed**: apply-time drift on several server-inferred attributes; hardened `CheckDestroy`/sweeper handling for container image resources.
- **Fixed**: a hardcoded 30-second HTTP client timeout caused `anyscale_cloud`'s `add_resource` call to fail on every real cloud creation (the exact path the quickstart examples exercise). The client no longer times out before the API responds.
- **Fixed**: `kubernetes_config` and `file_storage` attributes on both `anyscale_cloud` and `anyscale_cloud_resource` caused a perpetual plan diff that never converged (`Update` never called the API for these, and the attributes weren't marked to require replacement).
- **Fixed**: `anyscale_project.description` no longer forces the entire project to be destroyed and recreated when the server generates or changes it; it now updates in place.
- **Fixed**: `anyscale_cloud` now returns a clear error for `azure_config` instead of silently creating an unconfigured cloud.
- **Fixed**: "inconsistent result after apply" errors on `anyscale_compute_config` (worker group names, and CPU/GPU resource-key casing) and `anyscale_organization_collaborator` (`created_at` is now treated as write-once instead of being re-read on every apply).
- **Fixed**: pagination across the provider only ever returned the first 50 items — affected organization users, organization collaborators, project collaborators, and cloud resources. All list/lookup paths now paginate fully.
- **Changed**: `anyscale_organization_collaborator` now documents prominently that `terraform destroy` (including as part of tearing down a larger configuration) really does remove the user from the organization, not just from state — use `terraform state rm` if you only want Terraform to stop managing an existing collaborator.

---

## [0.0.1-dev] - Draft, never published (SDK v2 baseline)

### Added

- Initial release with Terraform Plugin SDK v2
- `anyscale_cloud` resource (AWS VM only)
- `anyscale_compute_config` resource
- Basic authentication support

### Notes

This version used Terraform Plugin SDK v2 and required `jsonencode()` for complex fields.

---

[Unreleased]: https://github.com/anyscale/terraform-provider-anyscale/compare/v0.8.0...HEAD
[0.8.0]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.8.0
[0.7.0]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.7.0
[0.6.0]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.6.0
[0.5.1]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.5.1
[0.5.0]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.5.0
[0.4.0]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.4.0
[0.3.4]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.3.4
[0.3.3]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.3.3
[0.3.2]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.3.2
[0.3.1]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.3.1
[0.3.0]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.3.0
[0.2.0]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.2.0
[0.1.2]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.1.2
[0.1.1]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.1.1
[0.0.1-dev]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.0.1-dev
