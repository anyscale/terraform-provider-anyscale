package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestDoRequestAndParse_404ToleranceSignal is exploratory verification for workbench #7,
// Cluster 1: getInvitationByID currently detects "not found" via an early, pre-body-read check
// on httpResp.StatusCode == http.StatusNotFound, returning a distinct error before ever parsing
// the response body. Migrating it to DoRequestAndParse (like compute_config's GET-with-404-
// tolerance) would fold 404 into the accepted-statuses list instead. This test proves,
// empirically rather than by assumption, exactly what a caller sees in that case: does err come
// back nil or non-nil, and is the parsed result's zero-value distinguishable as "not found"?
func TestDoRequestAndParse_404ToleranceSignal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail": "Invitation not found"}`))
	}))
	defer server.Close()

	client := NewClientWithToken(server.URL, "test-token")
	ctx := context.Background()

	result, err := DoRequestAndParse[OrganizationInvitationResponse](
		ctx, client, "GET", "/api/v2/organization_invitations/does-not-exist", nil,
		http.StatusOK, http.StatusNotFound,
	)

	// Empirical finding: a 404 in the accepted-statuses list does NOT produce an error - the
	// body ({"detail": "..."}) has no "result" key, so json.Unmarshal succeeds with Result left
	// at its zero value. err is nil and result is a non-nil pointer to a zero-valued struct.
	if err != nil {
		t.Fatalf("DoRequestAndParse() with 404 in accepted statuses returned an error = %v; expected nil (404 body unmarshals cleanly into a zero-valued Result)", err)
	}
	if result == nil {
		t.Fatal("DoRequestAndParse() with 404 in accepted statuses returned a nil result; expected a non-nil pointer to a zero-valued struct")
	}
	if result.Result.ID != "" {
		t.Errorf("DoRequestAndParse() Result.ID = %q on a 404, want empty (zero-value) - this IS the not-found signal a caller must check explicitly", result.Result.ID)
	}

	// Conclusion for the migration: the not-found signal survives, but as result.Result.ID == ""
	// with err == nil, NOT as a distinct error the way the current early-404-check works. A
	// straight swap to DoRequestAndParse without adding an explicit
	// "if result.Result.ID == "" { return nil, notFoundErr }" check after the call would silently
	// change getInvitationByID's contract: callers checking `err != nil` for not-found (as the
	// current code and its callers do) would stop detecting it at all.
}

// TestComputeInvitationStatus covers computeInvitationStatus's pending/accepted/expired
// branching, which had zero unit test coverage despite being a pure function. Traced against the
// real backend (org_invites_service.py): invalidate only ever sets expires_at to the current
// time and never clears accepted_at, so an invitation that was genuinely accepted and one that
// was later invalidated are never actually in conflict in real data - but the function itself
// checks acceptedAt before expiresAt, so this pins that precedence explicitly rather than leaving
// it as an unverified assumption.
func TestComputeInvitationStatus(t *testing.T) {
	future := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	past := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	acceptedTimestamp := "2024-01-01T00:00:00Z"
	emptyAccepted := ""

	tests := []struct {
		name       string
		acceptedAt *string
		expiresAt  string
		want       string
	}{
		{"no accepted_at, future expiry -> pending", nil, future, "pending"},
		{"no accepted_at, past expiry -> expired", nil, past, "expired"},
		{"accepted_at set, future expiry -> accepted", &acceptedTimestamp, future, "accepted"},
		{"accepted_at set, past expiry -> accepted still wins", &acceptedTimestamp, past, "accepted"},
		{"non-nil but empty accepted_at, future expiry -> pending, not accepted", &emptyAccepted, future, "pending"},
		{"non-nil but empty accepted_at, past expiry -> expired, not accepted", &emptyAccepted, past, "expired"},
		{"unparseable expires_at -> falls back to pending", nil, "not-a-real-timestamp", "pending"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeInvitationStatus(tt.acceptedAt, tt.expiresAt)
			if got != tt.want {
				t.Errorf("computeInvitationStatus(%v, %q) = %q, want %q", tt.acceptedAt, tt.expiresAt, got, tt.want)
			}
		})
	}
}

// runOrganizationInvitationResourceCreate drives OrganizationInvitationResource's real Create()
// method end-to-end against a plan model, the same pattern as runProjectResourceCreate.
func runOrganizationInvitationResourceCreate(t *testing.T, r *OrganizationInvitationResource, plan OrganizationInvitationResourceModel) (OrganizationInvitationResourceModel, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("failed to build schema: %v", schemaResp.Diagnostics)
	}

	tfPlan := tfsdk.Plan{Schema: schemaResp.Schema}
	planDiags := tfPlan.Set(ctx, &plan)
	if planDiags.HasError() {
		t.Fatalf("failed to build plan fixture: %v", planDiags)
	}

	createResp := &resource.CreateResponse{
		State: tfsdk.State(tfPlan),
	}
	r.Create(ctx, resource.CreateRequest{Plan: tfPlan}, createResp)

	if createResp.Diagnostics.HasError() {
		return OrganizationInvitationResourceModel{}, createResp.Diagnostics
	}

	var result OrganizationInvitationResourceModel
	getDiags := createResp.State.Get(ctx, &result)
	if getDiags.HasError() {
		t.Fatalf("failed to decode result state: %v", getDiags)
	}
	return result, createResp.Diagnostics
}

// TestOrganizationInvitationResourceCreate_AlreadyMemberError is a regression/documentation test
// for real backend behavior traced in org_invites_service.py's create_invitation: inviting an
// email that already belongs to the calling org returns HTTP 400 with an exact, contractually
// pinned detail string (a frontend regex parses it to power a "Resolve" button, so the wording
// itself is stable, not incidental - see format_already_member_of_org_error in the backend). The
// Go resource has no special-case handling for this response today; it flows through the generic
// DoRequestAndParse/AddAPIError path like any other unexpected status. This test pins that
// CURRENT behavior (the real backend detail text reaches the Terraform diagnostic, not silently
// swallowed or genericized) so a future change to either side is a deliberate, visible diff
// rather than a silent regression.
func TestOrganizationInvitationResourceCreate_AlreadyMemberError(t *testing.T) {
	const alreadyMemberDetail = "User with email invitee@example.com is already a member of your organization."

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v2/organization_invitations" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"detail":"` + alreadyMemberDetail + `"}}`))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := &OrganizationInvitationResource{client: NewClientWithToken(server.URL, "test-token")}
	_, diags := runOrganizationInvitationResourceCreate(t, r, OrganizationInvitationResourceModel{
		Email: types.StringValue("invitee@example.com"),
	})

	if !diags.HasError() {
		t.Fatal("expected a diagnostic error for an already-a-member response, got none")
	}
	if !diagsContainDetailSubstring(diags, alreadyMemberDetail) {
		t.Errorf("expected the real backend detail text %q to reach the diagnostic, got: %v", alreadyMemberDetail, diags)
	}
}
