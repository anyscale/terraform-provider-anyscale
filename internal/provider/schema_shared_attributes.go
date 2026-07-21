package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// cloudSharedAttributes returns the anyscale_cloud / anyscale_clouds attributes that are
// identical in name, type, and MarkdownDescription on both sides. Called directly by the
// singular data source and wrapped inside the plural's per-item NestedObject.
//
// Deliberately excluded, per CLOUD-SYNC-DESIGN.md C7: id/name (singular carries the
// either-id-or-name selector clause and Optional; plural's are Computed-only output),
// enable_lineage_tracking/lineage_tracking_enabled and enable_log_ingestion/
// is_aggregated_logs_enabled (same backend field, different already-shipped attribute
// name on each side - unifying would require a breaking rename). Singular-only
// (is_empty_cloud) fields have no counterpart to share against.
// is_k8s is identical text on both sides (DS-CLOUD-4) but stays defined directly on each
// DS's own Schema function rather than hoisted here, matching how the plural already had it.
func cloudSharedAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"cloud_provider": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The cloud provider (AWS, GCP, AZURE, or GENERIC).",
		},
		"region": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The region where the cloud is deployed.",
		},
		"status": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The operational status of the cloud (e.g., ready, pending, failed).",
		},
		"state": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The lifecycle state of the cloud (e.g., ACTIVE, CREATING, FAILED).",
		},
		"compute_stack": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The compute stack (VM or K8S).",
		},
		"created_at": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Timestamp when the cloud was created.",
		},
		"creator_id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The ID of the user who created the cloud.",
		},
		"is_default": schema.BoolAttribute{
			Computed:            true,
			MarkdownDescription: "Whether this is the default cloud for the organization.",
		},
		"is_aioa": schema.BoolAttribute{
			Computed:            true,
			MarkdownDescription: "Whether this is an AIOA (Anyscale In Your Own Account) cloud.",
		},
		"is_bring_your_own_resource": schema.BoolAttribute{
			Computed:            true,
			MarkdownDescription: "Whether this cloud allows bringing your own resources.",
		},
		"is_private_cloud": schema.BoolAttribute{
			Computed:            true,
			MarkdownDescription: "Whether this cloud is registered as private (see the `anyscale_cloud` resource's `is_private_cloud` for the full explanation). A self-asserted value with no backend verification of actual VPN/PrivateLink connectivity - not a guarantee that private connectivity is actually configured or reachable.",
		},
		"is_private_service_cloud": schema.BoolAttribute{
			Computed:            true,
			MarkdownDescription: "Whether this is a private service cloud.",
		},
		"auto_add_user": schema.BoolAttribute{
			Computed:            true,
			MarkdownDescription: "Whether users are automatically added to this cloud.",
		},
		// DS-CLOUD-5 (Phase B): cheap additive parity fields, present on the backend Cloud
		// model and stable.
		"availability_zones": schema.ListAttribute{
			ElementType:         types.StringType,
			Computed:            true,
			MarkdownDescription: "The availability zones considered for this cloud.",
		},
		"version": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The cluster management stack version of the cloud (`v1` or `v2`).",
		},
		"external_id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The external ID associated with this cloud, used for cross-account trust relationships. Null if not set.",
		},
	}
}

// containerImageSharedAttributes returns the anyscale_container_image / anyscale_container_images
// attributes that are identical in name, type, and MarkdownDescription on both sides. Called
// directly by the singular data source and wrapped inside the plural's per-item NestedObject.
//
// Deliberately excluded: id/name (same structural reason as cloudSharedAttributes - singular
// carries the either-id-or-name selector clause and Optional; plural's are Computed-only
// output). build_id/latest_build_id differ in both name and wording (pre-existing, not a
// confirmed drift). build_status/latest_build_status are identical text under different
// already-shipped names - same class as the cloud pair's excluded fields, so it stays local
// on both sides rather than being unified via a rename. ray_version/is_byod/digest
// (singular-only, since they still depend on the second per-build GET) and is_archived
// (plural-only) have no counterpart to share against. image_uri (DS-IMG-2) moved into this
// shared map since it is now identical text on both sides, no longer singular-only.
func containerImageSharedAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"creator_id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The ID of the user who created this container image. Null if the API does not report a creator for this image.",
		},
		"created_at": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Timestamp when the container image was created.",
		},
		"revision": schema.Int64Attribute{
			Computed:            true,
			MarkdownDescription: "The revision number of the latest build. Null if no build has been triggered yet, or if the build's details couldn't be retrieved.",
		},
		"name_version": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The name and revision formatted as `name:revision` for use with Anyscale APIs. Null if no build has been triggered yet, or if the build's details couldn't be retrieved.",
		},
		"image_uri": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The registry image URI (docker image path) of the container image's latest build. Null if the image has no build yet, or if the latest build hasn't produced an image yet (pending, in progress, or failed).",
		},
		// DS-IMG-4 (Phase B): template-level fields, present on both the get-by-id
		// and list responses.
		"cloud_id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The cloud ID this container image is associated with. Null if the image isn't associated with a specific cloud.",
		},
		"is_default": schema.BoolAttribute{
			Computed:            true,
			MarkdownDescription: "Whether this is an Anyscale-provided base container image, as opposed to one created by a user in this organization.",
		},
		"is_experimental": schema.BoolAttribute{
			Computed:            true,
			MarkdownDescription: "Whether this is an experimental container image.",
		},
		"last_modified_at": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Timestamp when the container image was last modified.",
		},
	}
}

