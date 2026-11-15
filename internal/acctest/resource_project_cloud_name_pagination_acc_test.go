package acctest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// newDataSourceCloudNamePaginationMockServer is a minimal, standalone fake of
// GET /api/v2/clouds (genuinely paginated, two pages) plus just enough of
// /api/v2/projects to drive a real data.anyscale_project lookup by name
// through cloud_name resolution end to end.
//
// The two-page shape is the point of this file, not an implementation
// detail: ResolveCloudNameToID (cloud_helpers.go) is the shared helper
// behind cloud_name on all five data sources (compute_config, project,
// projects, service, services) - the only remaining surface, now that
// cloud_name has been removed from the resources. A page-1-only resolver
// issues a single unpaginated GET and matches within page one only: a
// valid name past page 1 resolves to "not found", and a name duplicated
// across pages silently resolves to the older cloud. A mock that returns
// every cloud in one response cannot fail against a page-1-only resolver
// - it would find the name fine and this test would go green. The target
// cloud is placed on page 2 deliberately so a page-1-only resolver fails
// for the right reason.
//
// These tests live on data.anyscale_project because cloud_name exists
// only on data sources. ResolveCloudNameToID is reached from every data
// source regardless of which call site exercises it, so this coverage
// must not be dropped.
func newDataSourceCloudNamePaginationMockServer(t *testing.T, page1Clouds, page2Clouds []map[string]any, projects []map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/clouds", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		token := r.URL.Query().Get("paging_token")
		var results []map[string]any
		var nextToken any
		switch token {
		case "":
			results = page1Clouds
			nextToken = "page2"
		case "page2":
			results = page2Clouds
			nextToken = nil
		default:
			t.Errorf("unexpected paging_token %q on GET /api/v2/clouds", token)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": results,
			"metadata": map[string]any{
				"total":             len(page1Clouds) + len(page2Clouds),
				"next_paging_token": nextToken,
			},
		})
	})
	mux.HandleFunc("/api/v2/projects", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on /api/v2/projects", r.Method)
			return
		}
		// Genuinely filter by parent_cloud_id, matching findProjectByName's
		// real query param - a mock that ignores this and always returns the
		// full seeded list can't tell "resolved the right cloud" apart from
		// "resolved any cloud at all", since the returned project's own
		// parent_cloud_id would look right regardless of which cloud_id was
		// actually searched for. Load-bearing for
		// TestAccProjectDataSource_CloudNameDuplicateAcrossPagesPicksNewest.
		filterCloudID := r.URL.Query().Get("parent_cloud_id")
		matched := projects
		if filterCloudID != "" {
			matched = nil
			for _, p := range projects {
				if pc, _ := p["parent_cloud_id"].(string); pc == filterCloudID {
					matched = append(matched, p)
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results":  matched,
			"metadata": map[string]any{"total": len(matched), "next_paging_token": nil},
		})
	})
	for _, p := range projects {
		p := p
		id, _ := p["id"].(string)
		mux.HandleFunc("/api/v2/projects/"+id, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("unexpected method %s on /api/v2/projects/%s", r.Method, id)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": p})
		})
		mux.HandleFunc("/api/v2/projects/"+id+"/collaborators/users", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
		})
	}

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func projectResult(id, name, createdAt, parentCloudID string) map[string]any {
	return map[string]any{
		"id": id, "name": name, "description": "", "parent_cloud_id": parentCloudID,
		"creator_id": "user_mock_creator", "created_at": createdAt,
		"last_used_cloud_id": parentCloudID, "is_default": false,
		"directory_name": name + "-dir",
	}
}

// TestAccProjectDataSource_CloudNameResolvesAcrossPages is the
// mutation-proof regression guard for ResolveCloudNameToID's pagination
// (cloud_helpers.go) - a by-name lookup must scan every page of
// GET /api/v2/clouds, not just the first. See this file's header comment.
//
// It fails against a page-1-only cloud-name resolver: looking up the
// page-2 cloud returns "Failed to resolve cloud name... no cloud found
// with name..." instead of an ID.
func TestAccProjectDataSource_CloudNameResolvesAcrossPages(t *testing.T) {
	const targetCloudID = "cld_page_two_target"
	const targetCloudName = "cloud-on-page-two"
	const projectID = "prj_ds_page_two"

	page1 := []map[string]any{
		{"id": "cld_page_one_a", "name": "unrelated-cloud-a", "provider": "aws", "created_at": "2026-01-01T00:00:00Z"},
		{"id": "cld_page_one_b", "name": "unrelated-cloud-b", "provider": "aws", "created_at": "2026-01-01T00:00:00Z"},
	}
	page2 := []map[string]any{
		{"id": targetCloudID, "name": targetCloudName, "provider": "aws", "created_at": "2026-01-02T00:00:00Z"},
	}
	projects := []map[string]any{
		projectResult(projectID, "ds-cloud-name-pagination-test", "2026-01-01T00:00:00Z", targetCloudID),
	}

	server := newDataSourceCloudNamePaginationMockServer(t, page1, page2, projects)
	config := testAccProviderBlock(server.URL) + `
data "anyscale_project" "test" {
  name       = "ds-cloud-name-pagination-test"
  cloud_name = "cloud-on-page-two"
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_project.test", "id", projectID),
					resource.TestCheckResourceAttr("data.anyscale_project.test", "cloud_id", targetCloudID),
				),
			},
		},
	})
}

// TestAccProjectDataSource_CloudNameDuplicateAcrossPagesPicksNewest pins
// user-visible behavior: a duplicate cloud name can resolve to a different
// cloud than it would if the resolver stopped at page 1, because the
// most-recent tiebreak also sees page-2 candidates. Two clouds share the
// same name, one on page 1 (older), one on page 2 (newer) - asserts the
// resolver picks the newer, page-2 one.
//
// This property matters most: a not-found error is loud, but picking the
// wrong cloud silently is not. ResolveCloudNameToID is the same shared
// helper reached from every data source regardless of which call site
// exercises it, so dropping this test would remove the only guard on
// that behavior.
//
// It fails against a page-1-only cloud-name resolver: it would resolve the
// older page-1 cloud instead of the newer page-2 one, because the helper
// finds a page-1 match and returns immediately, never learning that page
// 2 holds a newer duplicate.
func TestAccProjectDataSource_CloudNameDuplicateAcrossPagesPicksNewest(t *testing.T) {
	const sharedName = "ds-dup-name-both-pages"
	const olderCloudID = "cld_ds_dup_page_one_older"
	const newerCloudID = "cld_ds_dup_page_two_newer"
	const projectID = "prj_ds_dup_mock"

	page1 := []map[string]any{
		{"id": olderCloudID, "name": sharedName, "provider": "aws", "created_at": "2026-01-01T00:00:00Z"},
	}
	page2 := []map[string]any{
		{"id": newerCloudID, "name": sharedName, "provider": "aws", "created_at": "2026-01-02T00:00:00Z"},
	}
	projects := []map[string]any{
		projectResult(projectID, "ds-dup-name-config", "2026-01-01T00:00:00Z", newerCloudID),
	}

	server := newDataSourceCloudNamePaginationMockServer(t, page1, page2, projects)
	config := testAccProviderBlock(server.URL) + `
data "anyscale_project" "test" {
  name       = "ds-dup-name-config"
  cloud_name = "ds-dup-name-both-pages"
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_project.test", "id", projectID),
					resource.TestCheckResourceAttr("data.anyscale_project.test", "cloud_id", newerCloudID),
				),
			},
		},
	})
}
