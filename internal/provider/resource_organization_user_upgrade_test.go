package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// buildOrgUserV0State builds real v0 prior state through the frozen v0 schema.
// Going through the schema rather than hand-rolling a value tree is what makes
// this fail if organizationUserSchemaV0 stops describing what v0 actually was.
func buildOrgUserV0State(t *testing.T, v0 organizationUserResourceModelV0) *tfsdk.State {
	t.Helper()
	ctx := context.Background()

	v0Schema := organizationUserSchemaV0()
	state := &tfsdk.State{
		Schema: *v0Schema,
		Raw:    tftypes.NewValue(v0Schema.Type().TerraformType(ctx), nil),
	}
	if diags := state.Set(ctx, &v0); diags.HasError() {
		t.Fatalf("failed to build v0 state fixture: %v", diags)
	}
	return state
}

// runOrgUserUpgrade drives the REAL registered upgrader for version 0, rather
// than calling the migration function directly - so a missing or mis-keyed
// registration fails here instead of passing silently.
func runOrgUserUpgrade(t *testing.T, prior *tfsdk.State) (OrganizationUserResourceModel, *resource.UpgradeStateResponse) {
	t.Helper()
	ctx := context.Background()

	r := &OrganizationUserResource{}
	upgraders := r.UpgradeState(ctx)
	upgrader, ok := upgraders[0]
	if !ok {
		t.Fatal("UpgradeState() has no entry for schema version 0 - the v0->v1 re-key migration is not registered")
	}

	var currentSchema resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &currentSchema)
	if currentSchema.Schema.Version != 1 {
		t.Fatalf("current schema Version = %d, want 1 - the re-key must bump it or the upgrader never runs",
			currentSchema.Schema.Version)
	}

	resp := &resource.UpgradeStateResponse{
		State: tfsdk.State{
			Schema: currentSchema.Schema,
			Raw:    tftypes.NewValue(currentSchema.Schema.Type().TerraformType(ctx), nil),
		},
	}
	upgrader.StateUpgrader(ctx, resource.UpgradeStateRequest{State: prior}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("upgrade must succeed, got: %v", resp.Diagnostics)
	}

	var upgraded OrganizationUserResourceModel
	if diags := resp.State.Get(ctx, &upgraded); diags.HasError() {
		t.Fatalf("failed to read upgraded state: %v", diags)
	}
	return upgraded, resp
}

// TestOrganizationUserStateUpgradeV0toV1_ReKeysToEmail is the migration's whole
// point: v0 stored the identity_id in `id`, v1 stores the email. Without this,
// prior state holds a key that nothing in the new Read can resolve.
func TestOrganizationUserStateUpgradeV0toV1_ReKeysToEmail(t *testing.T) {
	prior := buildOrgUserV0State(t, organizationUserResourceModelV0{
		ID:        types.StringValue("ide_legacyidentity"),
		Email:     types.StringValue("Legacy.User@Example.com"),
		UserID:    types.StringValue("usr_legacy"),
		Name:      types.StringValue("Legacy User"),
		CreatedAt: types.StringValue("2026-01-01T00:00:00Z"),
	})

	upgraded, _ := runOrgUserUpgrade(t, prior)

	if upgraded.ID.ValueString() != "Legacy.User@Example.com" {
		t.Errorf("id = %q, want the EMAIL %q - the re-key is the entire purpose of this upgrader",
			upgraded.ID.ValueString(), "Legacy.User@Example.com")
	}
	// The former key is preserved rather than discarded: identity_id is a real
	// attribute in v1 and prior state is the only place its value exists until
	// the next refresh.
	if upgraded.IdentityID.ValueString() != "ide_legacyidentity" {
		t.Errorf("identity_id = %q, want the v0 id %q carried across",
			upgraded.IdentityID.ValueString(), "ide_legacyidentity")
	}
	// Casing survives. The upgrader must not normalise: `email` is Required and
	// non-Computed in v1, and Terraform Core rejects a value that differs from
	// config.
	if upgraded.Email.ValueString() != "Legacy.User@Example.com" {
		t.Errorf("email = %q, want the stored casing %q preserved",
			upgraded.Email.ValueString(), "Legacy.User@Example.com")
	}
	if upgraded.UserID.ValueString() != "usr_legacy" {
		t.Errorf("user_id = %q, want it carried across", upgraded.UserID.ValueString())
	}
	if upgraded.Name.ValueString() != "Legacy User" {
		t.Errorf("name = %q, want it carried across", upgraded.Name.ValueString())
	}
	if upgraded.CreatedAt.ValueString() != "2026-01-01T00:00:00Z" {
		t.Errorf("created_at = %q, want it carried across", upgraded.CreatedAt.ValueString())
	}
}

// TestOrganizationUserStateUpgradeV0toV1_DoesNotInventTheOptIn pins that the
// upgrader leaves reinvite_if_expired NULL rather than writing false.
//
// The distinction is not cosmetic. A framework Default resolves at plan time,
// so null is filled in then; but a false WRITTEN HERE is indistinguishable in
// state from a user who declared it. An upgrader has no business asserting
// intent about an attribute that did not exist in the version it is migrating.
func TestOrganizationUserStateUpgradeV0toV1_DoesNotInventTheOptIn(t *testing.T) {
	prior := buildOrgUserV0State(t, organizationUserResourceModelV0{
		ID:        types.StringValue("ide_x"),
		Email:     types.StringValue("x@example.com"),
		UserID:    types.StringNull(),
		Name:      types.StringNull(),
		CreatedAt: types.StringValue("2026-01-01T00:00:00Z"),
	})

	upgraded, _ := runOrgUserUpgrade(t, prior)

	if !upgraded.ReinviteIfExpired.IsNull() {
		t.Errorf("reinvite_if_expired = %v, want NULL - the Default resolves at plan time, and a value written "+
			"here is indistinguishable from one the user declared", upgraded.ReinviteIfExpired)
	}
}

// TestOrganizationUserStateUpgradeV0toV1_NilPriorState guards the degenerate
// input rather than panicking on it.
func TestOrganizationUserStateUpgradeV0toV1_NilPriorState(t *testing.T) {
	resp := &resource.UpgradeStateResponse{}
	upgradeOrganizationUserStateV0toV1(context.Background(), resource.UpgradeStateRequest{State: nil}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a nil prior state must be a no-op, not an error: %v", resp.Diagnostics)
	}
}
