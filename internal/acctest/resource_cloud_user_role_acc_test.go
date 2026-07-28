package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// mockCloudUserRoleLifecycleServer is a scripted stand-in for the org-wide
// organization_collaborators list plus the cloud collaborators/roles APIs,
// tracking one org member and one cloud's bootstrap membership and roles so a
// real resource.Test can exercise Create (the two-ordered-call H21/H22
// sequence) -> Read -> Update -> Delete end to end without touching real
// infra. Mirrors mockCollaboratorLifecycleServer's shape and purpose for
// anyscale_organization_user.
type mockCloudUserRoleLifecycleServer struct {
	mu sync.Mutex

	cloudID    string
	email      string
	identityID string
	userID     string

	hasCloudPermission bool
	baseRole           string
	denyRoles          []string
}

func newMockCloudUserRoleLifecycleServer(t *testing.T) (*httptest.Server, *mockCloudUserRoleLifecycleServer) {
	t.Helper()
	s := &mockCloudUserRoleLifecycleServer{
		cloudID:    "cld_lifecycle_mock",
		email:      "analyst@example.com",
		identityID: "identity-lifecycle-mock",
		userID:     "usr_lifecycle_mock",
	}
	server := httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(server.Close)
	return server, s
}

func (s *mockCloudUserRoleLifecycleServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	path := r.URL.Path
	base := fmt.Sprintf("/api/v2/clouds/%s", s.cloudID)

	switch {
	case r.Method == http.MethodGet && path == "/api/v2/organization_collaborators":
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(provider.OrganizationCollaboratorsListResponse{
			Results: []provider.OrganizationCollaboratorResult{
				{ID: s.identityID, Email: s.email, UserID: &s.userID},
			},
		})
	case r.Method == http.MethodPost && path == base+"/collaborators/users":
		var body provider.CreateCloudCollaboratorRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if s.hasCloudPermission {
			w.WriteHeader(http.StatusConflict)
			_, _ = fmt.Fprintf(w, `{"error":{"detail":"User with email %s already has permissions for this cloud."}}`, body.Email)
			return
		}
		s.hasCloudPermission = true
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut && strings.HasPrefix(path, base+"/collaborators/users/") && strings.HasSuffix(path, "/roles"):
		var body provider.SetCloudRolesRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.baseRole = body.BaseRole
		s.denyRoles = body.DenyRoles
		if s.denyRoles == nil {
			s.denyRoles = []string{}
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && path == base+"/collaborators/roles":
		w.WriteHeader(http.StatusOK)
		if s.baseRole == "" {
			_ = json.NewEncoder(w).Encode(provider.CloudUserRolesListResponse{})
			return
		}
		_ = json.NewEncoder(w).Encode(provider.CloudUserRolesListResponse{
			Results: []provider.CloudUserRolesResult{
				{UserID: s.userID, BaseRoles: []string{s.baseRole}, DenyRoles: s.denyRoles},
			},
		})
	case r.Method == http.MethodDelete && strings.HasPrefix(path, base+"/collaborators/"):
		if !s.hasCloudPermission {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprintf(w, `{"error":{"detail":"User with identity %s is not a member of clouds: {%s}."}}`, s.identityID, s.cloudID)
			return
		}
		s.hasCloudPermission = false
		s.baseRole = ""
		s.denyRoles = nil
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"error":{"detail":"not found: %s %s"}}`, r.Method, path)
	}
}

func (s *mockCloudUserRoleLifecycleServer) isDeleted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.hasCloudPermission
}

// TestAccCloudUserRoleResource_Lifecycle_MockServer is the contract's
// mandatory CI-enforced lifecycle test (design doc section 6, acceptance
// criteria 6-9): Create (the real two-ordered-call H21/H22 sequence, legacy
// bootstrap then roles PUT) -> Read -> Update -> Delete, all against a
// scripted mock, no env-var gating, no real identity or cloud touched.
func TestAccCloudUserRoleResource_Lifecycle_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, mockServer := newMockCloudUserRoleLifecycleServer(t)
	const resourceAddr = "anyscale_cloud_user_role.analyst"

	configFor := func(baseRole string, denyRoles string) string {
		return testAccProviderBlock(httpServer.URL) + fmt.Sprintf(`
resource "anyscale_cloud_user_role" "analyst" {
  cloud_id   = %[1]q
  email      = %[2]q
  base_role  = %[3]q
  deny_roles = %[4]s
}
`, mockServer.cloudID, mockServer.email, baseRole, denyRoles)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create: exercises the real two-ordered-call sequence against the mock
			// (legacy bootstrap POST, then roles PUT) and reads the granted role back.
			{
				Config: configFor("compute_config_viewer", "[]"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceAddr, "cloud_id", mockServer.cloudID),
					resource.TestCheckResourceAttr(resourceAddr, "email", mockServer.email),
					resource.TestCheckResourceAttr(resourceAddr, "user_id", mockServer.userID),
					resource.TestCheckResourceAttr(resourceAddr, "identity_id", mockServer.identityID),
					resource.TestCheckResourceAttr(resourceAddr, "base_role", "compute_config_viewer"),
					resource.TestCheckResourceAttr(resourceAddr, "deny_roles.#", "0"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// Update: base_role change plus adding a deny role, in one apply -
			// touches only the roles PUT, never re-runs the bootstrap.
			{
				Config: configFor("writer", `["cloud_read_only"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceAddr, "base_role", "writer"),
					resource.TestCheckResourceAttr(resourceAddr, "deny_roles.#", "1"),
					resource.TestCheckResourceAttr(resourceAddr, "deny_roles.0", "cloud_read_only"),
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
	// fail) - the "Delete" leg of the required chain, against a role this
	// same test's Create bootstrapped, so it must succeed cleanly.
	if !mockServer.isDeleted() {
		t.Fatal("expected the cloud role's underlying membership to be removed by the test's teardown destroy, but it is still present")
	}
}

// TestAccCloudUserRoleResource_BootstrapAlwaysRunsEvenWhenAlreadyCollaborator
// is a focused mock-server acceptance test for H22: applying this resource
// against a user who is already a cloud collaborator must still succeed (the
// bootstrap's "already has permissions" 409 is expected, not fatal), and must
// still leave a destroyable resource behind.
func TestAccCloudUserRoleResource_BootstrapAlwaysRunsEvenWhenAlreadyCollaborator(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, mockServer := newMockCloudUserRoleLifecycleServer(t)
	mockServer.hasCloudPermission = true // already a collaborator before Terraform ever touches this user

	config := testAccProviderBlock(httpServer.URL) + fmt.Sprintf(`
resource "anyscale_cloud_user_role" "analyst" {
  cloud_id  = %[1]q
  email     = %[2]q
  base_role = "project_viewer"
}
`, mockServer.cloudID, mockServer.email)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud_user_role.analyst", "base_role", "project_viewer"),
				),
			},
		},
	})

	if !mockServer.isDeleted() {
		t.Fatal("expected teardown destroy to succeed for a role granted on top of a pre-existing collaborator")
	}
}
