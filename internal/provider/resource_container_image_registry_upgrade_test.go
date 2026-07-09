package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// v1SchemaForUpgradeTest returns the resource's real, live v1 schema by
// calling Schema() directly, rather than hand-rolling a second copy here that
// could silently drift from the one Terraform actually plans against.
func v1SchemaForUpgradeTest(t *testing.T) resource.SchemaResponse {
	t.Helper()
	r := &ContainerImageRegistryResource{}
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build v1 schema: %v", schemaResp.Diagnostics)
	}
	return schemaResp
}

// buildV0State constructs a real tfsdk.State against the given prior schema,
// mirroring exactly how the framework's own UpgradeResourceState dispatch
// populates UpgradeStateRequest.State before calling a StateUpgrader (see
// server_upgraderesourcestate.go in terraform-plugin-framework: req.State =
// &tfsdk.State{Raw: <unmarshaled>, Schema: *resourceStateUpgrader.PriorSchema}).
// A test that skips this and hand-builds a model some other way would not be
// proving anything about the real upgrade path. priorSchema should come from
// registryStateUpgraderV0, not a second direct call to
// containerImageRegistrySchemaV0 -- otherwise a test could pass even if
// UpgradeState's map entry pointed PriorSchema at a different schema than the
// one this file assumes.
func buildV0State(t *testing.T, priorSchema *schema.Schema, model containerImageRegistryResourceModelV0) *tfsdk.State {
	t.Helper()
	v0State := tfsdk.State{Schema: *priorSchema}
	diags := v0State.Set(context.Background(), &model)
	if diags.HasError() {
		t.Fatalf("failed to build v0 state fixture: %v", diags)
	}
	return &v0State
}

// registryStateUpgraderV0 looks the schema-version-0 entry up through the
// resource's real, exported UpgradeState() map instead of calling
// upgradeContainerImageRegistryStateV0toV1 directly. Terraform itself
// dispatches by looking up this map at the prior state's stored schema
// version, so a test that bypasses the map cannot catch a wiring bug (e.g.
// the entry keyed at 1 instead of 0) even if the upgrade function's own logic
// is otherwise correct -- the migration would simply never fire for a real
// v0 state, and every test in this file would still pass.
func registryStateUpgraderV0(t *testing.T) resource.StateUpgrader {
	t.Helper()
	r := &ContainerImageRegistryResource{}
	upgraders := r.UpgradeState(context.Background())
	upgrader, ok := upgraders[0]
	if !ok {
		t.Fatal("UpgradeState() has no entry for schema version 0 -- migration would never fire for a v0 state")
	}
	return upgrader
}

// TestUpgradeContainerImageRegistryStateV0toV1_ReKeysIDToClusterEnvironmentID
// is the headline GATE-F3 proof: a v0 state whose id held the (now-stale)
// build id must come out of the upgrade with id re-keyed to
// cluster_environment_id, matching what Create() has set id to ever since F3
// (see the identity comment on ContainerImageRegistryResourceModel.ID) -- and
// the next plan against that state must be empty, i.e. nothing else in the
// model may be dropped or altered by the migration.
func TestUpgradeContainerImageRegistryStateV0toV1_ReKeysIDToClusterEnvironmentID(t *testing.T) {
	const staleBuildID = "bld_old123"
	const stableClusterEnvID = "apptemp_stable456"

	v0Model := containerImageRegistryResourceModelV0{
		ID:                   types.StringValue(staleBuildID), // v0's identity: the build id
		Name:                 types.StringValue("my-image"),
		ImageURI:             types.StringValue("docker.io/example/my-image:v1"),
		RayVersion:           types.StringValue("2.9.0"),
		RegistryLoginSecret:  types.StringNull(),
		BuildID:              types.StringValue(staleBuildID),
		ClusterEnvironmentID: types.StringValue(stableClusterEnvID),
		BuildStatus:          types.StringValue("succeeded"),
		CreatedAt:            types.StringValue("2024-01-01T00:00:00Z"),
		IsBYOD:               types.BoolValue(true),
		Revision:             types.Int64Value(3),
		NameVersion:          types.StringValue("my-image:3"),
	}

	upgrader := registryStateUpgraderV0(t)
	req := resource.UpgradeStateRequest{State: buildV0State(t, upgrader.PriorSchema, v0Model)}
	resp := &resource.UpgradeStateResponse{State: tfsdk.State{Schema: v1SchemaForUpgradeTest(t).Schema}}

	upgrader.StateUpgrader(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("upgrade reported an unexpected error: %v", resp.Diagnostics)
	}

	var newModel ContainerImageRegistryResourceModel
	getDiags := resp.State.Get(context.Background(), &newModel)
	if getDiags.HasError() {
		t.Fatalf("failed to decode upgraded state: %v", getDiags)
	}

	if newModel.ID.ValueString() != stableClusterEnvID {
		t.Errorf("newModel.ID = %q, want %q (cluster_environment_id) -- id must be re-keyed off the stable identity, not left on the stale build id %q",
			newModel.ID.ValueString(), stableClusterEnvID, staleBuildID)
	}
	if newModel.ClusterEnvironmentID.ValueString() != stableClusterEnvID {
		t.Errorf("newModel.ClusterEnvironmentID = %q, want %q", newModel.ClusterEnvironmentID.ValueString(), stableClusterEnvID)
	}
	// build_id must survive the migration unchanged -- it is still tracked,
	// just no longer doubles as the resource's identity.
	if newModel.BuildID.ValueString() != staleBuildID {
		t.Errorf("newModel.BuildID = %q, want %q -- build_id must be preserved, not dropped or overwritten", newModel.BuildID.ValueString(), staleBuildID)
	}
	// F5's digest is new in v1; a v0 row has nothing to migrate it from.
	if !newModel.Digest.IsNull() {
		t.Errorf("newModel.Digest = %q, want null -- v0 had no digest to migrate; the next Read should backfill it", newModel.Digest.ValueString())
	}
	if newModel.Name.ValueString() != "my-image" {
		t.Errorf("newModel.Name = %q, want %q -- unrelated fields must pass through the migration untouched", newModel.Name.ValueString(), "my-image")
	}
	if newModel.ImageURI.ValueString() != "docker.io/example/my-image:v1" {
		t.Errorf("newModel.ImageURI = %q, unexpected", newModel.ImageURI.ValueString())
	}
	if newModel.RayVersion.ValueString() != "2.9.0" {
		t.Errorf("newModel.RayVersion = %q, unexpected", newModel.RayVersion.ValueString())
	}
	if newModel.BuildStatus.ValueString() != "succeeded" {
		t.Errorf("newModel.BuildStatus = %q, unexpected", newModel.BuildStatus.ValueString())
	}
	if newModel.CreatedAt.ValueString() != "2024-01-01T00:00:00Z" {
		t.Errorf("newModel.CreatedAt = %q, unexpected", newModel.CreatedAt.ValueString())
	}
	if !newModel.IsBYOD.ValueBool() {
		t.Errorf("newModel.IsBYOD = %v, want true", newModel.IsBYOD.ValueBool())
	}
	if newModel.Revision.ValueInt64() != 3 {
		t.Errorf("newModel.Revision = %d, want 3", newModel.Revision.ValueInt64())
	}
	if newModel.NameVersion.ValueString() != "my-image:3" {
		t.Errorf("newModel.NameVersion = %q, unexpected", newModel.NameVersion.ValueString())
	}

	// The upgraded state must also be a legal instance of the v1 schema a
	// plan would be built against -- Set() into the real v1-schema state
	// above already exercises this, but assert Raw was actually populated
	// (not left as the zero value) as a direct check that resp.State.Set was
	// really called and did not silently no-op.
	if resp.State.Raw.Type() == nil {
		t.Fatal("resp.State.Raw was never populated -- upgrade did not actually write state")
	}
}

