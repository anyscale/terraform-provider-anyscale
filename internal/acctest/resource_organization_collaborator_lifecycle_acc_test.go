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
// organization_collaborators API, tracking one mutable collaborator so a real
// resource.Test can exercise Import -> Read -> Update -> Delete end to end,
// including base_role/additional_roles hydration via the singular per-user GET
// (see hydrateCollaboratorRoles), without touching real infra.
//
// listBaseRole vs singularBaseRole models a real, live-proven backend split
// (found via an actual permission_level toggle against real infra, reverted
// after): alter_collaborator (the only write path for permission_level) writes
// Postgres only, never SpiceDB. The LIST endpoint's formatter derives base_role
// fresh from Postgres every call, so it always tracks a permission_level write
// immediately. The singular per-user GET (and search) derive base_role from
// SpiceDB-managed groups when the read flag is on - which alter_collaborator
// never touches - so it is a real live source of a genuinely stale value, not
// a mock artifact invented for this test. handleUpdate below updates
// listBaseRole (mirroring the real Postgres write) but deliberately leaves
// singularBaseRole frozen at its original value (mirroring SpiceDB never
// learning about the change) - this is what makes this mock strict enough to
// have caught the original overwrite bug, rather than assuming (as an earlier,
// too-generous version of this mock did) that the two sources always agree.
type mockCollaboratorLifecycleServer struct {
	mu               sync.Mutex
	identityID       string
	userID           string
	email            string
	name             string
	permissionLevel  string
	listBaseRole     string // Postgres-derived (LIST) - tracks permission_level writes
	singularBaseRole string // SpiceDB-derived (singular GET) - frozen; alter_collaborator never touches SpiceDB
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
	case r.Method == http.MethodPut && path == "/api/v2/organization_collaborators/"+s.identityID:
		s.handleUpdate(w, r)
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
// hydrateCollaboratorRoles calls). Its base_role is deliberately the frozen
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

func (s *mockCollaboratorLifecycleServer) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PermissionLevel string `json:"permission_level"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.permissionLevel = body.PermissionLevel
	// Mirrors the real backend: alter_collaborator writes Postgres only, so
	// listBaseRole (LIST's formatter, always Postgres-derived) tracks this
	// write immediately. singularBaseRole is deliberately left untouched -
	// alter_collaborator never calls SpiceDB, so the singular GET's
	// SpiceDB-derived base_role stays frozen at its pre-update value, live-
	// proven real behavior (see the type doc comment).
	s.listBaseRole = body.PermissionLevel
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, `{}`)
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

// TestAccOrganizationCollaboratorResource_Lifecycle_MockServer is the contract's T1/AC
// mandatory CI-enforced lifecycle test (section 7): Import -> Read (including
// base_role/additional_roles hydration via the singular GET) -> Update permission_level ->
// Delete, all against a scripted mock, no env-var gating, no real identity touched. This
// resource is import-only (Create always errors - covered separately), so the lifecycle
// starts from Import rather than Create, matching the real-infra test's own established shape.
func TestAccOrganizationCollaboratorResource_Lifecycle_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, mockServer := newMockCollaboratorLifecycleServer(t)
	const resourceAddr = "anyscale_organization_collaborator.test"

	configFor := func(permissionLevel string) string {
		return testAccProviderBlock(httpServer.URL) + fmt.Sprintf(`
resource "anyscale_organization_collaborator" "test" {
  permission_level = %[1]q
}
`, permissionLevel)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Import: populates identity fields plus the hydrated role fields (real
			// additional_roles from the singular GET, not list's hardcoded-empty value).
			// ImportStatePersist is required here - see the real-infra test's own
			// comment on this same resource: Create() always errors (import-only),
			// so without persisting, the next step would see no existing resource
			// and attempt to create one from Config, hitting "Direct Creation Not
			// Supported" instead of proceeding.
			{
				Config:             configFor("collaborator"),
				ResourceName:       resourceAddr,
				ImportState:        true,
				ImportStateId:      "identity-lifecycle-mock",
				ImportStatePersist: true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported resource, got %d", len(states))
					}
					s := states[0]
					if s.Attributes["id"] != "identity-lifecycle-mock" {
						return fmt.Errorf("id = %q, want %q", s.Attributes["id"], "identity-lifecycle-mock")
					}
					if s.Attributes["email"] != "lifecycle@example.com" {
						return fmt.Errorf("email = %q, want %q", s.Attributes["email"], "lifecycle@example.com")
					}
					if s.Attributes["permission_level"] != "collaborator" {
						return fmt.Errorf("permission_level = %q, want %q", s.Attributes["permission_level"], "collaborator")
					}
					if s.Attributes["base_role"] != "collaborator" {
						return fmt.Errorf("base_role = %q, want %q", s.Attributes["base_role"], "collaborator")
					}
					if s.Attributes["additional_roles.#"] != "1" || s.Attributes["additional_roles.0"] != "image_reader" {
						return fmt.Errorf("additional_roles = %#v, want a single element [image_reader] - hydrated from the singular GET, not list's hardcoded-empty value", s.Attributes)
					}
					return nil
				},
			},
			// Verify the persisted import matches a plain Read/Config cycle cleanly.
			{
				Config: configFor("collaborator"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceAddr, "permission_level", "collaborator"),
					resource.TestCheckResourceAttr(resourceAddr, "base_role", "collaborator"),
					resource.TestCheckResourceAttr(resourceAddr, "additional_roles.#", "1"),
					resource.TestCheckResourceAttr(resourceAddr, "additional_roles.0", "image_reader"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// Update permission_level: proves the write path AND that base_role
			// (re-hydrated post-update) tracks the change.
			{
				Config: configFor("owner"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceAddr, "permission_level", "owner"),
					resource.TestCheckResourceAttr(resourceAddr, "base_role", "owner"),
					resource.TestCheckResourceAttr(resourceAddr, "additional_roles.#", "1"),
					resource.TestCheckResourceAttr(resourceAddr, "additional_roles.0", "image_reader"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})

	// resource.Test's own teardown destroy already ran by this point (pass or
	// fail) - the "Delete" leg of the contract's required chain.
	if !mockServer.isDeleted() {
		t.Fatal("expected the collaborator to be deleted from the mock after the test's teardown destroy, but it is still present")
	}
}
