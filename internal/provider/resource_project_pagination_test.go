package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDataSourceGetCollaborators_PagesBeyondFirstPage is the design contract's
// AC3 (F2): the singular anyscale_project data source's getCollaborators used
// to call DoRequestAndParse directly (first page only), unlike the plural
// anyscale_projects data source's fetchProjects, which paginates. A project
// with more collaborators than fit on one page silently dropped the rest from
// the data source's output. (The resource-side twin of this test went away
// with the anyscale_project resource's collaborator block in v0.25.0 - the
// data source is now the only surface that reads collaborators.)
func TestDataSourceGetCollaborators_PagesBeyondFirstPage(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)

		if requestCount == 1 {
			_, _ = fmt.Fprint(w, `{
				"results": [{"id": "identity-1", "value": {"id": "user-1", "email": "a@example.com"}, "permission_level": "owner"}],
				"metadata": {"total": 2, "next_paging_token": "page2"}
			}`)
			return
		}

		_, _ = fmt.Fprint(w, `{
			"results": [{"id": "identity-2", "value": {"id": "user-2", "email": "b@example.com"}, "permission_level": "write"}],
			"metadata": {"total": 2, "next_paging_token": null}
		}`)
	}))
	defer server.Close()

	d := &ProjectDataSource{
		client: NewClientWithToken(server.URL, "test-token"),
	}

	collaborators, err := d.getCollaborators(context.Background(), "project-id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(collaborators) != 2 {
		t.Fatalf("expected 2 collaborators across both pages, got %d", len(collaborators))
	}
	if collaborators[1].Email.ValueString() != "b@example.com" {
		t.Errorf("second collaborator email = %q, want %q", collaborators[1].Email.ValueString(), "b@example.com")
	}
	if requestCount != 2 {
		t.Errorf("expected 2 requests (one per page), got %d", requestCount)
	}
}
