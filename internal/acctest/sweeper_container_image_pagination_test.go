package acctest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
)

// TestSearchContainerImagesByContains_MultiPage is the GATE-F2 multi-page
// proof for the sweeper's search call site. A single-page happy-path test
// would pass even if the pagination loop were silently broken - this is the
// same body-vs-query paging_token shape that CC5b hit in compute_config
// (deferred there; not deferred here, since forge is migrating this call
// site to api/v2). This drives the real searchContainerImagesByContains
// against a 2-page mock and asserts (a) both pages' results are collected
// and (b) the second request actually carries the exact token the first
// response returned, proving the loop follows the token rather than
// stopping after page 1 or re-requesting it.
//
// This characterizes TODAY's ext/v0 body-shaped pagination contract
// (paging_token nested inside the JSON POST body). Once forge reshapes this
// call to api/v2 query params, only the request-shape assertions below need
// to move from the body to the URL query string - the two-page collection
// assertion carries over unchanged.
func TestSearchContainerImagesByContains_MultiPage(t *testing.T) {
	var requests []map[string]interface{}

	const token = "sweep-page-2-token"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/ext/v0/cluster_environments/search" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		requests = append(requests, payload)

		w.Header().Set("Content-Type", "application/json")

		if len(requests) == 1 {
			if paging, ok := payload["paging"].(map[string]interface{}); ok {
				if _, hasToken := paging["paging_token"]; hasToken {
					t.Errorf("first request should not carry a paging_token, got %v", paging)
				}
			}
			resp := sweepContainerImageListResponse{
				Results: []sweepContainerImageResult{
					{ID: "apptemp_page1_a", Name: "tfacc-multipage-a", CreatedAt: "2024-01-01T00:00:00Z"},
					{ID: "apptemp_page1_b", Name: "tfacc-multipage-b", CreatedAt: "2024-01-01T00:00:00Z"},
				},
			}
			nextToken := token
			resp.Metadata.NextPagingToken = &nextToken
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		// Second (and, if the loop is correct, final) call: must carry the
		// exact token page 1 returned.
		paging, ok := payload["paging"].(map[string]interface{})
		if !ok {
			t.Fatalf("second request missing paging object: %v", payload)
		}
		gotToken, _ := paging["paging_token"].(string)
		if gotToken != token {
			t.Errorf("second request paging_token = %q, want %q -- loop is not following the token", gotToken, token)
		}
		resp := sweepContainerImageListResponse{
			Results: []sweepContainerImageResult{
				{ID: "apptemp_page2_c", Name: "tfacc-multipage-c", CreatedAt: "2024-01-01T00:00:00Z"},
			},
		}
		// NextPagingToken stays nil here -> loop must terminate after this page.
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := provider.NewClientWithToken(server.URL, "fake-token-multipage")
	results, err := searchContainerImagesByContains(context.Background(), client, "tfacc-multipage")
	if err != nil {
		t.Fatalf("searchContainerImagesByContains returned error: %v", err)
	}

	if len(requests) != 2 {
		t.Fatalf("expected exactly 2 HTTP requests (one per page), got %d", len(requests))
	}

	wantIDs := map[string]bool{"apptemp_page1_a": false, "apptemp_page1_b": false, "apptemp_page2_c": false}
	for _, r := range results {
		if _, known := wantIDs[r.ID]; !known {
			t.Errorf("unexpected result ID %q", r.ID)
			continue
		}
		wantIDs[r.ID] = true
	}
	for id, found := range wantIDs {
		if !found {
			t.Errorf("result %q from page 2 was NOT collected -- silent truncation past page 1", id)
		}
	}
	if len(results) != 3 {
		t.Errorf("got %d total results, want 3 (2 from page 1 + 1 from page 2)", len(results))
	}
}

// TestSearchContainerImagesByContains_SinglePageStopsAfterOnePage guards the
// other direction of the same bug class: a response with no
// next_paging_token must NOT trigger a second request. Without this, a loop
// that always re-requests (or infinite-loops on a nil/empty token) would
// only show up as a hang or a duplicate-results bug, not a clean failure.
func TestSearchContainerImagesByContains_SinglePageStopsAfterOnePage(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		resp := sweepContainerImageListResponse{
			Results: []sweepContainerImageResult{
				{ID: "apptemp_only", Name: "tfacc-singlepage", CreatedAt: "2024-01-01T00:00:00Z"},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := provider.NewClientWithToken(server.URL, "fake-token-singlepage")
	results, err := searchContainerImagesByContains(context.Background(), client, "tfacc-singlepage")
	if err != nil {
		t.Fatalf("searchContainerImagesByContains returned error: %v", err)
	}

	if requestCount != 1 {
		t.Errorf("expected exactly 1 HTTP request for a single-page response, got %d", requestCount)
	}
	if len(results) != 1 || results[0].ID != "apptemp_only" {
		t.Errorf("got %+v, want exactly one result with ID apptemp_only", results)
	}
}
