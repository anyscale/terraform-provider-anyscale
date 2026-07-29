package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// mockOrgUserRoleServer is a scripted stand-in for the organization
// collaborators list, the singular per-user GET, the legacy permission_level
// PUT, and the gated roles PUT - enough to exercise
// anyscale_organization_user_role's real Create/Read/Update/Delete against
// the real resource code, no real infra.
//
// Mirrors the list-hardcodes-empty / singular-has-real-value asymmetry the
// shipped anyscale_organization_user resource already depends on
// (findOrgCollaboratorByEmail always re-reads the singular path) - the list
// handler below deliberately does NOT report additionalRoles, so a test that
// accidentally reads roles off the list would see a false empty instead of
// the real value, the same way the real API does.
type mockOrgUserRoleServer struct {
	mu sync.Mutex

	email      string
	identityID string
	userID     string

	baseRole        string
	additionalRoles []string
	writes          int // count of PUT calls received, either write path
	legacyWrites    int // count of PUTs to the legacy permission_level path specifically
	rolesWrites     int // count of PUTs to the gated roles path specifically
}

func newMockOrgUserRoleServer(t *testing.T) (*httptest.Server, *mockOrgUserRoleServer) {
	t.Helper()
	s := &mockOrgUserRoleServer{
		email:           "org-role-mock@example.com",
		identityID:      "identity-org-role-mock",
		userID:          "usr_org_role_mock",
		baseRole:        "collaborator",
		additionalRoles: []string{},
	}
	server := httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(server.Close)
	return server, s
}

