package acctest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
)

// The live cloud_access tests assert that a removed member is ABSENT from the
// backend's list. If an error response decoded as an empty list, that
// assertion would pass without the backend ever answering.
func TestFetchCollaboratorEmails_Non200IsAnError(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"detail":"nope"}}`))
		}))
		client := provider.NewClientWithToken(server.URL, "test-token")
		emails, err := fetchCollaboratorEmails(context.Background(), client, http.MethodGet, "/api/v2/projects/prj_x/collaborators/users", nil)
		server.Close()
		if err == nil {
			t.Errorf("status %d: expected an error, got emails %v", status, emails)
		}
	}
}

// Positive control on the same path: a 200 list is parsed and lowercased.
func TestFetchCollaboratorEmails_ParsesAndLowercases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"value":{"email":"A@Example.com"}},{"value":{"email":"b@example.com"}}]}`))
	}))
	defer server.Close()
	client := provider.NewClientWithToken(server.URL, "test-token")
	emails, err := fetchCollaboratorEmails(context.Background(), client, http.MethodGet, "/api/v2/projects/prj_x/collaborators/users", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(emails) != 2 || !emails["a@example.com"] || !emails["b@example.com"] {
		t.Fatalf("got %v, want a@example.com and b@example.com", emails)
	}
}
