package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// cloudNamePaginationMockServer is a minimal, standalone fake of GET
// /api/v2/clouds (genuinely paginated, two pages) plus just enough of
// /api/v2/projects (create/read/delete) to drive a real anyscale_project
// apply through cloud_name resolution end to end.
//
// The two-page shape is the point of this file, not an implementation
// detail: ResolveCloudNameToID (cloud_helpers.go) is the shared helper
// behind cloud_name on compute_config, project, cloud_resource, and
// data_source_compute_config (design doc: .crystl/quest/design/
// cloud-selector-design.md, B1). Before its fix it issues a single
// unpaginated GET and matches within page one only. A mock that returns
// every cloud in one response cannot fail against that bug - the page-1-only
// helper would find the name fine and this test would go green against
// unfixed code, exactly the placebo shape architect flagged when assigning
// this. The target cloud here is placed on page 2 deliberately so the
// unfixed helper returns "no cloud found" and this test fails for the right
// reason - see the doc comment on the test function itself for the
// before/after run this was proven against.
type cloudNamePaginationMockServer struct {
	mu       sync.Mutex
	nextSeq  int
	projects map[string]map[string]any
}

func newCloudNamePaginationMockServer(t *testing.T, page1Clouds []map[string]any, page2Clouds []map[string]any) *httptest.Server {
	t.Helper()
	s := &cloudNamePaginationMockServer{projects: make(map[string]map[string]any)}

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
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s on /api/v2/projects", r.Method)
			return
		}
		s.handleCreate(w, r)
	})
	mux.HandleFunc("/api/v2/projects/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/v2/projects/")
		switch r.Method {
		case http.MethodGet:
			s.handleGet(w, id)
		case http.MethodDelete:
			s.mu.Lock()
			delete(s.projects, id)
			s.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s on /api/v2/projects/%s", r.Method, id)
		}
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func (s *cloudNamePaginationMockServer) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string  `json:"name"`
		ParentCloudID string  `json:"parent_cloud_id"`
		Description   *string `json:"description"`
	}
	body := map[string]any{}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if v, ok := body["name"].(string); ok {
		req.Name = v
	}
	if v, ok := body["parent_cloud_id"].(string); ok {
		req.ParentCloudID = v
	}

	s.mu.Lock()
	s.nextSeq++
	id := fmt.Sprintf("prj_cloudname_page_%d", s.nextSeq)
	result := map[string]any{
		"id":                 id,
		"name":               req.Name,
		"description":        "",
		"parent_cloud_id":    req.ParentCloudID,
		"creator_id":         "user_mock_creator",
		"created_at":         "2026-01-01T00:00:00Z",
		"last_used_cloud_id": req.ParentCloudID,
		"is_default":         false,
		"directory_name":     req.Name + "-dir",
	}
	s.projects[id] = result
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
}

func (s *cloudNamePaginationMockServer) handleGet(w http.ResponseWriter, id string) {
	s.mu.Lock()
	result, ok := s.projects[id]
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Project not found"}`))
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
}

// TestAccProjectResource_CloudNameResolvesAcrossPages is the mutation-proof
// regression guard for design doc B1 (ResolveCloudNameToID is page-1-only,
// cloud_helpers.go:227-259, shared by cloud_name on compute_config, project,
// cloud_resource, and data_source_compute_config).
//
// The mock places the target cloud only on page 2 of GET /api/v2/clouds, so
// this genuinely distinguishes a paginated fix from the pre-fix single-page
// GET. Confirmed failing-first (2026-07-25) with TF_ACC=1 go test against
// pre-B1 main, before forge's fix landed, with the exact expected error:
//
//	Error: Cloud Name Resolution Failed
//	Failed to resolve cloud name 'cloud-on-page-two' to ID: no cloud found
//	with name 'cloud-on-page-two'
//
// Once ResolveCloudNameToID routes through PaginatedRequest (matching
// data_source_cloud.go's findCloudByName), this passes: the create request's
// parent_cloud_id resolves to the page-2 cloud's real ID, observable via the
// last_used_cloud_id computed attribute the mock echoes back (project never
// writes cloud_id back to state when cloud_name is used, so last_used_cloud_id
// is the correct observable proxy here, not cloud_id).
func TestAccProjectResource_CloudNameResolvesAcrossPages(t *testing.T) {
	const targetCloudID = "cld_page_two_target"
	const targetCloudName = "cloud-on-page-two"

	page1 := []map[string]any{
		{"id": "cld_page_one_a", "name": "unrelated-cloud-a", "provider": "aws", "created_at": "2026-01-01T00:00:00Z"},
		{"id": "cld_page_one_b", "name": "unrelated-cloud-b", "provider": "aws", "created_at": "2026-01-01T00:00:00Z"},
	}
	page2 := []map[string]any{
		{"id": targetCloudID, "name": targetCloudName, "provider": "aws", "created_at": "2026-01-02T00:00:00Z"},
	}

	server := newCloudNamePaginationMockServer(t, page1, page2)
	config := testAccProviderBlock(server.URL) + testAccProjectResourceWithCloudNameConfig(targetCloudName, "cloud-name-pagination-test")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "cloud_name", targetCloudName),
					resource.TestCheckResourceAttr("anyscale_project.test", "last_used_cloud_id", targetCloudID),
				),
			},
		},
	})
}
