package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
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
// (is_empty_cloud, cloud_deployment_id) and plural-only (is_k8s) fields have no
// counterpart to share against.
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
// on both sides rather than being unified via a rename. image_uri/ray_version/is_byod/digest
// (singular-only) and is_archived (plural-only) have no counterpart to share against.
func containerImageSharedAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"creator_id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The ID of the user who created this container image.",
		},
		"created_at": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Timestamp when the container image was created.",
		},
		"revision": schema.Int64Attribute{
			Computed:            true,
			MarkdownDescription: "The revision number of the latest build.",
		},
		"name_version": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The name and revision formatted as `name:revision` for use with Anyscale APIs.",
		},
	}
}
