package provider

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// These tests cover the three invariants anyscale_cloud_access enforces at
// plan time from configuration alone. Every one of them exists because the
// alternative is an apply-time failure against real access grants: a duplicate
// identity that silently half-grants, an empty role that reaches the API as a
// role that does not exist, or a project role the backend 422s partway through
// a reconcile that has already written other members.
//
// The empty-member-set guard is deliberately NOT covered here - it needs prior
// state to answer "is this cloud currently non-empty?", ValidateConfigRequest
// carries only Config, and so that guard lives in ModifyPlan.

// cloudAccessMemberAttrTypes mirrors the schema's member NestedObject.
//
// Spelled out rather than derived from the schema on purpose: the framework
// hard-errors when a fixture's attribute types differ from the schema's in
// either direction, so if the member object ever gains or loses an attribute,
// every fixture below fails loudly at build time and forces this file to be
// revisited - which is the outcome we want, rather than new attributes quietly
// going untested.
func cloudAccessMemberAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"base_role":  types.StringType,
		"deny_roles": types.ListType{ElemType: types.StringType},
		"projects":   types.MapType{ElemType: types.StringType},
	}
}

func cloudAccessMemberObjectType() types.ObjectType {
	return types.ObjectType{AttrTypes: cloudAccessMemberAttrTypes()}
}

// cloudAccessUnmanagedGrantObjectType mirrors the Computed unmanaged_grants
// NestedObject. Needed only so the fixture can hand ValidateConfig a properly
// typed null for that attribute; nothing here asserts on it.
func cloudAccessUnmanagedGrantObjectType() types.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"email":      types.StringType,
		"project_id": types.StringType,
		"reason":     types.StringType,
	}}
}

// cloudAccessMember builds one entry of the member map.
//
// deny_roles and projects are taken as framework values rather than a
// []string / map[string]string because ValidateConfig branches on three states
// a plain Go slice cannot express: declared with content, declared but EMPTY,
// and never declared (null). The distinction is load-bearing - a null
// deny_roles short-circuits the projects check before it is ever reached,
// while an empty declared list runs it and finds no cloud_read_only.
func cloudAccessMember(baseRole string, denyRoles types.List, projects types.Map) types.Object {
	return types.ObjectValueMust(cloudAccessMemberAttrTypes(), map[string]attr.Value{
		"base_role":  types.StringValue(baseRole),
		"deny_roles": denyRoles,
		"projects":   projects,
	})
}

// cloudAccessMemberUnknownRole is cloudAccessMember for the one case that
// cannot be expressed by a Go string: base_role interpolated from a value
// Terraform does not know yet.
func cloudAccessMemberUnknownRole(denyRoles types.List, projects types.Map) types.Object {
	return types.ObjectValueMust(cloudAccessMemberAttrTypes(), map[string]attr.Value{
		"base_role":  types.StringUnknown(),
		"deny_roles": denyRoles,
		"projects":   projects,
	})
}

// cloudAccessDenyRoles builds a KNOWN deny_roles list. Called with no arguments
// it yields an unambiguously EMPTY list, never a null one - types.ListValueMust
// is used rather than ListValueFrom precisely so that "declared as []" can
// never be mistaken for "not declared".
func cloudAccessDenyRoles(values ...string) types.List {
	elems := make([]attr.Value, 0, len(values))
	for _, v := range values {
		elems = append(elems, types.StringValue(v))
	}
	return types.ListValueMust(types.StringType, elems)
}

// cloudAccessNoDenyRoles is the never-declared (null) deny_roles list.
func cloudAccessNoDenyRoles() types.List {
	return types.ListNull(types.StringType)
}

// cloudAccessProjects builds a known projects map from project ID -> role.
func cloudAccessProjects(roles map[string]string) types.Map {
	elems := make(map[string]attr.Value, len(roles))
	for id, role := range roles {
		elems[id] = types.StringValue(role)
	}
	return types.MapValueMust(types.StringType, elems)
}

