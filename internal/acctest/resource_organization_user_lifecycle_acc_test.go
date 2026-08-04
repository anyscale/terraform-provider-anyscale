package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// mockCollaboratorLifecycleServer is a scripted stand-in for the real
// organization_collaborators API, tracking one collaborator so a real
// resource.Test can exercise Import -> Read -> Delete end to end, including the
// singular per-user GET the read path still makes (see
// hydrateCollaboratorRoles), without touching real infra.
//
// There is deliberately NO update handler: anyscale_organization_user manages
// membership only and has no writable attribute, so the provider can no longer
// issue PUT /api/v2/organization_collaborators/{identity_id} at all. Leaving
// the route unhandled is the point - an unexpected PUT now falls through to the
// 404 default and fails the test loudly, instead of being quietly absorbed.
// Role writes moved to anyscale_organization_user_role and are covered there.
//
// listBaseRole vs singularBaseRole is kept because it models a real,
// live-proven backend split: the LIST endpoint's formatter derives base_role
// fresh from Postgres every call, while the singular per-user GET (and search)
// derive it from SpiceDB-managed groups when the read flag is on. The two
// genuinely can disagree, and keeping both fields keeps the mocked responses
// shaped like the real ones rather than an idealized agreement the backend does
// not actually guarantee.
type mockCollaboratorLifecycleServer struct {
	mu               sync.Mutex
	identityID       string
	userID           string
	email            string
	name             string
	permissionLevel  string
	listBaseRole     string // Postgres-derived (LIST)
	singularBaseRole string // SpiceDB-derived (singular GET)
	additionalRoles  []string
	createdAt        string
	deleted          bool
}

func newMockCollaboratorLifecycleServer(t *testing.T) (*httptest.Server, *mockCollaboratorLifecycleServer) {
	t.Helper()
	s := &mockCollaboratorLifecycleServer{
		identityID:       "identity-lifecycle-mock",
		userID:           "usr_lifecycle_mock",
		email:            "lifecycle@example.com",
		name:             "Lifecycle Test",
		permissionLevel:  "collaborator",
		listBaseRole:     "collaborator",
		singularBaseRole: "collaborator",
		additionalRoles:  []string{"image_reader"},
		createdAt:        "2026-01-01T00:00:00Z",
	}
	server := httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(server.Close)
	return server, s
}

func (s *mockCollaboratorLifecycleServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/api/v2/organization_collaborators":
		s.handleList(w)
	case r.Method == http.MethodGet && path == "/api/v2/organization_collaborators/"+s.userID:
		s.handleSingular(w)
	case r.Method == http.MethodDelete && path == "/api/v2/organization_collaborators/"+s.identityID:
		s.handleDelete(w)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error":{"detail":"not found"}}`)
	}
}

// handleList mirrors the real backend's list formatter: additional_roles is
// hardcoded to empty here regardless of the real value, exactly the structural
// limitation hydrateCollaboratorRoles exists to work around. Returning the real
// additionalRoles here instead would make this mock too generous and let a
// regression (Read/Import forgetting to call hydrateCollaboratorRoles at all)
// pass by accident.
func (s *mockCollaboratorLifecycleServer) handleList(w http.ResponseWriter) {
	if s.deleted {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results":[],"metadata":{"total":0,"next_paging_token":null}}`)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"results": []map[string]any{
			{
				"id":               s.identityID,
				"email":            s.email,
				"name":             s.name,
				"permission_level": s.permissionLevel,
				"base_role":        s.listBaseRole,
				"additional_roles": []string{}, // deliberately NOT s.additionalRoles - see doc comment
				"created_at":       s.createdAt,
				"user_id":          s.userID,
			},
		},
		"metadata": map[string]any{"total": 1, "next_paging_token": nil},
	})
}

// handleSingular is the only handler that returns the real additionalRoles,
// matching the real backend's read-flag-aware formatter (the path
// hydrateCollaboratorRoles calls). Its base_role is deliberately
// singularBaseRole, not listBaseRole - see the type doc comment on why that
// split is real backend behavior, not a mock artifact.
func (s *mockCollaboratorLifecycleServer) handleSingular(w http.ResponseWriter) {
	if s.deleted {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error":{"detail":"not found"}}`)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{
			"id":               s.identityID,
			"email":            s.email,
			"name":             s.name,
			"permission_level": s.permissionLevel,
			"base_role":        s.singularBaseRole,
			"additional_roles": s.additionalRoles,
			"created_at":       s.createdAt,
			"user_id":          s.userID,
		},
	})
}

func (s *mockCollaboratorLifecycleServer) handleDelete(w http.ResponseWriter) {
	s.deleted = true
	w.WriteHeader(http.StatusNoContent)
}

