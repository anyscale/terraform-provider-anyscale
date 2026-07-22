package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// TestSystemClusterResourceStateUpgradeV0toV1_DropsStartTimeout mirrors this
// repo's own established precedent for "prove a removed attribute
// auto-migrates cleanly" (TestCloudResourceStateUpgradeV1toV2_DropsEnableSystemCluster,
// TestCloudResourceResourceStateUpgradeV1toV2_DropsStatus). PR2-TIMEOUTS-PLAN.md
// calls this out explicitly as forge's own responsibility to verify with a
// real state-upgrade test, not assume auto-migrate - this is that proof, for
// system_cluster's first-ever schema version bump.
func TestSystemClusterResourceStateUpgradeV0toV1_DropsStartTimeout(t *testing.T) {
	ctx := context.Background()

	// THE reproducing shape: start_timeout carries a REAL, non-default value
	// in prior state (a user who customized it away from "20m"), not left at
	// the zero-value/default - a fixture that only ever saw the default
	// would go green trivially and prove nothing about the drop itself, nor
	// about a customized value being visible (not silently discarded) here.
	v0 := systemClusterResourceModelV0{
		ID:                 types.StringValue("cld_scv0v1"),
		CloudID:            types.StringValue("cld_scv0v1"),
		ClusterID:          types.StringValue("cluster-scv0v1"),
		State:              types.StringValue("Running"),
		IsEnabled:          types.BoolValue(true),
		WorkloadServiceURL: types.StringValue("https://scv0v1.example.com"),
		StartTimeout:       types.StringValue("45m"),
	}

	r := &SystemClusterResource{}
	upgraders := r.UpgradeState(ctx)
	upgrader, ok := upgraders[0]
	if !ok {
		t.Fatalf("UpgradeState() has no entry for schema version 0 - the v0->v1 upgrader dropping start_timeout is missing")
	}

	v0Schema := systemClusterResourceSchemaV0()
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
	if _, present := v1SchemaResp.Schema.Attributes["start_timeout"]; present {
		t.Fatal("current schema still declares start_timeout - it should be fully removed")
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
		t.Fatalf("upgradeSystemClusterResourceStateV0toV1() diagnostics: %v", resp.Diagnostics)
	}

	// THE load-bearing assertion: this must decode cleanly into the CURRENT
	// model (which has no StartTimeout field at all, only Timeouts) with
	// zero errors.
	var v1 SystemClusterResourceModel
	if diags := resp.State.Get(ctx, &v1); diags.HasError() {
		t.Fatalf("failed to decode upgraded v1 state: %v", diags)
	}

	if v1.ClusterID.ValueString() != "cluster-scv0v1" {
		t.Errorf("ClusterID = %v, want unchanged", v1.ClusterID.ValueString())
	}
	if v1.State.ValueString() != "Running" {
		t.Errorf("State = %v, want unchanged", v1.State.ValueString())
	}
	if !v1.IsEnabled.ValueBool() {
		t.Errorf("IsEnabled = %v, want unchanged (true)", v1.IsEnabled.ValueBool())
	}
	if v1.WorkloadServiceURL.ValueString() != "https://scv0v1.example.com" {
		t.Errorf("WorkloadServiceURL = %v, want unchanged", v1.WorkloadServiceURL.ValueString())
	}

	// The new timeouts block must come through null (not an error, not a
	// stray "45m" leaking into some other field) - a customized start_timeout
	// has no home in the new shape and is intentionally not recoverable
	// (locked design: hard removal, one release, no deprecation cycle). The
	// resulting null resolves to defaultSystemClusterCreateTimeout on next
	// apply via Timeouts.Create(ctx, default), same as a currently-omitted
	// start_timeout resolves to its Default today.
	if !v1.Timeouts.IsNull() {
		t.Errorf("Timeouts = %v, want null after upgrade (start_timeout has no successor value, by design)", v1.Timeouts)
	}
}
