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

// TestUserGroupMembersUpgradeStateV0toV1 drives the real upgrader through the
// tfsdk.State encode/decode path: v0 state with group_id must come out as v1
// state with the same value in user_group_id and the members unchanged.
func TestUserGroupMembersUpgradeStateV0toV1(t *testing.T) {
	ctx := context.Background()
	r := &UserGroupMembersResource{}

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Schema.Version != 1 {
		t.Fatalf("schema version = %d, want 1", schemaResp.Schema.Version)
	}

	upgrader, ok := r.UpgradeState(ctx)[0]
	if !ok {
		t.Fatal("UpgradeState() has no entry for schema version 0")
	}

	members := types.SetValueMust(types.StringType, []attr.Value{
		types.StringValue("a@example.com"), types.StringValue("b@example.com"),
	})
	v0Schema := userGroupMembersSchemaV0()
	prior := &tfsdk.State{Schema: *v0Schema, Raw: tftypes.NewValue(v0Schema.Type().TerraformType(ctx), nil)}
	if diags := prior.Set(ctx, &userGroupMembersResourceModelV0{
		ID: types.StringValue("ug_1"), GroupID: types.StringValue("ug_1"), Members: members,
	}); diags.HasError() {
		t.Fatalf("building v0 state: %v", diags)
	}

	resp := &resource.UpgradeStateResponse{State: tfsdk.State{
		Schema: schemaResp.Schema,
		Raw:    tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil),
	}}
	upgrader.StateUpgrader(ctx, resource.UpgradeStateRequest{State: prior}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("upgrader diagnostics: %v", resp.Diagnostics)
	}

	var got UserGroupMembersResourceModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatalf("decoding v1 state: %v", diags)
	}
	if got.UserGroupID.ValueString() != "ug_1" || got.ID.ValueString() != "ug_1" {
		t.Errorf("user_group_id = %q, id = %q, want both ug_1", got.UserGroupID.ValueString(), got.ID.ValueString())
	}
	if !got.Members.Equal(members) {
		t.Errorf("members = %v, want %v", got.Members, members)
	}
}
