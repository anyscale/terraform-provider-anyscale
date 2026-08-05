package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// mockOrgUserRoleServer is a scripted stand-in for the four organization
// collaborator endpoints this resource touches, plus the member DELETE it must
// never call.
//
// The single most important fixture behavior here is deliberate and looks like
// a bug if you skim it: the LIST endpoint always reports additional_roles as an
// empty slice, no matter what deny roles the member really holds, while the
// SINGULAR per-user endpoint reports the real set. That is not mock
// convenience - it is the real API's behavior (the list formatter hardcodes the
// field), and it is the entire reason findOrgCollaboratorByEmail has to make a
// second call. A mock that served the true roles from the list would let a
// list-only Read pass, which is exactly the shape of miss that has shipped bugs
// in this repo before.
//
// Every request is recorded in an ordered callLog as "METHOD path", and the
// last request body per write endpoint is captured both decoded and raw, so
// tests can assert on what was SENT rather than only on the final state - the
// two write paths can converge on the same end state while differing on the one
// thing that matters (which endpoint carried the write).
type mockOrgUserRoleServer struct {
	mu sync.Mutex

	// members: lowercased email -> the member's current backend state.
	members map[string]*orgUserRoleMemberFixture

	callLog []string

	// Last decoded + raw bodies per write endpoint. The raw copy matters for
	// additional_roles specifically: a decoded []string cannot distinguish a
	// field that arrived as [] from one that arrived as null or was omitted,
	// and "sent an explicit empty array" is precisely what test 2 asserts.
	lastLegacyBody    *UpdateOrganizationCollaboratorRequest
	lastLegacyRaw     string
	lastRolesBody     *SetOrganizationRolesRequest
	lastRolesRaw      string
	rolesEndpoint501  bool
	rolesEndpoint501D string
}

// orgUserRoleMemberFixture is one organization member's backend state. Named with the
// orgUserRole prefix rather than the plain orgMemberFixture to avoid a name collision with a
// fixture type in resource_cloud_user_role_test.go, since removed - kept as-is rather than
// renamed, since renaming now would touch every call site for no behavioral benefit.
type orgUserRoleMemberFixture struct {
	identityID string
	userID     string
	// email keeps the ORIGINAL casing the fixture was seeded with, while the
	// map key is lowercased - so the EqualFold match in
	// findOrgCollaboratorByEmail/resolveIdentityForEmail is genuinely
	// exercised rather than trivially satisfied.
	email           string
	baseRole        string
	additionalRoles []string
}

func newMockOrgUserRoleServer(t *testing.T) (*httptest.Server, *mockOrgUserRoleServer) {
	t.Helper()
	s := &mockOrgUserRoleServer{
		members:           map[string]*orgUserRoleMemberFixture{},
		rolesEndpoint501D: "Feature is not enabled for this organization.",
	}
	server := httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(server.Close)
	return server, s
}

// seedMember registers an existing organization member. This resource manages
// the role of someone who is ALREADY a member and never adds one, so every
// test must seed its target first, exactly as reality requires.
func (s *mockOrgUserRoleServer) seedMember(email, identityID, userID, baseRole string, additionalRoles []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members[strings.ToLower(email)] = &orgUserRoleMemberFixture{
		identityID:      identityID,
		userID:          userID,
		email:           email,
		baseRole:        baseRole,
		additionalRoles: append([]string{}, additionalRoles...),
	}
}

