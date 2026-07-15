package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// mockInvitationServer is a scripted stand-in for the real organization_invitations API,
// used to prove real Terraform Core plan/apply behavior (not just the Go handler in
// isolation) without sending a real invitation email or consuming the 20/day rate limit.
type mockInvitationServer struct {
	mu             sync.Mutex
	nextID         int
	invitations    map[string]*mockInvitation
	lowercaseEmail bool // mirrors the real backend's invitation["email"].lower() at INSERT time
}

type mockInvitation struct {
	ID             string
	Email          string
	OrganizationID string
	CreatedAt      string
	ExpiresAt      string
	AcceptedAt     *string
}

// newMockInvitationServer returns both the httptest.Server (for its URL, to point
// the provider block at) and the underlying mockInvitationServer (for tests that
// need to inspect state directly, e.g. confirming an invalidate happened).
func newMockInvitationServer(t *testing.T, lowercaseEmail bool) (*httptest.Server, *mockInvitationServer) {
	t.Helper()
	s := &mockInvitationServer{
		invitations:    make(map[string]*mockInvitation),
		lowercaseEmail: lowercaseEmail,
	}
	server := httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(server.Close)
	return server, s
}

func (s *mockInvitationServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && path == "/api/v2/organization_invitations":
		s.handleCreate(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/v2/organization_invitations/"):
		s.handleGet(w, strings.TrimPrefix(path, "/api/v2/organization_invitations/"))
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/invalidate"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v2/organization_invitations/"), "/invalidate")
		s.handleInvalidate(w, id)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error":{"detail":"not found"}}`)
	}
}

func (s *mockInvitationServer) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	s.nextID++
	id := fmt.Sprintf("invite_mock_%d", s.nextID)
	email := req.Email
	if s.lowercaseEmail {
		email = strings.ToLower(email)
	}
	s.invitations[id] = &mockInvitation{
		ID:             id,
		Email:          email,
		OrganizationID: "org_mock",
		CreatedAt:      "2026-01-01T00:00:00Z",
		ExpiresAt:      "2099-01-01T00:00:00Z",
		AcceptedAt:     nil,
	}
	w.WriteHeader(http.StatusCreated)
	_, _ = fmt.Fprintf(w, `{"result":{"id":%q}}`, id)
}

func (s *mockInvitationServer) handleGet(w http.ResponseWriter, id string) {
	inv, ok := s.invitations[id]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error":{"detail":"invitation not found"}}`)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": map[string]any{
			"id":              inv.ID,
			"email":           inv.Email,
			"organization_id": inv.OrganizationID,
			"created_at":      inv.CreatedAt,
			"expires_at":      inv.ExpiresAt,
			"accepted_at":     inv.AcceptedAt,
		},
	})
}

// handleInvalidate mirrors the real backend exactly (org_invites_service.py
// _invalidate_invitation): it only sets expires_at to a past timestamp, it
// never deletes the row and never touches accepted_at. A subsequent GET must
// keep returning 200 with the now-expired row, not 404 - that distinction
// matters for Delete()'s own "treat 404 as success" branch never actually
// firing against a real invalidate, and for status ever reading "expired"
// rather than the resource silently vanishing from state.
func (s *mockInvitationServer) handleInvalidate(w http.ResponseWriter, id string) {
	inv, ok := s.invitations[id]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error":{"detail":"invitation not found"}}`)
		return
	}
	inv.ExpiresAt = "2020-01-01T00:00:00Z"
	w.WriteHeader(http.StatusAccepted)
	_, _ = fmt.Fprint(w, `{"result":{}}`)
}

// snapshot returns a copy of the invitation for the given id, safe to call
// from a test's CheckDestroy/Check functions concurrently with the server's
// own handler goroutines.
func (s *mockInvitationServer) snapshot(id string) (mockInvitation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.invitations[id]
	if !ok {
		return mockInvitation{}, false
	}
	return *inv, true
}

