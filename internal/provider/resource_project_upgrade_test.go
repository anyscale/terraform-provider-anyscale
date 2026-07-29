package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// projectCollaboratorV0ObjectType is the element type of v0's collaborator
// block, used only to build prior-state fixtures below.
func projectCollaboratorV0ObjectType() types.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"email":            types.StringType,
		"permission_level": types.StringType,
		"identity_id":      types.StringType,
		"user_id":          types.StringType,
	}}
}

// TestProjectStateUpgradeV0toV1_DropsCollaborator mirrors
// TestCloudResourceResourceStateUpgradeV1toV2_DropsStatus, this repo's
// established shape for "prove a removed attribute auto-migrates cleanly," and
// drives the REAL production UpgradeState function end to end rather than a
// stand-in.
//
// This is the load-bearing test for the collaborator removal. A
// schema-absence check alone would prove the block is gone but NOT that
// existing state survives the version bump - and the failure it guards against
// (`Failed to marshal state to json: unsupported attribute "collaborator"`)
// does not surface at plan time, so a clean plan proves nothing.
func TestProjectStateUpgradeV0toV1_DropsCollaborator(t *testing.T) {
	ctx := context.Background()

	// THE reproducing shape: collaborator carries a REAL, non-empty list in
	// prior state. A fixture with an empty list would go green even against an
	// upgrader that mishandled real entries.
	collaborators := types.ListValueMust(projectCollaboratorV0ObjectType(), []attr.Value{
		types.ObjectValueMust(projectCollaboratorV0ObjectType().AttrTypes, map[string]attr.Value{
			"email":            types.StringValue("owner@example.com"),
			"permission_level": types.StringValue("owner"),
			"identity_id":      types.StringValue("ident_v0_1"),
			"user_id":          types.StringValue("usr_v0_1"),
		}),
		types.ObjectValueMust(projectCollaboratorV0ObjectType().AttrTypes, map[string]attr.Value{
			"email":            types.StringValue("writer@example.com"),
			"permission_level": types.StringValue("write"),
			"identity_id":      types.StringValue("ident_v0_2"),
			"user_id":          types.StringValue("usr_v0_2"),
		}),
	})

	v0 := projectResourceModelV0{
		ID:                     types.StringValue("prj_v0tov1"),
		CloudID:                types.StringValue("cld_v0tov1"),
		Name:                   types.StringValue("project-v0-to-v1"),
		Description:            types.StringValue("a v0 project"),
		InitialClusterConfigID: types.StringValue("ccfg_v0tov1"),
		Collaborators:          collaborators,
		CreatorID:              types.StringValue("usr_creator_v0tov1"),
		CreatedAt:              types.StringValue("2026-01-01T00:00:00Z"),
		LastUsedCloudID:        types.StringValue("cld_v0tov1"),
		IsDefault:              types.BoolValue(false),
		DirectoryName:          types.StringValue("project-v0-to-v1-dir"),
	}

	r := &ProjectResource{}
	upgraders := r.UpgradeState(ctx)
	upgrader, ok := upgraders[0]
	if !ok {
		t.Fatalf("UpgradeState() has no entry for schema version 0 - the v0->v1 upgrader dropping collaborator is missing")
	}

	v0Schema := projectSchemaV0()
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
		t.Fatalf("current schema Version = %d, want 1 (bumped for the collaborator removal)", v1SchemaResp.Schema.Version)
	}
	if _, present := v1SchemaResp.Schema.Blocks["collaborator"]; present {
		t.Fatal("current schema still declares the collaborator block - it should be fully removed")
	}
	if _, present := v1SchemaResp.Schema.Attributes["collaborator"]; present {
		t.Fatal("current schema declares a collaborator attribute - it should be fully removed, not converted")
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
		t.Fatalf("upgradeProjectStateV0toV1() diagnostics: %v", resp.Diagnostics)
	}

	// THE load-bearing assertion: the upgraded value must decode cleanly into
	// the CURRENT model, which has no Collaborators field at all. If the
	// upgrader still wrote a collaborator value, resp.Diagnostics above would
	// already have failed; this proves the actual prior-state payload survives
	// the round trip rather than just that the target schema looks right.
	var v1 ProjectResourceModel
	if diags := resp.State.Get(ctx, &v1); diags.HasError() {
		t.Fatalf("failed to decode upgraded v1 state: %v", diags)
	}

	// Every non-dropped attribute must come through byte-for-byte.
	for _, tc := range []struct {
		name, got, want string
	}{
		{"ID", v1.ID.ValueString(), "prj_v0tov1"},
		{"CloudID", v1.CloudID.ValueString(), "cld_v0tov1"},
		{"Name", v1.Name.ValueString(), "project-v0-to-v1"},
		{"Description", v1.Description.ValueString(), "a v0 project"},
		{"InitialClusterConfigID", v1.InitialClusterConfigID.ValueString(), "ccfg_v0tov1"},
		{"CreatorID", v1.CreatorID.ValueString(), "usr_creator_v0tov1"},
		{"CreatedAt", v1.CreatedAt.ValueString(), "2026-01-01T00:00:00Z"},
		{"LastUsedCloudID", v1.LastUsedCloudID.ValueString(), "cld_v0tov1"},
		{"DirectoryName", v1.DirectoryName.ValueString(), "project-v0-to-v1-dir"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q (unchanged across the upgrade)", tc.name, tc.got, tc.want)
		}
	}
	if v1.IsDefault.IsNull() || v1.IsDefault.ValueBool() {
		t.Errorf("IsDefault = %v, want a non-null false (unchanged across the upgrade)", v1.IsDefault)
	}
}