func (s *mockOrgUserRoleServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	// Local name is reqPath, not path: the framework's path package is imported
	// in this file for path.Root, and shadowing it here would be a trap for the
	// next reader even though the compiler allows it.
	reqPath := r.URL.Path
	s.callLog = append(s.callLog, fmt.Sprintf("%s %s", r.Method, reqPath))

	const collabBase = "/api/v2/organization_collaborators"

	switch {
	case r.Method == http.MethodGet && reqPath == collabBase:
		s.handleList(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(reqPath, collabBase+"/"):
		s.handleSingular(w, strings.TrimPrefix(reqPath, collabBase+"/"))
	case r.Method == http.MethodPut && strings.HasPrefix(reqPath, collabBase+"/") && strings.HasSuffix(reqPath, "/roles"):
		s.handleSetRoles(w, r, strings.TrimSuffix(strings.TrimPrefix(reqPath, collabBase+"/"), "/roles"))
	case r.Method == http.MethodPut && strings.HasPrefix(reqPath, collabBase+"/"):
		s.handleSetPermissionLevel(w, r, strings.TrimPrefix(reqPath, collabBase+"/"))
	case r.Method == http.MethodDelete && strings.HasPrefix(reqPath, collabBase+"/"):
		// Answered 204, deliberately. If this endpoint returned an error the
		// tests below could pass for the wrong reason - a Delete that wrongly
		// evicted the member would fail on the error rather than on the fact
		// that it called this at all. The assertion is on the call log.
		s.handleMemberDelete(w, strings.TrimPrefix(reqPath, collabBase+"/"))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"error":{"detail":"not found: %s %s"}}`, r.Method, reqPath)
	}
}

// handleList serves GET /api/v2/organization_collaborators, honoring the
// server-side email narrowing hint the provider passes.
//
// additional_roles is hardcoded to [] here on purpose - see the type comment.
func (s *mockOrgUserRoleServer) handleList(w http.ResponseWriter, r *http.Request) {
	emailFilter := strings.ToLower(r.URL.Query().Get("email"))

	results := make([]OrganizationCollaboratorResult, 0, len(s.members))
	for email, m := range s.members {
		if emailFilter != "" && email != emailFilter {
			continue
		}
		userID := m.userID
		results = append(results, OrganizationCollaboratorResult{
			ID:              m.identityID,
			UserID:          &userID,
			Email:           m.email,
			PermissionLevel: m.baseRole,
			CreatedAt:       "2026-07-28T00:00:00.000000+00:00",
			BaseRole:        m.baseRole,
			// THE FALSE EMPTY. The real list formatter reports [] regardless of
			// the member's actual deny roles; reading roles from here yields a
			// false empty, not a missing value.
			AdditionalRoles: []string{},
		})
	}

	listResp := OrganizationCollaboratorsListResponse{Results: results}
	listResp.Metadata.Total = len(results)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(listResp)
}

// handleSingular serves GET /api/v2/organization_collaborators/{user_id} - the
// only read path that reports a member's REAL additional_roles. Keyed by
// user_id, not identity_id.
func (s *mockOrgUserRoleServer) handleSingular(w http.ResponseWriter, userID string) {
	m := s.memberByUserIDLocked(userID)
	if m == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"error":{"detail":"No user found with id %s."}}`, userID)
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(OrganizationCollaboratorSingularResponse{
		Result: OrganizationCollaboratorResult{
			ID:              m.identityID,
			UserID:          &m.userID,
			Email:           m.email,
			PermissionLevel: m.baseRole,
			CreatedAt:       "2026-07-28T00:00:00.000000+00:00",
			BaseRole:        m.baseRole,
			AdditionalRoles: append([]string{}, m.additionalRoles...),
		},
	})
}

// handleSetRoles serves the GATED
// PUT /api/v2/organization_collaborators/{user_id}/roles - the only path that
// can write a base role and additional roles together, and the only one that
// can CLEAR additional roles.
func (s *mockOrgUserRoleServer) handleSetRoles(w http.ResponseWriter, r *http.Request, userID string) {
	raw, _ := io.ReadAll(r.Body)
	s.lastRolesRaw = string(raw)

	var body SetOrganizationRolesRequest
	_ = json.Unmarshal(raw, &body)
	// Captured as a detached copy: storing the decoded slice directly would
	// leave the capture aliasing the same backing array the fixture below
	// mutates, so a later write could silently rewrite what a test believes it
	// observed on the wire.
	captured := SetOrganizationRolesRequest{BaseRole: body.BaseRole}
	if body.AdditionalRoles != nil {
		captured.AdditionalRoles = append([]string{}, body.AdditionalRoles...)
	}
	s.lastRolesBody = &captured

	if s.rolesEndpoint501 {
		// The feature-gate response: the roles API is behind an organization
		// flag, surfaced as a 501.
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = fmt.Fprintf(w, `{"error":{"detail":%q}}`, s.rolesEndpoint501D)
		return
	}

	m := s.memberByUserIDLocked(userID)
	if m == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"error":{"detail":"No user found with id %s."}}`, userID)
		return
	}

	m.baseRole = body.BaseRole
	m.additionalRoles = append([]string{}, body.AdditionalRoles...)
	w.WriteHeader(http.StatusNoContent)
}

// handleSetPermissionLevel serves the LEGACY, ungated
// PUT /api/v2/organization_collaborators/{identity_id}. Its body has exactly
// one slot, so it structurally cannot express deny roles - it leaves whatever
// additional_roles the member already had untouched, which is the real
// behavior that makes it unusable for clearing them.
func (s *mockOrgUserRoleServer) handleSetPermissionLevel(w http.ResponseWriter, r *http.Request, identityID string) {
	raw, _ := io.ReadAll(r.Body)
	s.lastLegacyRaw = string(raw)

	var body UpdateOrganizationCollaboratorRequest
	_ = json.Unmarshal(raw, &body)
	captured := body
	s.lastLegacyBody = &captured

	m := s.memberByIdentityIDLocked(identityID)
	if m == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"error":{"detail":"No identity found with id %s."}}`, identityID)
		return
	}

	m.baseRole = body.PermissionLevel
	w.WriteHeader(http.StatusNoContent)
}