// cloudAccessNoProjects is the never-declared (null) projects map.
func cloudAccessNoProjects() types.Map {
	return types.MapNull(types.StringType)
}

// cloudAccessMemberMap builds the member map attribute from email -> member.
func cloudAccessMemberMap(members map[string]attr.Value) types.Map {
	return types.MapValueMust(cloudAccessMemberObjectType(), members)
}

// cloudAccessConfigModel wraps a member map in an otherwise realistic
// configuration: cloud_id set, the Computed attributes null (as they are in a
// real config), allow_empty_member_set at its default.
func cloudAccessConfigModel(member types.Map) CloudAccessResourceModel {
	return CloudAccessResourceModel{
		ID:                  types.StringNull(),
		CloudID:             types.StringValue("cld_cloudaccesstest"),
		AllowEmptyMemberSet: types.BoolValue(false),
		Member:              member,
		UnmanagedGrants:     types.ListNull(cloudAccessUnmanagedGrantObjectType()),
	}
}

// runCloudAccessValidateConfig drives the real ValidateConfig against a
// configuration built from the resource's own schema.
//
// tfsdk.Config has no Set method in this framework version, so the raw value is
// built through a tfsdk.Plan holder (which does) and its Raw handed to the
// Config - the same construction cloud_helpers_test.go uses. Building from the
// real schema rather than a synthetic one matters: it is what makes a fixture
// that disagrees with the shipped schema fail here instead of passing against a
// shape the provider never actually serves.
func runCloudAccessValidateConfig(t *testing.T, model CloudAccessResourceModel) diag.Diagnostics {
	t.Helper()
	ctx := context.Background()

	r := &CloudAccessResource{}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	holder := tfsdk.Plan{
		Schema: schemaResp.Schema,
		Raw:    tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil),
	}
	if diags := holder.Set(ctx, &model); diags.HasError() {
		t.Fatalf("failed to build config fixture: %v", diags)
	}

	resp := &resource.ValidateConfigResponse{}
	r.ValidateConfig(ctx, resource.ValidateConfigRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: holder.Raw},
	}, resp)
	return resp.Diagnostics
}

// cloudAccessFindError returns the first error-severity diagnostic with the
// given summary, or nil. Matching on summary rather than on "any error at all"
// keeps a test from passing because some unrelated check happened to fire.
func cloudAccessFindError(diags diag.Diagnostics, summary string) diag.Diagnostic {
	for _, d := range diags {
		if d.Severity() == diag.SeverityError && d.Summary() == summary {
			return d
		}
	}
	return nil
}

// cloudAccessAssertOnlyErrorSummary fails if any error carries a summary other
// than the expected one - a config crafted to trip exactly one invariant should
// not also be tripping another.
func cloudAccessAssertOnlyErrorSummary(t *testing.T, diags diag.Diagnostics, summary string) {
	t.Helper()
	for _, d := range diags {
		if d.Severity() == diag.SeverityError && d.Summary() != summary {
			t.Errorf("unexpected additional error %q: %s - this fixture is meant to trip only %q, so a second error means it is testing something other than what it claims",
				d.Summary(), d.Detail(), summary)
		}
	}
}

// cloudAccessAssertErrorPath fails unless the diagnostic is attribute-scoped to
// the expected path. The path is what puts the error on the right line in the
// practitioner's editor; an unscoped error on a map with fifty members is
// nearly useless.
func cloudAccessAssertErrorPath(t *testing.T, d diag.Diagnostic, want path.Path) {
	t.Helper()
	withPath, ok := d.(diag.DiagnosticWithPath)
	if !ok {
		t.Errorf("diagnostic %q is not attribute-scoped; it must point at %s so the practitioner knows which line to fix", d.Summary(), want)
		return
	}
	if !withPath.Path().Equal(want) {
		t.Errorf("diagnostic %q is scoped to %s, want %s", d.Summary(), withPath.Path(), want)
	}
}

