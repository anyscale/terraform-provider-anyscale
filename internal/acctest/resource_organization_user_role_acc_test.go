package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
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

// TestAccOrganizationUserRoleResource_DenyRolesOmittedPlanStability proves
// that omitting deny_roles from config after it was previously declared
// produces an empty plan rather than a perpetual "known after apply" - the
// behavior deny_roles' Optional+Computed shape with
// listplanmodifier.UseStateForUnknown exists to give.
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
// covers the second half of the request: explicit deny_roles = [] must
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

// setBackend changes the mock's stored roles under the lock, standing in for
// an out-of-band change or for pre-seeding a member before the test runs. A
// nil additionalRoles leaves the stored deny roles as they are.
func (s *mockOrgUserRoleServer) setBackend(baseRole string, additionalRoles []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.baseRole = baseRole
	if additionalRoles != nil {
		s.additionalRoles = additionalRoles
	}
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
// covers the DECLARED branch of Delete: when deny_roles was declared, destroy clears
// it to empty via one PUT that sends the CURRENT base_role back unchanged
// (that endpoint is a SET over the pair), but base_role itself is left in
// place - an owner's role resource does not get demoted to collaborator on
// destroy, only their deny roles are cleared.
func TestAccOrganizationUserRoleResource_DestroyClearsDeclaredDenyRolesLeavesBaseRole(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, mock := newMockOrgUserRoleServer(t)
	mock.setBackend("owner", []string{"image_reader"})
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
		t.Fatalf("expected destroy to LEAVE base_role as owner (destroy is not a revert-to-default), got %q", baseRole)
	}
	if len(additionalRoles) != 0 {
		t.Fatalf("expected destroy to CLEAR deny_roles since it was declared, got %v", additionalRoles)
	}
}

// TestAccOrganizationUserRoleResource_DestroyLeavesOmittedDenyRolesUntouched
// covers the OMITTED branch of Delete: when deny_roles was never declared,
// this resource never took authority over it, so destroy must make NO API
// call that could touch it - not even a value-preserving one. Asserted by a
// write-call counter on the mock, not just by the end value staying the
// same (a rewrite that happens to write back the same value would pass a
// value-only check and still break the "never asserted authority" contract).
//
// Delete cannot decide this from state: Read repopulates state.DenyRoles from
// the backend regardless of config, and destroy refreshes first, so state is
// essentially never null there; resource.DeleteRequest has no Config or Plan.
// Create and Update therefore derive the decision from req.Config
// (denyRolesDeclaredInConfig) and persist it to private state
// (recordDenyRolesDeclared), which Delete reads back via
// denyRolesWereDeclared. Basing the check on state instead fails this test.
func TestAccOrganizationUserRoleResource_DestroyLeavesOmittedDenyRolesUntouched(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, mock := newMockOrgUserRoleServer(t)
	mock.setBackend("owner", []string{"image_reader"})
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
	// the two checks above and still violate the contract.
	if writes != 1 {
		t.Fatalf("expected exactly 1 write call across Create+Destroy (Create only, Destroy makes none for the omitted branch), got %d", writes)
	}
}

// TestAccOrganizationUserRoleResource_UpdateOmittedDenyRolesStaysOnLegacyPath
// guards write-path selection in Update: with UseStateForUnknown on
// deny_roles, plan.DenyRoles carries the prior value forward even when config
// omits the attribute, so a selection that read the PLAN (rather than Config)
// would send an ordinary base_role-only update down the GATED roles endpoint
// instead of the ungated legacy one. Create and Update both select the path
// from denyRolesDeclaredInConfig(ctx, req.Config). Distinguishing the two
// paths by mock call count (not just the end value) is deliberate - a
// value-preserving SET through the wrong endpoint would still pass a
// value-only check.
func TestAccOrganizationUserRoleResource_UpdateOmittedDenyRolesStaysOnLegacyPath(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, mock := newMockOrgUserRoleServer(t)
	mock.setBackend("collaborator", []string{"image_reader"})
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
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_organization_user_role.mock", plancheck.ResourceActionUpdate),
					},
				},
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

