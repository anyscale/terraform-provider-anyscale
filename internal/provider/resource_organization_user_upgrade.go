package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure OrganizationUserResource satisfies the state-upgrade interface.
var _ resource.ResourceWithUpgradeState = &OrganizationUserResource{}

// UpgradeState implements the v0 -> v1 migration for the v0.25.0 re-key of
// anyscale_organization_user from identity_id to email.
//
// WHAT MOVES. Under v0, `id` held the identity_id. Under v1 it holds the
// email, because identity_id and user_id are two different backend identifiers
// for one human, user_id is optional in the API model and genuinely absent for
// some identity types, and NEITHER EXISTS until the person accepts an
// invitation. Email is the only value stable across the whole lifecycle.
//
// WHY THIS IS CHEAP HERE, and why it is still not optional. Every v0 state
// already carries `email` - it was a Computed attribute populated from the API
// on every read - so the new key is read straight out of prior state and
// nothing has to be fetched. That is what makes the migration a pure re-label
// rather than a lookup. It does NOT make the upgrader unnecessary: without one,
// prior state keeps identity_id in `id`, and Terraform would be holding a
// resource whose key does not match anything the new Read can resolve.
//
// The two attributes v1 ADDS - identity_id and reinvite_if_expired - are simply
// absent from v0 state. identity_id is populated on the next Read; the boolean
// carries a framework Default that resolves at plan time. Neither needs
// synthesising here, and synthesising them would be worse: a value invented by
// an upgrader is indistinguishable from one the backend reported.
//
// Deliberately NOT done here: seeding the private-state membership record.
// UpgradeStateRequest and UpgradeStateResponse carry no Private field at all
// (verified against the pinned framework), so an upgrader structurally cannot
// write one. Read self-heals it instead - see resolveMembership. The absent
// record is safe on its own regardless: Delete's no-record branch and its
// adopted branch both do nothing.
func (r *OrganizationUserResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {
			PriorSchema:   organizationUserSchemaV0(),
			StateUpgrader: upgradeOrganizationUserStateV0toV1,
		},
	}
}

// organizationUserResourceModelV0 is the frozen v0 shape.
//
// Every attribute was Computed under v0 - the resource had no writable
// attribute at all, so a v0 configuration was an empty block plus an import.
// `email` being present and populated here is the whole reason this migration
// needs no API call.
type organizationUserResourceModelV0 struct {
	ID        types.String `tfsdk:"id"`
	Email     types.String `tfsdk:"email"`
	UserID    types.String `tfsdk:"user_id"`
	Name      types.String `tfsdk:"name"`
	CreatedAt types.String `tfsdk:"created_at"`
}

// organizationUserSchemaV0 is a frozen copy of the schema exactly as it existed
// at version 0. It exists solely so UpgradeState can decode v0 state; do NOT
// evolve it alongside the live schema and do not "fix" anything that looks
// stale in it. It is a historical snapshot.
//
// Note there is no cloud_name-style trap here: unlike anyscale_project, this
// resource's attribute set was never changed without a version bump, so
// version 0 labels exactly one shape.
func organizationUserSchemaV0() *schema.Schema {
	return &schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"email": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"user_id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// upgradeOrganizationUserStateV0toV1 re-keys the resource: `id` becomes the
// email that v0 state already carried, and the former identity_id moves into
// the new `identity_id` attribute rather than being discarded.
//
// Written as an explicit field-by-field copy so that adding an attribute to
// OrganizationUserResourceModel later fails to compile here rather than
// silently upgrading to a zero value.
func upgradeOrganizationUserStateV0toV1(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	if req.State == nil {
		return
	}

	var v0 organizationUserResourceModelV0
	resp.Diagnostics.Append(req.State.Get(ctx, &v0)...)
	if resp.Diagnostics.HasError() {
		return
	}

	upgraded := OrganizationUserResourceModel{
		// The re-key itself. v0's id was the identity_id; v1's is the email.
		ID:    v0.Email,
		Email: v0.Email,
		// v0's id is not thrown away - it is exactly what the new identity_id
		// attribute holds, so a resource that has already been read keeps it
		// through the upgrade rather than waiting for the next refresh.
		IdentityID: v0.ID,
		UserID:     v0.UserID,
		Name:       v0.Name,
		CreatedAt:  v0.CreatedAt,
		// Left null rather than defaulted. The framework Default resolves at plan
		// time; writing false here would be indistinguishable from a user who
		// declared it, and this upgrader has no business asserting intent.
		ReinviteIfExpired: types.BoolNull(),
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &upgraded)...)
}
