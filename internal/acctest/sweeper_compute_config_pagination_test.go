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

// TestSearchComputeConfigsByContains_MultiPage is the CC5b-tail mutation-proof
// for the sweeper's search call site, mirroring
// TestSearchComputeTemplatesPaged_SendsPagingAsQueryParamsNotBody and
// TestFetchComputeConfigVersions_FollowsPagingToken (data_source_compute_config_test.go),
// the data source's already-proven tests for the identical api/v2 transport
// this sweeper now shares. A naive migration could keep nesting
// paging/paging_token/count inside the JSON body (the old ext/v0 shape) - it
// would compile, hit /api/v2/compute_templates/search, get HTTP 200 back, and
// silently paginate wrong (always page 1's worth of data, no error). This
// test's mock is deliberately strict about where it reads pagination from,
// and also asserts the second landmine forge found beyond the pagination
// transport: version and archive_status must be sent explicitly in the body,
// or api/v2's own defaults (latest-version-only, unarchived-only) would
// silently narrow which rows the sweeper ever sees.
func TestSearchComputeConfigsByContains_MultiPage(t *testing.T) {
	requestCount := 0
	var pagingTokensSeen []string
	var countsSeen []string

	const token = "sweep-compute-config-page-2-token"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/compute_templates/search" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
			return
		}

		requestCount++
		pagingTokensSeen = append(pagingTokensSeen, r.URL.Query().Get("paging_token"))
		countsSeen = append(countsSeen, r.URL.Query().Get("count"))

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("failed to parse request body: %v", err)
		}

		if _, hasPaging := payload["paging"]; hasPaging {
			t.Error(`request body contains a "paging" key - pagination must be sent as URL query params on api/v2, not nested in the body`)
		}
		if nameFilter, ok := payload["name"].(map[string]any); !ok || nameFilter["contains"] == nil {
			t.Errorf(`request body missing expected name.contains filter, got name=%v`, payload["name"])
		}
		if archiveStatus, ok := payload["archive_status"].(string); !ok || archiveStatus != "ALL" {
			t.Errorf(`request body archive_status = %v, want "ALL" (omitting it would silently narrow to api/v2's NOT_ARCHIVED default)`, payload["archive_status"])
		}
		if version, ok := payload["version"].(float64); !ok || version != -2 {
			t.Errorf(`request body version = %v, want -2 (omitting it would silently narrow to api/v2's latest-version-only default, letting a recently-churned leak evade the sweeper)`, payload["version"])
		}

		w.Header().Set("Content-Type", "application/json")

		if requestCount == 1 {
			_, _ = w.Write([]byte(`{
				"results": [
					{"id": "cpt_page1_a", "name": "tfacc-multipage", "created_at": "2024-01-01T00:00:00Z", "anonymous": false},
					{"id": "cpt_page1_b", "name": "tfacc-multipage", "created_at": "2024-01-01T00:00:00Z", "anonymous": false}
				],
				"metadata": {"next_paging_token": "` + token + `"}
			}`))
			return
		}

		// Second (and, if the loop is correct, final) call.
		_, _ = w.Write([]byte(`{
			"results": [
				{"id": "cpt_page2_c", "name": "tfacc-multipage", "created_at": "2024-01-01T00:00:00Z", "anonymous": false}
			],
			"metadata": {"next_paging_token": null}
		}`))
	}))
	defer server.Close()

	client := provider.NewClientWithToken(server.URL, "fake-token-multipage")
	results, err := searchComputeConfigsByContains(context.Background(), client, "tfacc-multipage")
	if err != nil {
		t.Fatalf("searchComputeConfigsByContains returned error: %v", err)
	}

	if requestCount != 2 {
		t.Fatalf("expected exactly 2 HTTP requests (one per page), got %d", requestCount)
	}
	if pagingTokensSeen[0] != "" {
		t.Errorf("first request should not carry a paging_token query param, got %q", pagingTokensSeen[0])
	}
	if pagingTokensSeen[1] != token {
		t.Errorf("second request paging_token query param = %q, want %q - loop is not following the token", pagingTokensSeen[1], token)
	}
	for i, c := range countsSeen {
		if c != "100" {
			t.Errorf("request %d count query param = %q, want \"100\"", i+1, c)
		}
	}

	wantIDs := map[string]bool{"cpt_page1_a": false, "cpt_page1_b": false, "cpt_page2_c": false}
	for _, r := range results {
		if _, known := wantIDs[r.ID]; !known {
			t.Errorf("unexpected result ID %q", r.ID)
			continue
		}
		wantIDs[r.ID] = true
	}
	for id, found := range wantIDs {
		if !found {
			t.Errorf("result %q from page 2 was NOT collected - silent truncation past page 1", id)
		}
	}
	if len(results) != 3 {
		t.Errorf("got %d total results, want 3 (2 from page 1 + 1 from page 2)", len(results))
	}
}

// TestSearchComputeConfigsByContains_SinglePageStopsAfterOnePage guards the
// other direction of the same bug class: a response with no
// next_paging_token must NOT trigger a second request. Without this, a loop
// that always re-requests (or infinite-loops on a nil/empty token) would only
// show up as a hang or duplicate-results bug, not a clean failure.
func TestSearchComputeConfigsByContains_SinglePageStopsAfterOnePage(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/compute_templates/search" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
			return
		}

		requestCount++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"results": [
				{"id": "cpt_only", "name": "tfacc-singlepage", "created_at": "2024-01-01T00:00:00Z", "anonymous": false}
			],
			"metadata": {"next_paging_token": null}
		}`))
	}))
	defer server.Close()

	client := provider.NewClientWithToken(server.URL, "fake-token-singlepage")
	results, err := searchComputeConfigsByContains(context.Background(), client, "tfacc-singlepage")
	if err != nil {
		t.Fatalf("searchComputeConfigsByContains returned error: %v", err)
	}

	if requestCount != 1 {
		t.Errorf("expected exactly 1 HTTP request for a single-page response, got %d", requestCount)
	}
	if len(results) != 1 || results[0].ID != "cpt_only" {
		t.Errorf("got %+v, want exactly one result with ID cpt_only", results)
	}
}