// TestCloudAccessValidateConfig_RejectsCaseInsensitiveDuplicateEmails covers
// the invariant Terraform itself cannot enforce. The member map is keyed by
// email, and Terraform does reject duplicate keys - but with a case-SENSITIVE
// comparison, so "Alice@x.com" and "alice@x.com" are two perfectly valid keys
// to Terraform and one single person to Anyscale. Left alone, one grant
// silently overwrites the other and the config reads as though it declared two
// different people with two different roles.
//
// The assertion that both spellings appear in the message is the point of the
// test, not decoration: the practitioner is staring at a map that Terraform
// says is fine, and an error that names only one of the two colliding keys
// leaves them hunting for the other.
func TestCloudAccessValidateConfig_RejectsCaseInsensitiveDuplicateEmails(t *testing.T) {
	const wantSummary = "Duplicate Member Email"

	// The two spellings are asserted in their QUOTED form (%q, matching how the
	// diagnostic renders them) rather than bare. That is required for the
	// whitespace case: "  bob@x.com" contains "bob@x.com" as a plain substring,
	// so a bare Contains check would be satisfied by the padded spelling alone
	// and would pass even if the message named only one key.
	for _, tc := range []struct {
		name    string
		keyA    string
		keyB    string
		wantDup bool
	}{
		{
			name:    "same address differing only in capitalization",
			keyA:    "Alice@example.com",
			keyB:    "alice@example.com",
			wantDup: true,
		},
		{
			name:    "same address differing only in surrounding whitespace",
			keyA:    "  bob@example.com",
			keyB:    "bob@example.com",
			wantDup: true,
		},
		{
			name:    "genuinely different addresses are not a collision",
			keyA:    "alice@example.com",
			keyB:    "bob@example.com",
			wantDup: false,
		},
		{
			name:    "different local parts that only share a domain are not a collision",
			keyA:    "Alice@example.com",
			keyB:    "alicia@example.com",
			wantDup: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diags := runCloudAccessValidateConfig(t, cloudAccessConfigModel(cloudAccessMemberMap(map[string]attr.Value{
				tc.keyA: cloudAccessMember("writer", cloudAccessNoDenyRoles(), cloudAccessNoProjects()),
				tc.keyB: cloudAccessMember("collaborator", cloudAccessNoDenyRoles(), cloudAccessNoProjects()),
			})))

			if !tc.wantDup {
				if diags.HasError() {
					t.Fatalf("unexpected error for two distinct people (%q and %q): %v - a false positive here blocks a legitimate config outright",
						tc.keyA, tc.keyB, diags)
				}
				return
			}

			d := cloudAccessFindError(diags, wantSummary)
			if d == nil {
				t.Fatalf("expected a %q error for %q vs %q - Terraform's own duplicate-key check is case-sensitive and will not catch this, so nothing else will. Got: %v",
					wantSummary, tc.keyA, tc.keyB, diags)
			}
			cloudAccessAssertOnlyErrorSummary(t, diags, wantSummary)
			cloudAccessAssertErrorPath(t, d, path.Root("member"))

			detail := d.Detail()
			for _, spelling := range []string{tc.keyA, tc.keyB} {
				if quoted := fmt.Sprintf("%q", spelling); !strings.Contains(detail, quoted) {
					t.Errorf("error detail does not name %s: %s\nBOTH colliding spellings must appear - the practitioner is looking at a map Terraform accepted, and naming only one key leaves them hunting for its partner",
						quoted, detail)
				}
			}
		})
	}
}