// TestAccOrganizationUserRoleResource_DestroyClearAuthoritySurvivesRefresh
// proves the declared-vs-omitted authority signal survives an intervening
// Read (refresh), not just immediately after Create. If private state were
// dropped on refresh, Delete would silently fall back to NOT clearing
// declared deny_roles - the declared branch failing in the other direction,
// invisible to a test that destroys right after Create.
func TestAccOrganizationUserRoleResource_DestroyClearAuthoritySurvivesRefresh(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, mock := newMockOrgUserRoleServer(t)
	mock.setBackend("owner", []string{"image_reader"})
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

// mockOrgUserRoleFlakyServer wraps mockOrgUserRoleServer with an
// injectable failure on a SPECIFIC numbered call to the list endpoint, to
// prove that a transient (non-404) error in Read must surface as a real
// error and leave the resource IN STATE, never silently removed the way a
// genuine 404 does. A 404-only test would pass against the exact bug (Read
// treating ANY error as "gone").
//
// Counting calls rather than arming a one-shot flag between steps is
// deliberate: resource.Test runs its own automatic post-apply refresh
// check after every step, which consumes list calls the test does not
// control the timing of directly - counting sidesteps needing to guess
// exactly which internal call that refresh maps to on any given version.
type mockOrgUserRoleFlakyServer struct {
	*mockOrgUserRoleServer
	mu           sync.Mutex
	listCalls    int
	failOnCallNo int
}

func newMockOrgUserRoleFlakyServer(t *testing.T, failOnCallNo int) (*httptest.Server, *mockOrgUserRoleFlakyServer) {
	t.Helper()
	inner := &mockOrgUserRoleServer{
		email:           "flaky-org-role-mock@example.com",
		identityID:      "identity-flaky-mock",
		userID:          "usr_flaky_mock",
		baseRole:        "collaborator",
		additionalRoles: []string{},
	}
	s := &mockOrgUserRoleFlakyServer{mockOrgUserRoleServer: inner, failOnCallNo: failOnCallNo}
	server := httptest.NewServer(http.HandlerFunc(s.handleFlaky))
	t.Cleanup(server.Close)
	return server, s
}

func (s *mockOrgUserRoleFlakyServer) handleFlaky(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/api/v2/organization_collaborators" {
		s.mu.Lock()
		s.listCalls++
		callNo := s.listCalls
		s.mu.Unlock()
		if callNo == s.failOnCallNo {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error":{"detail":"simulated transient failure, not a real not-found"}}`)
			return
		}
	}
	s.handle(w, r)
}

// TestAccOrganizationUserRoleResource_NonNotFoundReadErrorLeavesResourceInState
// proves that a 500 (or any non-404) from Read's underlying list call must
// produce a real Terraform error, and the resource must remain recoverable
// in state afterward - not be silently dropped the way a
// genuine 404 correctly is. The injected failure lands on the THIRD list
// call: Create makes two (resolveIdentityForEmail, then the read-back after
// the write), both of which must succeed so the resource genuinely gets
// created; resource.Test's own post-apply refresh is the third, and that is
// where the 500 hits. Proven by: (1) the first step failing with the expected
// error even though Create's apply succeeded, and (2) a following step, mock
// healthy again, succeeding with a clean plan against the SAME resource
// rather than a fresh create - which is what a wrongly-executed
// RemoveResource would have forced instead.
func TestAccOrganizationUserRoleResource_NonNotFoundReadErrorLeavesResourceInState(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, mock := newMockOrgUserRoleFlakyServer(t, 3)
	const addr = "anyscale_organization_user_role.mock"

	config := testAccProviderBlock(httpServer.URL) + fmt.Sprintf(`
resource "anyscale_organization_user_role" "mock" {
  email     = %[1]q
  base_role = "collaborator"
}
`, mock.email)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Create succeeds (list calls #1 and #2). resource.Test's own
				// post-apply refresh then makes list call #3, which hits the
				// injected 500 - surfacing as this step's error, not silence.
				Config:      config,
				ExpectError: regexp.MustCompile(`Could Not Read Organization User Role`),
			},
			{
				// Mock is healthy for every call from here on. If the earlier 500
				// had wrongly removed the resource from state, this step would
				// show a CREATE. It must instead be a clean no-op against the
				// SAME resource Create actually wrote.
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
				Check: resource.TestCheckResourceAttr(addr, "base_role", "collaborator"),
			},
		},
	})
}