// handleMemberDelete serves DELETE /api/v2/organization_collaborators/{identity_id},
// the endpoint that removes a human from the organization entirely. This
// resource must never call it; the fixture answers successfully so that a
// regression surfaces as an unwanted call-log entry rather than as an error.
func (s *mockOrgUserRoleServer) handleMemberDelete(w http.ResponseWriter, identityID string) {
	for email, m := range s.members {
		if m.identityID == identityID {
			delete(s.members, email)
			break
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *mockOrgUserRoleServer) memberByUserIDLocked(userID string) *orgUserRoleMemberFixture {
	for _, m := range s.members {
		if m.userID == userID {
			return m
		}
	}
	return nil
}

func (s *mockOrgUserRoleServer) memberByIdentityIDLocked(identityID string) *orgUserRoleMemberFixture {
	for _, m := range s.members {
		if m.identityID == identityID {
			return m
		}
	}
	return nil
}

func (s *mockOrgUserRoleServer) callLogSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.callLog))
	copy(out, s.callLog)
	return out
}

func (s *mockOrgUserRoleServer) legacyBody() *UpdateOrganizationCollaboratorRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastLegacyBody
}

func (s *mockOrgUserRoleServer) rolesBody() (*SetOrganizationRolesRequest, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRolesBody, s.lastRolesRaw
}

func (s *mockOrgUserRoleServer) memberState(t *testing.T, email string) orgUserRoleMemberFixture {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.members[strings.ToLower(email)]
	if !ok {
		t.Fatalf("member %s is no longer present in the fixture", email)
	}
	return *m
}

// orgRoleCallLogHas reports whether an exact "METHOD path" call was made.
func orgRoleCallLogHas(log []string, call string) bool {
	for _, c := range log {
		if c == call {
			return true
		}
	}
	return false
}

// orgRoleCallLogHasRolesPUT reports whether ANY call hit the gated roles
// endpoint, without the test needing to know the user_id in the path.
func orgRoleCallLogHasRolesPUT(log []string) bool {
	for _, c := range log {
		if strings.HasPrefix(c, "PUT ") && strings.HasSuffix(c, "/roles") {
			return true
		}
	}
	return false
}

// orgRoleCallLogHasMemberDelete reports whether the member-eviction DELETE was
// called at all - the single call this resource must never make.
func orgRoleCallLogHasMemberDelete(log []string) bool {
	for _, c := range log {
		if strings.HasPrefix(c, "DELETE ") {
			return true
		}
	}
	return false
}

// runOrganizationUserRoleCreate drives OrganizationUserRoleResource's real
// Create() end to end, the same pattern resource_cloud_user_role_test.go
// established.
func runOrganizationUserRoleCreate(t *testing.T, r *OrganizationUserRoleResource, plan OrganizationUserRoleResourceModel) (OrganizationUserRoleResourceModel, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	tfPlan := tfsdk.Plan{Schema: schemaResp.Schema}
	if diags := tfPlan.Set(ctx, &plan); diags.HasError() {
		t.Fatalf("failed to build plan fixture: %v", diags)
	}
	tfState := tfsdk.State{Schema: schemaResp.Schema}

	// Build the CONFIG too, because that - not the plan - is what selects the
	// write path. Rule: a plan value that is Null or Unknown means the config
	// omitted the attribute; a known value means it declared it. That mapping is
	// exact for Create, where there is no prior state for UseStateForUnknown to
	// carry forward.
	configModel := plan
	if plan.DenyRoles.IsUnknown() {
		configModel.DenyRoles = types.ListNull(types.StringType)
	}
	// tfsdk.Config has no Set method (only Plan and State do), so the value tree
	// is built through a throwaway Plan and its Raw is reused.
	configCarrier := tfsdk.Plan{Schema: schemaResp.Schema}
	if diags := configCarrier.Set(ctx, &configModel); diags.HasError() {
		t.Fatalf("failed to build config fixture: %v", diags)
	}
	tfConfig := tfsdk.Config{Schema: schemaResp.Schema, Raw: configCarrier.Raw}

	createResp := &resource.CreateResponse{State: tfState}
	r.Create(ctx, resource.CreateRequest{Plan: tfPlan, Config: tfConfig}, createResp)

	if createResp.Diagnostics.HasError() {
		return OrganizationUserRoleResourceModel{}, createResp.Diagnostics
	}

	var result OrganizationUserRoleResourceModel
	if getDiags := createResp.State.Get(ctx, &result); getDiags.HasError() {
		t.Fatalf("failed to decode result state: %v", getDiags)
	}
	return result, createResp.Diagnostics
}