// globalResourceSchedulerSharedAttributes returns the anyscale_global_resource_scheduler /
// anyscale_global_resource_schedulers attributes that are identical in name, type, and
// MarkdownDescription on both sides. Called directly by the singular data source and wrapped
// inside the plural's per-item NestedObject.
//
// Deliberately excluded: name (singular's is Required - the sole lookup key - while the
// plural's per-item name is Computed-only output; matching text, divergent optionality, same
// structural-divergence class as the cloud/container_image id/name exclusions). spec (the
// whole nested machine-type/partition tree) is singular-only - the plural's own description
// says "without detailed spec for performance". Schema-only: this file must never gain
// machine-pool/GRS request or read logic - the GRSv2 deferral applies to provider behavior,
// not to deduplicating already-identical schema text.
func globalResourceSchedulerSharedAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The unique identifier of the global resource scheduler.",
		},
		"organization_id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The organization ID that owns the global resource scheduler.",
		},
		"enable_rootless_dataplane_config": schema.BoolAttribute{
			Computed:            true,
			MarkdownDescription: "Whether rootless dataplane configuration is enabled.",
		},
		"cloud_ids": schema.ListAttribute{
			Computed:            true,
			ElementType:         types.StringType,
			MarkdownDescription: "List of cloud IDs attached to this global resource scheduler.",
		},
	}
}

// organizationUserSharedAttributes returns the anyscale_organization_user /
// anyscale_organization_users attributes that are identical in name, type, and
// MarkdownDescription on both sides. Called directly by the singular data source and wrapped
// inside the plural's per-item NestedObject.
//
// Deliberately excluded: id/user_id/email (singular carries the either-id-or-user_id-or-email
// selector clause and Optional; plural's per-item versions are Computed-only output with
// shorter text - same structural reason as the cloud/container_image id/name exclusions).
func organizationUserSharedAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"name": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The name of the user. Null if the user has no name set.",
		},
		"permission_level": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The organization permission level (`owner` or `collaborator`), deprecated in favor of `base_role` plus `additional_roles`; prefer those for new configurations.",
		},
		// DS-OU-2 (Phase B): permission_level above is deprecated backend-side in
		// favor of these two.
		"base_role": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The user's base role in the organization (`owner` or `collaborator`). `permission_level` is deprecated in favor of this attribute plus `additional_roles`; prefer these for new configurations.",
		},
		"additional_roles": schema.ListAttribute{
			ElementType:         types.StringType,
			Computed:            true,
			MarkdownDescription: "Additional restriction (deny) roles applied on top of the user's base role (for example `image_reader`, which restricts container-image creation a plain collaborator could otherwise do), if any - never an alternative permission level, and never additional capability beyond the base role. Three states: populated means the user genuinely has one or more additional roles; empty means the backend was queried and reports none (including in an organization where the underlying roles-read feature is off - there, the concept is simply inactive); null means the provider could not query it at all, which only happens for a user with no `user_id`. Guard against null in your configuration before calling `length()` or iterating over this value - for example `length(coalesce(additional_roles, []))` rather than `length(additional_roles)` directly, which errors on a null list.",
		},
		"created_at": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The timestamp when the user was added to the organization.",
		},
	}
}

// projectSharedAttributes returns the anyscale_project / anyscale_projects attributes that are
// identical in name, type, and MarkdownDescription on both sides. Called directly by the
// singular data source and wrapped inside the plural's per-item NestedObject.
//
// Deliberately excluded: id/name (singular carries the either-id-or-name selector clause and
// Optional; plural's per-item versions are Computed-only output). cloud_id (singular is
// dual-purpose - Optional filter-or-selector AND Computed, with an extra sentence; plural's
// per-item is Computed-only with shorter text - structurally different, not just textually).
// cloud_name (singular is an Optional selector-side filter; the plural's cloud_name is a
// top-level filter input with no per-item output counterpart at all - no shared attribute to
// factor). collaborators (nested list) is singular-only; the plural's own description notes it
// omits collaborator details for performance.
func projectSharedAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"description": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Description of the project.",
		},
		"creator_id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The ID of the user who created the project.",
		},
		"created_at": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Timestamp when the project was created.",
		},
		"last_used_cloud_id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The ID of the cloud last used by this project.",
		},
		"is_default": schema.BoolAttribute{
			Computed:            true,
			MarkdownDescription: "Whether this is the default project for its cloud. Anyscale creates one default project per cloud, not one per organization.",
		},
		"directory_name": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The directory name used for this project's storage.",
		},
	}
}

