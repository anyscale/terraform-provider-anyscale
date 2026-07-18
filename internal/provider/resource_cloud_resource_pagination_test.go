package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestReadCloudResource_PagesBeyondFirstPage is a regression test for task
// a41c8e2d: readCloudResource used to fetch a single, unpaginated page of a
// cloud's resources, and Read() removes the resource from state on a
// not-found - so a resource whose name only appears past page 1 would be
// phantom-deleted from state, the same severity class task d35713ef fixed
// for organization_collaborator.
func TestReadCloudResource_PagesBeyondFirstPage(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)

		if requestCount == 1 {
			_, _ = fmt.Fprint(w, `{
				"results": [{"name": "resource-1", "cloud_resource_id": "cr-1", "compute_stack": "VM", "region": "us-east-2", "is_default": true}],
				"metadata": {"total": 2, "next_paging_token": "page2"}
			}`)
			return
		}

		_, _ = fmt.Fprint(w, `{
			"results": [{"name": "resource-2", "cloud_resource_id": "cr-2", "compute_stack": "VM", "region": "us-east-2", "is_default": false}],
			"metadata": {"total": 2, "next_paging_token": null}
		}`)
	}))
	defer server.Close()

	r := &CloudResourceResource{
		client: NewClientWithToken(server.URL, "test-token"),
	}

	var state CloudResourceResourceModel
	err := r.readCloudResource(context.Background(), "cloud-id", "resource-2", &state)
	if err != nil {
		t.Fatalf("expected to find resource-2 on page 2, got error: %v", err)
	}
	if state.Name.ValueString() != "resource-2" {
		t.Errorf("name = %q, want %q", state.Name.ValueString(), "resource-2")
	}
	if requestCount != 2 {
		t.Errorf("expected 2 requests (one per page), got %d", requestCount)
	}
}