// TestProjectStateUpgradeV0toV1_EmptyCollaboratorList covers the population
// this upgrader actually exists for: someone who NEVER declared a collaborator
// block still has the attribute in state (as an empty list), because Terraform
// stores the full schema shape, not just what the config set. If the upgrader
// were skipped on the theory that "nobody used collaborators," this is the
// state that would fail to marshal.
func TestProjectStateUpgradeV0toV1_EmptyCollaboratorList(t *testing.T) {
	ctx := context.Background()

	v0 := projectResourceModelV0{
		ID:              types.StringValue("prj_empty_collab"),
		CloudID:         types.StringValue("cld_empty_collab"),
		Name:            types.StringValue("project-never-used-collaborators"),
		Description:     types.StringValue("api-generated description"),
		Collaborators:   types.ListValueMust(projectCollaboratorV0ObjectType(), []attr.Value{}),
		CreatorID:       types.StringValue("usr_creator_empty"),
		CreatedAt:       types.StringValue("2026-01-01T00:00:00Z"),
		LastUsedCloudID: types.StringValue("cld_empty_collab"),
		IsDefault:       types.BoolValue(false),
		DirectoryName:   types.StringValue("project-never-used-collaborators-dir"),
	}

	r := &ProjectResource{}
	upgrader := r.UpgradeState(ctx)[0]

	v0Schema := projectSchemaV0()
	priorState := &tfsdk.State{
		Schema: *v0Schema,
		Raw:    tftypes.NewValue(v0Schema.Type().TerraformType(ctx), nil),
	}
	if diags := priorState.Set(ctx, &v0); diags.HasError() {
		t.Fatalf("failed to build v0 prior state fixture: %v", diags)
	}

	var currentSchema resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &currentSchema)

	resp := &resource.UpgradeStateResponse{
		State: tfsdk.State{
			Schema: currentSchema.Schema,
			Raw:    tftypes.NewValue(currentSchema.Schema.Type().TerraformType(ctx), nil),
		},
	}
	upgrader.StateUpgrader(ctx, resource.UpgradeStateRequest{State: priorState}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("upgradeProjectStateV0toV1() diagnostics on an empty collaborator list: %v", resp.Diagnostics)
	}

	var v1 ProjectResourceModel
	if diags := resp.State.Get(ctx, &v1); diags.HasError() {
		t.Fatalf("failed to decode upgraded v1 state: %v", diags)
	}
	if v1.Name.ValueString() != "project-never-used-collaborators" {
		t.Errorf("Name = %q, want unchanged", v1.Name.ValueString())
	}
	// initial_cluster_config_id was never set in this fixture; it must stay
	// null rather than being defaulted to "" by the upgrade.
	if !v1.InitialClusterConfigID.IsNull() {
		t.Errorf("InitialClusterConfigID = %#v, want null (never set in v0 state, must not be defaulted)", v1.InitialClusterConfigID)
	}
}

// TestProjectStateUpgradeV0toV1_NilPriorState proves the upgrader surfaces a
// clear diagnostic instead of panicking when the framework hands it no prior
// state, matching the other upgraders in this package.
func TestProjectStateUpgradeV0toV1_NilPriorState(t *testing.T) {
	ctx := context.Background()

	resp := &resource.UpgradeStateResponse{}
	upgradeProjectStateV0toV1(ctx, resource.UpgradeStateRequest{State: nil}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error diagnostic for nil prior state, got none")
	}
	if !diagsContainSummary(resp.Diagnostics, "Missing Prior State") {
		t.Errorf("expected a 'Missing Prior State' diagnostic, got: %v", resp.Diagnostics)
	}
}