// runOrganizationUserRoleRead drives the real Read() against a state model.
func runOrganizationUserRoleRead(t *testing.T, r *OrganizationUserRoleResource, state OrganizationUserRoleResourceModel) (OrganizationUserRoleResourceModel, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	tfState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := tfState.Set(ctx, &state); diags.HasError() {
		t.Fatalf("failed to build state fixture: %v", diags)
	}

	readResp := &resource.ReadResponse{State: tfState}
	r.Read(ctx, resource.ReadRequest{State: tfState}, readResp)

	if readResp.Diagnostics.HasError() {
		return OrganizationUserRoleResourceModel{}, readResp.Diagnostics
	}
	if readResp.State.Raw.IsNull() {
		t.Fatal("Read removed the resource from state; expected it to refresh an existing member")
	}

	var result OrganizationUserRoleResourceModel
	if getDiags := readResp.State.Get(ctx, &result); getDiags.HasError() {
		t.Fatalf("failed to decode result state: %v", getDiags)
	}
	return result, readResp.Diagnostics
}

// runOrganizationUserRoleDelete drives the real Delete() against a state model.
// runOrganizationUserRoleDelete drives Delete's real body.
//
// denyRolesDeclared is passed EXPLICITLY rather than derived from state, because
// that is how the resource itself works: authority over deny_roles is recorded
// in provider private state at Create/Update time and read back here. State
// cannot answer it - deny_roles is Optional+Computed, so a refresh populates it
// for every member whether or not the config ever declared it, which is exactly
// the bug this seam exists to prevent regressing. The private-state type is
// internal to the framework and cannot be constructed in a unit test, so Delete
// reads it and delegates to this testable core.
func runOrganizationUserRoleDelete(t *testing.T, r *OrganizationUserRoleResource, state OrganizationUserRoleResourceModel, denyRolesDeclared bool) diag.Diagnostics {
	t.Helper()
	return r.deleteOrganizationRole(context.Background(), state, denyRolesDeclared)
}

// orgDenyRolesList builds a known deny_roles list fixture. types.ListValueMust
// (rather than ListValueFrom) is used deliberately for the empty case, so the
// fixture is unambiguously an EMPTY LIST and can never be mistaken for null.
func orgDenyRolesList(values ...string) types.List {
	elems := make([]attr.Value, 0, len(values))
	for _, v := range values {
		elems = append(elems, types.StringValue(v))
	}
	return types.ListValueMust(types.StringType, elems)
}

func orgDenyRolesStrings(t *testing.T, l types.List) []string {
	t.Helper()
	if l.IsNull() || l.IsUnknown() {
		t.Fatalf("expected a known, non-null deny_roles list, got %v", l)
	}
	var out []string
	if diags := l.ElementsAs(context.Background(), &out, false); diags.HasError() {
		t.Fatalf("failed to decode deny_roles: %v", diags)
	}
	return out
}

// TestOrganizationUserRoleCreate_DenyRolesOmittedUsesLegacyPathOnly is the
// central regression test for the write-path split: when the practitioner did
// not declare deny_roles, the write MUST go through the ungated legacy
// permission_level endpoint and must never touch the gated roles endpoint.
//
// The unknown subtest is the load-bearing one. deny_roles is Optional+Computed,
// and the framework presents an omitted Optional+Computed attribute as UNKNOWN
// during Create, not as null - so a declared-check written as IsNull() alone
// reads "omitted" as "declared" and routes every single Create onto the gated
// endpoint, which fails outright in organizations without the feature. That is
// the exact failure the Optional+Computed design exists to prevent, and it is
// invisible to a null-only test.
func TestOrganizationUserRoleCreate_DenyRolesOmittedUsesLegacyPathOnly(t *testing.T) {
	for _, tc := range []struct {
		name      string
		denyRoles types.List
	}{
		{"null (never declared)", types.ListNull(types.StringType)},
		{"unknown (omitted on Create, as the framework presents it)", types.ListUnknown(types.StringType)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			httpServer, mock := newMockOrgUserRoleServer(t)
			r := &OrganizationUserRoleResource{client: NewClientWithToken(httpServer.URL, "test-token")}
			mock.seedMember("Omitted@Example.com", "ide_omitted", "usr_omitted", "collaborator", nil)

			plan := OrganizationUserRoleResourceModel{
				Email:     types.StringValue("omitted@example.com"),
				BaseRole:  types.StringValue("owner"),
				DenyRoles: tc.denyRoles,
			}

			result, diags := runOrganizationUserRoleCreate(t, r, plan)
			if diags.HasError() {
				t.Fatalf("unexpected error: %v", diags)
			}

			log := mock.callLogSnapshot()
			if orgRoleCallLogHasRolesPUT(log) {
				t.Fatalf("deny_roles was not declared, so the gated roles endpoint must never be called - call log: %v", log)
			}
			if !orgRoleCallLogHas(log, "PUT /api/v2/organization_collaborators/ide_omitted") {
				t.Fatalf("expected the legacy permission_level PUT keyed by identity_id, got call log: %v", log)
			}

			body := mock.legacyBody()
			if body == nil {
				t.Fatal("expected a decoded legacy request body, got none")
			}
			if body.PermissionLevel != "owner" {
				t.Errorf("legacy body permission_level = %q, want the declared base_role %q", body.PermissionLevel, "owner")
			}

			if got := mock.memberState(t, "omitted@example.com").baseRole; got != "owner" {
				t.Errorf("member base role = %q, want %q (the legacy write should have applied)", got, "owner")
			}
			if result.BaseRole.ValueString() != "owner" {
				t.Errorf("state base_role = %q, want %q", result.BaseRole.ValueString(), "owner")
			}
			if result.IdentityID.ValueString() != "ide_omitted" || result.UserID.ValueString() != "usr_omitted" {
				t.Errorf("got identity_id=%q user_id=%q, want ide_omitted/usr_omitted resolved from email",
					result.IdentityID.ValueString(), result.UserID.ValueString())
			}
		})
	}
}

