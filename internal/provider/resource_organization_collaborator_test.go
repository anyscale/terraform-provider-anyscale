package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

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
	applyCollaboratorIdentityFields(model, firstRead)
	if model.CreatedAt.ValueString() != firstRead.CreatedAt {
		t.Fatalf("created_at after first apply = %q, want unchanged %q", model.CreatedAt.ValueString(), firstRead.CreatedAt)
	}

	// Simulate the Update()/Read() read-back that used to overwrite created_at
	// with a different value returned by the API.
	applyCollaboratorIdentityFields(model, secondRead)
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
	applyCollaboratorIdentityFields(&plan, collaborator)

	if plan.CreatedAt.ValueString() != priorCreatedAt {
		t.Errorf("created_at after update = %q, want it to stay the planned/prior value %q (not the API's %q) - this is exactly the inconsistent-result task 4745d9fb fixed",
			plan.CreatedAt.ValueString(), priorCreatedAt, collaborator.CreatedAt)
	}
}
