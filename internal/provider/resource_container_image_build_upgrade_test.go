package provider

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// TestContainerImageBuildResourceStateUpgradeV0toV1_DropsBuildTimeout mirrors
// this repo's established precedent for "prove a removed attribute
// auto-migrates cleanly" (TestSystemClusterResourceStateUpgradeV0toV1_DropsStartTimeout,
// TestServiceResourceStateUpgradeV0toV1_DropsRolloutTimeout).
// PR2-TIMEOUTS-PLAN.md calls this out explicitly as forge's own
// responsibility to verify with a real state-upgrade test.
//
// THE reproducing shape: prior state carries build_timeout = "90m" - a REAL
// customization that is neither the old default (30m) nor happens to equal
// any other meaningful value, so the drop is actually exercised (Option A,
// confirmed by the user 2026-07-22: reset to the new default, do not
// preserve customizations).
func TestContainerImageBuildResourceStateUpgradeV0toV1_DropsBuildTimeout(t *testing.T) {
	ctx := context.Background()

	v0 := containerImageBuildResourceModelV0{
		ID:                types.StringValue("cib_v0v1"),
		Name:              types.StringValue("build-v0v1"),
		Containerfile:     types.StringValue("FROM anyscale/ray:latest"),
		ContainerfilePath: types.StringNull(),
		ProjectID:         types.StringValue("prj_v0v1"),
		BuildTimeout:      types.StringValue("90m"),
		BuildID:           types.StringValue("bld_v0v1"),
		BuildStatus:       types.StringValue("succeeded"),
		ImageURI:          types.StringValue("example.com/img:v0v1"),
		RayVersion:        types.StringValue("2.9.0"),
		Revision:          types.Int64Value(3),
		Digest:            types.StringValue("sha256:abcdef"),
		NameVersion:       types.StringValue("build-v0v1:3"),
		CreatedAt:         types.StringValue("2026-07-22T00:00:00Z"),
	}

	r := &ContainerImageBuildResource{}
	upgraders := r.UpgradeState(ctx)
	upgrader, ok := upgraders[0]
	if !ok {
		t.Fatalf("UpgradeState() has no entry for schema version 0 - the v0->v1 upgrader dropping build_timeout is missing")
	}

	v0Schema := containerImageBuildResourceSchemaV0()
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
	if _, present := v1SchemaResp.Schema.Attributes["build_timeout"]; present {
		t.Fatal("current schema still declares build_timeout - it should be fully removed")
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
		t.Fatalf("upgradeContainerImageBuildResourceStateV0toV1() diagnostics: %v", resp.Diagnostics)
	}

	var v1 ContainerImageBuildResourceModel
	if diags := resp.State.Get(ctx, &v1); diags.HasError() {
		t.Fatalf("failed to decode upgraded v1 state: %v", diags)
	}

	if v1.Name.ValueString() != "build-v0v1" {
		t.Errorf("Name = %v, want unchanged", v1.Name.ValueString())
	}
	if v1.BuildID.ValueString() != "bld_v0v1" {
		t.Errorf("BuildID = %v, want unchanged", v1.BuildID.ValueString())
	}
	if v1.Digest.ValueString() != "sha256:abcdef" {
		t.Errorf("Digest = %v, want unchanged", v1.Digest.ValueString())
	}
	if v1.Revision.ValueInt64() != 3 {
		t.Errorf("Revision = %v, want unchanged (3)", v1.Revision.ValueInt64())
	}

	// THE load-bearing assertion: the customized 90m value has no successor
	// field and must come through null.
	if !v1.Timeouts.IsNull() {
		t.Errorf("Timeouts = %v, want null after upgrade (build_timeout=90m has no successor value, by design)", v1.Timeouts)
	}

	// Confirm the null value resolves to the NEW default (30m), not the
	// dropped 90m customization.
	resolved, diags := v1.Timeouts.Create(ctx, defaultBuildTimeout)
	if diags.HasError() {
		t.Fatalf("Timeouts.Create() diagnostics: %v", diags)
	}
	if resolved != 30*time.Minute {
		t.Errorf("Timeouts.Create(ctx, defaultBuildTimeout) = %v, want 30m", resolved)
	}
}