// TestOrganizationUserRoleCreate_DenyRolesEmptyListDeclaredUsesRolesPath is the
// other half of the split, and it closes a hole that fails SILENTLY under
// apply: an explicit empty list means "clear this member's deny roles", and the
// legacy endpoint structurally cannot do that - it has one slot, carries only
// permission_level, and leaves additional_roles exactly as it found them. A
// declared-check that tested len(elements) > 0 would route an explicit [] to
// the legacy path, report success, and leave the deny roles in place.
//
// So an explicit [] must reach the gated roles endpoint AND arrive there as a
// real empty JSON array, which is asserted against the raw wire body - a
// decoded []string cannot tell [] apart from null or an omitted field.
func TestOrganizationUserRoleCreate_DenyRolesEmptyListDeclaredUsesRolesPath(t *testing.T) {
	for _, tc := range []struct {
		name      string
		denyRoles types.List
		wantWire  string
		wantRoles []string
	}{
		{
			name:      "explicit empty list clears deny roles",
			denyRoles: orgDenyRolesList(),
			wantWire:  `"additional_roles":[]`,
			wantRoles: []string{},
		},
		{
			name:      "non-empty list sets deny roles",
			denyRoles: orgDenyRolesList("image_reader"),
			wantWire:  `"additional_roles":["image_reader"]`,
			wantRoles: []string{"image_reader"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			httpServer, mock := newMockOrgUserRoleServer(t)
			r := &OrganizationUserRoleResource{client: NewClientWithToken(httpServer.URL, "test-token")}
			// Seeded WITH a deny role already present, so "clear it" is an
			// observable state change rather than a no-op that any path passes.
			mock.seedMember("declared@example.com", "ide_declared", "usr_declared", "collaborator", []string{"image_reader_no_base_images"})

			plan := OrganizationUserRoleResourceModel{
				Email:     types.StringValue("declared@example.com"),
				BaseRole:  types.StringValue("collaborator"),
				DenyRoles: tc.denyRoles,
			}

			result, diags := runOrganizationUserRoleCreate(t, r, plan)
			if diags.HasError() {
				t.Fatalf("unexpected error: %v", diags)
			}

			log := mock.callLogSnapshot()
			if !orgRoleCallLogHas(log, "PUT /api/v2/organization_collaborators/usr_declared/roles") {
				t.Fatalf("declared deny_roles must route to the gated roles endpoint keyed by user_id, got call log: %v", log)
			}
			if orgRoleCallLogHas(log, "PUT /api/v2/organization_collaborators/ide_declared") {
				t.Fatalf("declared deny_roles must NOT be written through the legacy endpoint, which cannot express them at all - call log: %v", log)
			}

			body, raw := mock.rolesBody()
			if body == nil {
				t.Fatal("expected a decoded roles request body, got none")
			}
			if body.BaseRole != "collaborator" {
				t.Errorf("roles body base_role = %q, want %q", body.BaseRole, "collaborator")
			}
			if body.AdditionalRoles == nil {
				t.Error("roles body additional_roles decoded as nil; the API requires the field and rejects null")
			}
			if !strings.Contains(strings.ReplaceAll(raw, " ", ""), tc.wantWire) {
				t.Errorf("roles request body on the wire = %s, want it to contain %s", raw, tc.wantWire)
			}

			gotMember := mock.memberState(t, "declared@example.com")
			if strings.Join(gotMember.additionalRoles, ",") != strings.Join(tc.wantRoles, ",") {
				t.Errorf("member additional_roles = %v, want %v", gotMember.additionalRoles, tc.wantRoles)
			}
			if got := orgDenyRolesStrings(t, result.DenyRoles); strings.Join(got, ",") != strings.Join(tc.wantRoles, ",") {
				t.Errorf("state deny_roles = %v, want %v", got, tc.wantRoles)
			}
		})
	}
}