func (s *mockOrgUserRoleServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	path := r.URL.Path
	singularPath := "/api/v2/organization_collaborators/" + s.userID
	legacyPath := "/api/v2/organization_collaborators/" + s.identityID

	switch {
	case r.Method == http.MethodGet && path == "/api/v2/organization_collaborators":
		w.WriteHeader(http.StatusOK)
		// base_role IS reliable on the list endpoint (live-verified against the
		// real API: 107/107 real records populated) - only additional_roles is
		// the known false-empty hazard here, which is why hydrateCollaboratorRoles
		// only ever overwrites AdditionalRoles and never BaseRole. An earlier
		// draft of this mock omitted base_role from the list response entirely
		// and produced a spurious "provider produced inconsistent result"
		// failure that had nothing to do with what this test is proving - fixed
		// by matching the real endpoint's actual shape.
		_ = json.NewEncoder(w).Encode(provider.OrganizationCollaboratorsListResponse{
			Results: []provider.OrganizationCollaboratorResult{
				{ID: s.identityID, Email: s.email, UserID: &s.userID, PermissionLevel: s.baseRole, BaseRole: s.baseRole},
			},
		})
	case r.Method == http.MethodGet && path == singularPath:
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(provider.OrganizationCollaboratorSingularResponse{
			Result: provider.OrganizationCollaboratorResult{
				ID: s.identityID, Email: s.email, UserID: &s.userID,
				PermissionLevel: s.baseRole,
				BaseRole:        s.baseRole,
				AdditionalRoles: s.additionalRoles,
			},
		})
	case r.Method == http.MethodPut && path == legacyPath:
		var body provider.UpdateOrganizationCollaboratorRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.baseRole = body.PermissionLevel
		s.writes++
		s.legacyWrites++
		// Legacy path is single-field on the wire; additionalRoles must not move.
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut && path == singularPath+"/roles":
		var body provider.SetOrganizationRolesRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.baseRole = body.BaseRole
		s.additionalRoles = body.AdditionalRoles
		if s.additionalRoles == nil {
			s.additionalRoles = []string{}
		}
		s.writes++
		s.rolesWrites++
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"error":{"detail":"not found: %s %s"}}`, r.Method, path)
	}
}

// TestAccOrganizationUserRoleResource_DenyRolesOmittedPlanStability is the
// direct empirical answer architect asked for on R5/Gate 2: does omitting
// deny_roles from config after it was previously declared produce a stable
// (empty) plan, or does it show a perpetual "known after apply"? Confirmed
// PASSING against the shipped schema (deny_roles Optional+Computed, WITH
// listplanmodifier.UseStateForUnknown per the R5 reversal).
func TestAccOrganizationUserRoleResource_DenyRolesOmittedPlanStability(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, mock := newMockOrgUserRoleServer(t)
	const addr = "anyscale_organization_user_role.mock"

	withDenyRoles := testAccProviderBlock(httpServer.URL) + fmt.Sprintf(`
resource "anyscale_organization_user_role" "mock" {
  email      = %[1]q
  base_role  = "collaborator"
  deny_roles = ["image_reader"]
}
`, mock.email)

	denyRolesOmitted := testAccProviderBlock(httpServer.URL) + fmt.Sprintf(`
resource "anyscale_organization_user_role" "mock" {
  email     = %[1]q
  base_role = "collaborator"
}
`, mock.email)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Step 1: declare deny_roles explicitly, through the gated path.
			{
				Config: withDenyRoles,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "deny_roles.#", "1"),
					resource.TestCheckResourceAttr(addr, "deny_roles.0", "image_reader"),
				),
			},
			// Step 2: identical config re-applied. Sanity baseline - must be a
			// clean no-op regardless of the UseStateForUnknown question, since
			// nothing changed about what was declared.
			{
				Config: withDenyRoles,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// Step 3: THE QUESTION. Config now OMITS deny_roles entirely. The
			// resource's own write-path selection takes this to mean "leave
			// the backend's existing deny roles alone" and uses the legacy
			// endpoint, so the real, applied end state does not change - the
			// backend still holds ["image_reader"]. Whether the PLAN reflects
			// that as a clean no-op or as deny_roles going unknown depends
			// entirely on the plan modifier under discussion. Checked with a
			// PreApply plan check so this proves the PLAN, not just that
			// apply eventually converges.
			{
				Config: denyRolesOmitted,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "deny_roles.#", "1"),
					resource.TestCheckResourceAttr(addr, "deny_roles.0", "image_reader"),
				),
			},
		},
	})
}

// TestAccOrganizationUserRoleResource_DenyRolesExplicitEmptyPlanStability
// covers the second half of forge's request: explicit deny_roles = [] must
// also be stable across a re-plan, and must remain distinct from the omitted
// case above rather than collapsing into it.
func TestAccOrganizationUserRoleResource_DenyRolesExplicitEmptyPlanStability(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, mock := newMockOrgUserRoleServer(t)
	const addr = "anyscale_organization_user_role.mock"

	explicitEmpty := testAccProviderBlock(httpServer.URL) + fmt.Sprintf(`
resource "anyscale_organization_user_role" "mock" {
  email      = %[1]q
  base_role  = "collaborator"
  deny_roles = []
}
`, mock.email)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: explicitEmpty,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "deny_roles.#", "0"),
				),
			},
			{
				Config: explicitEmpty,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}

func (s *mockOrgUserRoleServer) snapshot() (baseRole string, additionalRoles []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.baseRole, append([]string(nil), s.additionalRoles...)
}

func (s *mockOrgUserRoleServer) writeCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes
}

func (s *mockOrgUserRoleServer) pathCounts() (legacy, roles int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.legacyWrites, s.rolesWrites
}

// TestAccOrganizationUserRoleResource_DestroyClearsDeclaredDenyRolesLeavesBaseRole
// covers R9's DECLARED branch: when deny_roles was declared, destroy clears
// it to empty via one PUT that sends the CURRENT base_role back unchanged
// (that endpoint is a SET over the pair), but base_role itself is left in
// place - an owner's role resource does not get demoted to collaborator on
// destroy, only their deny roles are cleared.
func TestAccOrganizationUserRoleResource_DestroyClearsDeclaredDenyRolesLeavesBaseRole(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, mock := newMockOrgUserRoleServer(t)
	mock.baseRole = "owner"
	mock.additionalRoles = []string{"image_reader"}
	const addr = "anyscale_organization_user_role.mock"

	config := testAccProviderBlock(httpServer.URL) + fmt.Sprintf(`
resource "anyscale_organization_user_role" "mock" {
  email      = %[1]q
  base_role  = "owner"
  deny_roles = ["image_reader"]
}
`, mock.email)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "base_role", "owner"),
					resource.TestCheckResourceAttr(addr, "deny_roles.#", "1"),
				),
			},
		},
	})

	// resource.Test's teardown destroy already ran (pass or fail) - assert what
	// it left behind on the mock backend directly, since state is gone.
	baseRole, additionalRoles := mock.snapshot()
	t.Logf("post-destroy backend state: base_role=%s additional_roles=%v", baseRole, additionalRoles)
	if baseRole != "owner" {
		t.Fatalf("expected destroy to LEAVE base_role as owner (R9: not a revert-to-default), got %q", baseRole)
	}
	if len(additionalRoles) != 0 {
		t.Fatalf("expected destroy to CLEAR deny_roles since it was declared, got %v", additionalRoles)
	}
}

// TestAccOrganizationUserRoleResource_DestroyLeavesOmittedDenyRolesUntouched
// covers R9's OMITTED branch: when deny_roles was never declared, this
// resource never took authority over it, so destroy must make NO API call
// that could touch it - not even a value-preserving one. Asserted by a
// write-call counter on the mock, not just by the end value staying the
// same (a rewrite that happens to write back the same value would pass a
// value-only check and still contradict R9's "never asserted authority"
// contract).
//
// CURRENTLY FAILS against the shipped code, and that failure is the finding:
// denyRolesDeclared(state.DenyRoles) in Delete checks STATE, but Read always
// repopulates state.DenyRoles from the observed backend value regardless of
// what config declared, and a destroy refreshes (Reads) first - so by the
// time Delete runs, state.DenyRoles is essentially never null, the omitted
// branch never fires, and destroy clears deny_roles unconditionally.
// resource.DeleteRequest has no Config/Plan field (framework-verified via go
// doc) to check instead. This is the acceptance criterion for the fix, not a
// broken test to work around - leave it red until Delete's authority check
// is rebuilt on something that survives into Delete (e.g. resp.Private set
// during Create/Update, per forge's proposed fix), then it should go green
// with no change to the assertions themselves.
func TestAccOrganizationUserRoleResource_DestroyLeavesOmittedDenyRolesUntouched(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, mock := newMockOrgUserRoleServer(t)
	mock.baseRole = "owner"
	mock.additionalRoles = []string{"image_reader"}
	const addr = "anyscale_organization_user_role.mock"

	config := testAccProviderBlock(httpServer.URL) + fmt.Sprintf(`
resource "anyscale_organization_user_role" "mock" {
  email     = %[1]q
  base_role = "owner"
}
`, mock.email)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "base_role", "owner"),
				),
			},
		},
	})

	baseRole, additionalRoles := mock.snapshot()
	writes := mock.writeCallCount()
	t.Logf("post-destroy backend state: base_role=%s additional_roles=%v total_write_calls=%d", baseRole, additionalRoles, writes)
	if baseRole != "owner" {
		t.Fatalf("expected base_role untouched at owner, got %q", baseRole)
	}
	if len(additionalRoles) != 1 || additionalRoles[0] != "image_reader" {
		t.Fatalf("expected deny_roles left exactly as the backend had them (image_reader), got %v", additionalRoles)
	}
	// Exactly one write in this test's whole lifecycle: Create's own legacy
	// permission_level PUT (deny_roles was omitted, so it took that path).
	// Destroy must add ZERO more - not a value-preserving rewrite, no call at
	// all - because omitting deny_roles means this resource never asserted
	// authority over it. A destroy that re-sends the same value would pass
	// the two checks above and still violate R9's actual contract.
	if writes != 1 {
		t.Fatalf("expected exactly 1 write call across Create+Destroy (Create only, Destroy makes none for the omitted branch), got %d", writes)
	}
}

// TestAccOrganizationUserRoleResource_UpdateOmittedDenyRolesStaysOnLegacyPath
// covers forge's second finding on the same root cause as the Delete bug:
// with UseStateForUnknown on deny_roles, plan.DenyRoles carries the prior
// value forward even when config omits the attribute, so a write-path
// selection that reads the PLAN (rather than Config) sends an ordinary
// base_role-only update down the GATED roles endpoint instead of the
// ungated legacy one - the exact failure Optional+Computed was chosen to
// avoid, reintroduced by the UseStateForUnknown fix for plan stability.
//
// CURRENTLY FAILS against the shipped code for the same reason as the
// Delete test above: the fix is to select the path from Config in both
// Create and Update, not Plan. Distinguishing the two paths by mock call
// count (not just the end value) is deliberate - a value-preserving SET
// through the wrong endpoint would still pass a value-only check.
func TestAccOrganizationUserRoleResource_UpdateOmittedDenyRolesStaysOnLegacyPath(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, mock := newMockOrgUserRoleServer(t)
	mock.baseRole = "collaborator"
	mock.additionalRoles = []string{"image_reader"}
	const addr = "anyscale_organization_user_role.mock"

	withDenyRoles := testAccProviderBlock(httpServer.URL) + fmt.Sprintf(`
resource "anyscale_organization_user_role" "mock" {
  email      = %[1]q
  base_role  = "collaborator"
  deny_roles = ["image_reader"]
}
`, mock.email)

	baseRoleOnlyUpdate := testAccProviderBlock(httpServer.URL) + fmt.Sprintf(`
resource "anyscale_organization_user_role" "mock" {
  email     = %[1]q
  base_role = "owner"
}
`, mock.email)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: withDenyRoles,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "base_role", "collaborator"),
					resource.TestCheckResourceAttr(addr, "deny_roles.#", "1"),
				),
			},
			{
				Config: baseRoleOnlyUpdate,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "base_role", "owner"),
					// deny_roles must survive this update untouched, even though
					// this step's own config never mentions it.
					resource.TestCheckResourceAttr(addr, "deny_roles.#", "1"),
					resource.TestCheckResourceAttr(addr, "deny_roles.0", "image_reader"),
				),
			},
		},
	})

	legacy, roles := mock.pathCounts()
	t.Logf("legacy path writes=%d roles path writes=%d", legacy, roles)
	// Create used the roles path once (deny_roles was declared). The Update
	// above must use the legacy path, not the roles path, since its own
	// config omits deny_roles - so roles-path writes must stay at 1 (from
	// Create only) and legacy-path writes must reach 1 (from this Update).
	if roles != 1 {
		t.Fatalf("expected exactly 1 roles-path write (Create only), got %d - an Update routing through the gated endpoint for a base_role-only config", roles)
	}
	if legacy != 1 {
		t.Fatalf("expected exactly 1 legacy-path write (this Update), got %d", legacy)
	}
}

// TestAccOrganizationUserRoleResource_DestroyClearAuthoritySurvivesRefresh is
// architect's R12 requirement 2: the declared-vs-omitted authority signal
// must survive an intervening Read (refresh), not just work immediately
// after Create. If Private state is dropped or not re-read on refresh,
// Delete would silently fall back to NOT clearing declared deny_roles -
// R9 refinement 2 failing in the OTHER direction, and invisible to a test
// that destroys right after Create without a refresh in between.
func TestAccOrganizationUserRoleResource_DestroyClearAuthoritySurvivesRefresh(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, mock := newMockOrgUserRoleServer(t)
	mock.baseRole = "owner"
	mock.additionalRoles = []string{"image_reader"}
	const addr = "anyscale_organization_user_role.mock"

	config := testAccProviderBlock(httpServer.URL) + fmt.Sprintf(`
resource "anyscale_organization_user_role" "mock" {
  email      = %[1]q
  base_role  = "owner"
  deny_roles = ["image_reader"]
}
`, mock.email)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "base_role", "owner"),
					resource.TestCheckResourceAttr(addr, "deny_roles.#", "1"),
				),
			},
			{
				// Refresh only - no config, no apply. This is the step that
				// must not lose the authority signal.
				RefreshState: true,
			},
		},
	})

	baseRole, additionalRoles := mock.snapshot()
	t.Logf("post-refresh-then-destroy backend state: base_role=%s additional_roles=%v", baseRole, additionalRoles)
	if baseRole != "owner" {
		t.Fatalf("expected base_role left as owner, got %q", baseRole)
	}
	if len(additionalRoles) != 0 {
		t.Fatalf("expected destroy to CLEAR deny_roles even after an intervening refresh, got %v - the authority signal did not survive the refresh", additionalRoles)
	}
}
