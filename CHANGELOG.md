# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/anyscale/terraform-provider-anyscale/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.1.1
[0.0.1-dev]: https://github.com/anyscale/terraform-provider-anyscale/releases/tag/v0.0.1-dev