// TestCloudAccessValidateConfig_RejectsEmptyBaseRole covers the gap Required
// leaves open. Required stops base_role from being null or omitted; it does
// nothing about an empty string, which is exactly what an interpolation off a
// missing map entry or an unset variable produces. That value would reach the
// API as a cloud role that does not exist.
func TestCloudAccessValidateConfig_RejectsEmptyBaseRole(t *testing.T) {
	const (
		wantSummary = "Empty Cloud Role"
		email       = "empty-role@example.com"
	)

	for _, tc := range []struct {
		name      string
		member    types.Object
		wantError bool
	}{
		{
			name:      "empty string",
			member:    cloudAccessMember("", cloudAccessNoDenyRoles(), cloudAccessNoProjects()),
			wantError: true,
		},
		{
			// Whitespace-only is the shape an interpolation of a padded variable
			// produces. It is not "nearly empty" to the API - it is a role name
			// that does not exist, same as "".
			name:      "whitespace only",
			member:    cloudAccessMember("   ", cloudAccessNoDenyRoles(), cloudAccessNoProjects()),
			wantError: true,
		},
		{
			name:      "a real role passes",
			member:    cloudAccessMember("writer", cloudAccessNoDenyRoles(), cloudAccessNoProjects()),
			wantError: false,
		},
		{
			// Unknown means the role comes from another resource that has not
			// been applied yet. There is nothing to check, and erroring here
			// would reject a perfectly valid config; Terraform validates again
			// once the value is known.
			name:      "unknown (interpolated from another resource) is not an error yet",
			member:    cloudAccessMemberUnknownRole(cloudAccessNoDenyRoles(), cloudAccessNoProjects()),
			wantError: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diags := runCloudAccessValidateConfig(t, cloudAccessConfigModel(cloudAccessMemberMap(map[string]attr.Value{
				email: tc.member,
			})))

			if !tc.wantError {
				if diags.HasError() {
					t.Fatalf("unexpected error: %v", diags)
				}
				return
			}

			d := cloudAccessFindError(diags, wantSummary)
			if d == nil {
				t.Fatalf("expected an %q error - base_role being Required does not stop an empty or whitespace-only string, which reaches the API as a role that does not exist. Got: %v",
					wantSummary, diags)
			}
			cloudAccessAssertOnlyErrorSummary(t, diags, wantSummary)
			// Scoped to that member's base_role specifically, not to the member
			// map as a whole - with many members, "one of these has an empty
			// role" is not an actionable message.
			cloudAccessAssertErrorPath(t, d, path.Root("member").AtMapKey(email).AtName("base_role"))

			if !strings.Contains(d.Detail(), email) {
				t.Errorf("error detail does not name the offending member %q: %s", email, d.Detail())
			}
		})
	}
}