// TestUpgradeContainerImageRegistryStateV0toV1_MissingClusterEnvironmentID_ReturnsError
// covers the other branch named in the task: a v0 row somehow missing
// cluster_environment_id must surface the real defensive AddError diagnostic
// (not a silent "keep the prior id" fallback -- there is no such fallback in
// the real code, and a test asserting one would be proving a contract the
// provider does not have), and must leave resp.State unwritten so Terraform
// does not persist a half-migrated resource.
func TestUpgradeContainerImageRegistryStateV0toV1_MissingClusterEnvironmentID_ReturnsError(t *testing.T) {
	cases := []struct {
		name                 string
		clusterEnvironmentID types.String
	}{
		{name: "null", clusterEnvironmentID: types.StringNull()},
		{name: "empty string", clusterEnvironmentID: types.StringValue("")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v0Model := containerImageRegistryResourceModelV0{
				ID:                   types.StringValue("bld_orphaned789"),
				Name:                 types.StringValue("my-image"),
				ImageURI:             types.StringValue("docker.io/example/my-image:v1"),
				RayVersion:           types.StringNull(),
				RegistryLoginSecret:  types.StringNull(),
				BuildID:              types.StringValue("bld_orphaned789"),
				ClusterEnvironmentID: tc.clusterEnvironmentID,
				BuildStatus:          types.StringValue("succeeded"),
				CreatedAt:            types.StringValue("2024-01-01T00:00:00Z"),
				IsBYOD:               types.BoolValue(true),
				Revision:             types.Int64Value(1),
				NameVersion:          types.StringValue("my-image:1"),
			}

			upgrader := registryStateUpgraderV0(t)
			req := resource.UpgradeStateRequest{State: buildV0State(t, upgrader.PriorSchema, v0Model)}
			resp := &resource.UpgradeStateResponse{State: tfsdk.State{Schema: v1SchemaForUpgradeTest(t).Schema}}

			upgrader.StateUpgrader(context.Background(), req, resp)

			if !resp.Diagnostics.HasError() {
				t.Fatal("expected upgrade to report an error for a missing cluster_environment_id, got none")
			}

			const wantSummary = "Missing Cluster Environment ID During State Upgrade"
			var found bool
			for _, d := range resp.Diagnostics {
				if d.Summary() == wantSummary {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("diagnostics = %v, want a diagnostic with summary %q -- this must be the real defensive-branch error, not some other failure", resp.Diagnostics, wantSummary)
			}

			// No state write: resp.State.Raw must still be the zero value,
			// exactly as the framework's own "Missing Upgraded Resource
			// State" safety net checks for (Type() == nil) when a
			// StateUpgrader returns without ever calling State.Set.
			if !resp.State.Raw.Equal(tftypes.Value{}) {
				t.Error("resp.State.Raw was populated despite the error path -- the defensive branch must return before writing any state")
			}
		})
	}
}
