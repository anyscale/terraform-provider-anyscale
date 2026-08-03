package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// SCHEMA VERSION 0 LABELS TWO INCOMPATIBLE SHAPES FOR THIS RESOURCE, and the
// upgrader's frozen PriorSchema describes the one that was never released.
//
// PR #228 removed permission_level, base_role and additional_roles from
// anyscale_organization_user WITHOUT bumping Version. So:
//
//	pre-#228  (v0.24.0, v0.24.1 - REAL, PUBLISHED TAGS): EIGHT attributes
//	          id, permission_level, email, user_id, name, created_at,
//	          base_role, additional_roles          ... and SchemaVersion absent, so version 0
//	post-#228 (main today, never released):        FIVE attributes
//	          id, email, user_id, name, created_at ... also version 0
//
// resource_organization_user_upgrade.go freezes the FIVE-attribute shape. But
// the state the upgrader will actually be handed on a real upgrade is the
// EIGHT-attribute one, because that is what shipped. A PriorSchema that does not
// declare an attribute present in the stored state fails to decode it.
//
// This is the same defect class as the project resource's pre-#219 state
// (resource_project_upgrade_priorschema_test.go, commit b11481c), one resource
// later - and sharper here, because that case leaned on a no-users premise while
// this shape is on the public registry. "Nobody uses user management" is a claim
// about adoption; the state shape is a fact about anyone who ever ran
// `terraform import` against v0.24.x.
//
// WHY THIS RECONSTRUCTS v0 INDEPENDENTLY rather than reusing
// organizationUserResourceModelV0 with three fields bolted on: a fixture derived
// from the artifact under test shares its blind spot. If the frozen schema is
// wrong, a fixture built from it is wrong in exactly the same way and the test
// goes green against the bug. Separate struct, separate schema function, written
// from the v0.24.1 source rather than from the upgrader - the same rationale
// applied to the project case, and the reason that one caught its bug.

// preIssue228OrganizationUserModel is the REAL released v0 shape, transcribed
// from `git show v0.24.1:internal/provider/resource_organization_user.go`.
// Do not "simplify" this toward organizationUserResourceModelV0; the divergence
// is the point.
type preIssue228OrganizationUserModel struct {
	ID              types.String `tfsdk:"id"`
	PermissionLevel types.String `tfsdk:"permission_level"`
	Email           types.String `tfsdk:"email"`
	UserID          types.String `tfsdk:"user_id"`
	Name            types.String `tfsdk:"name"`
	CreatedAt       types.String `tfsdk:"created_at"`
	BaseRole        types.String `tfsdk:"base_role"`
	AdditionalRoles types.List   `tfsdk:"additional_roles"`
}

// preIssue228OrganizationUserSchema mirrors the released v0 schema. Every
// attribute was Computed - the resource had no writable attribute at all.
func preIssue228OrganizationUserSchema() schema.Schema {
	return schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id":               schema.StringAttribute{Computed: true},
			"permission_level": schema.StringAttribute{Computed: true},
			"email":            schema.StringAttribute{Computed: true},
			"user_id":          schema.StringAttribute{Computed: true},
			"name":             schema.StringAttribute{Computed: true},
			"created_at":       schema.StringAttribute{Computed: true},
			"base_role":        schema.StringAttribute{Computed: true},
			"additional_roles": schema.ListAttribute{Computed: true, ElementType: types.StringType},
		},
	}
}