// TestAccOrganizationUserRoleResource_ImportRecoversBaseRoleAndDenyRoles
// covers `terraform import <email>` end to end.
//
// What this test can and cannot prove, verified directly rather than assumed:
// `terraform import` always performs an automatic Read (refresh) immediately
// after ImportState, so whatever ImportState itself wrote to state is
// overwritten by Read's own result before ImportStateCheck ever sees it -
// confirmed empirically by mutating ImportState to set deny_roles to an empty
// list and observing ImportStateCheck still receive the real value. So this
// test does NOT isolate ImportState's hydration from Read's (that pair is
// covered separately - see TestOrganizationUserRoleImportState_RecoversFromSingularEndpoint
// in the provider package, which calls ImportState directly with no refresh
// involved). What this test DOES prove is that `terraform import <email>` resolves the right collaborator end to end
// - the two-ID (identity_id/user_id) resolution, the schema round-trip, and
// ImportStateVerify's comparison against Create's own state all actually
// work for this resource, none of which a unit test calling ImportState
// directly would exercise.
func TestAccOrganizationUserRoleResource_ImportRecoversBaseRoleAndDenyRoles(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, mock := newMockOrgUserRoleServer(t)
	mock.setBackend("collaborator", []string{"image_reader"})
	const addr = "anyscale_organization_user_role.mock"

	config := testAccProviderBlock(httpServer.URL) + fmt.Sprintf(`
resource "anyscale_organization_user_role" "mock" {
  email      = %[1]q
  base_role  = "collaborator"
  deny_roles = ["image_reader"]
}
`, mock.email)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "base_role", "collaborator"),
					resource.TestCheckResourceAttr(addr, "deny_roles.#", "1"),
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     mock.email,
				ImportStateVerify: true,
				// ImportStateCheck runs in the same throwaway import directory as the
				// import itself (see TF_IMPORTSTATEPERSIST notes in CLAUDE.md), so it
				// sees what ImportState actually recovered rather than what Create left
				// behind - the PLACEBO guard this test needs, since ImportStateVerify
				// alone would still pass if ImportState silently returned Create's own
				// cached values instead of making its own API calls.
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected exactly 1 imported instance, got %d", len(states))
					}
					s := states[0]
					if s.Attributes["base_role"] != "collaborator" {
						return fmt.Errorf("expected imported base_role=collaborator, got %q", s.Attributes["base_role"])
					}
					if s.Attributes["deny_roles.#"] != "1" || s.Attributes["deny_roles.0"] != "image_reader" {
						return fmt.Errorf("expected imported deny_roles=[image_reader] (from the singular endpoint, not the list's hardcoded empty), got attrs: %v", s.Attributes)
					}
					return nil
				},
			},
		},
	})
}

// TestAccOrganizationUserRoleResource_ColdImportThenDestroyLeavesDenyRolesUntouched
// covers the documented, deliberate consequence of ImportState never writing
// the orgUserRoleDenyRolesDeclaredKey private-state entry: denyRolesWereDeclared
// defaults to false when the key is absent, so a resource that was imported and
// then destroyed WITHOUT an intervening Create/Update never clears deny_roles,
// even though state shows them populated. That default is documented in
// denyRolesWereDeclared's own comment as the safe direction (under-act, not
// over-act) but had no test proving it holds through a real import - only
// through Create, which always does record the key.
func TestAccOrganizationUserRoleResource_ColdImportThenDestroyLeavesDenyRolesUntouched(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, mock := newMockOrgUserRoleServer(t)
	mock.setBackend("owner", []string{"image_reader"})
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
				// Cold import as the FIRST step, against a resource pre-seeded on the
				// mock backend out of band (never created through this Terraform run) -
				// the only way to observe what ImportState alone leaves in private
				// state, per CLAUDE.md's note that ImportStatePersist is required for
				// this and refuses an address already managed in the working directory.
				ResourceName:       addr,
				ImportState:        true,
				ImportStateId:      mock.email,
				ImportStatePersist: true,
				Config:             config,
			},
		},
	})

	baseRole, additionalRoles := mock.snapshot()
	t.Logf("post-cold-import-destroy backend state: base_role=%s additional_roles=%v", baseRole, additionalRoles)
	if baseRole != "owner" {
		t.Fatalf("expected base_role left as owner, got %q", baseRole)
	}
	if len(additionalRoles) != 1 || additionalRoles[0] != "image_reader" {
		t.Fatalf("expected deny_roles left untouched at [image_reader] since a cold import never recorded authority over them, got %v", additionalRoles)
	}
}

