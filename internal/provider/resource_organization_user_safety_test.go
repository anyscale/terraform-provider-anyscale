package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// These cover the two SAFETY properties of anyscale_organization_user, as
// distinct from its happy paths: destroy must never remove a human, and
// declaring somebody who is already a member must not mail them.
//
// Both are asserted on the REQUEST LOG rather than on final state, because both
// failures are "a call that should not have been made". A test that only
// compared end state would pass against a Delete that evicted somebody and then
// reported the same absence, and against a Create that re-invited an existing
// member whose membership then looked unchanged.

// mockOrgUserServer is a scripted stand-in for the collaborator and invitation
// endpoints this resource touches, plus the member-eviction DELETE it must
// never call.
type mockOrgUserServer struct {
	mu sync.Mutex

	// members: lowercased email -> identity/user ids. The map key is lowercased
	// while the stored email keeps its original casing, so the case-insensitive
	// match is genuinely exercised rather than trivially satisfied.
	members map[string]orgUserFixture

	callLog []string
}

type orgUserFixture struct {
	identityID string
	userID     string
	email      string
}

func newMockOrgUserServer(t *testing.T) (*httptest.Server, *mockOrgUserServer) {
	t.Helper()
	s := &mockOrgUserServer{members: map[string]orgUserFixture{}}
	server := httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(server.Close)
	return server, s
}

func (s *mockOrgUserServer) seedMember(email, identityID, userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members[strings.ToLower(email)] = orgUserFixture{identityID: identityID, userID: userID, email: email}
}

