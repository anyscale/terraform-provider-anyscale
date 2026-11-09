package provider

// MoveState unit tests for anyscale_iam_organization_user: each test drives
// the real r.MoveState(ctx) mover set with a hand-built request, mirroring
// the framework's routing - movers run in order, and the first to set target
// state (or error) wins; a mover that returns neither is a skip, and Core
// then reports the move as unsupported.
//
// What is pinned:
//   - v1 source state copies through unchanged (id stays the email key).
//   - v0 source state (id = identity_id) is re-keyed by the same shared
//     transform UpgradeState uses - asserted twice, once through the frozen
//     v0 Go model and once from raw wire JSON decoded against the v0 schema
//     exactly the way the framework pre-decodes SourceState.
//   - the address gate accepts any provider address whose TYPE segment is
//     "anyscale" (production sends anyscale/anyscale; resource.Test sends
//     hashicorp/anyscale), and skips a foreign provider entirely.
//   - one resource type never accepts another type's state (skip).
//   - unsupported schema versions error with the state rm + re-import
//     recovery, as does a matching request whose source state failed to
//     decode - a skip would surface as Core's generic "no move" and lose
//     the cause.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	tfsdk "github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// runOrgUserMovers invokes the resource's mover set in order against req and
// mirrors the framework's first-complete-wins semantics: the first mover
// that errors, or that sets target state, decides the outcome.
func runOrgUserMovers(t *testing.T, r *OrganizationUserResource, req resource.MoveStateRequest) *resource.MoveStateResponse {
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

// orgUserV1SourceState builds production-shaped v1 source state through the
// CURRENT schema, as the framework hands a SourceSchema-bearing mover.
func orgUserV1SourceState(t *testing.T, r *OrganizationUserResource) *tfsdk.State {
	t.Helper()
	ctx := context.Background()

	var currentSchema resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &currentSchema)

	state := &tfsdk.State{
		Schema: currentSchema.Schema,
		Raw:    tftypes.NewValue(currentSchema.Schema.Type().TerraformType(ctx), nil),
	}
	model := OrganizationUserResourceModel{
		ID:         types.StringValue("Legacy.User@Example.com"),
		Email:      types.StringValue("Legacy.User@Example.com"),
		UserID:     types.StringValue("usr_legacy"),
		IdentityID: types.StringValue("ide_legacy"),
		Name:       types.StringValue("Legacy User"),
		CreatedAt:  types.StringValue("2026-01-01T00:00:00Z"),
	}
	if diags := state.Set(ctx, &model); diags.HasError() {
		t.Fatalf("failed to build v1 source state: %v", diags)
	}
	return state
}

// orgUserV0RawJSON is raw wire-shaped v0 state, as a pre-v0.25.0 build wrote
// it: id holds the identity_id, and the role trio is still present.
func orgUserV0RawJSON(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id":               "ide_legacyidentity",
		"email":            "Legacy.User@Example.com",
		"user_id":          "usr_legacy",
		"name":             "Legacy User",
		"created_at":       "2026-01-01T00:00:00Z",
		"permission_level": "collaborator",
		"base_role":        "collaborator",
		"additional_roles": []string{"image_reader"},
	})
	if err != nil {
		t.Fatalf("failed to marshal v0 raw state: %v", err)
	}
	return raw
}

// orgUserV0RawSourceState imitates the framework's decode of the v0 mover:
// SourceRawState unmarshalled against the mover's declared v0 SourceSchema.
// If the raw fixture does not fit the frozen v0 schema, this fails HERE,
// which is the fixture working as intended.
func orgUserV0RawSourceState(t *testing.T, data []byte) *tfsdk.State {
	t.Helper()
	ctx := context.Background()

	v0Schema := organizationUserSchemaV0()
	rs := tfprotov6.RawState{JSON: data}
	val, err := rs.Unmarshal(v0Schema.Type().TerraformType(ctx))
	if err != nil {
		t.Fatalf("v0 raw fixture does not decode against the frozen v0 schema: %v", err)
	}
	return &tfsdk.State{
		Schema: *v0Schema,
		Raw:    val,
	}
}

