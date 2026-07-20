package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestFindUserByID_PagesBeyondFirstPage is a regression test for task
// a41c8e2d: findUser's by-ID/by-user-ID/by-email callers, which share the
// same listAllOrganizationCollaborators helper, used to only look at the
// first page, silently returning "not found" for a user past it.
func TestFindUserByID_PagesBeyondFirstPage(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)

		if requestCount == 1 {
			_, _ = fmt.Fprint(w, `{
				"results": [{"id": "identity-1", "email": "a@example.com", "name": "A", "permission_level": "collaborator"}],
				"metadata": {"total": 2, "next_paging_token": "page2"}
			}`)
			return
		}

		_, _ = fmt.Fprint(w, `{
			"results": [{"id": "identity-2", "email": "b@example.com", "name": "B", "permission_level": "owner"}],
			"metadata": {"total": 2, "next_paging_token": null}
		}`)
	}))
	defer server.Close()

	d := &OrganizationUserDataSource{
		client: NewClientWithToken(server.URL, "test-token"),
	}

	user, diags, err := d.findUser(context.Background(), nil, func(u OrganizationCollaboratorResult) bool { return u.ID == "identity-2" })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if user == nil {
		t.Fatal("expected to find user on page 2, got nil")
	}
	if user.Email.ValueString() != "b@example.com" {
		t.Errorf("email = %q, want %q", user.Email.ValueString(), "b@example.com")
	}
	if requestCount != 2 {
		t.Errorf("expected 2 requests (one per page), got %d", requestCount)
	}
}

// TestFindUserByEmail_PagesBeyondFirstPageAndSendsFilter confirms the email
// filter is still sent server-side (as a narrowing hint) while pagination is
// applied on top of it, and that a case-insensitive match past page 1 is found.
func TestFindUserByEmail_PagesBeyondFirstPageAndSendsFilter(t *testing.T) {
	var sawEmailParam string
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if e := r.URL.Query().Get("email"); e != "" {
			sawEmailParam = e
		}
		w.WriteHeader(http.StatusOK)

		if requestCount == 1 {
			_, _ = fmt.Fprint(w, `{
				"results": [{"id": "identity-1", "email": "other@example.com", "name": "Other", "permission_level": "collaborator"}],
				"metadata": {"total": 2, "next_paging_token": "page2"}
			}`)
			return
		}

		_, _ = fmt.Fprint(w, `{
			"results": [{"id": "identity-2", "email": "Target@Example.com", "name": "Target", "permission_level": "owner"}],
			"metadata": {"total": 2, "next_paging_token": null}
		}`)
	}))
	defer server.Close()

	d := &OrganizationUserDataSource{
		client: NewClientWithToken(server.URL, "test-token"),
	}

	const targetEmail = "target@example.com"
	user, diags, err := d.findUser(context.Background(), url.Values{"email": []string{targetEmail}}, func(u OrganizationCollaboratorResult) bool {
		return strings.EqualFold(u.Email, targetEmail)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if user == nil {
		t.Fatal("expected a case-insensitive match on page 2, got nil")
	}
	if sawEmailParam != "target@example.com" {
		t.Errorf("email query param = %q, want the requested email to be sent server-side", sawEmailParam)
	}
	if requestCount != 2 {
		t.Errorf("expected 2 requests (one per page), got %d", requestCount)
	}
}

// TestOrganizationCollaboratorToUserModel_NullName is the DS-OU-1 mutation-proof
// regression guard for the singular data source. models.go documents the wire
// field as `Name *string` ("Can be null"), the same nullability the adjacent
// UserID field already handles correctly, but organizationCollaboratorToUserModel
// collapses a nil Name into "" instead of Terraform null. This currently FAILS
// against that code (name comes back "" not null) - that failure is the
// mutation-proof evidence the null collapse is real. It must pass once the
// conversion uses types.StringPointerValue for Name.
func TestOrganizationCollaboratorToUserModel_NullName(t *testing.T) {
	result := OrganizationCollaboratorResult{
		ID:              "identity-1",
		UserID:          nil,
		Email:           "svc@example.com",
		Name:            nil,
		PermissionLevel: "collaborator",
		CreatedAt:       "2024-01-01T00:00:00Z",
	}

	got, diags := organizationCollaboratorToUserModel(context.Background(), result)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if !got.Name.IsNull() {
		t.Errorf("Name = %#v, want null for a nil API name, got a non-null value (likely \"\")", got.Name)
	}
}

// TestOrganizationCollaboratorToUserModel_NonNullName guards the other side of
// the same fix: a real name must still come through as its exact value, not
// null, so the DS-OU-1 fix cannot be satisfied by trivially always returning
// null regardless of input.
func TestOrganizationCollaboratorToUserModel_NonNullName(t *testing.T) {
	name := "Ada Lovelace"
	result := OrganizationCollaboratorResult{
		ID:              "identity-2",
		Email:           "ada@example.com",
		Name:            &name,
		PermissionLevel: "owner",
		CreatedAt:       "2024-01-01T00:00:00Z",
	}

	got, diags := organizationCollaboratorToUserModel(context.Background(), result)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if got.Name.IsNull() {
		t.Fatal("Name = null, want the real name to come through")
	}
	if got.Name.ValueString() != name {
		t.Errorf("Name = %q, want %q", got.Name.ValueString(), name)
	}
}

// TestOrganizationUsersDataSource_PagesBeyondFirstPage is a regression test
// for the plural anyscale_organization_users data source, which used to
// silently truncate its result list at the first page.
func TestOrganizationUsersDataSource_PagesBeyondFirstPage(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)

		if requestCount == 1 {
			_, _ = fmt.Fprint(w, `{
				"results": [{"id": "identity-1", "email": "a@example.com", "name": "A", "permission_level": "collaborator"}],
				"metadata": {"total": 2, "next_paging_token": "page2"}
			}`)
			return
		}

		_, _ = fmt.Fprint(w, `{
			"results": [{"id": "identity-2", "email": "b@example.com", "name": "B", "permission_level": "owner"}],
			"metadata": {"total": 2, "next_paging_token": null}
		}`)
	}))
	defer server.Close()

	users, err := listAllOrganizationCollaborators(context.Background(), NewClientWithToken(server.URL, "test-token"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 users across both pages, got %d", len(users))
	}
	if requestCount != 2 {
		t.Errorf("expected 2 requests (one per page), got %d", requestCount)
	}
}