// TestAccOrganizationInvitationResource_Lifecycle_MockServer is the contract's T1/AC
// mandatory CI-enforced lifecycle test (section 7): Create -> Read -> RequiresReplace on
// email change -> Delete/invalidate -> Import, all against a scripted mock, no env-var
// gating, no real invitation sent. Uses a case-stable mock (lowercaseEmail: false) to keep
// this test focused on general lifecycle correctness - the email-casing defect has its own
// dedicated regression test above (TestAccOrganizationInvitationResource_MixedCaseEmail_MockServer).
func TestAccOrganizationInvitationResource_Lifecycle_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, server := newMockInvitationServer(t, false)
	const email1 = "lifecycle-test-1@example.com"
	const email2 = "lifecycle-test-2@example.com"
	const resourceAddr = "anyscale_organization_invitation.test"

	configFor := func(email string) string {
		return testAccProviderBlock(httpServer.URL) + fmt.Sprintf(`
resource "anyscale_organization_invitation" "test" {
  email = %[1]q
}
`, email)
	}

	var firstID, finalID string

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create + Read.
			{
				Config: configFor(email1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceAddr, "id"),
					resource.TestCheckResourceAttr(resourceAddr, "email", email1),
					resource.TestCheckResourceAttrSet(resourceAddr, "organization_id"),
					resource.TestCheckResourceAttrSet(resourceAddr, "created_at"),
					resource.TestCheckResourceAttrSet(resourceAddr, "expires_at"),
					resource.TestCheckResourceAttr(resourceAddr, "status", "pending"),
					resource.TestCheckNoResourceAttr(resourceAddr, "accepted_at"),
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources[resourceAddr]
						if !ok {
							return fmt.Errorf("resource %s not found in state", resourceAddr)
						}
						firstID = rs.Primary.ID
						if firstID == "" {
							return fmt.Errorf("resource %s has an empty id", resourceAddr)
						}
						return nil
					},
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// Changing email forces replacement (RequiresReplace), proves the new
			// resource gets a genuinely new id rather than reusing the old one.
			{
				Config: configFor(email2),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceAddr, plancheck.ResourceActionReplace),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceAddr, "email", email2),
					func(s *terraform.State) error {
						rs, ok := s.RootModule().Resources[resourceAddr]
						if !ok {
							return fmt.Errorf("resource %s not found in state", resourceAddr)
						}
						finalID = rs.Primary.ID
						if finalID == firstID {
							return fmt.Errorf("expected a new invitation id after email replace, got the same id %q", firstID)
						}
						// The old invitation must have been invalidated (destroy-before-create),
						// not left dangling as a second live pending invitation.
						oldInv, ok := server.snapshot(firstID)
						if !ok {
							return fmt.Errorf("old invitation %q vanished entirely from the mock; real invalidate only expires it, never deletes the row", firstID)
						}
						expires, err := time.Parse(time.RFC3339, oldInv.ExpiresAt)
						if err != nil {
							return fmt.Errorf("old invitation %q has unparseable expires_at %q: %w", firstID, oldInv.ExpiresAt, err)
						}
						if time.Now().Before(expires) {
							return fmt.Errorf("old invitation %q was not invalidated by the replace (expires_at=%s is in the future)", firstID, oldInv.ExpiresAt)
						}
						return nil
					},
				),
			},
			// Import by id.
			{
				ResourceName:      resourceAddr,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})

	// resource.Test's own teardown destroy already ran by this point (pass or fail) -
	// this is the "Delete/invalidate" leg of the contract's required chain. The final
	// invitation must exist in the mock but be expired, matching real backend semantics
	// (invalidate soft-expires, never hard-deletes the row).
	if finalID == "" {
		t.Fatal("finalID was never captured; an earlier step must have failed before reaching it")
	}
	finalInv, ok := server.snapshot(finalID)
	if !ok {
		t.Fatalf("invitation %q vanished entirely from the mock after teardown destroy; real invalidate only expires it, never deletes the row", finalID)
	}
	expires, err := time.Parse(time.RFC3339, finalInv.ExpiresAt)
	if err != nil {
		t.Fatalf("invitation %q has unparseable expires_at %q after teardown destroy: %v", finalID, finalInv.ExpiresAt, err)
	}
	if time.Now().Before(expires) {
		t.Fatalf("invitation %q was not invalidated by the final teardown destroy (expires_at=%s is in the future)", finalID, finalInv.ExpiresAt)
	}
}

// TestAccOrganizationInvitationResource_MixedCaseEmail_MockServer resolves contract open
// item I-OPEN empirically. Traced against the real backend (organization_invitations_dao.py):
// create_invitation INSERTs invitation["email"].lower() unconditionally, and find_invitation
// queries WHERE LOWER(email) = LOWER($1) - the stored email is ALWAYS lowercase regardless of
// the casing a caller sends. email is Required (not Computed) with no plan modifier beyond
// RequiresReplace, so the planned value for a new resource is exactly the config value. If
// Create() persists a different-cased value than what was planned, Terraform Core's own
// apply-consistency check should catch it - this test proves whether that actually happens,
// using a mock that mirrors the real backend's forced-lowercase behavior, so no real invitation
// email is sent and no rate-limit quota is spent to find out.
func TestAccOrganizationInvitationResource_MixedCaseEmail_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	httpServer, _ := newMockInvitationServer(t, true /* lowercaseEmail: mirrors the real backend */)
	const mixedCaseEmail = "Mixed.Case+Invite@Example.COM"

	config := testAccProviderBlock(httpServer.URL) + fmt.Sprintf(`
resource "anyscale_organization_invitation" "test" {
  email = %[1]q
}
`, mixedCaseEmail)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckResourceAttr("anyscale_organization_invitation.test", "email", mixedCaseEmail),
			},
		},
	})

	// Reaching this point without resource.Test failing the test IS the finding for
	// I-OPEN's practical consequence: either the framework/Core tolerated the
	// backend's forced-lowercase rewrite of a Required attribute, or (more likely,
	// see the companion regression test below) something in the current Create()
	// path already keeps state's email equal to the configured value despite the
	// backend storing lowercase. If this test starts failing with "Provider
	// produced inconsistent result after apply", that IS I-OPEN's empirical answer
	// surfacing as a real, user-facing failure mode - do not silence it without
	// understanding why first.
}
