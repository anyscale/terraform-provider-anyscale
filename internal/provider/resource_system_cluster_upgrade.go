package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// UpgradeState is PR2's timeouts{} migration for anyscale_system_cluster -
// this resource's first-ever schema version bump. v0 (start_timeout as a
// flat, always-materialized Optional+Computed+Default string) drops that
// attribute and adopts the new (null-unless-set) timeouts{ create } block.
func (r *SystemClusterResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {
			PriorSchema:   systemClusterResourceSchemaV0(),
			StateUpgrader: upgradeSystemClusterResourceStateV0toV1,
		},
	}
}

// systemClusterResourceModelV0 mirrors SystemClusterResourceModel exactly,
// but with the flat start_timeout string in place of the timeouts.Value
// block field - the only shape difference between v0 and the current (v1)
// schema.
type systemClusterResourceModelV0 struct {
	ID                 types.String `tfsdk:"id"`
	CloudID            types.String `tfsdk:"cloud_id"`
	ClusterID          types.String `tfsdk:"cluster_id"`
	State              types.String `tfsdk:"state"`
	IsEnabled          types.Bool   `tfsdk:"is_enabled"`
	WorkloadServiceURL types.String `tfsdk:"workload_service_url"`
	StartTimeout       types.String `tfsdk:"start_timeout"`
}

// systemClusterResourceSchemaV0 is a frozen copy of anyscale_system_cluster's
// schema exactly as shipped through v0.19.0 (pre-PR2) - see
// cloudResourceSchemaV0's doc comment (resource_cloud_upgrade.go) for why
// flags don't need to match historical values exactly but names/types/
// structure must, and why this must not evolve alongside the live schema.
func systemClusterResourceSchemaV0() *schema.Schema {
	return &schema.Schema{
		Version: 0,
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
			"cluster_id": schema.StringAttribute{
				Computed: true,
			},
			"state": schema.StringAttribute{
				Computed: true,
			},
			"is_enabled": schema.BoolAttribute{
				Computed: true,
			},
			"workload_service_url": schema.StringAttribute{
				Computed: true,
			},
			"start_timeout": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("20m"),
			},
		},
	}
}

// upgradeSystemClusterResourceStateV0toV1 drops start_timeout and leaves the
// new timeouts block null - a null timeouts{} resolves to the default
// (defaultSystemClusterCreateTimeout) on the next apply via
// plan.Timeouts.Create(ctx, default), exactly matching what an omitted
// start_timeout used to resolve to via its Default. No information is lost:
// a user who had customized start_timeout away from "20m" would already see
// that as a real, non-null value here - see the state-upgrade test for the
// customized-value case, not just the default one.
func upgradeSystemClusterResourceStateV0toV1(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	if req.State == nil {
		resp.Diagnostics.AddError(
			"Missing Prior State",
			"State upgrade from version 0 requires prior state data, but none was provided. This is a bug in the provider; please report it.",
		)
		return
	}

	var priorState systemClusterResourceModelV0
	resp.Diagnostics.Append(req.State.Get(ctx, &priorState)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState := SystemClusterResourceModel{
		ID:                 priorState.ID,
		CloudID:            priorState.CloudID,
		ClusterID:          priorState.ClusterID,
		State:              priorState.State,
		IsEnabled:          priorState.IsEnabled,
		WorkloadServiceURL: priorState.WorkloadServiceURL,
		Timeouts:           timeouts.Value{Object: types.ObjectNull(map[string]attr.Type{"create": types.StringType})},
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}
