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
// (see those files' header comments): plan-emptiness and
// inconsistent-result-after-apply are terraform FRAMEWORK properties. It is
// also the design contract's primary correctness gate for F1 (see
// .crystl/docs/PROJECT-API-SYNC-DESIGN.md, AC1/AC2): the real acctests that
// exercise collaborator permission_level (TestAccProjectResource_WithCollaborators
// etc.) are skip-gated on ANYSCALE_TEST_USER_EMAIL_1/2, which CI never sets -
// this mock, gated only by the ordinary SkipIfNotAcceptanceTest, is the one
// that actually runs in CI and would fail if permission_level regressed back
// to "writer" or accepted anything outside {owner, write, readonly}.

type mockProjectCollaborator struct {
	IdentityID      string
	UserID          string
	Email           string
	PermissionLevel string
}

// mockProjectValidPermissionLevels enforces the real backend's enum
// (backend/server/database/api/common.py's PermissionLevel: owner="owner",
// writer="write", readonly="readonly" -- see PROJECT-API-SYNC-DESIGN.md F1).
// Rejecting anything else, "writer" included, is what makes this mock an
// actual regression guard rather than an echo server.
var mockProjectValidPermissionLevels = map[string]bool{"owner": true, "write": true, "readonly": true}

type mockProjectServer struct {
	mu            sync.Mutex
	nextSeq       int
	projects      map[string]map[string]any
	collaborators map[string][]*mockProjectCollaborator
}

func newMockProjectServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := &mockProjectServer{
		projects:      make(map[string]map[string]any),
		collaborators: make(map[string][]*mockProjectCollaborator),
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
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/collaborators/users"):
		s.handleListCollaborators(w, path)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/collaborators/users/batch_create"):
		s.handleBatchCreateCollaborators(w, r, path)
	case r.Method == http.MethodPut && strings.Contains(path, "/collaborators/"):
		s.handleUpdateCollaborator(w, r, path)
	case r.Method == http.MethodDelete && strings.Contains(path, "/collaborators/"):
		s.handleRemoveCollaborator(w, path)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/v2/projects/"):
		s.handleGet(w, path)
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/api/v2/projects/"):
		s.handleDelete(w, path)
	default:
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
	s.collaborators[id] = nil

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
	delete(s.collaborators, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *mockProjectServer) handleListCollaborators(w http.ResponseWriter, path string) {
	projectID := projectIDFromPath(path)
	collabs := s.collaborators[projectID]
	results := make([]map[string]any, 0, len(collabs))
	for _, c := range collabs {
		results = append(results, map[string]any{
			"id":               c.IdentityID,
			"permission_level": c.PermissionLevel,
			"value": map[string]any{
				"id":    c.UserID,
				"name":  c.Email,
				"email": c.Email,
			},
		})
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"results":  results,
		"metadata": map[string]any{"total": len(results), "next_paging_token": nil},
	})
}

func mockProjectRejectInvalidPermissionLevel(w http.ResponseWriter, level string) bool {
	if mockProjectValidPermissionLevels[level] {
		return false
	}
	w.WriteHeader(http.StatusUnprocessableEntity)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"detail": fmt.Sprintf("value is not a valid enumeration member; permitted: 'owner', 'write', 'readonly' (got %q)", level),
	})
	return true
}