func (s *mockOrgUserServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	reqPath := r.URL.Path
	s.callLog = append(s.callLog, fmt.Sprintf("%s %s", r.Method, reqPath))

	switch {
	// The SSO preflight runs before every invitation create. Serving it with
	// sso_mode=off is what puts these tests on the REAL allowed path: without
	// this route the GET 404s, the guard cannot determine sso_mode, and every
	// test here silently exercises the fail-open branch instead of the destroy
	// behaviour it names.
	case r.Method == http.MethodGet && reqPath == "/api/v2/userinfo":
		_, _ = w.Write([]byte(`{"result":{"organizations":[{"sso_mode":"off"}]}}`))

	case r.Method == http.MethodGet && strings.HasPrefix(reqPath, "/api/v2/organization_collaborators"):
		s.handleCollaboratorList(w, r, reqPath)

	case r.Method == http.MethodPost && strings.HasSuffix(reqPath, "/invalidate"):
		// Cancelling an invitation. The real backend answers 202 and soft-expires
		// the row rather than deleting it.
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"result":{"id":"inv_mock","email":"alice@example.com","organization_id":"org_1",` +
			`"created_at":"2026-01-01T00:00:00.000000+00:00","expires_at":"2020-01-01T00:00:00.000000+00:00"}}`))

	case r.Method == http.MethodGet && reqPath == "/api/v2/organization_invitations":
		// No pending invitations in these fixtures.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[],"metadata":{"total":0}}`))

	case r.Method == http.MethodGet && strings.HasPrefix(reqPath, "/api/v2/organization_invitations/"):
		// Singular GET: a still-pending invitation.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":{"id":"inv_mock","email":"alice@example.com","organization_id":"org_1",` +
			`"created_at":"2026-01-01T00:00:00.000000+00:00","expires_at":"2099-01-01T00:00:00.000000+00:00"}}`))

	case r.Method == http.MethodPost && strings.HasPrefix(reqPath, "/api/v2/organization_invitations"):
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"result":{"id":"inv_mock"}}`))

	default:
		// Deliberately answered rather than errored. If Delete wrongly evicted a
		// member, an error here would fail the test for the wrong reason - the
		// assertion is on the call log, not on the response.
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *mockOrgUserServer) handleCollaboratorList(w http.ResponseWriter, r *http.Request, reqPath string) {
	// Singular per-user GET.
	if strings.HasPrefix(reqPath, "/api/v2/organization_collaborators/") {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	filter := strings.ToLower(r.URL.Query().Get("email"))
	results := make([]OrganizationCollaboratorResult, 0, len(s.members))
	for key, m := range s.members {
		if filter != "" && key != filter {
			continue
		}
		userID := m.userID
		results = append(results, OrganizationCollaboratorResult{
			ID: m.identityID, UserID: &userID,
			// The API always echoes a LOWERCASED address. The casing test below
			// depends on this being the lowercased form, not the seeded one.
			Email:     strings.ToLower(m.email),
			CreatedAt: "2026-01-01T00:00:00.000000+00:00",
			BaseRole:  "collaborator", AdditionalRoles: []string{},
		})
	}
	resp := OrganizationCollaboratorsListResponse{Results: results}
	resp.Metadata.Total = len(results)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *mockOrgUserServer) callLogSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.callLog...)
}

// orgUserLogHasMemberDelete reports whether the member-eviction DELETE was
// called at all - the single call this resource must never make.
func orgUserLogHasMemberDelete(log []string) bool {
	for _, c := range log {
		if strings.HasPrefix(c, "DELETE /api/v2/organization_collaborators/") {
			return true
		}
	}
	return false
}

// orgUserLogHasInvitationPost reports whether an invitation was sent.
func orgUserLogHasInvitationPost(log []string) bool {
	for _, c := range log {
		if strings.HasPrefix(c, "POST /api/v2/organization_invitations") {
			return true
		}
	}
	return false
}

func runOrgUserCreate(t *testing.T, r *OrganizationUserResource, plan OrganizationUserResourceModel) (OrganizationUserResourceModel, diag.Diagnostics) {
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
	// tfsdk.Config has no Set method; build the value tree through a Plan and
	// reuse its Raw.
	configCarrier := tfsdk.Plan{Schema: schemaResp.Schema}
	if diags := configCarrier.Set(ctx, &plan); diags.HasError() {
		t.Fatalf("failed to build config fixture: %v", diags)
	}

	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, resource.CreateRequest{
		Plan:   tfPlan,
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configCarrier.Raw},
	}, resp)

	if resp.Diagnostics.HasError() {
		return OrganizationUserResourceModel{}, resp.Diagnostics
	}
	var out OrganizationUserResourceModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("failed to read created state: %v", diags)
	}
	return out, resp.Diagnostics
}

// runOrgUserDelete drives Delete's real body with the membership record passed
// EXPLICITLY. Private state's type is internal to the framework and cannot be
// constructed in a test, so calling Delete directly would land every case in the
// default (no-record) branch - which is precisely the placebo this seam exists to
// prevent: a mutation in any other branch went undetected until it was extracted.
func runOrgUserDelete(t *testing.T, r *OrganizationUserResource, state OrganizationUserResourceModel, membership organizationUserMembership) diag.Diagnostics {
	t.Helper()
	resp := &resource.DeleteResponse{}
	r.deleteMembership(context.Background(), state, membership, resp)
	return resp.Diagnostics
}

// TestOrganizationUserCreate_AdoptsExistingMemberWithoutInviting is the second
// safety property: declaring somebody who is already a member must not mail
// them. Asserted on the request log - a state-only check would pass against a
// Create that re-invited an existing member, since their membership looks
// identical either way.
func TestOrganizationUserCreate_AdoptsExistingMemberWithoutInviting(t *testing.T) {
	httpServer, mock := newMockOrgUserServer(t)
	r := &OrganizationUserResource{client: NewClientWithToken(httpServer.URL, "test-token")}
	mock.seedMember("alice@example.com", "ide_alice", "usr_alice")

	out, diags := runOrgUserCreate(t, r, OrganizationUserResourceModel{
		Email:             types.StringValue("alice@example.com"),
		ReinviteIfExpired: types.BoolValue(false),
	})
	if diags.HasError() {
		t.Fatalf("adopting an existing member must succeed, got: %v", diags)
	}

	log := mock.callLogSnapshot()
	if orgUserLogHasInvitationPost(log) {
		t.Fatalf("declaring an EXISTING member must never send an invitation - they are already in the organization "+
			"and would be mailed for nothing. Call log: %v", log)
	}
	if out.IdentityID.ValueString() != "ide_alice" {
		t.Errorf("adopt must record the real identity_id, got %q", out.IdentityID.ValueString())
	}
}

// TestOrganizationUserDelete_NeverEvictsAHuman is the first safety property and
// the most important assertion in this file. Destroying this resource must
// never call the member-eviction endpoint, in ANY private-state condition -
// including the one where no record exists at all, which is what every resource
// that predates the re-key will look like.
func TestOrganizationUserDelete_NeverEvictsAHuman(t *testing.T) {
	for _, tc := range []struct {
		name       string
		membership organizationUserMembership
	}{
		{name: "no record at all (predates the re-key)", membership: organizationUserMembership{}},
		{name: "adopted an existing member", membership: organizationUserMembership{Origin: membershipOriginAdopted}},
		{name: "invited, invitation still recorded", membership: organizationUserMembership{Origin: membershipOriginInvited, InvitationID: "inv_mock"}},
		{name: "imported a pending invitation", membership: organizationUserMembership{Origin: membershipOriginImported, InvitationID: "inv_mock"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			httpServer, mock := newMockOrgUserServer(t)
			r := &OrganizationUserResource{client: NewClientWithToken(httpServer.URL, "test-token")}
			mock.seedMember("alice@example.com", "ide_alice", "usr_alice")

			diags := runOrgUserDelete(t, r, OrganizationUserResourceModel{
				ID:                types.StringValue("alice@example.com"),
				Email:             types.StringValue("alice@example.com"),
				IdentityID:        types.StringValue("ide_alice"),
				UserID:            types.StringValue("usr_alice"),
				Name:              types.StringNull(),
				CreatedAt:         types.StringNull(),
				ReinviteIfExpired: types.BoolValue(false),
			}, tc.membership)
			if diags.HasError() {
				t.Fatalf("destroy must not fail: %v", diags)
			}

			log := mock.callLogSnapshot()
			if orgUserLogHasMemberDelete(log) {
				t.Fatalf("destroying this resource must NEVER call the member-eviction endpoint - that removes a "+
					"real human from the organization. Call log: %v", log)
			}
		})
	}
}

// TestOrganizationUserCreate_KeepsConfiguredEmailCasing pins the one casing
// rule. Anyscale stores and echoes addresses lower-cased; email is Required
// (not Computed), so writing the API's echo back into state makes Terraform
// Core reject the apply outright for any address with an uppercase letter.
// The resource next door shipped exactly that bug.
func TestOrganizationUserCreate_KeepsConfiguredEmailCasing(t *testing.T) {
	httpServer, mock := newMockOrgUserServer(t)
	r := &OrganizationUserResource{client: NewClientWithToken(httpServer.URL, "test-token")}
	// Seeded mixed-case; the mock's list endpoint echoes it LOWERCASED, exactly
	// as the real API does.
	mock.seedMember("Alice@Example.com", "ide_alice", "usr_alice")

	out, diags := runOrgUserCreate(t, r, OrganizationUserResourceModel{
		Email:             types.StringValue("Alice@Example.com"),
		ReinviteIfExpired: types.BoolValue(false),
	})
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	if out.Email.ValueString() != "Alice@Example.com" {
		t.Errorf("state must keep the CONFIGURED casing %q, not the API's lower-cased echo; got %q. "+
			"Echoing the API value back into a Required attribute makes Core reject the apply.",
			"Alice@Example.com", out.Email.ValueString())
	}
	if out.ID.ValueString() != "Alice@Example.com" {
		t.Errorf("id must match the configured email, got %q", out.ID.ValueString())
	}
}
