package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// containerImagesPaginationMock serves GET /api/v2/application_templates/ as
// three pages chained by next_paging_token, and records every request's query
// so the test can assert what the data source actually sent.
type containerImagesPaginationMock struct {
	mu       sync.Mutex
	requests []map[string]string
}

// containerImagesMockPages is the server-order item names on each page. Names
// are deliberately not in sorted order, so a client-side sort would be caught.
var containerImagesMockPages = [][]string{
	{"img-c", "img-a"},
	{"img-e", "img-b"},
	{"img-d"},
}

func (m *containerImagesPaginationMock) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s on %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		m.mu.Lock()
		m.requests = append(m.requests, map[string]string{
			"include_archived": q.Get("include_archived"),
			"paging_token":     q.Get("paging_token"),
		})
		m.mu.Unlock()

		page := 0
		if tok := q.Get("paging_token"); tok != "" {
			n, err := strconv.Atoi(tok)
			if err != nil || n < 1 || n >= len(containerImagesMockPages) {
				t.Errorf("unexpected paging_token %q", tok)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			page = n
		}
		archived := q.Get("include_archived") == "true"

		results := make([]map[string]any, 0, len(containerImagesMockPages[page]))
		for _, name := range containerImagesMockPages[page] {
			item := map[string]any{
				"id":           "apptemp_" + name,
				"name":         name,
				"created_at":   "2024-01-01T00:00:00Z",
				"is_default":   false,
				"anonymous":    false,
				"latest_build": nil,
			}
			if archived {
				item["archived_at"] = "2024-02-01T00:00:00Z"
			}
			results = append(results, item)
		}
		var next *string
		if page+1 < len(containerImagesMockPages) {
			s := strconv.Itoa(page + 1)
			next = &s
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results":  results,
			"metadata": map[string]any{"next_paging_token": next},
		})
	}
}

// TestAccContainerImagesDataSource_Pagination_MockServer proves the data source
// walks every page via next_paging_token, preserves server order, and forwards
// include_archived as sent. It replaces the unbounded real-org listing _Basic
// used to do, whose runtime grew with the org's (never-deleted) archived set.
func TestAccContainerImagesDataSource_Pagination_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	mock := &containerImagesPaginationMock{}
	mux := http.NewServeMux()
	// Both forms: a subtree-only pattern makes ServeMux redirect the bare path.
	mux.HandleFunc("/api/v2/application_templates/", mock.handler(t))
	mux.HandleFunc("/api/v2/application_templates", mock.handler(t))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	config := testAccProviderBlock(server.URL) + `
data "anyscale_container_images" "all" {
  include_archived = true
}

data "anyscale_container_images" "active" {
  include_archived = false
}
`
	wantOrder := []string{"img-c", "img-a", "img-e", "img-b", "img-d"}
	checks := []resource.TestCheckFunc{
		resource.TestCheckResourceAttr("data.anyscale_container_images.all", "container_images.#", strconv.Itoa(len(wantOrder))),
		resource.TestCheckResourceAttr("data.anyscale_container_images.active", "container_images.#", strconv.Itoa(len(wantOrder))),
	}
	for i, name := range wantOrder {
		checks = append(checks,
			resource.TestCheckResourceAttr("data.anyscale_container_images.all", fmt.Sprintf("container_images.%d.name", i), name),
			resource.TestCheckResourceAttr("data.anyscale_container_images.all", fmt.Sprintf("container_images.%d.is_archived", i), "true"),
			resource.TestCheckResourceAttr("data.anyscale_container_images.active", fmt.Sprintf("container_images.%d.name", i), name),
			resource.TestCheckResourceAttr("data.anyscale_container_images.active", fmt.Sprintf("container_images.%d.is_archived", i), "false"),
		)
	}
	checks = append(checks, func(*terraform.State) error {
		return mock.verifyRequests()
	})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.ComposeAggregateTestCheckFunc(checks...),
			},
		},
	})
}

// verifyRequests asserts that both include_archived values were sent, each
// across all three pages.
func (m *containerImagesPaginationMock) verifyRequests() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	pagesByArchived := map[string]map[string]bool{}
	for _, req := range m.requests {
		ia := req["include_archived"]
		if ia != "true" && ia != "false" {
			return fmt.Errorf("request %v: include_archived = %q, want true or false", req, ia)
		}
		if pagesByArchived[ia] == nil {
			pagesByArchived[ia] = map[string]bool{}
		}
		pagesByArchived[ia][req["paging_token"]] = true
	}
	for _, ia := range []string{"true", "false"} {
		if got := len(pagesByArchived[ia]); got != len(containerImagesMockPages) {
			return fmt.Errorf("include_archived=%s: fetched %d distinct pages, want %d", ia, got, len(containerImagesMockPages))
		}
	}
	return nil
}
