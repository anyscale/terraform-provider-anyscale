package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure ContainerImageRegistryResource satisfies the state-upgrade interface.
var _ resource.ResourceWithUpgradeState = &ContainerImageRegistryResource{}

// UpgradeState implements the v0 -> v1 migration for F3 + V1(c): id held the build ID under
// v0, but a registry's build can be superseded by a new latest build without the resource
// itself being replaced, so the build ID cannot serve as stable identity (see the identity
// comment on ContainerImageRegistryResourceModel.ID). v1 re-keys id to the cluster
// environment id instead - already present under v0 as the separate cluster_environment_id
// field, just not the one id pointed at, so no API call is needed to migrate it.
// cluster_environment_id itself is then dropped rather than carried forward as its own v1
// field (V1(c): id alone is the durable handle, no redundant attribute) - its value survives
// only via id, which the transform below sets from it. F5 (digest) landed in this same
// version bump; it is new in v1 and has nothing to migrate.
func (r *ContainerImageRegistryResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {
			PriorSchema:   containerImageRegistrySchemaV0(),
			StateUpgrader: upgradeContainerImageRegistryStateV0toV1,
		},
	}
}

// containerImageRegistryResourceModelV0 is the v0 (pre-F3/F5/V1(c)) resource model. It
// differs from the current (v1) model in two ways: there is no digest field (added by F5),
// and there is a separate cluster_environment_id field that v1 does not carry forward
// (removed by V1(c); id alone is the durable handle in v1). id's Go type is unchanged
// (types.String), only its meaning differs, so it does not need a distinct field name here.
type containerImageRegistryResourceModelV0 struct {
	ID                   types.String `tfsdk:"id"`
	Name                 types.String `tfsdk:"name"`
	ImageURI             types.String `tfsdk:"image_uri"`
	RayVersion           types.String `tfsdk:"ray_version"`
	RegistryLoginSecret  types.String `tfsdk:"registry_login_secret"`
	BuildID              types.String `tfsdk:"build_id"`
	ClusterEnvironmentID types.String `tfsdk:"cluster_environment_id"`
	BuildStatus          types.String `tfsdk:"build_status"`
	CreatedAt            types.String `tfsdk:"created_at"`
	IsBYOD               types.Bool   `tfsdk:"is_byod"`
	Revision             types.Int64  `tfsdk:"revision"`
	NameVersion          types.String `tfsdk:"name_version"`
}

// containerImageRegistrySchemaV0 is a frozen copy of the schema as shipped before F3 (id
// identity change), F5 (digest), and V1(c) (cluster_environment_id removal) landed: id held
// the build ID instead of the cluster environment id, there is no digest attribute, and
// cluster_environment_id is present as its own attribute (dropped from v1 by V1(c)).
// Descriptions and plan modifiers are deliberately omitted - PriorSchema is only ever used
// to decode raw state into containerImageRegistryResourceModelV0, never to plan against -
// matching computeConfigSchemaV0's precedent. Do not evolve this going forward; it is a
// historical snapshot, not a second copy of the live schema.
func containerImageRegistrySchemaV0() *schema.Schema {
	return &schema.Schema{
		Version: 0,
		Attributes: map[string]schema.Attribute{
			"id":                     schema.StringAttribute{Computed: true},
			"name":                   schema.StringAttribute{Optional: true},
			"image_uri":              schema.StringAttribute{Required: true},
			"ray_version":            schema.StringAttribute{Optional: true, Computed: true},
			"registry_login_secret":  schema.StringAttribute{Optional: true, Sensitive: true},
			"build_id":               schema.StringAttribute{Computed: true},
			"cluster_environment_id": schema.StringAttribute{Computed: true},
			"build_status":           schema.StringAttribute{Computed: true},
			"created_at":             schema.StringAttribute{Computed: true},
			"is_byod":                schema.BoolAttribute{Computed: true},
			"revision":               schema.Int64Attribute{Computed: true},
			"name_version":           schema.StringAttribute{Computed: true},
		},
	}
}

// upgradeContainerImageRegistryStateV0toV1 re-keys id from the old build-ID identity to the
// value previously held in cluster_environment_id (V1(c): that field itself is not carried
// into v1 - id alone is the durable handle) and fills digest with null (F5: new in v1,
// nothing to migrate; the next Read backfills it from the API, since digest is plain
// Computed with no fill-guard, unlike ray_version).
func upgradeContainerImageRegistryStateV0toV1(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	if req.State == nil {
		resp.Diagnostics.AddError(
			"Missing Prior State",
			"State upgrade from version 0 requires prior state data, but none was provided. This is a bug in the provider; please report it.",
		)
		return
	}

	var priorState containerImageRegistryResourceModelV0
	resp.Diagnostics.Append(req.State.Get(ctx, &priorState)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Every v0 state that reached this point completed Create()'s call 1 (that is where
	// cluster_environment_id's value originates), so it should never be empty in practice.
	// Guard anyway rather than silently writing a blank identity into v1 state: a resource
	// with id = "" would fail unpredictably on its next Read/Delete instead of surfacing a
	// clear, actionable error now.
	if priorState.ClusterEnvironmentID.IsNull() || priorState.ClusterEnvironmentID.ValueString() == "" {
		resp.Diagnostics.AddError(
			"Missing Cluster Environment ID During State Upgrade",
			"State upgrade from version 0 to version 1 requires a non-empty cluster_environment_id in the prior state, but none was found. "+
				"This is a bug in the provider; please report it. As a workaround, remove this resource from state "+
				"(terraform state rm) and re-import it by its cluster environment id (terraform import).",
		)
		return
	}

	newState := ContainerImageRegistryResourceModel{
		ID:                  priorState.ClusterEnvironmentID,
		Name:                priorState.Name,
		ImageURI:            priorState.ImageURI,
		RayVersion:          priorState.RayVersion,
		RegistryLoginSecret: priorState.RegistryLoginSecret,
		BuildID:             priorState.BuildID,
		BuildStatus:         priorState.BuildStatus,
		CreatedAt:           priorState.CreatedAt,
		IsBYOD:              priorState.IsBYOD,
		Revision:            priorState.Revision,
		Digest:              types.StringNull(), // F5: new in v1, nothing to migrate
		NameVersion:         priorState.NameVersion,
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}