// snapshot returns whether the collaborator has been deleted, safe to call
// concurrently with the server's own handler goroutines.
func (s *mockCollaboratorLifecycleServer) isDeleted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleted
}

// TestAccOrganizationUserResource_Lifecycle_MockServer is the contract's T1/AC
// mandatory CI-enforced lifecycle test (section 7): Import -> Read -> Delete,
// all against a scripted mock, no env-var gating, no real identity touched. The
// lifecycle starts from Import rather than Create because that is what this
// contract section requires covered here, matching the real-infra test's own
// established shape - not because Create is blocked or errors for this
// resource. It does neither: Create adopts an existing member or invites a new
// one (see resource_organization_user.go's own doc comment), and that path has
// its own dedicated coverage in resource_organization_user_origin_acc_test.go
// (TestAccOrganizationUserResourceDestroyAdoptedMemberIsNotEvicted and its
// invited-path sibling).
//
// There is no Update leg any more: the resource is membership-only and exposes
// no writable attribute, so no config change can produce an update plan. The
// role write path it used to cover now belongs to
// anyscale_organization_user_role and is tested there. The mock has no PUT
// handler at all (see its doc comment), so a regression that reintroduced a
// role write from this resource would 404 rather than pass silently.
//
// The role attributes are likewise not asserted here - they are no longer part
// of this resource's state. The singular per-user GET is still exercised
// indirectly (findUserByID hydrates through it on every read), and the
// role-field assertions themselves live with the data sources that still
// expose them.
func TestAccOrganizationUserResource_Lifecycle_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, mockServer := newMockCollaboratorLifecycleServer(t)
	const resourceAddr = "anyscale_organization_user.test"

	config := testAccProviderBlock(httpServer.URL) + `
resource "anyscale_organization_user" "test" {
  email = "lifecycle@example.com"
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Import by EMAIL - the resource's key since the re-key.
			// ImportStatePersist keeps the imported state for the next step so it
			// refreshes an existing resource rather than creating a second one.
			{
				Config:             config,
				ResourceName:       resourceAddr,
				ImportState:        true,
				ImportStateId:      "lifecycle@example.com",
				ImportStatePersist: true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported resource, got %d", len(states))
					}
					s := states[0]
					// id holds the EMAIL since the re-key.
					if s.Attributes["id"] != "lifecycle@example.com" {
						return fmt.Errorf("id = %q, want the email %q", s.Attributes["id"], "lifecycle@example.com")
					}
					if s.Attributes["email"] != "lifecycle@example.com" {
						return fmt.Errorf("email = %q, want %q", s.Attributes["email"], "lifecycle@example.com")
					}
					if s.Attributes["user_id"] != "usr_lifecycle_mock" {
						return fmt.Errorf("user_id = %q, want %q", s.Attributes["user_id"], "usr_lifecycle_mock")
					}
					if s.Attributes["created_at"] != "2026-01-01T00:00:00Z" {
						return fmt.Errorf("created_at = %q, want %q", s.Attributes["created_at"], "2026-01-01T00:00:00Z")
					}
					// The role fields must NOT be present at all: they were
					// removed from this resource, so recovering one here would
					// mean the schema/import path regressed.
					for _, gone := range []string{"permission_level", "base_role", "additional_roles.#"} {
						if _, ok := s.Attributes[gone]; ok {
							return fmt.Errorf("imported state still carries %q (= %q); roles belong to anyscale_organization_user_role, not this membership-only resource", gone, s.Attributes[gone])
						}
					}
					return nil
				},
			},
			// Verify the persisted import matches a plain Read/Config cycle cleanly.
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceAddr, "email", "lifecycle@example.com"),
					resource.TestCheckResourceAttr(resourceAddr, "name", "Lifecycle Test"),
					resource.TestCheckResourceAttr(resourceAddr, "created_at", "2026-01-01T00:00:00Z"),
					resource.TestCheckNoResourceAttr(resourceAddr, "permission_level"),
					resource.TestCheckNoResourceAttr(resourceAddr, "base_role"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})

	// resource.Test's own teardown destroy already ran by this point, pass or
	// fail. THIS ASSERTION IS INVERTED FROM WHAT IT USED TO BE, and the inversion
	// is the point: before the re-key, destroying this resource evicted the human
	// from the organization. It no longer does, and must never do so again -
	// destroy cancels only a pending invitation this resource itself sent, and
	// this member was adopted rather than invited. If a regression ever restores
	// eviction, this is the assertion that catches it.
	if mockServer.isDeleted() {
		t.Fatal("the member was DELETED from the organization by teardown destroy - destroying this resource must " +
			"never evict a human. Only a pending invitation this resource itself sent may be cancelled.")
	}
}