// TestOrganizationUserRoleRead_UsesSingularEndpointNotListForDenyRoles is the
// false-empty regression test. The list endpoint reports additional_roles as []
// for EVERY member regardless of their real roles, so a Read that sources deny
// roles from the list returns a confident, wrong answer - not an obviously
// missing one. The bug it produces is a permanent phantom diff (or, worse,
// state that says a restriction was lifted when it was not).
//
// The fixture below is exactly that trap: the member really holds
// ["image_reader"], the list says [], and only the singular per-user GET tells
// the truth.
func TestOrganizationUserRoleRead_UsesSingularEndpointNotListForDenyRoles(t *testing.T) {
	httpServer, mock := newMockOrgUserRoleServer(t)
	r := &OrganizationUserRoleResource{client: NewClientWithToken(httpServer.URL, "test-token")}
	mock.seedMember("hydrated@example.com", "ide_hydrated", "usr_hydrated", "collaborator", []string{"image_reader"})

	// PLACEBO GUARD, not a redundant check: this test only proves anything
	// while the list endpoint is genuinely lying. If the fixture ever drifted
	// to serve real additional_roles from the list, a list-only Read would
	// satisfy every assertion below and this test would quietly stop
	// protecting the two-step read.
	listOnly, err := listAllOrganizationCollaborators(context.Background(), r.client, nil)
	if err != nil {
		t.Fatalf("precondition query failed: %v", err)
	}
	if len(listOnly) != 1 || len(listOnly[0].AdditionalRoles) != 0 {
		t.Fatalf("precondition failed: the list endpoint must report a FALSE EMPTY additional_roles (real API behavior) for this test to prove anything, got %+v", listOnly)
	}

	state := OrganizationUserRoleResourceModel{
		ID:         types.StringValue("hydrated@example.com"),
		Email:      types.StringValue("hydrated@example.com"),
		UserID:     types.StringValue("usr_hydrated"),
		IdentityID: types.StringValue("ide_hydrated"),
		BaseRole:   types.StringValue("collaborator"),
		DenyRoles:  orgDenyRolesList("image_reader"),
	}

	result, diags := runOrganizationUserRoleRead(t, r, state)
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	got := orgDenyRolesStrings(t, result.DenyRoles)
	if len(got) != 1 || got[0] != "image_reader" {
		t.Errorf("deny_roles = %v, want [image_reader] - reading them off the list endpoint yields a false empty", got)
	}

	log := mock.callLogSnapshot()
	if !orgRoleCallLogHas(log, "GET /api/v2/organization_collaborators/usr_hydrated") {
		t.Errorf("expected the singular per-user GET (the only read path that reports real additional_roles), got call log: %v", log)
	}
}