// TestCloudAccessValidateConfig_RejectsProjectRoleConflictingWithCloudReadOnly
// covers the cross-field constraint that is the entire reason projects nest
// inside members rather than living in a sibling resource: a member carrying
// the cloud_read_only deny role may only hold readonly on that cloud's
// projects, and the backend 422s anything else.
//
// Split across two resources this could only ever surface as a failure partway
// through an apply, after other grants had already been written. Here it is a
// plan-time error, which is only worth anything if it actually fires - hence
// this test - and only worth anything if it does not fire on the legitimate
// combinations, hence the passing cases.
func TestCloudAccessValidateConfig_RejectsProjectRoleConflictingWithCloudReadOnly(t *testing.T) {
	const (
		wantSummary = "Project Role Conflicts With cloud_read_only"
		email       = "restricted@example.com"
		projectID   = "prj_1"
	)

	for _, tc := range []struct {
		name      string
		denyRoles types.List
		projects  types.Map
		wantError bool
	}{
		{
			name:      "cloud_read_only with a write project role is rejected",
			denyRoles: cloudAccessDenyRoles(cloudAccessReadOnlyDenyRole),
			projects:  cloudAccessProjects(map[string]string{projectID: "write"}),
			wantError: true,
		},
		{
			// owner is the other role the backend rejects for a cloud_read_only
			// member, included so the check is proven to be "anything but
			// readonly" rather than a special case for the string "write".
			name:      "cloud_read_only with an owner project role is rejected",
			denyRoles: cloudAccessDenyRoles(cloudAccessReadOnlyDenyRole),
			projects:  cloudAccessProjects(map[string]string{projectID: "owner"}),
			wantError: true,
		},
		{
			name:      "cloud_read_only with readonly is the one allowed combination",
			denyRoles: cloudAccessDenyRoles(cloudAccessReadOnlyDenyRole),
			projects:  cloudAccessProjects(map[string]string{projectID: cloudAccessReadOnlyProjectRole}),
			wantError: false,
		},
		{
			// Declared-but-EMPTY deny_roles, deliberately: this reaches the
			// cloud_read_only membership check and finds it absent. A null
			// deny_roles would short-circuit earlier and so cannot prove that
			// check behaves, which is why both states are exercised.
			name:      "write with an empty deny_roles list is allowed",
			denyRoles: cloudAccessDenyRoles(),
			projects:  cloudAccessProjects(map[string]string{projectID: "write"}),
			wantError: false,
		},
		{
			name:      "write with deny_roles never declared is allowed",
			denyRoles: cloudAccessNoDenyRoles(),
			projects:  cloudAccessProjects(map[string]string{projectID: "write"}),
			wantError: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diags := runCloudAccessValidateConfig(t, cloudAccessConfigModel(cloudAccessMemberMap(map[string]attr.Value{
				email: cloudAccessMember("collaborator", tc.denyRoles, tc.projects),
			})))

			if !tc.wantError {
				if diags.HasError() {
					t.Fatalf("unexpected error: %v - this combination is accepted by the backend, and rejecting it blocks a legitimate config at plan time", diags)
				}
				return
			}

			d := cloudAccessFindError(diags, wantSummary)
			if d == nil {
				t.Fatalf("expected a %q error - the backend 422s this combination, and catching it only at apply time means failing partway through a reconcile that has already written other members. Got: %v",
					wantSummary, diags)
			}
			cloudAccessAssertOnlyErrorSummary(t, diags, wantSummary)
			// Scoped to the individual project entry: a member may hold roles on
			// many projects and only one of them is the problem.
			cloudAccessAssertErrorPath(t, d, path.Root("member").AtMapKey(email).AtName("projects").AtMapKey(projectID))

			detail := d.Detail()
			if !strings.Contains(detail, projectID) {
				t.Errorf("error detail does not name project %q: %s - a member can hold roles on many projects, so the message has to say which one conflicts", projectID, detail)
			}
			if !strings.Contains(detail, email) {
				t.Errorf("error detail does not name the member %q: %s", email, detail)
			}
			if !strings.Contains(detail, cloudAccessReadOnlyDenyRole) {
				t.Errorf("error detail does not name the %q deny role that causes the conflict: %s - without it the practitioner cannot tell which of the two sides to change", cloudAccessReadOnlyDenyRole, detail)
			}
		})
	}
}

// TestCloudAccessValidateConfig_MemberMapShortCircuits covers the states where
// there is nothing to validate. Unknown is the one that matters: a member map
// built from another resource's output is unknown at validate time, and any
// check that tried to iterate it would either panic or produce a confident,
// wrong verdict about a config Terraform will hand back once the value is
// known.
func TestCloudAccessValidateConfig_MemberMapShortCircuits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		member types.Map
	}{
		{
			name:   "unknown (computed from another resource)",
			member: types.MapUnknown(cloudAccessMemberObjectType()),
		},
		{
			name:   "null",
			member: types.MapNull(cloudAccessMemberObjectType()),
		},
		{
			// An empty map is a legitimate, checkable config as far as
			// ValidateConfig is concerned - the guard against emptying a
			// populated cloud needs prior state and so lives in ModifyPlan, not
			// here. If this ever starts erroring, that guard has been put in the
			// wrong place and will reject an intentional empty set on a brand
			// new cloud.
			name:   "empty (the empty-set guard is ModifyPlan's job, not this method's)",
			member: cloudAccessMemberMap(map[string]attr.Value{}),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diags := runCloudAccessValidateConfig(t, cloudAccessConfigModel(tc.member))
			if len(diags) != 0 {
				t.Fatalf("expected no diagnostics at all, got: %v", diags)
			}
		})
	}
}

