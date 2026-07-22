package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// UpgradeState is PR2's timeouts{} migration for anyscale_container_image_build -
// this resource's first-ever schema version bump. v0 (build_timeout as a
// flat, always-materialized Optional+Computed+Default string) drops that
// attribute and adopts the new (null-unless-set) timeouts{ create, update }
// block, one value governing both ops exactly as build_timeout did.
func (r *ContainerImageBuildResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {
			PriorSchema:   containerImageBuildResourceSchemaV0(),
			StateUpgrader: upgradeContainerImageBuildResourceStateV0toV1,
		},
	}
}

// containerImageBuildResourceModelV0 mirrors ContainerImageBuildResourceModel
// exactly, but with the flat build_timeout string in place of the
// timeouts.Value block field - the only shape difference between v0 and the
// current (v1) schema.
type containerImageBuildResourceModelV0 struct {
	ID types.String `tfsdk:"id"`

	Name              types.String `tfsdk:"name"`
	Containerfile     types.String `tfsdk:"containerfile"`
	ContainerfilePath types.String `tfsdk:"containerfile_path"`
	ProjectID         types.String `tfsdk:"project_id"`
	BuildTimeout      types.String `tfsdk:"build_timeout"`

	BuildID     types.String `tfsdk:"build_id"`
	BuildStatus types.String `tfsdk:"build_status"`
	ImageURI    types.String `tfsdk:"image_uri"`
	RayVersion  types.String `tfsdk:"ray_version"`
	Revision    types.Int64  `tfsdk:"revision"`
	Digest      types.String `tfsdk:"digest"`
	NameVersion types.String `tfsdk:"name_version"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

// containerImageBuildResourceSchemaV0 is a frozen copy of
// anyscale_container_image_build's schema exactly as shipped through
// v0.19.0 (pre-PR2) - see cloudResourceSchemaV0's doc comment
// (resource_cloud_upgrade.go) for why flags don't need to match historical
// values exactly but names/types/structure must, and why this must not
// evolve alongside the live schema.
func containerImageBuildResourceSchemaV0() *schema.Schema {
	return &schema.Schema{
		Version: 0,
		MarkdownDescription: `Builds a container image from a Containerfile (Dockerfile). Use this resource to create custom container images for Anyscale workloads.

~> **Note:** When this resource is destroyed, it archives the underlying cluster environment. However, the Anyscale API does not currently support permanent deletion of container images. Archived images can be viewed by setting ` + "`include_archived = true`" + ` on the ` + "`anyscale_container_images`" + ` data source.`,

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique identifier of the cluster environment.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			// User-provided attributes
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The name for the container image (cluster environment). Changing this replaces the resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"containerfile": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "The content of the Containerfile (Dockerfile) to build. Mutually exclusive with `containerfile_path`. Updating this value triggers a new build revision.",
				Validators: []validator.String{
					stringvalidator.ExactlyOneOf(
						path.MatchRoot("containerfile"),
						path.MatchRoot("containerfile_path"),
					),
				},
			},
			"containerfile_path": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Path to the Containerfile (Dockerfile) to build. Mutually exclusive with `containerfile`. Updating this value triggers a new build revision.",
			},
			"project_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "The ID of the project to associate this container image with. Changing this replaces the resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"build_timeout": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("30m"),
				MarkdownDescription: "Maximum time to wait for the build to complete (e.g., `30m`, `1h`). Defaults to `30m`.",
			},

			// Computed attributes - these change when containerfile is updated
			"build_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The unique identifier of the build. Changes when a new build is created.",
			},
			"build_status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The current status of the build (`pending`, `in_progress`, `succeeded`, `failed`, `pending_cancellation`, `canceled`).",
			},
			"image_uri": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The URI of the built container image.",
			},
			"ray_version": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The Ray version used in the build.",
			},
			"revision": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "The revision number of the container image build. Increments with each new build.",
			},
			"digest": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The content digest of the built container image (e.g. `sha256:...`). May occasionally be briefly empty immediately after creation, or after an update that triggers a new build, if the build is still settling.",
			},
			"name_version": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The name and revision formatted as `name:revision` for use with Anyscale APIs.",
			},
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Timestamp when the build was created. Changes when a new build is created.",
			},
		},
	}
}

// upgradeContainerImageBuildResourceStateV0toV1 drops build_timeout and
// leaves the new timeouts block null - a null timeouts{} resolves to
// defaultBuildTimeout on the next apply via
// plan.Timeouts.Create/Update(ctx, default), exactly matching what an
// omitted build_timeout used to resolve to via its Default. No information
// is lost: a user who had customized build_timeout away from "30m" would
// already see that as a real, non-null value here - see the state-upgrade
// test for the customized-value case, not just the default one.
func upgradeContainerImageBuildResourceStateV0toV1(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	if req.State == nil {
		resp.Diagnostics.AddError(
			"Missing Prior State",
			"State upgrade from version 0 requires prior state data, but none was provided. This is a bug in the provider; please report it.",
		)
		return
	}

	var priorState containerImageBuildResourceModelV0
	resp.Diagnostics.Append(req.State.Get(ctx, &priorState)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState := ContainerImageBuildResourceModel{
		ID:                priorState.ID,
		Name:              priorState.Name,
		Containerfile:     priorState.Containerfile,
		ContainerfilePath: priorState.ContainerfilePath,
		ProjectID:         priorState.ProjectID,
		Timeouts:          timeouts.Value{Object: types.ObjectNull(map[string]attr.Type{"create": types.StringType, "update": types.StringType})},
		BuildID:           priorState.BuildID,
		BuildStatus:       priorState.BuildStatus,
		ImageURI:          priorState.ImageURI,
		RayVersion:        priorState.RayVersion,
		Revision:          priorState.Revision,
		Digest:            priorState.Digest,
		NameVersion:       priorState.NameVersion,
		CreatedAt:         priorState.CreatedAt,
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}
