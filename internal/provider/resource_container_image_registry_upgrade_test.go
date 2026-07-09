package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// This file is F3's state-upgrade regression: id held the build ID under v0, but a
// registry's build can be superseded by a new latest build without the resource itself
// being replaced, so v1 re-keys id to cluster_environment_id instead (see the identity
// comment on ContainerImageRegistryResourceModel.ID). F5 (digest) landed in the same
// version bump; it has nothing to migrate and must come back null.
//
// Like resource_compute_config_upgrade_test.go's TestComputeConfigStateUpgradeV0toV1, this
// drives the real exported UpgradeState() map and upgradeContainerImageRegistryStateV0toV1
// through the actual tfsdk.State encode/decode path, not a hand-rolled reimplementation.

// upgradeV0RegistryFixture upgrades a manufactured v0 state through the real tfsdk.State
// encode/decode path and returns the resulting v1 model. Diagnostics from the upgrader
// itself are returned rather than failing the test directly, so the defensive-error test
// case below can assert on them; diagnostics from fixture setup are still t.Fatalf'd since
// those indicate a broken test, not a case under test.
func upgradeV0RegistryFixture(t *testing.T, v0Model containerImageRegistryResourceModelV0) (ContainerImageRegistryResourceModel, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()

	r := &ContainerImageRegistryResource{}
	upgraders := r.UpgradeState(ctx)
	upgrader, ok := upgraders[0]
	if !ok {
		t.Fatalf("UpgradeState() has no entry for schema version 0")
	}

	v0Schema := containerImageRegistrySchemaV0()
	priorState := &tfsdk.State{
		Schema: *v0Schema,
		Raw:    tftypes.NewValue(v0Schema.Type().TerraformType(ctx), nil),
	}
	diags := priorState.Set(ctx, &v0Model)
	if diags.HasError() {
		t.Fatalf("failed to build v0 prior state fixture: %v", diags)
	}

	// resp.State must start initialized against the CURRENT (v1) schema, the same way the
	// real framework runtime primes it before calling the upgrader.
	var v1SchemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &v1SchemaResp)
	if v1SchemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build v1 schema: %v", v1SchemaResp.Diagnostics)
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
		return ContainerImageRegistryResourceModel{}, resp.Diagnostics
	}

	var v1Model ContainerImageRegistryResourceModel
	diags = resp.State.Get(ctx, &v1Model)
	if diags.HasError() {
		t.Fatalf("failed to decode upgraded v1 state: %v", diags)
	}
	return v1Model, nil
}

// minimalV0RegistryModel returns a fully-populated v0 model so each test case only has to
// override the field(s) it actually cares about.
func minimalV0RegistryModel(t *testing.T) containerImageRegistryResourceModelV0 {
	t.Helper()
	return containerImageRegistryResourceModelV0{
		ID:                   types.StringValue("bld_old_build_id"),
		Name:                 types.StringValue("my-image"),
		ImageURI:             types.StringValue("anyscale/ray:2.9.0-py310"),
		RayVersion:           types.StringValue("2.9.0"),
		RegistryLoginSecret:  types.StringNull(),
		BuildID:              types.StringValue("bld_old_build_id"),
		ClusterEnvironmentID: types.StringValue("apptemp_abc123"),
		BuildStatus:          types.StringValue("succeeded"),
		CreatedAt:            types.StringValue("2024-01-01T00:00:00Z"),
		IsBYOD:               types.BoolValue(true),
		Revision:             types.Int64Value(1),
		NameVersion:          types.StringValue("my-image:1"),
	}
}

func TestContainerImageRegistryStateUpgradeV0toV1(t *testing.T) {
	t.Run("id is re-keyed from the old build id to cluster_environment_id", func(t *testing.T) {
		v0Model := minimalV0RegistryModel(t)
		v1Model, diags := upgradeV0RegistryFixture(t, v0Model)
		if diags.HasError() {
			t.Fatalf("unexpected upgrade diagnostics: %v", diags)
		}

		if v1Model.ID.ValueString() != "apptemp_abc123" {
			t.Errorf("ID = %v, want apptemp_abc123 (cluster_environment_id, not the old build id)", v1Model.ID.ValueString())
		}
		if v1Model.ID.ValueString() == v0Model.BuildID.ValueString() {
			t.Error("ID must not still equal the old build id after upgrade")
		}
		if v1Model.BuildID.ValueString() != "bld_old_build_id" {
			t.Errorf("BuildID = %v, want bld_old_build_id (unchanged - only id's meaning moves, build_id itself is untouched)", v1Model.BuildID.ValueString())
		}
	})

	t.Run("digest is null after upgrade (F5: new in v1, nothing to migrate)", func(t *testing.T) {
		v0Model := minimalV0RegistryModel(t)
		v1Model, diags := upgradeV0RegistryFixture(t, v0Model)
		if diags.HasError() {
			t.Fatalf("unexpected upgrade diagnostics: %v", diags)
		}

		if !v1Model.Digest.IsNull() {
			t.Errorf("Digest = %v, want null (v0 never had a digest attribute; the next Read backfills it)", v1Model.Digest)
		}
	})

	t.Run("unrelated fields pass through unchanged", func(t *testing.T) {
		v0Model := minimalV0RegistryModel(t)
		v1Model, diags := upgradeV0RegistryFixture(t, v0Model)
		if diags.HasError() {
			t.Fatalf("unexpected upgrade diagnostics: %v", diags)
		}

		if v1Model.Name.ValueString() != "my-image" {
			t.Errorf("Name = %v, want my-image", v1Model.Name.ValueString())
		}
		if v1Model.ImageURI.ValueString() != v0Model.ImageURI.ValueString() {
			t.Errorf("ImageURI = %v, want %v", v1Model.ImageURI.ValueString(), v0Model.ImageURI.ValueString())
		}
		if v1Model.RayVersion.ValueString() != "2.9.0" {
			t.Errorf("RayVersion = %v, want 2.9.0", v1Model.RayVersion.ValueString())
		}
		if v1Model.BuildStatus.ValueString() != v0Model.BuildStatus.ValueString() {
			t.Errorf("BuildStatus = %v, want %v", v1Model.BuildStatus.ValueString(), v0Model.BuildStatus.ValueString())
		}
		if v1Model.Revision.ValueInt64() != 1 {
			t.Errorf("Revision = %v, want 1", v1Model.Revision.ValueInt64())
		}
		if !v1Model.IsBYOD.ValueBool() {
			t.Error("IsBYOD = false, want true (must pass through unchanged)")
		}
		if v1Model.NameVersion.ValueString() != v0Model.NameVersion.ValueString() {
			t.Errorf("NameVersion = %v, want %v", v1Model.NameVersion.ValueString(), v0Model.NameVersion.ValueString())
		}
	})

	t.Run("empty cluster_environment_id in prior state produces a defensive error rather than a blank id", func(t *testing.T) {
		v0Model := minimalV0RegistryModel(t)
		v0Model.ClusterEnvironmentID = types.StringNull()
		_, diags := upgradeV0RegistryFixture(t, v0Model)

		if !diags.HasError() {
			t.Fatal("expected an error for empty cluster_environment_id, got none")
		}
	})
}
