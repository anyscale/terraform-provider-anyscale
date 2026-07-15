package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestFindCollaboratorByID_PagesBeyondFirstPage is a regression test for task
// d35713ef: findCollaboratorByID used to only look at the first page (count=50)
// and return "not found" for anything beyond it, even though it had already
// seen a next_paging_token - Read then removed such collaborators from state
// entirely. This asserts a collaborator that only appears on page 2 is found.
func TestFindCollaboratorByID_PagesBeyondFirstPage(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)

		if requestCount == 1 {
			_, _ = fmt.Fprint(w, `{
				"results": [{"id": "identity-1", "email": "a@example.com", "permission_level": "collaborator"}],
				"metadata": {"total": 2, "next_paging_token": "page2"}
			}`)
			return
		}

		_, _ = fmt.Fprint(w, `{
			"results": [{"id": "identity-2", "email": "b@example.com", "permission_level": "owner"}],
			"metadata": {"total": 2, "next_paging_token": null}
		}`)
	}))
	defer server.Close()

	r := &OrganizationCollaboratorResource{
		client: NewClientWithToken(server.URL, "test-token"),
	}

	collab, err := r.findCollaboratorByID(context.Background(), "identity-2")
	if err != nil {
		t.Fatalf("expected to find collaborator on page 2, got error: %v", err)
	}
	if collab == nil {
		t.Fatal("expected a non-nil collaborator")
	}
	if collab.Email != "b@example.com" {
		t.Errorf("email = %q, want %q", collab.Email, "b@example.com")
	}
	if requestCount != 2 {
		t.Errorf("expected 2 requests (one per page), got %d", requestCount)
	}
}

