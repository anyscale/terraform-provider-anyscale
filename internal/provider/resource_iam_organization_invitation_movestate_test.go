package provider

// MoveState unit tests for anyscale_iam_organization_invitation: the
// invitation resource has only ever had schema version 0, so its mover
// surface is copy-through under the old type name (address-gated on the
// provider TYPE segment, so both the production registry.terraform.io/
// anyscale/anyscale form and the test harness's hashicorp/anyscale form
// move), an error naming the state rm + re-import recovery for anything
// else - including a matching request whose source state failed to decode.

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	tfsdk "github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func runInvitationMovers(t *testing.T, r *OrganizationInvitationResource, req resource.MoveStateRequest) *resource.MoveStateResponse {
	t.Helper()
	ctx := context.Background()

	var currentSchema resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &currentSchema)
	emptyResp := func() *resource.MoveStateResponse {
		return &resource.MoveStateResponse{
			TargetState: tfsdk.State{
				Schema: currentSchema.Schema,
				Raw:    tftypes.NewValue(currentSchema.Schema.Type().TerraformType(ctx), nil),
			},
		}
	}

	for _, mover := range r.MoveState(ctx) {
		resp := emptyResp()
		mover.StateMover(ctx, req, resp)
		if resp.Diagnostics.HasError() {
			return resp
		}
		if !resp.TargetState.Raw.IsNull() {
			return resp
		}
	}
	return emptyResp()
}

func invitationV0SourceState(t *testing.T, r *OrganizationInvitationResource) *tfsdk.State {
	t.Helper()
	ctx := context.Background()

	var currentSchema resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &currentSchema)

	state := &tfsdk.State{
		Schema: currentSchema.Schema,
		Raw:    tftypes.NewValue(currentSchema.Schema.Type().TerraformType(ctx), nil),
	}
	// Minimal production-shaped invitation state: the schema's required key
	// (email) plus the fields Read refreshes.
	model := OrganizationInvitationResourceModel{
		ID:        types.StringValue("inv_mockinvitation00"),
		Email:     types.StringValue("moved-invite@example.com"),
		CreatedAt: types.StringValue("2026-01-01T00:00:00Z"),
		Status:    types.StringValue("pending"),
	}
	if diags := state.Set(ctx, &model); diags.HasError() {
		t.Fatalf("failed to build invitation source state: %v", diags)
	}
	return state
}

func TestOrganizationInvitationMoveState_CopyThrough(t *testing.T) {
	r := &OrganizationInvitationResource{}
	resp := runInvitationMovers(t, r, resource.MoveStateRequest{
		SourceTypeName:        "anyscale_organization_invitation",
		SourceProviderAddress: "registry.terraform.io/anyscale/anyscale",
		SourceSchemaVersion:   0,
		SourceState:           invitationV0SourceState(t, r),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("invitation move must succeed, got: %v", resp.Diagnostics)
	}
	if resp.TargetState.Raw.IsNull() {
		t.Fatal("invitation move set no target state - the copy-through mover skipped")
	}
}

func TestOrganizationInvitationMoveState_WrongTypeNameSkips(t *testing.T) {
	r := &OrganizationInvitationResource{}
	resp := runInvitationMovers(t, r, resource.MoveStateRequest{
		SourceTypeName:        "anyscale_organization_user",
		SourceProviderAddress: "registry.terraform.io/anyscale/anyscale",
		SourceSchemaVersion:   0,
		SourceState:           invitationV0SourceState(t, r),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("wrong type name must SKIP, got error: %v", resp.Diagnostics)
	}
	if !resp.TargetState.Raw.IsNull() {
		t.Fatal("wrong type name must not move state")
	}
}

func TestOrganizationInvitationMoveState_WrongProviderAddressSkips(t *testing.T) {
	r := &OrganizationInvitationResource{}
	resp := runInvitationMovers(t, r, resource.MoveStateRequest{
		SourceTypeName:        "anyscale_organization_invitation",
		SourceProviderAddress: "registry.terraform.io/someone/other",
		SourceSchemaVersion:   0,
		SourceState:           invitationV0SourceState(t, r),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("wrong provider address must SKIP, got error: %v", resp.Diagnostics)
	}
	if !resp.TargetState.Raw.IsNull() {
		t.Fatal("wrong provider address must not move state")
	}
}

func TestOrganizationInvitationMoveState_UnsupportedVersionErrors(t *testing.T) {
	r := &OrganizationInvitationResource{}
	resp := runInvitationMovers(t, r, resource.MoveStateRequest{
		SourceTypeName:        "anyscale_organization_invitation",
		SourceProviderAddress: "registry.terraform.io/anyscale/anyscale",
		SourceSchemaVersion:   3,
		SourceState:           invitationV0SourceState(t, r),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("unsupported invitation schema version must produce an error diagnostic naming the recovery")
	}
	joined := ""
	for _, d := range resp.Diagnostics.Errors() {
		joined += d.Detail() + " " + d.Summary()
	}
	if !strings.Contains(strings.ToLower(joined), "state rm") {
		t.Fatalf("unsupported-version diagnostic must name the terraform state rm recovery, got: %s", joined)
	}
}

// TestOrganizationInvitationMoveState_NilSourceStateErrors pins the
// architect/forge agreement: gates all match but SourceState is nil (decode
// failure) -> ERROR, not skip.
func TestOrganizationInvitationMoveState_NilSourceStateErrors(t *testing.T) {
	r := &OrganizationInvitationResource{}
	resp := runInvitationMovers(t, r, resource.MoveStateRequest{
		SourceTypeName:        "anyscale_organization_invitation",
		SourceProviderAddress: "registry.terraform.io/anyscale/anyscale",
		SourceSchemaVersion:   0,
		SourceState:           nil,
		SourceRawState:        &tfprotov6.RawState{JSON: []byte("{\"bogus\": }")},
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("invitation move with nil SourceState must ERROR (not skip) - gates all matched")
	}
}
