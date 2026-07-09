package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGetApplicationTemplateByName_ExactMatchOnPageTwo is the GATE-A3 proof.
// getApplicationTemplateByName searches via a name_contains substring filter,
// so an exact match can legitimately sit on any page. Before A3, this call
// site did not follow next_paging_token at all, so an exact match that only
// existed on page 2+ was silently missed -- a false negative ("no cluster
// environment found") even though the environment genuinely existed. This
// engineers page 1 to contain ONLY non-exact substring hits and puts the
// real exact match on page 2, so the test can only pass if the pagination
// loop is actually followed to completion before filtering.
//
// Sibling multi-page proofs for the other two call sites that share this
// PaginatedRequest/next_paging_token contract: the plural data source's
// TestFetchContainerImages_MultiPage in
// data_source_container_images_pagination_test.go, and the sweeper's
// TestSearchContainerImagesByContains_MultiPage in
// internal/acctest/sweeper_container_image_pagination_test.go. Unlike those
// two (F2 behavior-neutrality proofs), this one proves A3's fix: a real bug,
// not a migration-neutrality guard.
func TestGetApplicationTemplateByName_ExactMatchOnPageTwo(t *testing.T) {
	const searchName = "tfacc-buildenv"
	const token = "buildenv-page-2-token"

	requestCount := 0
	var pagingTokens []string
	var nameContainsValues []string
	var includeArchivedValues []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/application_templates/" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
			return
		}

		requestCount++
		pagingTokens = append(pagingTokens, r.URL.Query().Get("paging_token"))
		nameContainsValues = append(nameContainsValues, r.URL.Query().Get("name_contains"))
		includeArchivedValues = append(includeArchivedValues, r.URL.Query().Get("include_archived"))

		w.Header().Set("Content-Type", "application/json")

		if requestCount == 1 {
			// Page 1: substring hits only -- NEITHER name is an exact match
			// for "tfacc-buildenv". If the loop stops here (the A3 bug),
			// filterExactApplicationTemplateMatches finds nothing and the
			// call must fail.
			resp := ApplicationTemplatesListResponse{
				Results: []ApplicationTemplateResult{
					{ID: "apptemp_old", Name: "tfacc-buildenv-old", CreatorID: "user_1", CreatedAt: "2024-01-01T00:00:00Z"},
					{ID: "apptemp_v2", Name: "tfacc-buildenv-v2", CreatorID: "user_1", CreatedAt: "2024-01-01T00:00:00Z"},
				},
			}
			nextToken := token
			resp.Metadata.NextPagingToken = &nextToken
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		// Page 2: the real exact match, plus one more substring decoy.
		resp := ApplicationTemplatesListResponse{
			Results: []ApplicationTemplateResult{
				{ID: "apptemp_v3", Name: "tfacc-buildenv-v3", CreatorID: "user_1", CreatedAt: "2024-01-01T00:00:00Z"},
				{ID: "apptemp_exact", Name: searchName, CreatorID: "user_1", CreatedAt: "2024-01-02T00:00:00Z"},
			},
		}
		// NextPagingToken stays nil -> loop must terminate after this page.
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	d := &ContainerImageDataSource{client: NewClientWithToken(server.URL, "fake-token-buildenv-pagetwo")}

	template, err := d.getApplicationTemplateByName(context.Background(), searchName)
	if err != nil {
		t.Fatalf("getApplicationTemplateByName returned error: %v -- exact match on page 2 was not found", err)
	}

	if requestCount != 2 {
		t.Fatalf("expected exactly 2 requests (one per page), got %d", requestCount)
	}
	if pagingTokens[0] != "" {
		t.Errorf("first request should not carry a paging_token, got %q", pagingTokens[0])
	}
	if pagingTokens[1] != token {
		t.Errorf("second request paging_token = %q, want %q -- loop is not following the token", pagingTokens[1], token)
	}
	for i, nc := range nameContainsValues {
		if nc != searchName {
			t.Errorf("request %d name_contains = %q, want %q", i+1, nc, searchName)
		}
	}
	for i, ia := range includeArchivedValues {
		if ia != "false" {
			t.Errorf("request %d include_archived = %q, want %q", i+1, ia, "false")
		}
	}

	if template == nil {
		t.Fatal("template is nil")
	}
	if template.ID != "apptemp_exact" {
		t.Errorf("got template ID %q, want %q -- either a page-1 decoy was returned or matching is broken", template.ID, "apptemp_exact")
	}
	if template.Name != searchName {
		t.Errorf("got template Name %q, want %q", template.Name, searchName)
	}
}

// TestGetApplicationTemplateByName_NoExactMatchAcrossAllPages guards the
// opposite failure mode of the same bug class: when every page's results are
// substring hits but NONE is an exact match, the call must walk every page
// (proving it isn't short-circuiting early) and then report a clean
// not-found error rather than returning a false-positive on the first
// close-enough decoy or hanging.
func TestGetApplicationTemplateByName_NoExactMatchAcrossAllPages(t *testing.T) {
	const searchName = "tfacc-buildenv"
	const token = "buildenv-decoy-page-2-token"

	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")

		if requestCount == 1 {
			resp := ApplicationTemplatesListResponse{
				Results: []ApplicationTemplateResult{
					{ID: "apptemp_old", Name: "tfacc-buildenv-old", CreatorID: "user_1", CreatedAt: "2024-01-01T00:00:00Z"},
				},
			}
			nextToken := token
			resp.Metadata.NextPagingToken = &nextToken
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		resp := ApplicationTemplatesListResponse{
			Results: []ApplicationTemplateResult{
				{ID: "apptemp_v2", Name: "tfacc-buildenv-v2", CreatorID: "user_1", CreatedAt: "2024-01-01T00:00:00Z"},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	d := &ContainerImageDataSource{client: NewClientWithToken(server.URL, "fake-token-buildenv-nomatch")}

	template, err := d.getApplicationTemplateByName(context.Background(), searchName)
	if err == nil {
		t.Fatalf("expected a not-found error, got template %+v", template)
	}
	if requestCount != 2 {
		t.Fatalf("expected exactly 2 requests (both pages walked before failing), got %d", requestCount)
	}
}