// serviceStatusChecklistItemAttributes returns the attribute map for one row of a service's
// per-component status checklist (used for both the `shared` list and each `per_version` group's
// `items` list within service_status_checklist). Returns a fresh map on every call, since the two
// call sites below each embed it in a different NestedAttributeObject.
func serviceStatusChecklistItemAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"kind": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The kind of resource this checklist row represents (e.g. `SERVICE`, `SERVICE_VERSION`, `LOAD_BALANCER`). Ships as a plain string with no client-side enum validation, matching this provider's convention of not hand-maintaining a copy of the backend's enum list.",
		},
		"label": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "User-facing label for this resource (e.g. `Cluster`, `Load Balancer`).",
		},
		"state": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The state of this resource (e.g. `RUNNING`, `UNHEALTHY`, `STARTING`).",
		},
		"message": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Reconciler-provided message describing the current state. Empty string, not null, when the backend has no message to report.",
		},
		"version_id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The service version this item belongs to. Null for items shared across versions (e.g. cloud networking).",
		},
		"observed_at": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "When this row's state was last observed by the cluster manager. Null when the snapshot is missing a timestamp (older event).",
		},
	}
}

// serviceVersionAttributes returns the attribute map shared by primary_version and canary_version
// on both anyscale_service and anyscale_services. Returns a fresh map on every call - each of the
// (up to 4) call sites across the singular and plural schemas needs its own independent map value,
// since terraform-plugin-framework attribute maps must not be shared/mutated across attributes.
//
// Deliberately excluded: ray_gcs_external_storage_config and tracing_config (niche/advanced
// per-version configs, documented gap, follow up on demand).
func serviceVersionAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The unique identifier of this service version.",
		},
		"created_at": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Timestamp when this version was created.",
		},
		"version": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The version string identifier for this version.",
		},
		"current_state": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The current state of this service version (e.g. `RUNNING`, `STARTING`, `UNHEALTHY`).",
		},
		"weight": schema.Int64Attribute{
			Computed:            true,
			MarkdownDescription: "The configured traffic weight currently stored for this version, 0-100. During rollouts this may be an intermediate desired load-balancer weight rather than the live figure - see `current_weight`.",
		},
		"current_weight": schema.Int64Attribute{
			Computed:            true,
			MarkdownDescription: "The current percentage of live traffic observed for this version, 0-100. Null if not currently observed.",
		},
		"target_weight": schema.Int64Attribute{
			Computed:            true,
			MarkdownDescription: "The intended final traffic weight for this version, 0-100, when known. Null otherwise.",
		},
		"build_id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The ID of the cluster environment build (container image) this version runs.",
		},
		"compute_config_id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The ID of the compute configuration this version uses.",
		},
		"production_job_ids": schema.ListAttribute{
			ElementType:         types.StringType,
			Computed:            true,
			MarkdownDescription: "The production job IDs associated with this service version. Empty (not null) if none.",
		},
		"connection_ids": schema.ListAttribute{
			ElementType:         types.StringType,
			Computed:            true,
			MarkdownDescription: "The connection IDs associated with this service version. Null if the API does not report connections for this version.",
		},
		"ray_serve_config": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The Ray Serve config for this version, as a JSON string. This is a dynamic, open-ended structure upstream with no fixed schema in this provider - use `jsondecode()` in HCL to access individual fields, the same convention `anyscale_compute_config`'s `advanced_instance_config`/`flags` use for similarly open-ended config. Always present (required upstream), even when its contents are trivial.",
		},
	}
}

