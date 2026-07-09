package acctest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
)

// TestSearchContainerImagesByContains_MultiPage is the GATE-F2 multi-page
// proof for the sweeper's search call site. A single-page happy-path test
// would pass even if the pagination loop were silently broken - this is the
// same body-vs-query paging_token shape that CC5b hit in compute_config
// (deferred there; not deferred here, since forge migrated this call site to
// api/v2 in 3c43eea). This drives the real searchContainerImagesByContains
// against a 2-page mock and asserts (a) both pages' results are collected
// and (b) the second request actually carries the exact token the first
// response returned, proving the loop follows the token rather than
// stopping after page 1 or re-requesting it.
//
// Mirrors the already-migrated api/v2 query-param shape proven in
// TestGetApplicationTemplateByName_ExactMatchOnPageTwo
// (data_source_container_image_pagination_test.go) -- same endpoint, same
// PaginatedRequest helper, same paging_token/name_contains/include_archived
// query params, just a different include_archived value (sweep wants
// archived rows visible too, so it can log alreadyArchivedCount).
func TestSearchContainerImagesByContains_MultiPage(t *testing.T) {
	requestCount := 0
	var pagingTokens []string
	var includeArchivedValues []string

	const token = "sweep-page-2-token"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/application_templates/" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
			return
		}

		requestCount++
		pagingTokens = append(pagingTokens, r.URL.Query().Get("paging_token"))
		includeArchivedValues = append(includeArchivedValues, r.URL.Query().Get("include_archived"))

		w.Header().Set("Content-Type", "application/json")

		if requestCount == 1 {
			resp := provider.ApplicationTemplatesListResponse{
				Results: []provider.ApplicationTemplateResult{
					{ID: "apptemp_page1_a", Name: "tfacc-multipage-a", CreatedAt: "2024-01-01T00:00:00Z"},
					{ID: "apptemp_page1_b", Name: "tfacc-multipage-b", CreatedAt: "2024-01-01T00:00:00Z"},
				},
			}
			nextToken := token
			resp.Metadata.NextPagingToken = &nextToken
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		// Second (and, if the loop is correct, final) call.
		resp := provider.ApplicationTemplatesListResponse{
			Results: []provider.ApplicationTemplateResult{
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

	if requestCount != 2 {
		t.Fatalf("expected exactly 2 HTTP requests (one per page), got %d", requestCount)
	}
	if pagingTokens[0] != "" {
		t.Errorf("first request should not carry a paging_token, got %q", pagingTokens[0])
	}
	if pagingTokens[1] != token {
		t.Errorf("second request paging_token = %q, want %q -- loop is not following the token", pagingTokens[1], token)
	}
	for i, ia := range includeArchivedValues {
		if ia != "true" {
			t.Errorf("request %d include_archived = %q, want %q -- sweep needs archived rows visible for alreadyArchivedCount bookkeeping", i+1, ia, "true")
		}
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
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/application_templates/" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
			return
		}

		requestCount++
		w.Header().Set("Content-Type", "application/json")
		resp := provider.ApplicationTemplatesListResponse{
			Results: []provider.ApplicationTemplateResult{
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
