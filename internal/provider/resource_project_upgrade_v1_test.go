package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// upgradeProjectFixtureV1 runs v1 state through the real UpgradeState map and
// returns the decoded current model. Every v1 project's state carries an
// initial_cluster_config_id key, so without the v1 upgrader refreshing any of
// them against the current schema fails with `unsupported attribute
// "initial_cluster_config_id"` when the state is serialised, not at plan.
func upgradeProjectFixtureV1(t *testing.T, v1 projectResourceModelV1) ProjectResourceModel {
	t.Helper()
	ctx := context.Background()

	r := &ProjectResource{}
	upgrader, ok := r.UpgradeState(ctx)[1]
	if !ok {
		t.Fatal("UpgradeState() has no entry for schema version 1")
	}

	priorState := &tfsdk.State{
		Schema: *upgrader.PriorSchema,
		Raw:    tftypes.NewValue(upgrader.PriorSchema.Type().TerraformType(ctx), nil),
	}
	if diags := priorState.Set(ctx, &v1); diags.HasError() {
		t.Fatalf("failed to build v1 prior state fixture: %v", diags)
	}

	var current resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &current)
	if current.Schema.Version != 2 {
		t.Fatalf("current schema Version = %d, want 2", current.Schema.Version)
	}
	if _, present := current.Schema.Attributes["initial_cluster_config_id"]; present {
		t.Fatal("current schema still declares initial_cluster_config_id")
	}

	resp := &resource.UpgradeStateResponse{
		State: tfsdk.State{
			Schema: current.Schema,
			Raw:    tftypes.NewValue(current.Schema.Type().TerraformType(ctx), nil),
		},
	}
	upgrader.StateUpgrader(ctx, resource.UpgradeStateRequest{State: priorState}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("upgradeProjectStateV1() diagnostics: %v", resp.Diagnostics)
	}

	var upgraded ProjectResourceModel
	if diags := resp.State.Get(ctx, &upgraded); diags.HasError() {
		t.Fatalf("failed to decode upgraded state: %v", diags)
	}
	return upgraded
}

func projectV1Fixture(initialClusterConfigID types.String) projectResourceModelV1 {
	return projectResourceModelV1{
		ID:                     types.StringValue("prj_v1tov2"),
		CloudID:                types.StringValue("cld_v1tov2"),
		Name:                   types.StringValue("project-v1-to-v2"),
		Description:            types.StringValue("a v1 project"),
		InitialClusterConfigID: initialClusterConfigID,
		CreatorID:              types.StringValue("usr_creator_v1tov2"),
		CreatedAt:              types.StringValue("2026-01-01T00:00:00Z"),
		LastUsedCloudID:        types.StringValue("cld_v1tov2"),
		IsDefault:              types.BoolValue(true),
		DirectoryName:          types.StringValue("project-v1-to-v2-dir"),
	}
}

func assertProjectV1FieldsCarried(t *testing.T, got ProjectResourceModel) {
	t.Helper()
	for _, tc := range []struct{ name, got, want string }{
		{"ID", got.ID.ValueString(), "prj_v1tov2"},
		{"CloudID", got.CloudID.ValueString(), "cld_v1tov2"},
		{"Name", got.Name.ValueString(), "project-v1-to-v2"},
		{"Description", got.Description.ValueString(), "a v1 project"},
		{"CreatorID", got.CreatorID.ValueString(), "usr_creator_v1tov2"},
		{"CreatedAt", got.CreatedAt.ValueString(), "2026-01-01T00:00:00Z"},
		{"LastUsedCloudID", got.LastUsedCloudID.ValueString(), "cld_v1tov2"},
		{"DirectoryName", got.DirectoryName.ValueString(), "project-v1-to-v2-dir"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q (unchanged across the upgrade)", tc.name, tc.got, tc.want)
		}
	}
	if !got.IsDefault.ValueBool() {
		t.Error("IsDefault = false, want true (unchanged across the upgrade)")
	}
}

// Real v1 state: the API rejected every create that set the attribute, so
// the key is always null.
func TestProjectStateUpgradeV1toV2_DropsNullInitialClusterConfigID(t *testing.T) {
	assertProjectV1FieldsCarried(t, upgradeProjectFixtureV1(t, projectV1Fixture(types.StringNull())))
}

// Defensive: a non-null value cannot come from a real create, but it must
// still upgrade rather than fail.
func TestProjectStateUpgradeV1toV2_DropsNonNullInitialClusterConfigID(t *testing.T) {
	assertProjectV1FieldsCarried(t, upgradeProjectFixtureV1(t, projectV1Fixture(types.StringValue("ccfg_v1tov2"))))
}

func TestProjectStateUpgradeV1toV2_NilPriorState(t *testing.T) {
	resp := &resource.UpgradeStateResponse{}
	upgradeProjectStateV1(context.Background(), resource.UpgradeStateRequest{State: nil}, resp)
	if !diagsContainSummary(resp.Diagnostics, "Missing Prior State") {
		t.Errorf("expected a 'Missing Prior State' diagnostic, got: %v", resp.Diagnostics)
	}
}