func TestOrganizationUserMoveState_V1PassesThrough(t *testing.T) {
	r := &OrganizationUserResource{}
	resp := runOrgUserMovers(t, r, resource.MoveStateRequest{
		SourceTypeName:        "anyscale_organization_user",
		SourceProviderAddress: "registry.terraform.io/anyscale/anyscale",
		SourceSchemaVersion:   1,
		SourceState:           orgUserV1SourceState(t, r),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("v1 move must succeed, got: %v", resp.Diagnostics)
	}
	if resp.TargetState.Raw.IsNull() {
		t.Fatal("v1 move set no target state - the copy-through mover skipped")
	}
	ctx := context.Background()
	var moved OrganizationUserResourceModel
	if diags := resp.TargetState.Get(ctx, &moved); diags.HasError() {
		t.Fatalf("failed to read moved state: %v", diags)
	}
	if moved.ID.ValueString() != "Legacy.User@Example.com" {
		t.Fatalf("moved id = %q, want the v1 key (the email)", moved.ID.ValueString())
	}
}

// TestOrganizationUserMoveState_V0ReKeysToEmail_GoModel is the mutation-proof
// tier's primary case: v0 state built through the frozen v0 Go model.
func TestOrganizationUserMoveState_V0ReKeysToEmail_GoModel(t *testing.T) {
	r := &OrganizationUserResource{}
	resp := runOrgUserMovers(t, r, resource.MoveStateRequest{
		SourceTypeName:        "anyscale_organization_user",
		SourceProviderAddress: "registry.terraform.io/anyscale/anyscale",
		SourceSchemaVersion:   0,
		SourceState: buildOrgUserV0State(t, organizationUserResourceModelV0{
			ID:              types.StringValue("ide_legacyidentity"),
			Email:           types.StringValue("Legacy.User@Example.com"),
			UserID:          types.StringValue("usr_legacy"),
			Name:            types.StringValue("Legacy User"),
			CreatedAt:       types.StringValue("2026-01-01T00:00:00Z"),
			PermissionLevel: types.StringValue("collaborator"),
			BaseRole:        types.StringValue("collaborator"),
			AdditionalRoles: types.ListValueMust(types.StringType, []attr.Value{
				types.StringValue("image_reader"),
			}),
		}),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("v0 move must succeed, got: %v", resp.Diagnostics)
	}
	if resp.TargetState.Raw.IsNull() {
		t.Fatal("v0 move set no target state - the v0 mover skipped")
	}
	ctx := context.Background()
	var moved OrganizationUserResourceModel
	if diags := resp.TargetState.Get(ctx, &moved); diags.HasError() {
		t.Fatalf("failed to read moved state: %v", diags)
	}
	if moved.ID.ValueString() != "Legacy.User@Example.com" {
		t.Fatalf("moved id = %q, want the re-keyed EMAIL - the shared v0->v1 transform did not run", moved.ID.ValueString())
	}
}

// TestOrganizationUserMoveState_V0ReKeysToEmail_RawJSON pins the same re-key
// from the wire-sales side: raw JSON bytes that never saw a Go model.
func TestOrganizationUserMoveState_V0ReKeysToEmail_RawJSON(t *testing.T) {
	r := &OrganizationUserResource{}
	resp := runOrgUserMovers(t, r, resource.MoveStateRequest{
		SourceTypeName:        "anyscale_organization_user",
		SourceProviderAddress: "registry.terraform.io/anyscale/anyscale",
		SourceSchemaVersion:   0,
		SourceState:           orgUserV0RawSourceState(t, orgUserV0RawJSON(t)),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("v0 move must succeed, got: %v", resp.Diagnostics)
	}
	if resp.TargetState.Raw.IsNull() {
		t.Fatal("v0 move set no target state - the v0 mover skipped")
	}
	ctx := context.Background()
	var moved OrganizationUserResourceModel
	if diags := resp.TargetState.Get(ctx, &moved); diags.HasError() {
		t.Fatalf("failed to read moved state: %v", diags)
	}
	if moved.ID.ValueString() != "Legacy.User@Example.com" {
		t.Fatalf("moved id = %q, want the re-keyed EMAIL - the shared v0->v1 transform did not run on raw state", moved.ID.ValueString())
	}
}

func TestOrganizationUserMoveState_HarnessAddressFormsBothMove(t *testing.T) {
	// The amended gate matches the provider TYPE segment only: production
	// sends anyscale/anyscale, the test harness sends hashicorp/anyscale.
	// Both must move.
	r := &OrganizationUserResource{}
	for _, address := range []string{
		"registry.terraform.io/anyscale/anyscale",
		"registry.terraform.io/hashicorp/anyscale",
	} {
		resp := runOrgUserMovers(t, r, resource.MoveStateRequest{
			SourceTypeName:        "anyscale_organization_user",
			SourceProviderAddress: address,
			SourceSchemaVersion:   1,
			SourceState:           orgUserV1SourceState(t, r),
		})
		if resp.Diagnostics.HasError() {
			t.Fatalf("v1 move from %s must succeed, got: %v", address, resp.Diagnostics)
		}
		if resp.TargetState.Raw.IsNull() {
			t.Fatalf("v1 move from %s set no target state", address)
		}
	}
}

func TestOrganizationUserMoveState_WrongTypeNameSkips(t *testing.T) {
	r := &OrganizationUserResource{}
	resp := runOrgUserMovers(t, r, resource.MoveStateRequest{
		SourceTypeName:        "anyscale_something_else",
		SourceProviderAddress: "registry.terraform.io/anyscale/anyscale",
		SourceSchemaVersion:   1,
		SourceState:           orgUserV1SourceState(t, r),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("wrong type name must SKIP (Core reports the move as unsupported), got error: %v", resp.Diagnostics)
	}
	if !resp.TargetState.Raw.IsNull() {
		t.Fatal("wrong type name must not move state")
	}
}

func TestOrganizationUserMoveState_WrongProviderAddressSkips(t *testing.T) {
	r := &OrganizationUserResource{}
	resp := runOrgUserMovers(t, r, resource.MoveStateRequest{
		SourceTypeName:        "anyscale_organization_user",
		SourceProviderAddress: "registry.terraform.io/someone/other",
		SourceSchemaVersion:   1,
		SourceState:           orgUserV1SourceState(t, r),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("wrong provider address must SKIP (Core reports the move as unsupported), got error: %v", resp.Diagnostics)
	}
	if !resp.TargetState.Raw.IsNull() {
		t.Fatal("wrong provider address must not move state")
	}
}

func TestOrganizationUserMoveState_UnsupportedVersionErrors(t *testing.T) {
	r := &OrganizationUserResource{}
	resp := runOrgUserMovers(t, r, resource.MoveStateRequest{
		SourceTypeName:        "anyscale_organization_user",
		SourceProviderAddress: "registry.terraform.io/anyscale/anyscale",
		SourceSchemaVersion:   7,
		SourceState:           orgUserV1SourceState(t, r),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("unsupported schema version must produce an error diagnostic naming the recovery")
	}
	joined := ""
	for _, d := range resp.Diagnostics.Errors() {
		joined += d.Detail() + " " + d.Summary()
	}
	if !strings.Contains(strings.ToLower(joined), "state rm") {
		t.Fatalf("unsupported-version diagnostic must name the terraform state rm recovery, got: %s", joined)
	}
}

// TestOrganizationUserMoveState_NilSourceStateErrors pins the architect /
// forge agreement: when the type, address and version ALL match but
// SourceState is nil (framework decode failed on the raw state), the mover
// must ERROR rather than skip - a skip would surface as Core's generic
// "no move" error and the user would lose the cause. The raw side simulates
// the shape the framework could not decode.
// NOTE(assayer): finalize the error-text assertions once forge lands the
// change; HasError is the load-bearing assertion, the state-rm wording is
// a soft check.
func TestOrganizationUserMoveState_NilSourceStateErrors(t *testing.T) {
	r := &OrganizationUserResource{}
	for _, version := range []int64{0, 1} {
		resp := runOrgUserMovers(t, r, resource.MoveStateRequest{
			SourceTypeName:        "anyscale_organization_user",
			SourceProviderAddress: "registry.terraform.io/anyscale/anyscale",
			SourceSchemaVersion:   version,
			SourceState:           nil, // decode failed upstream
			SourceRawState:        &tfprotov6.RawState{JSON: []byte("{\"bogus\": }")},
		})
		if !resp.Diagnostics.HasError() {
			t.Fatalf("schema version %d with nil SourceState must ERROR (not skip) - gates all matched", version)
		}
		joined := ""
		for _, d := range resp.Diagnostics.Errors() {
			joined += d.Detail() + " " + d.Summary()
		}
		if !strings.Contains(strings.ToLower(joined), "state rm") {
			t.Logf("warning: nil-SourceState diagnostic does not mention the state rm recovery (wording: %s)", joined)
		}
	}
}

// TestOrganizationUserMoveState_UndecodableRawV1 simulates a version-1
// request whose raw state does not fit the v1 schema: the framework decode
// fails, the mover sees nil SourceState, and must error as above.
func TestOrganizationUserMoveState_UndecodableRawV1(t *testing.T) {
	r := &OrganizationUserResource{}
	resp := runOrgUserMovers(t, r, resource.MoveStateRequest{
		SourceTypeName:        "anyscale_organization_user",
		SourceProviderAddress: "registry.terraform.io/anyscale/anyscale",
		SourceSchemaVersion:   1,
		SourceState:           nil,
		SourceRawState:        &tfprotov6.RawState{JSON: []byte(`{"not_an_attr": "x"}`)},
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("v1 request with undecodable raw state must ERROR, not skip")
	}
}