// TestOrganizationUserStateUpgradeV0toV1_PreIssue228EightAttributeShape feeds the
// upgrader the state a v0.24.1 user actually has, through the upgrader's OWN
// declared PriorSchema, and requires the re-key to succeed.
//
// The failure this guards against is not subtle in effect: if the PriorSchema
// cannot decode released state, the upgrade errors, and for this resource a
// resource that cannot come back through an upgrade is a resource that
// re-creates - which here means sending an unexpected invitation to a real
// person.
func TestOrganizationUserStateUpgradeV0toV1_PreIssue228EightAttributeShape(t *testing.T) {
	ctx := context.Background()

	additionalRoles, diags := types.ListValue(types.StringType, []attr.Value{
		types.StringValue("image_reader"),
	})
	if diags.HasError() {
		t.Fatalf("building additional_roles fixture: %v", diags)
	}

	// A realistic v0.24.1 row: populated, not zero-valued. A fixture with empty
	// strings would decode against almost any schema and prove nothing.
	prior := preIssue228OrganizationUserModel{
		ID:              types.StringValue("ide_pre228shape"),
		PermissionLevel: types.StringValue("collaborator"),
		Email:           types.StringValue("pre228@example.com"),
		UserID:          types.StringValue("usr_pre228shape"),
		Name:            types.StringValue("Pre 228 Member"),
		CreatedAt:       types.StringValue("2026-01-01T00:00:00Z"),
		BaseRole:        types.StringValue("collaborator"),
		AdditionalRoles: additionalRoles,
	}

	priorSchema := preIssue228OrganizationUserSchema()
	priorState := tfsdk.State{Schema: priorSchema}
	if d := priorState.Set(ctx, &prior); d.HasError() {
		t.Fatalf("seeding pre-#228 prior state: %v", d)
	}

	// Route through the upgrader's OWN registered PriorSchema. Using the fixture
	// schema on both sides would test the fixture, not the upgrader.
	r := &OrganizationUserResource{}
	upgraders := r.UpgradeState(ctx)
	upgrader, ok := upgraders[0]
	if !ok {
		t.Fatal("no v0 state upgrader registered - SchemaVersion 0 must have one")
	}
	if upgrader.PriorSchema == nil {
		t.Fatal("the v0 upgrader declares no PriorSchema, so released state cannot be decoded at all")
	}

	// Decode the released state THROUGH the upgrader's declared PriorSchema.
	// This is the step that fails when the frozen schema describes the wrong v0.
	decoded := tfsdk.State{Schema: *upgrader.PriorSchema, Raw: priorState.Raw}

	req := resource.UpgradeStateRequest{State: &decoded}
	resp := &resource.UpgradeStateResponse{
		State: tfsdk.State{Schema: organizationUserSchemaV1ForTest(t)},
	}

	upgrader.StateUpgrader(ctx, req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("UPGRADING REAL v0.24.1 STATE FAILED: %v\n\n"+
			"The upgrader's frozen PriorSchema does not describe the v0 that shipped. "+
			"v0.24.1 stored EIGHT attributes (including permission_level, base_role and "+
			"additional_roles, removed by PR #228 with no Version bump); the frozen schema "+
			"declares five. Anyone upgrading from a released version hits this.", resp.Diagnostics)
	}

	var upgraded OrganizationUserResourceModel
	if d := resp.State.Get(ctx, &upgraded); d.HasError() {
		t.Fatalf("reading upgraded state: %v", d)
	}

	// The re-key itself must still have happened.
	if got := upgraded.ID.ValueString(); got != "pre228@example.com" {
		t.Errorf("id was not re-keyed to the email: got %q, want %q", got, "pre228@example.com")
	}
	if got := upgraded.Email.ValueString(); got != "pre228@example.com" {
		t.Errorf("email = %q, want %q", got, "pre228@example.com")
	}
	// The old identity_id must be carried across, not discarded.
	if got := upgraded.IdentityID.ValueString(); got != "ide_pre228shape" {
		t.Errorf("identity_id = %q, want the prior id %q", got, "ide_pre228shape")
	}
}

// organizationUserSchemaV1ForTest returns the CURRENT schema for the upgraded
// state to be written into. Sourced from the resource itself rather than
// hand-copied, because unlike the prior schema there is no risk of sharing a
// blind spot - a v1 schema that is wrong here is wrong in production too, and
// the resource is the single source of truth for it.
func organizationUserSchemaV1ForTest(t *testing.T) schema.Schema {
	t.Helper()
	req := resource.SchemaRequest{}
	resp := &resource.SchemaResponse{}
	(&OrganizationUserResource{}).Schema(context.Background(), req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("building the current schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}
