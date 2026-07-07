package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFindUserByID_PagesBeyondFirstPage is a regression test for task
// a41c8e2d: findUserByID (and findUserByUserID/findUserByEmail, which share
// the same listAllOrganizationCollaborators helper) used to only look at the
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

	user, err := d.findUserByID(context.Background(), "identity-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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

	user, err := d.findUserByEmail(context.Background(), "target@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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