// TestOrganizationUserRoleDelete_ClearsDenyRolesAndNeverEvictsMember guards the
// resource's most consequential boundary: destroying a ROLE must never remove a
// HUMAN from the organization.
//
// There is no DELETE endpoint for organization roles at all - the only DELETE
// under /api/v2/organization_collaborators removes the person entirely, which
// would make this resource authoritative over membership, which it deliberately
// is not. So that call must appear nowhere in the call log for ANY variant, and
// that is the assertion both subtests share.
//
// What destroy DOES do splits by whether this resource ever took authority over
// deny_roles - "remove what this resource granted, leave what it merely
// changed":
//
//   - declared: the deny roles are cleared through the gated roles endpoint,
//     with the member's CURRENT base_role sent back unchanged (that endpoint is
//     a SET over the pair, so omitting it would rewrite the base role as a side
//     effect of clearing).
//   - omitted: nothing is written at all.
//
// base_role is left in place either way, deliberately: it has no absent state,
// and demoting an organization owner on destroy could strip their access across
// every cloud in the organization. Because that makes destroy a partial or
// total no-op, it must WARN rather than pass silently - an undisclosed no-op
// after "destroy 1 resource" is its own bug, so both subtests assert the
// warning too.
func TestOrganizationUserRoleDelete_ClearsDenyRolesAndNeverEvictsMember(t *testing.T) {
	// NOTE both cases carry the SAME populated state.DenyRoles. That is the
	// regression guard: deny_roles is Optional+Computed, so a refresh - which a
	// destroy always performs first - populates it for every member regardless of
	// whether the config ever declared it. A Delete that decides authority from
	// state therefore clears deny roles for everyone. Only denyRolesDeclared,
	// which comes from private state recorded at Create/Update from CONFIG,
	// distinguishes these two cases. If the two rows differed in state as well,
	// this test would pass against the buggy state-based implementation.
	for _, tc := range []struct {
		name              string
		denyRoles         types.List
		denyRolesDeclared bool
		wantWrite         bool
		wantWarning       string
	}{
		{
			name:              "deny_roles never managed writes nothing and says so",
			denyRoles:         orgDenyRolesList("image_reader"),
			denyRolesDeclared: false,
			wantWrite:         false,
			wantWarning:       "Organization Role Left In Place",
		},
		{
			name:              "deny_roles managed clears them via the gated roles endpoint",
			denyRoles:         orgDenyRolesList("image_reader"),
			denyRolesDeclared: true,
			wantWrite:         true,
			wantWarning:       "Organization Base Role Left In Place",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			httpServer, mock := newMockOrgUserRoleServer(t)
			r := &OrganizationUserRoleResource{client: NewClientWithToken(httpServer.URL, "test-token")}
			mock.seedMember("leaving@example.com", "ide_leaving", "usr_leaving", "owner", []string{"image_reader"})

			state := OrganizationUserRoleResourceModel{
				ID:         types.StringValue("leaving@example.com"),
				Email:      types.StringValue("leaving@example.com"),
				UserID:     types.StringValue("usr_leaving"),
				IdentityID: types.StringValue("ide_leaving"),
				BaseRole:   types.StringValue("owner"),
				DenyRoles:  tc.denyRoles,
			}

			diags := runOrganizationUserRoleDelete(t, r, state, tc.denyRolesDeclared)
			if diags.HasError() {
				t.Fatalf("unexpected error: %v", diags)
			}

			log := mock.callLogSnapshot()
			if orgRoleCallLogHasMemberDelete(log) {
				t.Fatalf("destroying a role must NEVER call the member-eviction DELETE - that endpoint removes the human from the organization. Call log: %v", log)
			}
			if _, stillThere := mock.members[strings.ToLower("leaving@example.com")]; !stillThere {
				t.Fatal("the member was removed from the organization by a role destroy")
			}

			foundWarning := false
			for _, d := range diags {
				if d.Severity() == diag.SeverityWarning && strings.Contains(d.Summary(), tc.wantWarning) {
					foundWarning = true
					if !strings.Contains(d.Detail(), "owner") {
						t.Errorf("expected the warning to name the base_role left behind, got: %s", d.Detail())
					}
				}
			}
			if !foundWarning {
				t.Errorf("expected a %q warning - a destroy that leaves a privilege in place must say so, got: %v", tc.wantWarning, diags)
			}

			member := mock.memberState(t, "leaving@example.com")
			if member.baseRole != "owner" {
				t.Errorf("base role after destroy = %q, want it left at %q untouched", member.baseRole, "owner")
			}

			if !tc.wantWrite {
				if orgRoleCallLogHasRolesPUT(log) || orgRoleCallLogHas(log, "PUT /api/v2/organization_collaborators/ide_leaving") {
					t.Fatalf("deny_roles were never managed, so destroy must write nothing at all - call log: %v", log)
				}
				if len(member.additionalRoles) != 1 || member.additionalRoles[0] != "image_reader" {
					t.Errorf("additional_roles = %v, want them left untouched - this resource never took authority over them", member.additionalRoles)
				}
				return
			}

			if !orgRoleCallLogHas(log, "PUT /api/v2/organization_collaborators/usr_leaving/roles") {
				t.Fatalf("expected deny_roles to be cleared through the gated roles endpoint, got call log: %v", log)
			}
			if orgRoleCallLogHas(log, "PUT /api/v2/organization_collaborators/ide_leaving") {
				t.Fatalf("the legacy endpoint cannot clear deny roles and must not be used here - call log: %v", log)
			}

			body, raw := mock.rolesBody()
			if body == nil {
				t.Fatal("expected a decoded roles request body, got none")
			}
			if body.BaseRole != "owner" {
				t.Errorf("roles clear body base_role = %q, want the current %q sent back unchanged - this endpoint is a SET over the pair", body.BaseRole, "owner")
			}
			if !strings.Contains(strings.ReplaceAll(raw, " ", ""), `"additional_roles":[]`) {
				t.Errorf("roles clear body on the wire = %s, want additional_roles cleared to []", raw)
			}
			if len(member.additionalRoles) != 0 {
				t.Errorf("additional_roles after destroy = %v, want them cleared", member.additionalRoles)
			}
		})
	}
}

