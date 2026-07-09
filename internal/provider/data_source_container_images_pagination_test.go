package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFetchContainerImages_MultiPage is the GATE-F2 multi-page proof for the
// plural container-images data source's search call site. Sibling proof for
// the sweeper's identical search endpoint lives in
// internal/acctest/sweeper_container_image_pagination_test.go - same shape,
// same silent-truncation failure mode, different call site. A single-page
// happy-path test would pass even if the loop never followed
// next_paging_token at all, so this drives the real fetchContainerImages
// against a 2-page mock and asserts both pages are collected AND that the
// second request carries the exact token the first response returned.
//
// Characterizes TODAY's ext/v0 body-shaped pagination contract. Once forge
// migrates this call to api/v2 query params, only the request-shape
// assertions below move from the JSON body to the URL query string - the
// two-page collection assertion is unchanged.
func TestFetchContainerImages_MultiPage(t *testing.T) {
	var searchRequests []map[string]interface{}

	const token = "images-page-2-token"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/ext/v0/cluster_environments/search":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			var payload map[string]interface{}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("unmarshal request body: %v", err)
			}
			searchRequests = append(searchRequests, payload)

			w.Header().Set("Content-Type", "application/json")

			if len(searchRequests) == 1 {
				if paging, ok := payload["paging"].(map[string]interface{}); ok {
					if _, hasToken := paging["paging_token"]; hasToken {
						t.Errorf("first request should not carry a paging_token, got %v", paging)
					}
				}
				resp := ClusterEnvironmentsListResponse{
					Results: []ClusterEnvironmentResult{
						{ID: "apptemp_img_a", Name: "tfacc-images-a", CreatorID: "user_1", CreatedAt: "2024-01-01T00:00:00Z"},
						{ID: "apptemp_img_b", Name: "tfacc-images-b", CreatorID: "user_1", CreatedAt: "2024-01-01T00:00:00Z"},
					},
				}
				nextToken := token
				resp.Metadata.NextPagingToken = &nextToken
				_ = json.NewEncoder(w).Encode(resp)
				return
			}

			paging, ok := payload["paging"].(map[string]interface{})
			if !ok {
				t.Fatalf("second request missing paging object: %v", payload)
			}
			gotToken, _ := paging["paging_token"].(string)
			if gotToken != token {
				t.Errorf("second request paging_token = %q, want %q -- loop is not following the token", gotToken, token)
			}
			resp := ClusterEnvironmentsListResponse{
				Results: []ClusterEnvironmentResult{
					{ID: "apptemp_img_c", Name: "tfacc-images-c", CreatorID: "user_1", CreatedAt: "2024-01-01T00:00:00Z"},
				},
			}
			// NextPagingToken stays nil -> loop must terminate after this page.
			_ = json.NewEncoder(w).Encode(resp)

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/ext/v0/cluster_environment_builds/"):
			// None of the fixtures above have builds. Returning an empty list
			// keeps this test focused on the search-pagination loop, not
			// build-detail mapping (that's covered elsewhere).
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ClusterEnvironmentBuildsListResponse{})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	d := &ContainerImagesDataSource{client: NewClientWithToken(server.URL, "fake-token-images-multipage")}
	query := ClusterEnvironmentsSearchQuery{
		Paging:           PageQuery{Count: 100},
		IncludeArchived:  false,
		IncludeAnonymous: false,
	}

	images, err := d.fetchContainerImages(context.Background(), query)
	if err != nil {
		t.Fatalf("fetchContainerImages returned error: %v", err)
	}

	if len(searchRequests) != 2 {
		t.Fatalf("expected exactly 2 search requests (one per page), got %d", len(searchRequests))
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
	searchRequestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/ext/v0/cluster_environments/search":
			searchRequestCount++
			w.Header().Set("Content-Type", "application/json")
			resp := ClusterEnvironmentsListResponse{
				Results: []ClusterEnvironmentResult{
					{ID: "apptemp_only", Name: "tfacc-singlepage", CreatorID: "user_1", CreatedAt: "2024-01-01T00:00:00Z"},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/ext/v0/cluster_environment_builds/"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ClusterEnvironmentBuildsListResponse{})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	d := &ContainerImagesDataSource{client: NewClientWithToken(server.URL, "fake-token-images-singlepage")}
	query := ClusterEnvironmentsSearchQuery{Paging: PageQuery{Count: 100}}

	images, err := d.fetchContainerImages(context.Background(), query)
	if err != nil {
		t.Fatalf("fetchContainerImages returned error: %v", err)
	}

	if searchRequestCount != 1 {
		t.Errorf("expected exactly 1 search request for a single-page response, got %d", searchRequestCount)
	}
	if len(images) != 1 || images[0].ID.ValueString() != "apptemp_only" {
		t.Errorf("got %+v, want exactly one image with ID apptemp_only", images)
	}
}