func (s *mockProjectServer) handleBatchCreateCollaborators(w http.ResponseWriter, r *http.Request, path string) {
	// path: /api/v2/projects/{id}/collaborators/users/batch_create
	projectID := strings.TrimSuffix(path, "/collaborators/users/batch_create")
	projectID = projectIDFromPath(projectID + "/")

	var entries []struct {
		Value struct {
			Email string `json:"email"`
		} `json:"value"`
		PermissionLevel string `json:"permission_level"`
	}
	body, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(body, &entries); err != nil {
		http.Error(w, `{"detail":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	// Mirrors the real backend: the whole batch is validated against the
	// enum before anything is applied (Pydantic validates the full request
	// body up front), so one bad entry 422s the entire call rather than
	// partially landing.
	for _, e := range entries {
		if mockProjectRejectInvalidPermissionLevel(w, e.PermissionLevel) {
			return
		}
	}

	for _, e := range entries {
		s.nextSeq++
		s.collaborators[projectID] = append(s.collaborators[projectID], &mockProjectCollaborator{
			IdentityID:      fmt.Sprintf("identity_mock_%d", s.nextSeq),
			UserID:          fmt.Sprintf("user_mock_%d", s.nextSeq),
			Email:           e.Value.Email,
			PermissionLevel: e.PermissionLevel,
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *mockProjectServer) handleUpdateCollaborator(w http.ResponseWriter, r *http.Request, path string) {
	// path: /api/v2/projects/{id}/collaborators/{identity_id}
	identityID := path[strings.LastIndex(path, "/")+1:]
	projectID := projectIDFromPath(path)

	var req struct {
		PermissionLevel string `json:"permission_level"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)

	if mockProjectRejectInvalidPermissionLevel(w, req.PermissionLevel) {
		return
	}

	for _, c := range s.collaborators[projectID] {
		if c.IdentityID == identityID {
			c.PermissionLevel = req.PermissionLevel
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	w.WriteHeader(http.StatusNotFound)
}

func (s *mockProjectServer) handleRemoveCollaborator(w http.ResponseWriter, path string) {
	identityID := path[strings.LastIndex(path, "/")+1:]
	projectID := projectIDFromPath(path)

	collabs := s.collaborators[projectID]
	for i, c := range collabs {
		if c.IdentityID == identityID {
			s.collaborators[projectID] = append(collabs[:i], collabs[i+1:]...)
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	w.WriteHeader(http.StatusNotFound)
}

// TestAccProjectResource_Lifecycle_MockServer is the design contract's AC2:
// create with 2 collaborators, update one's permission_level, remove the
// other, re-read - all against a mock that enforces the real
// {owner,write,readonly} enum, so a regression back to "writer" (or anything
// else invalid) fails this test, which runs in ordinary CI with no real
// credentials or test-user emails required.
func TestAccProjectResource_Lifecycle_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server := newMockProjectServer(t)
	const name = "project-lifecycle-mock"

	configTwoCollaborators := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_project" "test" {
  name        = %[1]q
  cloud_id    = "cld_mock"
  description = "mock lifecycle test project"

  collaborator {
    email            = "owner@example.com"
    permission_level = "owner"
  }

  collaborator {
    email            = "writer@example.com"
    permission_level = "write"
  }
}
`, name)

	configOneCollaboratorUpdated := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_project" "test" {
  name        = %[1]q
  cloud_id    = "cld_mock"
  description = "mock lifecycle test project"

  collaborator {
    email            = "owner@example.com"
    permission_level = "readonly"
  }
}
`, name)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: configTwoCollaborators,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "name", name),
					resource.TestCheckResourceAttr("anyscale_project.test", "collaborator.#", "2"),
					resource.TestCheckResourceAttr("anyscale_project.test", "collaborator.0.permission_level", "owner"),
					resource.TestCheckResourceAttr("anyscale_project.test", "collaborator.1.permission_level", "write"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			{
				// Update one collaborator's permission and remove the other:
				// must be a clean update with no "inconsistent result after
				// apply" and no permanent diff. If permission_level's wire
				// value were still wrong, the mock would 422 on create/update
				// and this step would never get this far.
				Config: configOneCollaboratorUpdated,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "collaborator.#", "1"),
					resource.TestCheckResourceAttr("anyscale_project.test", "collaborator.0.permission_level", "readonly"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}

// TestAccProjectResource_WriteCollaboratorSymmetry is the design contract's
// AC5: a full apply -> empty plan -> import -> ImportStateVerify round trip
// specifically on a "write"-permission collaborator (not owner/readonly,
// which happened to validate under the old buggy schema too and would not
// have caught F1). This is the proof that the passthrough of
// collab.PermissionLevel really does round-trip clean end to end with the
// canonical wire value, not just that create succeeds.
func TestAccProjectResource_WriteCollaboratorSymmetry(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	server := newMockProjectServer(t)
	const name = "project-write-symmetry-mock"

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_project" "test" {
  name        = %[1]q
  cloud_id    = "cld_mock"
  description = "write-permission symmetry test project"

  collaborator {
    email            = "writer@example.com"
    permission_level = "write"
  }
}
`, name)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_project.test", "collaborator.#", "1"),
					resource.TestCheckResourceAttr("anyscale_project.test", "collaborator.0.permission_level", "write"),
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
				ImportStateVerifyIgnore: []string{
					"cloud_name", // input-only alias for cloud_id; API stores only parent_cloud_id
				},
			},
		},
	})
}