// TestProjectStateUpgradeV0toV1_PreV024StateWithCloudName covers the fact that
// SCHEMA VERSION 0 LABELS TWO INCOMPATIBLE STATE SHAPES.
//
// cloud_name was removed from anyscale_project in v0.24.0 WITHOUT a schema
// version bump, so state written before that release carries a cloud_name key
// and state written after does not - both stamped version 0. The upgrader has
// to decode either. This test drives the older of the two, which is the one an
// upgrader written against only the current source would miss, and it is also
// the larger population: every project created before v0.24.0.
//
// The sibling tests above cover the post-v0.24.0 shape (cloud_name absent,
// decoding as null), so the pair covers both halves of version 0.
func TestProjectStateUpgradeV0toV1_PreV024StateWithCloudName(t *testing.T) {
	ctx := context.Background()

	collaborators := types.ListValueMust(projectCollaboratorV0ObjectType(), []attr.Value{
		types.ObjectValueMust(projectCollaboratorV0ObjectType().AttrTypes, map[string]attr.Value{
			"email":            types.StringValue("legacy@example.com"),
			"permission_level": types.StringValue("owner"),
			"identity_id":      types.StringValue("ident_legacy"),
			"user_id":          types.StringValue("usr_legacy"),
		}),
	})

	v0 := projectResourceModelV0{
		ID:      types.StringValue("prj_pre024"),
		CloudID: types.StringValue("cld_pre024"),
		// The whole point of this fixture: a real pre-v0.24.0 project resolved its
		// cloud BY NAME, so cloud_id could even be null in that state while
		// cloud_name carried the selector.
		CloudName:              types.StringValue("my-legacy-cloud"),
		Name:                   types.StringValue("project-pre-024"),
		Description:            types.StringValue("created before cloud_name was removed"),
		InitialClusterConfigID: types.StringNull(),
		Collaborators:          collaborators,
		CreatorID:              types.StringValue("usr_creator_pre024"),
		CreatedAt:              types.StringValue("2026-01-01T00:00:00Z"),
		LastUsedCloudID:        types.StringValue("cld_pre024"),
		IsDefault:              types.BoolValue(false),
		DirectoryName:          types.StringValue("project-pre-024-dir"),
	}

	r := &ProjectResource{}
	upgrader := r.UpgradeState(ctx)[0]

	v0Schema := projectSchemaV0()
	priorState := &tfsdk.State{
		Schema: *v0Schema,
		Raw:    tftypes.NewValue(v0Schema.Type().TerraformType(ctx), nil),
	}
	// This Set is the real assertion: it fails outright if the frozen v0 schema
	// does not declare cloud_name, which is exactly the bug this test guards.
	if diags := priorState.Set(ctx, &v0); diags.HasError() {
		t.Fatalf("failed to build pre-v0.24.0 v0 state fixture - the frozen v0 schema must declare cloud_name, "+
			"because it was removed in v0.24.0 without a version bump and real state from before then carries it: %v", diags)
	}

	var currentSchema resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &currentSchema)

	resp := &resource.UpgradeStateResponse{
		State: tfsdk.State{
			Schema: currentSchema.Schema,
			Raw:    tftypes.NewValue(currentSchema.Schema.Type().TerraformType(ctx), nil),
		},
	}
	upgrader.StateUpgrader(ctx, resource.UpgradeStateRequest{State: priorState}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("upgrading pre-v0.24.0 state must succeed, got: %v", resp.Diagnostics)
	}

	var upgraded ProjectResourceModel
	if diags := resp.State.Get(ctx, &upgraded); diags.HasError() {
		t.Fatalf("failed to read upgraded state: %v", diags)
	}

	// Everything that still exists must survive; cloud_name and collaborator are
	// both dropped because neither exists on the current schema.
	if upgraded.ID.ValueString() != "prj_pre024" {
		t.Errorf("id = %q, want prj_pre024 - the upgrade must not lose identity", upgraded.ID.ValueString())
	}
	if upgraded.CloudID.ValueString() != "cld_pre024" {
		t.Errorf("cloud_id = %q, want cld_pre024", upgraded.CloudID.ValueString())
	}
	if upgraded.Name.ValueString() != "project-pre-024" {
		t.Errorf("name = %q, want project-pre-024", upgraded.Name.ValueString())
	}
	if upgraded.DirectoryName.ValueString() != "project-pre-024-dir" {
		t.Errorf("directory_name = %q, want project-pre-024-dir", upgraded.DirectoryName.ValueString())
	}
}