// runCloudAccessModifyPlan drives the real ModifyPlan with a prior state and a
// planned state, which is the only way to exercise the empty-member-set guard:
// the question it answers ("would this empty a cloud that currently has
// members?") needs both, and is precisely why the guard cannot live in
// ValidateConfig.
func runCloudAccessModifyPlan(t *testing.T, priorState, planned *CloudAccessResourceModel) diag.Diagnostics {
	t.Helper()
	ctx := context.Background()

	r := &CloudAccessResource{}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}
	rawType := schemaResp.Schema.Type().TerraformType(ctx)

	build := func(m *CloudAccessResourceModel, what string) tftypes.Value {
		if m == nil {
			// A null raw value is how the framework represents "no prior state"
			// (create) or "no plan" (destroy).
			return tftypes.NewValue(rawType, nil)
		}
		holder := tfsdk.Plan{Schema: schemaResp.Schema, Raw: tftypes.NewValue(rawType, nil)}
		if diags := holder.Set(ctx, m); diags.HasError() {
			t.Fatalf("failed to build %s fixture: %v", what, diags)
		}
		return holder.Raw
	}

	resp := &resource.ModifyPlanResponse{}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: build(priorState, "prior state")},
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: build(planned, "plan")},
	}, resp)
	return resp.Diagnostics
}

// TestCloudAccessModifyPlan_RefusesToEmptyACloudThatHasMembers covers the single
// most destructive thing this resource can do by accident.
//
// The guard's whole reason for existing is that a for_each over a mistyped or
// missing upstream group renders as an EMPTY member map, byte-identical to a
// deliberate purge - so the opt-in is a separate boolean rather than a
// distinction drawn from the map itself.
func TestCloudAccessModifyPlan_RefusesToEmptyACloudThatHasMembers(t *testing.T) {
	populated := cloudAccessMemberMap(map[string]attr.Value{
		"alice@example.com": cloudAccessMember("writer", cloudAccessNoDenyRoles(), cloudAccessNoProjects()),
		"bob@example.com":   cloudAccessMember("collaborator", cloudAccessNoDenyRoles(), cloudAccessNoProjects()),
	})
	empty := cloudAccessMemberMap(map[string]attr.Value{})

	emptyPlan := func(allow bool) *CloudAccessResourceModel {
		m := cloudAccessConfigModel(empty)
		m.AllowEmptyMemberSet = types.BoolValue(allow)
		return &m
	}
	priorWithMembers := func() *CloudAccessResourceModel {
		m := cloudAccessConfigModel(populated)
		return &m
	}

	t.Run("emptying a populated cloud without the opt-in is refused, naming the count", func(t *testing.T) {
		diags := runCloudAccessModifyPlan(t, priorWithMembers(), emptyPlan(false))

		d := cloudAccessFindError(diags, "Refusing To Revoke Every Member Of This Cloud")
		if d == nil {
			t.Fatalf("expected the empty-member-set guard to refuse this plan - without it, a mistyped group name silently revokes every member of the cloud. Got: %v", diags)
		}
		// The count is the blast radius. "Refusing to empty member set" without it
		// tells the user nothing about what they nearly did.
		if !strings.Contains(d.Detail(), "2 member(s)") {
			t.Errorf("the diagnostic must state HOW MANY members would be revoked; got detail: %s", d.Detail())
		}
		if !strings.Contains(d.Detail(), "cld_cloudaccesstest") {
			t.Errorf("the diagnostic must name the cloud; got detail: %s", d.Detail())
		}
		if !strings.Contains(d.Detail(), "allow_empty_member_set") {
			t.Errorf("the diagnostic must name the attribute that permits this; got detail: %s", d.Detail())
		}
	})

	t.Run("emptying a populated cloud WITH the opt-in is allowed", func(t *testing.T) {
		diags := runCloudAccessModifyPlan(t, priorWithMembers(), emptyPlan(true))
		if diags.HasError() {
			t.Fatalf("allow_empty_member_set = true is the documented way to do this deliberately; it must not be blocked. Got: %v", diags)
		}
	})

	t.Run("creating with an empty member map is allowed - there is nobody to revoke", func(t *testing.T) {
		// nil prior state is how the framework represents a create.
		diags := runCloudAccessModifyPlan(t, nil, emptyPlan(false))
		if diags.HasError() {
			t.Fatalf("an empty member map on CREATE revokes nobody and must not be blocked. Got: %v", diags)
		}
	})

	t.Run("a non-empty plan is never blocked", func(t *testing.T) {
		planned := cloudAccessConfigModel(populated)
		diags := runCloudAccessModifyPlan(t, priorWithMembers(), &planned)
		if diags.HasError() {
			t.Fatalf("a plan that declares members must never trip the empty-set guard. Got: %v", diags)
		}
	})

	t.Run("destroy is not blocked by this guard", func(t *testing.T) {
		// A nil plan is a destroy. It is governed by prevent_destroy guidance on the
		// resource page, not by this attribute - blocking it here would make the
		// resource impossible to remove.
		diags := runCloudAccessModifyPlan(t, priorWithMembers(), nil)
		if diags.HasError() {
			t.Fatalf("destroy must not be blocked by the empty-member-set guard. Got: %v", diags)
		}
	})
}

