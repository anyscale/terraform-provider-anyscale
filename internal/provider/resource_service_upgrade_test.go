package provider

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// TestServiceResourceStateUpgradeV0toV1_DropsRolloutTimeout mirrors this
// repo's established precedent for "prove a removed attribute auto-migrates
// cleanly" (TestCloudResourceStateUpgradeV1toV2_DropsEnableSystemCluster,
// TestCloudResourceResourceStateUpgradeV1toV2_DropsStatus,
// TestSystemClusterResourceStateUpgradeV0toV1_DropsStartTimeout).
// PR2-TIMEOUTS-PLAN.md calls this out explicitly as forge's own
// responsibility to verify with a real state-upgrade test, not assume
// auto-migrate.
//
// THE reproducing shape (deliberate, per assayer's PR2-TEST-PLAN.md): prior
// state carries rollout_timeout = "90m" - a REAL customization that is
// neither the OLD default (45m) nor the NEW default (30m), so the assertion
// below can only pass if the value was genuinely dropped, not by
// coincidentally matching either default.
func TestServiceResourceStateUpgradeV0toV1_DropsRolloutTimeout(t *testing.T) {
	ctx := context.Background()

	v0 := serviceResourceModelV0{
		Name:            types.StringValue("svc-v0v1"),
		RayServeConfig:  types.DynamicNull(),
		BuildID:         types.StringValue("bld_v0v1"),
		ComputeConfigID: types.StringValue("cpt_v0v1"),
		Description:     types.StringNull(),
		ProjectID:       types.StringValue("prj_v0v1"),
		Tags:            types.MapValueMust(types.StringType, map[string]attr.Value{}),
		RolloutStrategy: types.StringValue("ROLLOUT"),
		MaxSurgePercent: types.Int64Null(),
		ConnectionIDs:   types.ListNull(types.StringType),
		RolloutTimeout:  types.StringValue("90m"),

		ID:                       types.StringValue("svc_v0v1"),
		CloudID:                  types.StringValue("cld_v0v1"),
		Hostname:                 types.StringValue("v0v1.example.com"),
		BaseURL:                  types.StringValue("https://v0v1.example.com"),
		CurrentState:             types.StringValue("RUNNING"),
		GoalState:                types.StringValue("RUNNING"),
		CreatorID:                types.StringValue("user_v0v1"),
		CreatedAt:                types.StringValue("2026-07-22T00:00:00Z"),
		EndedAt:                  types.StringNull(),
		IsMultiVersion:           types.BoolValue(false),
		AutoRolloutEnabled:       types.BoolValue(true),
		ErrorMessage:             types.StringNull(),
		ServiceObservabilityURLs: types.ObjectNull(serviceObservabilityURLsAttrTypes),
		PrimaryVersion:           types.ObjectNull(serviceVersionAttrTypes),
		CanaryVersion:            types.ObjectNull(serviceVersionAttrTypes),
		ServiceStatusChecklist:   types.ObjectNull(serviceStatusChecklistAttrTypes),
	}

	r := &ServiceResource{}
	upgraders := r.UpgradeState(ctx)
	upgrader, ok := upgraders[0]
	if !ok {
		t.Fatalf("UpgradeState() has no entry for schema version 0 - the v0->v1 upgrader dropping rollout_timeout is missing")
	}

	v0Schema := serviceResourceSchemaV0()
	priorState := &tfsdk.State{
		Schema: *v0Schema,
		Raw:    tftypes.NewValue(v0Schema.Type().TerraformType(ctx), nil),
	}
	if diags := priorState.Set(ctx, &v0); diags.HasError() {
		t.Fatalf("failed to build v0 prior state fixture: %v", diags)
	}

	var v1SchemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &v1SchemaResp)
	if v1SchemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build v1 (current) schema: %v", v1SchemaResp.Diagnostics)
	}
	if v1SchemaResp.Schema.Version != 1 {
		t.Fatalf("current schema Version = %d, want 1 (bumped for the timeouts{} migration)", v1SchemaResp.Schema.Version)
	}
	if _, present := v1SchemaResp.Schema.Attributes["rollout_timeout"]; present {
		t.Fatal("current schema still declares rollout_timeout - it should be fully removed")
	}
	if _, present := v1SchemaResp.Schema.Blocks["timeouts"]; !present {
		t.Fatal("current schema is missing the timeouts block")
	}

	req := resource.UpgradeStateRequest{State: priorState}
	resp := &resource.UpgradeStateResponse{
		State: tfsdk.State{
			Schema: v1SchemaResp.Schema,
			Raw:    tftypes.NewValue(v1SchemaResp.Schema.Type().TerraformType(ctx), nil),
		},
	}
	upgrader.StateUpgrader(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("upgradeServiceResourceStateV0toV1() diagnostics: %v", resp.Diagnostics)
	}

	var v1 ServiceResourceModel
	if diags := resp.State.Get(ctx, &v1); diags.HasError() {
		t.Fatalf("failed to decode upgraded v1 state: %v", diags)
	}

	if v1.Name.ValueString() != "svc-v0v1" {
		t.Errorf("Name = %v, want unchanged", v1.Name.ValueString())
	}
	if v1.CurrentState.ValueString() != "RUNNING" {
		t.Errorf("CurrentState = %v, want unchanged", v1.CurrentState.ValueString())
	}
	if v1.Hostname.ValueString() != "v0v1.example.com" {
		t.Errorf("Hostname = %v, want unchanged", v1.Hostname.ValueString())
	}

	// THE load-bearing assertion: the customized 90m value has no successor
	// field and must come through null, not "90m" leaking into some other
	// field and not an error.
	if !v1.Timeouts.IsNull() {
		t.Errorf("Timeouts = %v, want null after upgrade (rollout_timeout=90m has no successor value, by design)", v1.Timeouts)
	}

	// Confirm the null value resolves to the NEW default (30m), not the old
	// 45m default and not the dropped 90m customization - proves the whole
	// "drops customization, adopts new default" chain end to end, not just
	// the drop half.
	resolved, diags := v1.Timeouts.Update(ctx, defaultServiceRolloutTimeout)
	if diags.HasError() {
		t.Fatalf("Timeouts.Update() diagnostics: %v", diags)
	}
	if resolved != 30*time.Minute {
		t.Errorf("Timeouts.Update(ctx, defaultServiceRolloutTimeout) = %v, want 30m", resolved)
	}
}
