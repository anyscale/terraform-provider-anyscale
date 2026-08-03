package acctest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
)

// TestListAllCloudsForSweep_MultiPage proves listAllCloudsForSweep actually
// follows next_paging_token across pages instead of silently truncating to
// page one - a truncation bug here would not error, it would just make the
// sweeper blind to leaked clouds on later pages (project and service already
// have this same guard test; cloud did not).
func TestListAllCloudsForSweep_MultiPage(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			if got := r.URL.Query().Get("paging_token"); got != "" {
				t.Errorf("first request should not carry a paging_token, got %q", got)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"results":[{"id":"cld_1","name":"tfacc-cloud-one","created_at":"2020-01-01T00:00:00Z"}],"metadata":{"next_paging_token":"page2"}}`)
			return
		}
		if got := r.URL.Query().Get("paging_token"); got != "page2" {
			t.Errorf("second request should carry paging_token=page2 as a query param, got %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results":[{"id":"cld_2","name":"tfacc-cloud-two","created_at":"2020-01-01T00:00:00Z"}],"metadata":{"next_paging_token":null}}`)
	}))
	defer server.Close()

	client := provider.NewClientWithToken(server.URL, "test-token")
	clouds, err := listAllCloudsForSweep(context.Background(), client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("expected 2 requests (one per page), got %d", requestCount)
	}
	if len(clouds) != 2 {
		t.Fatalf("expected 2 clouds across both pages, got %d (silent truncation would show up as a short result here)", len(clouds))
	}
	if clouds[0].ID != "cld_1" || clouds[1].ID != "cld_2" {
		t.Fatalf("expected [cld_1, cld_2] in page order, got %+v", clouds)
	}
}
