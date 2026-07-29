package acctest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// This file closes the same framework-level gap for Project that
// resource_cloud_c3_lifecycle_acc_test.go and
// resource_compute_config_lifecycle_acc_test.go close for their resources
// (see those files' header comments): plan-emptiness, import round-tripping,
// and inconsistent-result-after-apply are terraform FRAMEWORK properties.
// Every OTHER project acctest that exercises real behavior is skip-gated on
// credentials or on ANYSCALE_TEST_USER_EMAIL_*, which CI never sets - this
// mock, gated only by the ordinary SkipIfNotAcceptanceTest, is the one that
// actually runs in CI.
//
// v0.25.0: the collaborator block was removed from anyscale_project (project
// access is managed through anyscale_cloud_access instead), so the
// collaborator lifecycle test (old AC2) and the write-permission symmetry test
// (old AC5) that used to live here are gone, along with the mock's
// collaborator endpoints and its {owner,write,readonly} enum guard - the
// resource no longer calls any of them. What remains is the part that was
// never collaborator-specific: create, refresh to an empty plan, and import
// with full ImportStateVerify.

type mockProjectServer struct {
	mu       sync.Mutex
	nextSeq  int
	projects map[string]map[string]any
}

func newMockProjectServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := &mockProjectServer{
		projects: make(map[string]map[string]any),
	}
	server := httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(server.Close)
	return server
}

func (s *mockProjectServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && path == "/api/v2/projects":
		s.handleCreate(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/v2/projects/"):
		s.handleGet(w, path)
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/api/v2/projects/"):
		s.handleDelete(w, path)
	default:
		// Deliberately a hard 404 rather than a permissive echo: the removed
		// collaborator endpoints used to be handled here, so an unexpected
		// /collaborators/... call from the resource would now fail loudly
		// instead of being quietly absorbed.
		http.Error(w, `{"detail":"not found"}`, http.StatusNotFound)
	}
}

// projectIDFromPath extracts {id} from /api/v2/projects/{id}[/...].
func projectIDFromPath(path string) string {
	const prefix = "/api/v2/projects/"
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.SplitN(rest, "/", 2)
	return parts[0]
}

func (s *mockProjectServer) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string  `json:"name"`
		ParentCloudID string  `json:"parent_cloud_id"`
		Description   *string `json:"description"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)

	s.nextSeq++
	id := fmt.Sprintf("prj_mock_%d", s.nextSeq)
	desc := ""
	if req.Description != nil {
		desc = *req.Description
	}
	result := map[string]any{
		"id":                 id,
		"name":               req.Name,
		"description":        desc,
		"parent_cloud_id":    req.ParentCloudID,
		"creator_id":         "user_mock_creator",
		"created_at":         "2026-01-01T00:00:00Z",
		"last_used_cloud_id": req.ParentCloudID,
		"is_default":         false,
		"directory_name":     req.Name + "-dir",
	}
	s.projects[id] = result

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
}

func (s *mockProjectServer) handleGet(w http.ResponseWriter, path string) {
	id := projectIDFromPath(path)
	result, ok := s.projects[id]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Project not found"}`))
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
}

func (s *mockProjectServer) handleDelete(w http.ResponseWriter, path string) {
	id := projectIDFromPath(path)
	delete(s.projects, id)
	w.WriteHeader(http.StatusNoContent)
}

// TestAccProjectResource_Lifecycle_MockServer drives create -> refresh ->
// import against a mock backend, with no real credentials required, and
// asserts the two framework properties a project apply must hold: the
// post-apply-post-refresh plan is empty (no perpetual diff, no inconsistent
// result after apply), and a fresh import reproduces the applied state
// attribute-for-attribute (ImportStateVerify with nothing ignored).
//
// The full-verify import step is what would catch a regression where Read or
// ImportState populates an attribute the other one leaves null.
func TestAccProjectResource_Lifecycle_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server := newMockProjectServer(t)
	const name = "project-lifecycle-mock"

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_project" "test" {
  name        = %[1]q
  cloud_id    = "cld_mock"
  description = "mock lifecycle test project"
}
`, name)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "name", name),
					resource.TestCheckResourceAttr("anyscale_project.test", "cloud_id", "cld_mock"),
					resource.TestCheckResourceAttr("anyscale_project.test", "description", "mock lifecycle test project"),
					resource.TestCheckResourceAttrSet("anyscale_project.test", "id"),
					resource.TestCheckResourceAttrSet("anyscale_project.test", "created_at"),
					resource.TestCheckResourceAttrSet("anyscale_project.test", "directory_name"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				ResourceName:      "anyscale_project.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}