// TestOrganizationUserRoleWrite_501ProducesActionableDiagnostic covers the one
// failure a practitioner is most likely to hit and least able to diagnose: the
// roles endpoint is gated behind an organization feature flag, surfaced as a
// bare 501.
//
// The trigger is never obvious from the plan - it is the presence of
// deny_roles, not base_role, that forces this resource onto the gated endpoint
// at all. So the diagnostic must be attribute-scoped to deny_roles AND must say
// that managing base_role alone works, rather than passing through
// "unexpected status 501".
func TestOrganizationUserRoleWrite_501ProducesActionableDiagnostic(t *testing.T) {
	httpServer, mock := newMockOrgUserRoleServer(t)
	r := &OrganizationUserRoleResource{client: NewClientWithToken(httpServer.URL, "test-token")}
	mock.seedMember("gated@example.com", "ide_gated", "usr_gated", "collaborator", nil)
	mock.rolesEndpoint501 = true

	// PLACEBO GUARD: the assertions below check that the diagnostic CONTAINS
	// the explanatory wording, which says nothing about whether the backend's
	// own 501 text already contained it. If this fixture's detail ever drifted
	// to mention deny_roles or base_role, a regressed raw-passthrough
	// implementation would satisfy those assertions by accident.
	if strings.Contains(mock.rolesEndpoint501D, "deny_roles") || strings.Contains(mock.rolesEndpoint501D, "base_role") {
		t.Fatal("precondition failed: this fixture's raw 501 text already contains wording only the real explanatory diagnostic should add - the assertions below would no longer prove anything")
	}

	model := OrganizationUserRoleResourceModel{
		Email:     types.StringValue("gated@example.com"),
		BaseRole:  types.StringValue("collaborator"),
		DenyRoles: orgDenyRolesList("image_reader"),
	}

	// true = the configuration declared deny_roles, which is what routes this to
	// the gated endpoint. The flag is passed explicitly rather than derived from
	// the model: routing is decided from CONFIG, and neither the plan nor state
	// can answer that question once UseStateForUnknown is in play.
	diags := r.writeOrganizationRole(context.Background(), &model, "ide_gated", "usr_gated", true)
	if !diags.HasError() {
		t.Fatal("expected an error when the gated roles endpoint returns 501, got none")
	}

	for _, d := range diags {
		withPath, ok := d.(diag.DiagnosticWithPath)
		if !ok {
			continue
		}
		if !withPath.Path().Equal(path.Root("deny_roles")) {
			continue
		}
		detail := withPath.Detail()
		if strings.Contains(detail, "unexpected status 501") {
			t.Errorf("expected an explanation, not a raw status passthrough, got: %s", detail)
		}
		if !strings.Contains(detail, "base_role on its own") {
			t.Errorf("expected the detail to say that managing base_role alone uses an ungated endpoint and works everywhere, got: %s", detail)
		}
		return
	}
	t.Errorf("expected an error scoped to the deny_roles attribute (the attribute whose presence forces the gated endpoint), got: %v", diags)
}

// TestResolveIdentityForEmail_ResolvesBothIDs confirms the org-wide, email-keyed
// resolution this resource depends on for everything (Create, Update via state, Import)
// returns both identity_id and user_id from one call, case-insensitively. Relocated from
// resource_cloud_user_role_test.go along with the function it tests.
func TestResolveIdentityForEmail_ResolvesBothIDs(t *testing.T) {
	httpServer, mock := newMockOrgUserRoleServer(t)
	client := NewClientWithToken(httpServer.URL, "test-token")
	mock.seedMember("Mixed.Case@Example.com", "ide_mixed", "usr_mixed", "collaborator", nil)

	identityID, userID, err := resolveIdentityForEmail(context.Background(), client, "mixed.case@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if identityID != "ide_mixed" || userID != "usr_mixed" {
		t.Errorf("got identityID=%q userID=%q, want identityID=%q userID=%q", identityID, userID, "ide_mixed", "usr_mixed")
	}
}

// TestResolveIdentityForEmail_NotFoundErrors confirms a clean error (not a panic or a
// silently empty result) for an email with no matching org member. Relocated from
// resource_cloud_user_role_test.go along with the function it tests.
func TestResolveIdentityForEmail_NotFoundErrors(t *testing.T) {
	httpServer, _ := newMockOrgUserRoleServer(t)
	client := NewClientWithToken(httpServer.URL, "test-token")

	_, _, err := resolveIdentityForEmail(context.Background(), client, "nobody@example.com")
	if err == nil {
		t.Fatal("expected an error for an email with no matching org member, got nil")
	}
}
