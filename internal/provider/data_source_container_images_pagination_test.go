package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestFetchContainerImages_MultiPage is the GATE-F2 multi-page proof for the
// plural container-images data source's list call site. Sibling proof for
// the sweeper's identical search endpoint lives in
// internal/acctest/sweeper_container_image_pagination_test.go - same shape,
// same silent-truncation failure mode, different call site. A single-page
// happy-path test would pass even if the loop never followed
// next_paging_token at all, so this drives the real fetchContainerImages
// against a 2-page mock and asserts both pages are collected AND that the
// second request carries the exact token the first response returned.
//
// Post-F2 (6e6ea79): fetchContainerImages calls GET /api/v2/application_templates/
// via the shared PaginatedRequest helper, which threads next_paging_token as a
// URL query parameter (paging_token) rather than an ext/v0-style request body
// field. Each result's latest-build summary now arrives embedded on the
// decorated ApplicationTemplateResult (LatestBuild), so there is no more
// per-item build lookup to stub out here.
func TestFetchContainerImages_MultiPage(t *testing.T) {
	requestCount := 0
	var pagingTokens []string

	const token = "images-page-2-token"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/application_templates/" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
			return
		}

		requestCount++
		pagingTokens = append(pagingTokens, r.URL.Query().Get("paging_token"))

		w.Header().Set("Content-Type", "application/json")

		if requestCount == 1 {
			resp := ApplicationTemplatesListResponse{
				Results: []ApplicationTemplateResult{
					{ID: "apptemp_img_a", Name: "tfacc-images-a", CreatorID: "user_1", CreatedAt: "2024-01-01T00:00:00Z"},
					{ID: "apptemp_img_b", Name: "tfacc-images-b", CreatorID: "user_1", CreatedAt: "2024-01-01T00:00:00Z"},
				},
			}
			nextToken := token
			resp.Metadata.NextPagingToken = &nextToken
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		resp := ApplicationTemplatesListResponse{
			Results: []ApplicationTemplateResult{
				{ID: "apptemp_img_c", Name: "tfacc-images-c", CreatorID: "user_1", CreatedAt: "2024-01-01T00:00:00Z"},
			},
		}
		// NextPagingToken stays nil -> loop must terminate after this page.
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	d := &ContainerImagesDataSource{client: NewClientWithToken(server.URL, "fake-token-images-multipage")}

	images, err := d.fetchContainerImages(context.Background(), url.Values{})
	if err != nil {
		t.Fatalf("fetchContainerImages returned error: %v", err)
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

	wantIDs := map[string]bool{"apptemp_img_a": false, "apptemp_img_b": false, "apptemp_img_c": false}
	for _, img := range images {
		id := img.ID.ValueString()
		if _, known := wantIDs[id]; !known {
			t.Errorf("unexpected result ID %q", id)
			continue
		}
		wantIDs[id] = true
	}
	for id, found := range wantIDs {
		if !found {
			t.Errorf("result %q from page 2 was NOT collected -- silent truncation past page 1", id)
		}
	}
	if len(images) != 3 {
		t.Errorf("got %d total images, want 3 (2 from page 1 + 1 from page 2)", len(images))
	}
}

// TestFetchContainerImages_SinglePageStopsAfterOnePage guards the other
// direction of the same bug class: a response with no next_paging_token
// must not trigger a second request. Without this, a loop that always
// re-requests (or infinite-loops on a nil/empty token) would only show up
// as a hang or duplicate results, not a clean test failure.
func TestFetchContainerImages_SinglePageStopsAfterOnePage(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/application_templates/" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
			return
		}

		requestCount++
		w.Header().Set("Content-Type", "application/json")
		resp := ApplicationTemplatesListResponse{
			Results: []ApplicationTemplateResult{
				{ID: "apptemp_only", Name: "tfacc-singlepage", CreatorID: "user_1", CreatedAt: "2024-01-01T00:00:00Z"},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	d := &ContainerImagesDataSource{client: NewClientWithToken(server.URL, "fake-token-images-singlepage")}

	images, err := d.fetchContainerImages(context.Background(), url.Values{})
	if err != nil {
		t.Fatalf("fetchContainerImages returned error: %v", err)
	}

	if requestCount != 1 {
		t.Errorf("expected exactly 1 request for a single-page response, got %d", requestCount)
	}
	if len(images) != 1 || images[0].ID.ValueString() != "apptemp_only" {
		t.Errorf("got %+v, want exactly one image with ID apptemp_only", images)
	}
}