// serviceSharedAttributes returns the anyscale_service / anyscale_services attributes that are
// identical in name, type, and MarkdownDescription on both sides. Called directly by the singular
// data source and wrapped inside the plural's per-item NestedObject. See
// .crystl/quest/CONTRACT_anyscale_service.md for the full field-scope contract this implements.
//
// Deliberately excluded from this shared map, defined separately per-DS instead: id/name (singular
// carries the either-id-or-name selector clause and Optional; plural's per-item versions are
// Computed-only), project_id/cloud_id (singular is dual-purpose - Optional narrowing filter for
// the by-name lookup AND Computed, same structural reason as projectSharedAttributes' cloud_id
// exclusion above; plural's top-level versions are Optional-only filters with no per-item
// counterpart in this map, while the plural's per-item project_id/cloud_id are plain Computed
// output defined alongside the rest of that NestedObject).
//
// Deliberately excluded from the schema entirely (documented gaps, not silent drops): auth_token
// (a live bearer credential - a data source's output is always written to Terraform state in
// plaintext regardless of any Sensitive marking, so this is omitted rather than
// included-and-marked-Sensitive), versions (deprecated upstream in favor of primary_version/
// canary_version below), type (list-response-only field; redundant discriminator since this
// provider's backend only ever deals in V2 services), and a nested creator object (flat creator_id
// below is kept instead, consistent with this provider's existing choice to defer a nested creator
// object on anyscale_cloud for the same reason).
func serviceSharedAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"description": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Description of the service. Null if not set.",
		},
		"creator_id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The ID of the user who created the service.",
		},
		"created_at": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Timestamp when the service was created.",
		},
		"ended_at": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Timestamp when the service was terminated. Null while the service is active.",
		},
		"hostname": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The hostname of the service.",
		},
		"base_url": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The base URL of this service.",
		},
		"current_state": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The current state of this service (e.g. `RUNNING`, `UNHEALTHY`, `TERMINATED`). Ships as a plain string with no client-side enum validation, matching this provider's convention of not hand-maintaining a copy of the backend's enum list.",
		},
		"goal_state": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The goal state of this service (`RUNNING` or `TERMINATED`).",
		},
		"auto_rollout_enabled": schema.BoolAttribute{
			Computed:            true,
			MarkdownDescription: "Whether this service uses automatic rollout.",
		},
		"is_multi_version": schema.BoolAttribute{
			Computed:            true,
			MarkdownDescription: "Whether this service is a multi-version service (multiple active versions with no single canary).",
		},
		"error_message": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Error message from processing the most recent API request against this service, if any. Null otherwise.",
		},
		"service_observability_urls": schema.SingleNestedAttribute{
			Computed: true,
			MarkdownDescription: "Dashboard URLs for this service. The whole block is null while a service is still being processed (a confirmed real transitional state, e.g. a not-yet-healthy service that has not finished its first reconcile). " +
				"Once present, each individual URL is separately null if the backend has none to report for it (e.g. before the service's first successful deploy).",
			Attributes: map[string]schema.Attribute{
				"service_dashboard_url": schema.StringAttribute{
					Computed:            true,
					MarkdownDescription: "URL to a dashboard with graphs about the entire service.",
				},
				"service_dashboard_embedding_url": schema.StringAttribute{
					Computed:            true,
					MarkdownDescription: "Embeddable variant of `service_dashboard_url`.",
				},
				"serve_deployment_dashboard_url": schema.StringAttribute{
					Computed:            true,
					MarkdownDescription: "URL to a dashboard with graphs about a single deployment or replica of the service.",
				},
				"serve_deployment_dashboard_embedding_url": schema.StringAttribute{
					Computed:            true,
					MarkdownDescription: "Embeddable variant of `serve_deployment_dashboard_url`.",
				},
			},
		},
		"primary_version": schema.SingleNestedAttribute{
			Computed:            true,
			MarkdownDescription: "The primary version of this service. If the service is terminated, this refers to the most recently active version. Can be null if the backend has not returned one yet.",
			Attributes:          serviceVersionAttributes(),
		},
		"canary_version": schema.SingleNestedAttribute{
			Computed:            true,
			MarkdownDescription: "The canary version of this service. Null unless the service is currently rolling out.",
			Attributes:          serviceVersionAttributes(),
		},
		"service_status_checklist": schema.SingleNestedAttribute{
			Computed:            true,
			MarkdownDescription: "Per-component status breakdown derived from the most recent reconciler snapshot. Null for terminated services and during the brief window before the reconciler's first tick on a brand-new service.",
			Attributes: map[string]schema.Attribute{
				"shared": schema.ListNestedAttribute{
					Computed:            true,
					MarkdownDescription: "Components shared across all versions (load balancer, listener rule, DNS, TLS certificate). Empty (not null) if none.",
					NestedObject: schema.NestedAttributeObject{
						Attributes: serviceStatusChecklistItemAttributes(),
					},
				},
				"per_version": schema.ListNestedAttribute{
					Computed:            true,
					MarkdownDescription: "Per-version components (cluster, application, target group), one entry per active service version. Empty (not null) if none.",
					NestedObject: schema.NestedAttributeObject{
						Attributes: map[string]schema.Attribute{
							"version_id": schema.StringAttribute{
								Computed:            true,
								MarkdownDescription: "The service version these checklist items belong to.",
							},
							"items": schema.ListNestedAttribute{
								Computed:            true,
								MarkdownDescription: "Per-component statuses for this version. Empty (not null) if none.",
								NestedObject: schema.NestedAttributeObject{
									Attributes: serviceStatusChecklistItemAttributes(),
								},
							},
						},
					},
				},
			},
		},
	}
}
