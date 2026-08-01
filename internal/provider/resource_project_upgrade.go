package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure ProjectResource satisfies the state-upgrade interface.
var _ resource.ResourceWithUpgradeState = &ProjectResource{}

// UpgradeState implements the v0 -> v1 migration for the v0.25.0 removal of
// anyscale_project's `collaborator` block (project access moved to a separate
// resource).
//
// This upgrader is NOT optional and NOT limited to people who actually used
// the block. Terraform persists a resource's full SCHEMA SHAPE in state, so
// every anyscale_project ever created under schema version 0 has a
// `collaborator` key in its state - an empty list for the (common) case where
// the block was never declared. Without an upgrader, refreshing any such state
// against the v1 schema fails with:
//
//	Failed to marshal state to json: unsupported attribute "collaborator"
//
// The failure mode is easy to miss because it is NOT a plan failure: the plan
// renders clean, and the error only surfaces afterwards when Terraform tries
// to serialise the resulting state. So "the plan looked fine" is not evidence
// this upgrader is unnecessary.
//
// The migration is deliberately narrow: drop `collaborator`, carry every other
// attribute through byte-for-byte. Nothing is fanned out into another resource
// type and nothing is defaulted or recomputed - the dropped value is simply
// not represented in the new schema, exactly like anyscale_cloud_resource's
// v1 -> v2 `status` removal (resource_cloud_resource_upgrade.go), which this
// mirrors.
func (r *ProjectResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {
			PriorSchema:   projectSchemaV0(),
			StateUpgrader: upgradeProjectStateV0toV1,
		},
	}
}

// projectResourceModelV0 is the v0 resource model: identical to
// ProjectResourceModel plus the `collaborator` block. Decoding straight into
// ProjectResourceModel instead would produce a hard "Object defines fields not
// found in struct" error, since v0's stored schema really does declare
// collaborator - the same constraint cloudResourceResourceModelV1 exists for.
//
// Collaborators is a plain types.List rather than a nested Go struct: the
// value is discarded, so nothing here needs to reach inside the objects, and
// keeping it generic avoids a second copy of the collaborator model that
// would outlive the schema it describes.
type projectResourceModelV0 struct {
	ID      types.String `tfsdk:"id"`
	CloudID types.String `tfsdk:"cloud_id"`
	// CloudName existed on this resource until it was removed in v0.24.0 WITHOUT
	// a schema-version bump. That means schema version 0 labels TWO incompatible
	// state shapes: pre-v0.24.0 state carries cloud_name, post-v0.24.0 state does
	// not. Declaring it here lets this upgrader decode BOTH - a state that lacks
	// the key simply decodes it as null. Omitting it would fail to decode every
	// project created before v0.24.0, which is the older and likely larger
	// population. The value is discarded, same as Collaborators.
	CloudName              types.String `tfsdk:"cloud_name"`
	Name                   types.String `tfsdk:"name"`
	Description            types.String `tfsdk:"description"`
	InitialClusterConfigID types.String `tfsdk:"initial_cluster_config_id"`
	Collaborators          types.List   `tfsdk:"collaborator"`
	CreatorID              types.String `tfsdk:"creator_id"`
	CreatedAt              types.String `tfsdk:"created_at"`
	LastUsedCloudID        types.String `tfsdk:"last_used_cloud_id"`
	IsDefault              types.Bool   `tfsdk:"is_default"`
	DirectoryName          types.String `tfsdk:"directory_name"`
}

// toProjectResourceModel carries every v0 field except Collaborators and
// CloudName into the current model - both were removed from the live schema. Written as an explicit field-by-field copy so that adding an
// attribute to ProjectResourceModel later fails to compile here rather than
// silently upgrading to a zero value.
func (m projectResourceModelV0) toProjectResourceModel() ProjectResourceModel {
	return ProjectResourceModel{
		ID:                     m.ID,
		CloudID:                m.CloudID,
		Name:                   m.Name,
		Description:            m.Description,
		InitialClusterConfigID: m.InitialClusterConfigID,
		CreatorID:              m.CreatorID,
		CreatedAt:              m.CreatedAt,
		LastUsedCloudID:        m.LastUsedCloudID,
		IsDefault:              m.IsDefault,
		DirectoryName:          m.DirectoryName,
	}
}

// projectSchemaV0 is a frozen copy of anyscale_project's schema exactly as it
// existed at schema version 0 - collaborator block included, with its
// validators, since that is what a real v0 state was actually validated
// against. It exists solely so UpgradeState can decode v0 state; do NOT evolve
// it alongside the live schema, and do not "fix" anything that looks stale in
// it. It is a historical snapshot.
func projectSchemaV0() *schema.Schema {
	return &schema.Schema{
		Version:             0,
		MarkdownDescription: "Manages an Anyscale Project. Projects organize workspaces and resources within a cloud.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"cloud_id": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			// See the CloudName note on projectResourceModelV0: version 0 covers
			// both the pre- and post-v0.24.0 shapes, because cloud_name was removed
			// without a version bump. Optional so state written either side of that
			// removal decodes.
			"cloud_name": schema.StringAttribute{
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.NoneOfCaseInsensitive("-", "default"),
				},
			},
			"description": schema.StringAttribute{
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIfConfigured(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"initial_cluster_config_id": schema.StringAttribute{
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"creator_id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"last_used_cloud_id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"is_default": schema.BoolAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"directory_name": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},

		Blocks: map[string]schema.Block{
			"collaborator": schema.ListNestedBlock{
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"email": schema.StringAttribute{
							Required: true,
						},
						"permission_level": schema.StringAttribute{
							Required: true,
							Validators: []validator.String{
								stringvalidator.OneOf("owner", "write", "readonly"),
							},
						},
						"identity_id": schema.StringAttribute{
							Computed: true,
						},
						"user_id": schema.StringAttribute{
							Computed: true,
						},
					},
				},
			},
		},
	}
}

// upgradeProjectStateV0toV1 drops collaborator and carries every other field
// through unchanged. There is nothing to migrate for the dropped value: the
// project's real collaborator grants live in the Anyscale backend and are
// untouched by this upgrade - only Terraform's record of them goes away, since
// this resource no longer manages them.
func upgradeProjectStateV0toV1(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	if req.State == nil {
		resp.Diagnostics.AddError(
			"Missing Prior State",
			"State upgrade from version 0 requires prior state data, but none was provided. This is a bug in the provider; please report it.",
		)
		return
	}

	var priorState projectResourceModelV0
	resp.Diagnostics.Append(req.State.Get(ctx, &priorState)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState := priorState.toProjectResourceModel()

	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}
