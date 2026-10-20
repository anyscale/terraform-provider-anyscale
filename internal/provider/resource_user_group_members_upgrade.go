package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.ResourceWithUpgradeState = &UserGroupMembersResource{}

// UpgradeState implements the v0 -> v1 migration: group_id was renamed to
// user_group_id, the name anyscale_role_binding uses for the same value.
// Nothing else changed, so existing state carries over without a re-import.
func (r *UserGroupMembersResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {
			PriorSchema:   userGroupMembersSchemaV0(),
			StateUpgrader: upgradeUserGroupMembersStateV0toV1,
		},
	}
}

// userGroupMembersResourceModelV0 is the v0 resource model.
type userGroupMembersResourceModelV0 struct {
	ID      types.String `tfsdk:"id"`
	GroupID types.String `tfsdk:"group_id"`
	Members types.Set    `tfsdk:"members"`
}

// userGroupMembersSchemaV0 is a frozen copy of the v0 schema, kept only so
// UpgradeState can decode v0 state. Do not evolve it.
func userGroupMembersSchemaV0() *schema.Schema {
	return &schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id":       schema.StringAttribute{Computed: true},
			"group_id": schema.StringAttribute{Required: true},
			"members":  schema.SetAttribute{Required: true, ElementType: types.StringType},
		},
	}
}

func upgradeUserGroupMembersStateV0toV1(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	var prior userGroupMembersResourceModelV0
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, UserGroupMembersResourceModel{
		ID:          prior.ID,
		UserGroupID: prior.GroupID,
		Members:     prior.Members,
	})...)
}
