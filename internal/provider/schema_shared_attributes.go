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
// (is_empty_cloud, cloud_deployment_id) fields have no counterpart to share against.
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
			MarkdownDescription: "Whether this is a private cloud.",
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
			MarkdownDescription: "The name of the user.",
		},
		"permission_level": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The organization permission level (owner, collaborator, etc.).",
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
			MarkdownDescription: "Whether this is the default project for the organization.",
		},
		"directory_name": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The directory name used for this project's storage.",
		},
	}
}