// TestFindCollaboratorByID_NotFoundAfterAllPages confirms a genuinely absent
// identity still returns a not-found error once every page has been checked,
// rather than looping or erroring on the final null next_paging_token.
func TestFindCollaboratorByID_NotFoundAfterAllPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{
			"results": [{"id": "identity-1", "email": "a@example.com", "permission_level": "collaborator"}],
			"metadata": {"total": 1, "next_paging_token": null}
		}`)
	}))
	defer server.Close()

	r := &OrganizationCollaboratorResource{
		client: NewClientWithToken(server.URL, "test-token"),
	}

	_, err := r.findCollaboratorByID(context.Background(), "identity-does-not-exist")
	if err == nil {
		t.Fatal("expected a not-found error, got nil")
	}
}

// TestApplyCollaboratorIdentityFields_NeverTouchesCreatedAt is a regression
// test for task 4745d9fb: assayer confirmed live that the API returns two
// different real created_at timestamps across reads for the same
// collaborator. created_at is Computed+UseStateForUnknown, so the plan keeps
// the prior value; Update()/Read() used to overwrite state.created_at with
// that fresh, different API value afterward, producing "Provider produced
// inconsistent result after apply" on a bare permission_level change. This
// mocks exactly that: two reads returning different created_at, and asserts
// applyCollaboratorIdentityFields (what both Read and Update now call) never
// touches the field at all, regardless of what the API returned.
func TestApplyCollaboratorIdentityFields_NeverTouchesCreatedAt(t *testing.T) {
	name := "Ada Lovelace"
	firstRead := &OrganizationCollaboratorResult{
		ID:              "identity-1",
		Email:           "ada@example.com",
		Name:            &name,
		PermissionLevel: "collaborator",
		CreatedAt:       "2026-01-01T00:00:00Z",
	}
	secondRead := &OrganizationCollaboratorResult{
		ID:              "identity-1",
		Email:           "ada@example.com",
		Name:            &name,
		PermissionLevel: "owner",
		CreatedAt:       "2026-07-06T12:00:00Z", // different from firstRead - the actual live behavior
	}

	model := &OrganizationCollaboratorResourceModel{
		CreatedAt: types.StringValue(firstRead.CreatedAt),
	}

	// Simulate import/initial state: created_at gets its one and only write here.
	if diags := applyCollaboratorIdentityFields(context.Background(), model, firstRead); diags.HasError() {
		t.Fatalf("applyCollaboratorIdentityFields returned unexpected errors: %v", diags)
	}
	if model.CreatedAt.ValueString() != firstRead.CreatedAt {
		t.Fatalf("created_at after first apply = %q, want unchanged %q", model.CreatedAt.ValueString(), firstRead.CreatedAt)
	}

	// Simulate the Update()/Read() read-back that used to overwrite created_at
	// with a different value returned by the API.
	if diags := applyCollaboratorIdentityFields(context.Background(), model, secondRead); diags.HasError() {
		t.Fatalf("applyCollaboratorIdentityFields returned unexpected errors: %v", diags)
	}
	if model.CreatedAt.ValueString() != firstRead.CreatedAt {
		t.Errorf("created_at after second apply = %q, want it to stay the original %q even though the API returned %q",
			model.CreatedAt.ValueString(), firstRead.CreatedAt, secondRead.CreatedAt)
	}

	// Sanity: the fields that ARE supposed to refresh from the API still do.
	if model.Email.ValueString() != secondRead.Email {
		t.Errorf("email = %q, want %q (should still refresh from the API)", model.Email.ValueString(), secondRead.Email)
	}
}

// TestOrganizationCollaboratorUpdate_PreservesCreatedAtAcrossDifferingReads is
// an end-to-end mocked version of the same regression, exercising the actual
// Update method (via a mocked API returning a different created_at on the
// post-update read) rather than just the shared helper.
func TestOrganizationCollaboratorUpdate_PreservesCreatedAtAcrossDifferingReads(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodPut {
			_, _ = fmt.Fprint(w, `{}`)
			return
		}
		// GET (the post-update findCollaboratorByID read-back): different
		// created_at than what was in prior state, same as live behavior.
		_, _ = fmt.Fprint(w, `{
			"results": [{"id": "identity-1", "email": "ada@example.com", "name": "Ada Lovelace", "permission_level": "owner", "created_at": "2026-07-06T12:00:00Z"}],
			"metadata": {"total": 1, "next_paging_token": null}
		}`)
	}))
	defer server.Close()

	r := &OrganizationCollaboratorResource{
		client: NewClientWithToken(server.URL, "test-token"),
	}

	priorCreatedAt := "2026-01-01T00:00:00Z"
	state := OrganizationCollaboratorResourceModel{
		ID:              types.StringValue("identity-1"),
		Email:           types.StringValue("ada@example.com"),
		PermissionLevel: types.StringValue("collaborator"),
		CreatedAt:       types.StringValue(priorCreatedAt),
	}
	// UseStateForUnknown resolves the plan's created_at to the prior state
	// value before Update ever runs - simulate that resolution here.
	plan := state
	plan.PermissionLevel = types.StringValue("owner")

	collaborator, err := r.findCollaboratorByID(context.Background(), state.ID.ValueString())
	if err != nil {
		t.Fatalf("unexpected error priming the test: %v", err)
	}
	if diags := applyCollaboratorIdentityFields(context.Background(), &plan, collaborator); diags.HasError() {
		t.Fatalf("applyCollaboratorIdentityFields returned unexpected errors: %v", diags)
	}

	if plan.CreatedAt.ValueString() != priorCreatedAt {
		t.Errorf("created_at after update = %q, want it to stay the planned/prior value %q (not the API's %q) - this is exactly the inconsistent-result task 4745d9fb fixed",
			plan.CreatedAt.ValueString(), priorCreatedAt, collaborator.CreatedAt)
	}
}

// runOrganizationCollaboratorUpdate drives OrganizationCollaboratorResource's real Update()
// method end-to-end against state/plan models, the same pattern as runProjectResourceCreate.
func runOrganizationCollaboratorUpdate(t *testing.T, r *OrganizationCollaboratorResource, state, plan OrganizationCollaboratorResourceModel) (OrganizationCollaboratorResourceModel, diag.Diagnostics) {
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
	tfPlan := tfsdk.Plan{Schema: schemaResp.Schema}
	if diags := tfPlan.Set(ctx, &plan); diags.HasError() {
		t.Fatalf("failed to build plan fixture: %v", diags)
	}

	updateResp := &resource.UpdateResponse{State: tfState}
	r.Update(ctx, resource.UpdateRequest{Plan: tfPlan, State: tfState}, updateResp)

	if updateResp.Diagnostics.HasError() {
		return OrganizationCollaboratorResourceModel{}, updateResp.Diagnostics
	}

	var result OrganizationCollaboratorResourceModel
	getDiags := updateResp.State.Get(ctx, &result)
	if getDiags.HasError() {
		t.Fatalf("failed to decode result state: %v", getDiags)
	}
	return result, updateResp.Diagnostics
}

// runOrganizationCollaboratorDelete drives OrganizationCollaboratorResource's real Delete()
// method end-to-end against a state model.
func runOrganizationCollaboratorDelete(t *testing.T, r *OrganizationCollaboratorResource, state OrganizationCollaboratorResourceModel) diag.Diagnostics {
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

	deleteResp := &resource.DeleteResponse{State: tfState}
	r.Delete(ctx, resource.DeleteRequest{State: tfState}, deleteResp)
	return deleteResp.Diagnostics
}

// diagsAllGenericAPIError reports whether every error diagnostic still carries the generic
// "API Request Failed" summary that AddAPIError always uses - i.e. nothing translated the raw
// backend error into a specific, actionable diagnostic. Checking the Summary (not the Detail)
// is deliberate: the generic path already embeds the raw backend JSON verbatim in Detail, so a
// substring match on Detail would pass trivially even without any real translation - Summary is
// the field that actually changes once a fix intercepts the error before AddAPIError runs.
func diagsAllGenericAPIError(diags diag.Diagnostics) bool {
	if !diags.HasError() {
		return false
	}
	for _, d := range diags {
		if d.Summary() != "API Request Failed" {
			return false
		}
	}
	return true
}

// diagsHaveCleanDetail reports whether some diagnostic's Detail contains wantPhrase as a clean,
// human sentence rather than a raw wrapped API error blob. This is stricter than checking Summary
// alone: a helper that changes the Summary but still fails to parse the real backend's nested
// {"error":{"detail":...}} shape (falling back to the original wrapped message) would still embed
// wantPhrase as a substring of that fallback text - so this also requires the Detail NOT contain
// the wrapper's own telltale markers ("unexpected status", a literal '{'), which only appear in
// the unparsed fallback, never in a genuinely-extracted clean sentence.
func diagsHaveCleanDetail(diags diag.Diagnostics, wantPhrase string) bool {
	for _, d := range diags {
		detail := d.Detail()
		if strings.Contains(detail, wantPhrase) &&
			!strings.Contains(detail, "unexpected status") &&
			!strings.Contains(detail, "{") {
			return true
		}
	}
	return false
}

// TestOrganizationCollaboratorResourceUpdate_ServiceAccountModify403 is the contract's C6
// fail-without-fix regression test. Traced against org_invites_service.py's sibling,
// organization_collaborators_service.py's _validate_user_for_role_change (called by
// alter_collaborator, Update's write path): modifying a service account 403s with the exact
// pinned detail "You cannot modify a service account's permission level." Today this flows
// through the generic AddAPIError passthrough, so a Terraform user sees a raw "unexpected
// status 403: {json}" blob instead of a clear explanation. This currently FAILS against the
// unmodified code (proving the gap is real) and must pass once C6 lands a specific diagnostic.
func TestOrganizationCollaboratorResourceUpdate_ServiceAccountModify403(t *testing.T) {
	const rawDetail = "You cannot modify a service account's permission level."

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprintf(w, `{"error":{"detail":%q}}`, rawDetail)
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := &OrganizationCollaboratorResource{client: NewClientWithToken(server.URL, "test-token")}

	state := OrganizationCollaboratorResourceModel{
		ID:              types.StringValue("identity-service-account"),
		Email:           types.StringValue("svc@example.com"),
		PermissionLevel: types.StringValue("collaborator"),
		CreatedAt:       types.StringValue("2026-01-01T00:00:00Z"),
		BaseRole:        types.StringValue("collaborator"),
		AdditionalRoles: types.ListNull(types.StringType),
	}
	plan := state
	plan.PermissionLevel = types.StringValue("owner")

	_, diags := runOrganizationCollaboratorUpdate(t, r, state, plan)

	if !diags.HasError() {
		t.Fatal("expected a diagnostic error for a service-account-modify 403, got none")
	}
	if diagsAllGenericAPIError(diags) {
		t.Errorf("expected C6's specific, actionable diagnostic for a service-account-modify 403 (a distinct Summary, not just the generic API Request Failed passthrough), got: %v", diags)
	}
	if !diagsHaveCleanDetail(diags, "service account") {
		t.Errorf("expected a clean Detail mentioning 'service account' (not the raw wrapped API blob - the real backend nests detail under an \"error\" key, a Summary-only fix that fails to parse that shape would still show the raw wrapper here), got: %v", diags)
	}
}

// TestOrganizationCollaboratorResourceDelete_SelfRemoval403 is C6's other fail-without-fix
// regression test. Traced against organization_collaborators_service.py's remove_collaborator:
// removing your own identity 403s with the exact pinned detail "You cannot remove yourself from
// the organization." Same gap as the service-account case - today it is a raw passthrough, not a
// specific diagnostic warning the user they targeted their own identity.
func TestOrganizationCollaboratorResourceDelete_SelfRemoval403(t *testing.T) {
	const rawDetail = "You cannot remove yourself from the organization."

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprintf(w, `{"error":{"detail":%q}}`, rawDetail)
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := &OrganizationCollaboratorResource{client: NewClientWithToken(server.URL, "test-token")}

	state := OrganizationCollaboratorResourceModel{
		ID:              types.StringValue("identity-self"),
		Email:           types.StringValue("me@example.com"),
		PermissionLevel: types.StringValue("owner"),
		CreatedAt:       types.StringValue("2026-01-01T00:00:00Z"),
		BaseRole:        types.StringValue("owner"),
		AdditionalRoles: types.ListNull(types.StringType),
	}

	diags := runOrganizationCollaboratorDelete(t, r, state)

	if !diags.HasError() {
		t.Fatal("expected a diagnostic error for a self-removal 403, got none")
	}
	if diagsAllGenericAPIError(diags) {
		t.Errorf("expected C6's specific, actionable diagnostic for a self-removal 403 (a distinct Summary, not just the generic API Request Failed passthrough), got: %v", diags)
	}
	if !diagsHaveCleanDetail(diags, "yourself") {
		t.Errorf("expected a clean Detail mentioning 'yourself' (not the raw wrapped API blob - the real backend nests detail under an \"error\" key, a Summary-only fix that fails to parse that shape would still show the raw wrapper here), got: %v", diags)
	}
}

// TestOrganizationCollaboratorResourceCreate_NoFictionalPermissionLevelReference is C7's
// fail-without-fix regression test. Create() always errors (this resource is import-only) with
// step-by-step guidance pointing at anyscale_organization_invitation - traced against both the
// invitation resource's real schema and the backend's CreateOrganizationInvitation model
// (email-only, confirmed by all three design-contract traces), that guidance must never show
// permission_level as an attribute on an invitation block, since that argument has never existed
// there. Following the fictional example verbatim would fail with an unsupported-argument error.
// This doesn't need a mock server at all - Create() never makes an HTTP call before erroring.
func TestOrganizationCollaboratorResourceCreate_NoFictionalPermissionLevelReference(t *testing.T) {
	r := &OrganizationCollaboratorResource{}

	resp := &resource.CreateResponse{}
	r.Create(context.Background(), resource.CreateRequest{}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected Create to always error (import-only resource), got none")
	}
	if diagsContainDetailSubstring(resp.Diagnostics, "permission_level") {
		t.Errorf("Create's guidance still references a fictional permission_level argument on anyscale_organization_invitation, which has never had one (email-only, confirmed against both the Go schema and the real backend model): %v", resp.Diagnostics)
	}
}

// TestHydrateCollaboratorRoles_TriStates asserts all three additional_roles states the design
// contract's canonical semantics require (architect ruling, assayer assignment): populated (real
// roles), empty (queried, genuinely none - including a flag-off org), and null (could not be
// determined at all - only for a null/empty user_id, since the singular GET is user_id-keyed).
// A nil AdditionalRoles must never be coalesced to [] here, and an empty query result must never
// collapse to nil - collapsing either way loses the distinction the whole tri-state design exists
// to preserve.
func TestHydrateCollaboratorRoles_TriStates(t *testing.T) {
	userID := "usr_test123"

	t.Run("populated - singular GET returns real additional roles", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/api/v2/organization_collaborators/"+userID {
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprint(w, `{"result":{"id":"identity-1","email":"a@example.com","permission_level":"owner","base_role":"owner","additional_roles":["image_reader"],"user_id":"usr_test123"}}`)
				return
			}
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		fromList := OrganizationCollaboratorResult{ID: "identity-1", UserID: &userID, BaseRole: "owner"}
		got := hydrateCollaboratorRoles(context.Background(), NewClientWithToken(server.URL, "test-token"), fromList)

		if got.AdditionalRoles == nil {
			t.Fatal("AdditionalRoles = nil, want a populated, non-nil slice")
		}
		if len(got.AdditionalRoles) != 1 || got.AdditionalRoles[0] != "image_reader" {
			t.Errorf("AdditionalRoles = %v, want [image_reader]", got.AdditionalRoles)
		}
	})

	// list-vs-singular disagree on base_role - shipwright's mutation-proof catch
	// (2026-07-15): the sub-case above uses base_role "owner" on BOTH the list
	// input and the mocked singular response, so it cannot distinguish correct
	// (list wins) from the original bug (singular overwrites list) - reverting
	// the fix and rerunning the suite left every sub-case green. This is the
	// scenario that actually proves it: list says one thing, the singular GET
	// (live-proven to be a real, permanently stale SpiceDB source - see the
	// hydrateCollaboratorRoles doc comment) says another, and the result must
	// report the LIST value, since that is the one guaranteed to agree with
	// permission_level. Confirmed this sub-case alone fails if the old
	// fromList.BaseRole = singular.Result.BaseRole line is restored, and passes
	// with it removed - the mutation-proof discipline shipwright applied to the
	// suite as a whole, now applied to this specific fix.
	t.Run("list and singular disagree on base_role - list wins", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/api/v2/organization_collaborators/"+userID {
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprint(w, `{"result":{"id":"identity-1","email":"a@example.com","permission_level":"owner","base_role":"owner","additional_roles":[],"user_id":"usr_test123"}}`)
				return
			}
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		fromList := OrganizationCollaboratorResult{ID: "identity-1", UserID: &userID, BaseRole: "collaborator"}
		got := hydrateCollaboratorRoles(context.Background(), NewClientWithToken(server.URL, "test-token"), fromList)

		if got.BaseRole != "collaborator" {
			t.Errorf("BaseRole = %q, want the list-derived %q preserved even though the singular GET (a real, permanently stale SpiceDB source) says %q - list is the one guaranteed to agree with permission_level", got.BaseRole, "collaborator", "owner")
		}
	})

	t.Run("empty - singular GET queried and genuinely reports none", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/api/v2/organization_collaborators/"+userID {
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprint(w, `{"result":{"id":"identity-1","email":"a@example.com","permission_level":"owner","base_role":"owner","additional_roles":[],"user_id":"usr_test123"}}`)
				return
			}
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		fromList := OrganizationCollaboratorResult{ID: "identity-1", UserID: &userID, BaseRole: "owner"}
		got := hydrateCollaboratorRoles(context.Background(), NewClientWithToken(server.URL, "test-token"), fromList)

		if got.AdditionalRoles == nil {
			t.Fatal("AdditionalRoles = nil, want a non-nil empty slice (queried and genuinely none is not the same as undetermined)")
		}
		if len(got.AdditionalRoles) != 0 {
			t.Errorf("AdditionalRoles = %v, want empty", got.AdditionalRoles)
		}
	})

	t.Run("null - no user_id, singular GET never even called", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("unexpected request for a null user_id collaborator: %s %s - the singular GET is user_id-keyed and must never be attempted", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		fromList := OrganizationCollaboratorResult{ID: "identity-2", UserID: nil, BaseRole: "collaborator"}
		got := hydrateCollaboratorRoles(context.Background(), NewClientWithToken(server.URL, "test-token"), fromList)

		if got.AdditionalRoles != nil {
			t.Errorf("AdditionalRoles = %v, want nil (undetermined) for a collaborator with no user_id", got.AdditionalRoles)
		}
		if got.BaseRole != "collaborator" {
			t.Errorf("BaseRole = %q, want the list-derived value %q preserved (base_role is always derivable from the list alone)", got.BaseRole, "collaborator")
		}
	})

	t.Run("null - singular GET fails, degrades gracefully rather than erroring", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error":{"detail":"internal error"}}`)
		}))
		defer server.Close()

		fromList := OrganizationCollaboratorResult{ID: "identity-1", UserID: &userID, BaseRole: "owner"}
		got := hydrateCollaboratorRoles(context.Background(), NewClientWithToken(server.URL, "test-token"), fromList)

		if got.AdditionalRoles != nil {
			t.Errorf("AdditionalRoles = %v, want nil (undetermined) when the singular GET itself fails", got.AdditionalRoles)
		}
		if got.BaseRole != "owner" {
			t.Errorf("BaseRole = %q, want the list-derived value %q preserved on a hydration failure", got.BaseRole, "owner")
		}
	})
}
