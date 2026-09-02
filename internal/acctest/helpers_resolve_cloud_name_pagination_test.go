package acctest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestResolveCloudNameToIDPaginatesAcrossPages is the coverage this repo's
// own test-suite fixture resolver shipped without: B1b's fix
// (05c13d9, helpers.go's resolveCloudNameToID, the fifth instance of the
// same page-1-only bug across this provider's by-name lookups) landed with
// zero tests, and the real test org fits on one page, so nothing in the
// acceptance suite ever exercises the pagination loop. This closes that gap.
//
// Deliberately NOT TestAcc-prefixed and does NOT call
// SkipIfNotAcceptanceTest: this is a plain unit test (that prefix means
// shard-gated real acceptance test everywhere else in this repo), and
// make test runs go test ./... with TF_ACC unset - an acceptance-gated,
// non-TestAcc-prefixed test would match neither acctest shard's -run regex
// AND skip under a plain unit-test run, executing nowhere while looking
// perfectly healthy locally under TF_ACC=1. Points GetTestClient at a local
// httptest mock via ANYSCALE_API_URL, with a dummy ANYSCALE_CLI_TOKEN so
// client construction does not fail for an unrelated reason first (both
// read directly in GetTestClient, helpers.go).
//
// Confirmed failing-first (2026-07-25) against pre-B1b helpers.go: fails
// with "no cloud found with name 'helper-cloud-on-page-two'", since the
// unfixed resolveCloudNameToID only read page 1. Confirmed passing against
// the landed fix, and mutation-proven with the normal break-and-revert
// cycle, byte-diff clean.
func TestResolveCloudNameToIDPaginatesAcrossPages(t *testing.T) {
	const targetCloudID = "cld_helper_page_two_target"
	const targetCloudName = "helper-cloud-on-page-two"

	page1 := []map[string]any{
		{"id": "cld_helper_page_one_a", "name": "helper-unrelated-cloud-a", "created_at": "2026-01-01T00:00:00Z"},
		{"id": "cld_helper_page_one_b", "name": "helper-unrelated-cloud-b", "created_at": "2026-01-01T00:00:00Z"},
	}
	page2 := []map[string]any{
		{"id": targetCloudID, "name": targetCloudName, "created_at": "2026-01-02T00:00:00Z"},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/clouds", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		token := r.URL.Query().Get("paging_token")
		var results []map[string]any
		var nextToken any
		switch token {
		case "":
			results = page1
			nextToken = "page2"
		case "page2":
			results = page2
			nextToken = nil
		default:
			t.Errorf("unexpected paging_token %q on GET /api/v2/clouds", token)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": results,
			"metadata": map[string]any{
				"total":             len(page1) + len(page2),
				"next_paging_token": nextToken,
			},
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "dummy-token-for-pagination-test")

	gotID, err := resolveCloudNameToID(t, targetCloudName)
	if err != nil {
		t.Fatalf("resolveCloudNameToID(%q) returned an unexpected error: %v", targetCloudName, err)
	}
	if gotID != targetCloudID {
		t.Errorf("resolveCloudNameToID(%q) = %q, want %q (the page-2 cloud)", targetCloudName, gotID, targetCloudID)
	}
}