// TestCloudAccessValidateConfig_RejectsCloudWriteRoleOnAProject covers the one
// vocabulary collision in this resource that is a near-homograph rather than a
// distinct word: the cloud role is spelled "writer" and the project role is
// spelled "write". A project 422s on "writer".
//
// Rejecting it at plan time matters more here than for a typical typo, because
// this resource reconciles a whole member list in one apply - an apply-time 422
// on one project entry lands after other members have already been written.
//
// The check deliberately does NOT reject the mirror-image mistake (a cloud
// base_role of "write"): base_role carries no OneOf because the roles API is
// actively extended, so a hard rejection there could only be lifted by a
// provider release. The cases below pin that asymmetry down so it cannot be
// "tidied up" into a symmetric check without a deliberate decision.
func TestCloudAccessValidateConfig_RejectsCloudWriteRoleOnAProject(t *testing.T) {
	const (
		wantSummary = "Cloud Role Spelling Used For A Project Role"
		email       = "typo@example.com"
		projectID   = "prj_1"
	)

	t.Run("writer on a project is rejected, with the right value suggested", func(t *testing.T) {
		diags := runCloudAccessValidateConfig(t, cloudAccessConfigModel(cloudAccessMemberMap(map[string]attr.Value{
			email: cloudAccessMember(cloudAccessCloudWriteRole, cloudAccessNoDenyRoles(),
				cloudAccessProjects(map[string]string{projectID: cloudAccessCloudWriteRole})),
		})))

		d := cloudAccessFindError(diags, wantSummary)
		if d == nil {
			t.Fatalf("expected a %q error: the API 422s a project role of %q, and catching it only at apply time fails partway through a reconcile that has already written other members. Got: %v",
				wantSummary, cloudAccessCloudWriteRole, diags)
		}
		cloudAccessAssertOnlyErrorSummary(t, diags, wantSummary)
		// Scoped to the offending entry: a member may hold roles on many projects.
		cloudAccessAssertErrorPath(t, d, path.Root("member").AtMapKey(email).AtName("projects").AtMapKey(projectID))

		detail := d.Detail()
		// The suggestion is the whole value of this check over a bare "invalid
		// role" rejection - without it the practitioner is told the word is wrong
		// but not which near-identical word to use.
		if !strings.Contains(detail, fmt.Sprintf("Did you mean %q?", cloudAccessProjectWriteRole)) {
			t.Errorf("error detail does not suggest %q: %s", cloudAccessProjectWriteRole, detail)
		}
		if !strings.Contains(detail, projectID) {
			t.Errorf("error detail does not name project %q: %s", projectID, detail)
		}
		if !strings.Contains(detail, email) {
			t.Errorf("error detail does not name the member %q: %s", email, detail)
		}
	})

	for _, tc := range []struct {
		name     string
		baseRole string
		projects types.Map
	}{
		{
			name:     "write on a project is the correct spelling and is allowed",
			baseRole: cloudAccessCloudWriteRole,
			projects: cloudAccessProjects(map[string]string{projectID: cloudAccessProjectWriteRole}),
		},
		{
			// The mirror-image mistake, deliberately allowed through - see the
			// comment on this test and on the constants in the resource.
			name:     "write as a cloud base_role is NOT rejected here",
			baseRole: cloudAccessProjectWriteRole,
			projects: cloudAccessNoProjects(),
		},
		{
			// Proves the check is exact rather than a substring or prefix match:
			// "writer" appears inside a longer role name that is not the typo.
			name:     "a longer role name containing writer is not the typo",
			baseRole: cloudAccessCloudWriteRole,
			projects: cloudAccessProjects(map[string]string{projectID: "writer_of_nothing"}),
		},
		{
			name:     "no projects declared at all",
			baseRole: cloudAccessCloudWriteRole,
			projects: cloudAccessNoProjects(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diags := runCloudAccessValidateConfig(t, cloudAccessConfigModel(cloudAccessMemberMap(map[string]attr.Value{
				email: cloudAccessMember(tc.baseRole, cloudAccessNoDenyRoles(), tc.projects),
			})))
			if diags.HasError() {
				t.Fatalf("unexpected error: %v - rejecting this blocks a legitimate config at plan time", diags)
			}
		})
	}

	// A project role interpolated from an unresolved value is unknown at validate
	// time. Reading it as a Go string is an error, so the check has to skip it -
	// and this case is what proves it skips rather than reporting a spurious
	// failure against a value nobody has typed wrong yet. Terraform validates
	// again once the value is known.
	t.Run("an unknown project role is skipped, not reported", func(t *testing.T) {
		projects, diags := types.MapValue(types.StringType, map[string]attr.Value{
			projectID: types.StringUnknown(),
		})
		if diags.HasError() {
			t.Fatalf("building the fixture failed: %v", diags)
		}

		got := runCloudAccessValidateConfig(t, cloudAccessConfigModel(cloudAccessMemberMap(map[string]attr.Value{
			email: cloudAccessMember(cloudAccessCloudWriteRole, cloudAccessNoDenyRoles(), projects),
		})))
		if got.HasError() {
			t.Fatalf("unexpected error for an unknown project role: %v", got)
		}
	})

	// The typo check runs BEFORE the deny-role short-circuit in ValidateConfig,
	// so both errors surface from one validate pass rather than the practitioner
	// fixing one and discovering the other. A null deny_roles is the common case
	// and it short-circuits, so ordering the checks the other way would leave
	// this validator silent for most configs that hit the typo.
	t.Run("reported alongside the cloud_read_only conflict", func(t *testing.T) {
		diags := runCloudAccessValidateConfig(t, cloudAccessConfigModel(cloudAccessMemberMap(map[string]attr.Value{
			email: cloudAccessMember("collaborator", cloudAccessDenyRoles(cloudAccessReadOnlyDenyRole),
				cloudAccessProjects(map[string]string{projectID: cloudAccessCloudWriteRole})),
		})))

		if cloudAccessFindError(diags, wantSummary) == nil {
			t.Errorf("expected the %q error: %v", wantSummary, diags)
		}
		if cloudAccessFindError(diags, "Project Role Conflicts With cloud_read_only") == nil {
			t.Errorf("expected the cloud_read_only conflict error as well - both are true of this entry and one validate pass should report both: %v", diags)
		}
	})
}