// TestAccOrganizationUserRoleResource_RefreshDetectsBaseRoleDrift mutates the
// backend out of band and asserts the refresh carries the new base_role into
// state. Mock-server, not real API: mutating a real backend mid-test means
// touching a shared disposable identity with no sweeper to restore it, and a
// mock gives a deterministic backend-side change to assert against.
//
// Uses a custom plan check on the "before" value of base_role specifically,
// not ExpectKnownValue or a bare ExpectNonEmptyPlan alone - confirmed the hard
// way while writing this test. ExpectNonEmptyPlan alone passed against a
// completely masked drift (Read discarding the backend's real value and
// keeping the prior state's "collaborator"), because deny_roles' own
// Optional+Computed/UseStateForUnknown shape produces a plan-shaped diff on
// every refresh regardless of base_role - an incidental non-empty plan for an
// unrelated reason. ExpectKnownValue on the planned base_role is equally
// unfalsifiable here: config declares "collaborator" either way, so the
// planned AFTER value is "collaborator" whether or not Read actually noticed
// the drift. Only asserting the plan's BEFORE value is "owner" proves Read
// carried the real backend value into state ahead of the plan.
func TestAccOrganizationUserRoleResource_RefreshDetectsBaseRoleDrift(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, mock := newMockOrgUserRoleServer(t)
	mock.setBackend("collaborator", nil)
	const addr = "anyscale_organization_user_role.mock"

	config := testAccProviderBlock(httpServer.URL) + fmt.Sprintf(`
resource "anyscale_organization_user_role" "mock" {
  email     = %[1]q
  base_role = "collaborator"
}
`, mock.email)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckResourceAttr(addr, "base_role", "collaborator"),
			},
			{
				// Out-of-band change, equivalent to an admin changing the role
				// through the console or CLI between applies. Mutating the mock
				// directly, not through this resource, is the point. A RefreshState
				// step cannot carry Config or ConfigPlanChecks, so the specific-value
				// assertion is the PostRefresh plan check on the plan's BEFORE value.
				PreConfig: func() {
					mock.setBackend("owner", nil)
				},
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
				RefreshPlanChecks: resource.RefreshPlanChecks{
					PostRefresh: []plancheck.PlanCheck{
						expectPlanBeforeValue{addr: addr, attr: "base_role", want: "owner"},
					},
				},
			},
		},
	})
}

// expectPlanBeforeValue asserts a resource's pre-change ("before") value for a
// single top-level string attribute in the plan. Neither
// plancheck.ExpectKnownValue (which reads the planned AFTER value) nor
// ExpectNonEmptyPlan (which does not look at any specific attribute) can
// distinguish "the plan changed this attribute because of real drift" from
// "the plan is non-empty for an unrelated reason, and this attribute was
// never actually re-read" - see the comment on
// TestAccOrganizationUserRoleResource_RefreshDetectsBaseRoleDrift for how
// that gap was actually caught, not assumed.
type expectPlanBeforeValue struct {
	addr string
	attr string
	want string
}

func (e expectPlanBeforeValue) CheckPlan(ctx context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	for _, rc := range req.Plan.ResourceChanges {
		if rc.Address != e.addr {
			continue
		}
		before, ok := rc.Change.Before.(map[string]interface{})
		if !ok {
			resp.Error = fmt.Errorf("%s: plan Before is not an object: %#v", e.addr, rc.Change.Before)
			return
		}
		got, _ := before[e.attr].(string)
		if got != e.want {
			resp.Error = fmt.Errorf("%s: plan Before[%s] = %q, want %q - the refresh did not carry the real backend value into state", e.addr, e.attr, got, e.want)
		}
		return
	}
	resp.Error = fmt.Errorf("no plan change found for resource %s", e.addr)
}
