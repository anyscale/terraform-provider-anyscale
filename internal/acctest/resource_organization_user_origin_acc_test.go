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
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// These tests exist to cover the ONE THING A UNIT TEST STRUCTURALLY CANNOT:
// that Delete correctly READS the membership origin out of Terraform provider
// private state.
//
// anyscale_organization_user records an origin at Create/ImportState and Delete
// branches on it - "adopted" must never evict a human who was already a member,
// "invited" must invalidate the invitation this resource sent, "imported" must
// do nothing. Those branches are unit-tested by extracting Delete's body to take
// the origin as a parameter, which proves the branching GIVEN an origin.
//
// It cannot prove the origin arrives. The private-state type is internal to
// terraform-plugin-framework, so a unit test cannot construct one; the extracted
// seam sits BELOW the read, which means a wrong key, bad marshalling, or a marker
// that Create never wrote all stay invisible while every mutation at that seam
// still fails correctly. That is not hypothetical - an earlier version of this
// resource's unit tests labelled a subtest "adopted member" while both subtests
// actually reached the default branch, and a mutation restoring the eviction call
// in the adopted branch PASSED.
//
// An acceptance test never constructs private state; it exercises the real
// write -> persist -> read round trip through the framework, which is exactly the
// half the unit tests miss. The two are complements, not alternatives.
//
// What is asserted is deliberately the API CALL, not resource state: the harm
// being guarded against is an HTTP request that removes a real person from a real
// organization. State can look correct while that call still went out.

const (
	originTestAdoptedEmail = "already.a.member@example.com"
	originTestInvitedEmail = "brand.new.person@example.com"
)

// mockOrganizationUserOriginServer serves the collaborator and invitation
// surfaces the re-keyed resource uses, and - the point of the fixture - RECORDS
// whether either of the two destructive calls was ever made.
type mockOrganizationUserOriginServer struct {
	mu sync.Mutex

	// memberEmail, when non-empty, is an existing organization member. Seeding it
	// is what makes Create take the ADOPT path rather than the invite path.
	memberEmail string
	identityID  string
	userID      string

	invitations map[string]*mockOriginInvitation
	nextID      int

	// The two facts these tests turn on.
	evictionCalled   bool // DELETE /api/v2/organization_collaborators/{identity_id}
	invalidateCalled bool // POST   /api/v2/organization_invitations/{id}/invalidate
	inviteCreated    bool // POST   /api/v2/organization_invitations
}

type mockOriginInvitation struct {
	ID         string
	Email      string
	ExpiresAt  string
	AcceptedAt *string
}

func newMockOrganizationUserOriginServer(t *testing.T, existingMemberEmail string) (*httptest.Server, *mockOrganizationUserOriginServer) {
	t.Helper()
	s := &mockOrganizationUserOriginServer{
		memberEmail: existingMemberEmail,
		identityID:  "ide_origin_mock_member",
		userID:      "usr_origin_mock_member",
		invitations: make(map[string]*mockOriginInvitation),
	}
	server := httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(server.Close)
	return server, s
}

func (s *mockOrganizationUserOriginServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	path := r.URL.Path
	switch {
	// The SSO preflight runs before every invitation create. Serving it with
	// sso_mode=off is what puts these tests on the REAL allowed path: without
	// this route the GET hits the 404 below, the guard cannot determine
	// sso_mode, and every test in this file silently exercises the fail-open
	// branch rather than the origin behaviour it names.
	case r.Method == http.MethodGet && path == "/api/v2/userinfo":
		_, _ = fmt.Fprint(w, `{"result":{"organizations":[{"sso_mode":"off"}]}}`)

	case r.Method == http.MethodGet && path == "/api/v2/organization_collaborators":
		s.handleListCollaborators(w, r)

	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/api/v2/organization_collaborators/"):
		// THE EVICTION CALL. Recording it is the entire purpose of this mock.
		s.evictionCalled = true
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodGet && path == "/api/v2/organization_invitations":
		s.handleListInvitations(w, r)

	case r.Method == http.MethodPost && path == "/api/v2/organization_invitations":
		s.handleCreateInvitation(w, r)

	case r.Method == http.MethodPost && strings.HasSuffix(path, "/invalidate"):
		// THE CANCEL CALL.
		s.invalidateCalled = true
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v2/organization_invitations/"), "/invalidate")
		if inv, ok := s.invitations[id]; ok {
			// Mirrors the real backend (org_invites_service.py
			// _invalidate_invitation): expire the row, never delete it, never
			// touch accepted_at.
			inv.ExpiresAt = "2020-01-01T00:00:00Z"
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"result":{}}`)

	case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/v2/organization_invitations/"):
		s.handleGetInvitation(w, strings.TrimPrefix(path, "/api/v2/organization_invitations/"))

	default:
		// Hard 404 rather than a permissive echo: an unexpected call must fail
		// loudly instead of being absorbed into a green run.
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"error":{"detail":"unexpected %s %s"}}`, r.Method, path)
	}
}

func (s *mockOrganizationUserOriginServer) handleListCollaborators(w http.ResponseWriter, r *http.Request) {
	results := []map[string]any{}
	wanted := r.URL.Query().Get("email")
	if s.memberEmail != "" && (wanted == "" || strings.EqualFold(wanted, s.memberEmail)) {
		results = append(results, map[string]any{
			"id":               s.identityID,
			"email":            s.memberEmail,
			"name":             "Already A Member",
			"permission_level": "collaborator",
			"base_role":        "collaborator",
			"additional_roles": []string{},
			"created_at":       "2026-01-01T00:00:00Z",
			"user_id":          s.userID,
		})
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"results":  results,
		"metadata": map[string]any{"total": len(results), "next_paging_token": nil},
	})
}

// handleListInvitations backs findPendingOrganizationInvitation, which filters
// by email. It returns only live, unaccepted invitations, matching the real
// backend's list query (organization_invitations_dao.py: expires_at > now AND
// accepted_at IS NULL) - so an invalidated invitation disappears from it while
// remaining fetchable by id.
func (s *mockOrganizationUserOriginServer) handleListInvitations(w http.ResponseWriter, r *http.Request) {
	wanted := r.URL.Query().Get("email")
	results := []map[string]any{}
	for _, inv := range s.invitations {
		if wanted != "" && !strings.EqualFold(inv.Email, wanted) {
			continue
		}
		if inv.AcceptedAt != nil || inv.ExpiresAt < "2026" {
			continue
		}
		results = append(results, invitationPayload(inv))
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"results":  results,
		"metadata": map[string]any{"total": len(results), "next_paging_token": nil},
	})
}

func (s *mockOrganizationUserOriginServer) handleCreateInvitation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	s.inviteCreated = true
	s.nextID++
	id := fmt.Sprintf("invite_origin_mock_%d", s.nextID)
	// Real backend lower-cases at INSERT (users_dao/org invites both normalize).
	s.invitations[id] = &mockOriginInvitation{
		ID:        id,
		Email:     strings.ToLower(req.Email),
		ExpiresAt: "2099-01-01T00:00:00Z",
	}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"result": invitationPayload(s.invitations[id])})
}

func (s *mockOrganizationUserOriginServer) handleGetInvitation(w http.ResponseWriter, id string) {
	inv, ok := s.invitations[id]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error":{"detail":"invitation not found"}}`)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"result": invitationPayload(inv)})
}

func invitationPayload(inv *mockOriginInvitation) map[string]any {
	return map[string]any{
		"id":              inv.ID,
		"email":           inv.Email,
		"organization_id": "org_origin_mock",
		"created_at":      "2026-01-01T00:00:00Z",
		"expires_at":      inv.ExpiresAt,
		"accepted_at":     inv.AcceptedAt,
	}
}

func (s *mockOrganizationUserOriginServer) snapshot() (eviction, invalidate, invited bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evictionCalled, s.invalidateCalled, s.inviteCreated
}

func originTestConfig(serverURL, email string) string {
	return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_organization_user" "test" {
  email = %[1]q
}
`, email)
}

// TestAccOrganizationUserResourceDestroyAdoptedMemberIsNotEvicted is the safety
// property with the worst failure mode in this resource: destroying a resource
// that merely ADOPTED an existing member must not remove that human from the
// organization.
//
// The origin is written by Create (adopt path) into private state, persisted by
// the framework, and read back by Delete. Nothing between those points is
// stubbed, which is what makes this test able to catch a broken read.
func TestAccOrganizationUserResourceDestroyAdoptedMemberIsNotEvicted(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	// Seeded as an existing member -> Create must ADOPT.
	httpServer, mock := newMockOrganizationUserOriginServer(t, originTestAdoptedEmail)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		// CheckDestroy, not a step Check: the harness always destroys at the end
		// of a test case and offers no hook to skip it, so this is the only place
		// that observes what the destroy actually did. A step Check runs BEFORE
		// the destroy and would pass regardless.
		CheckDestroy: func(*terraform.State) error {
			eviction, invalidate, invited := mock.snapshot()
			if eviction {
				return fmt.Errorf("destroy EVICTED an adopted member: DELETE /organization_collaborators was called. " +
					"This resource must never remove a human it did not add")
			}
			if invalidate {
				return fmt.Errorf("destroy invalidated an invitation for an adopted member; no invitation was ever sent")
			}
			if invited {
				return fmt.Errorf("create sent an invitation to somebody who was already a member")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: originTestConfig(httpServer.URL, originTestAdoptedEmail),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_organization_user.test", "email", originTestAdoptedEmail),
					// Adoption is silent: no invitation, no email, no error.
					func(*terraform.State) error {
						if _, _, invited := mock.snapshot(); invited {
							return fmt.Errorf("create invited an existing member instead of adopting them")
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccOrganizationUserResourceDestroyInvitedCancelsInvitation is the other
// half of the same read: when Create DID send the invitation, destroy must take
// it back. Getting this wrong leaks a live invitation that Terraform no longer
// tracks and that the person can still accept.
func TestAccOrganizationUserResourceDestroyInvitedCancelsInvitation(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	// No existing member -> Create must INVITE.
	httpServer, mock := newMockOrganizationUserOriginServer(t, "")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			eviction, invalidate, invited := mock.snapshot()
			if !invited {
				return fmt.Errorf("create never sent an invitation, so this test is not exercising the invited origin")
			}
			if !invalidate {
				return fmt.Errorf("destroy did NOT invalidate the invitation this resource sent: " +
					"POST /organization_invitations/{id}/invalidate was never called, leaving a live invitation untracked")
			}
			if eviction {
				return fmt.Errorf("destroy called the collaborator DELETE endpoint for someone who never joined")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: originTestConfig(httpServer.URL, originTestInvitedEmail),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_organization_user.test", "email", originTestInvitedEmail),
					func(*terraform.State) error {
						if _, _, invited := mock.snapshot(); !invited {
							return fmt.Errorf("create did not send an invitation for a non-member")
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccOrganizationUserResourceImportRoundTripPreservesCasing is criterion 1
// for this resource: import must produce a NO-OP PLAN for a realistic config,
// never a diff and never a replacement.
//
// The specific thing it pins is the casing rule. The Anyscale backend stores
// emails lower-cased (users_dao.py lower-cases at INSERT; a live read of the
// collaborators endpoint returned 7/7 lower-case), so an import that wrote back
// the API's echo would put "alice@example.com" into state while the config says
// "Alice@Example.com" - a permanent diff on a Required attribute, forever, with
// no way for the practitioner to make it converge except by retyping their
// config to match the backend's normalization.
//
// The rule is therefore: IMPORT WRITES THE EMAIL AS GIVEN IN THE IMPORT ID -
// what the operator typed - never the API echo. That is what step 2's
// ImportStateVerify pins below - it compares the freshly imported resource
// against the one Create already applied, in the same throwaway directory,
// before either is discarded.
//
// ON WHICH STEP ACTUALLY CATCHES WHAT - measured, because the obvious story is
// wrong for this resource and a wrong story here invites a bad "simplification":
//
//	MUT: ImportState writes strings.ToLower(email) instead of the import ID
//	  -> caught by STEP 2. ImportStateVerify matches by ID, and the mutation
//	     changed the ID, so imported and created state diverge:
//	     "Failed state verification, resource with ID alice.import@example.com not found"
//
//	MUT: the SHARED apply path normalizes email to the API's echo, so Create and
//	Import agree and only the CONFIG differs - the case ImportStateVerify cannot
//	see, since it compares imported state to created state rather than to config
//	  -> caught by STEP 1, and NOT by my plancheck: Terraform Core's own
//	     post-apply consistency check fires first, because email is Required and
//	     non-Computed and Core forbids returning a value that differs from config:
//	     "produced an unexpected new value: .email: was cty.StringVal(...)"
//
// So step 3 did not catch either mutation I could construct, and I could not
// construct one it uniquely catches - by construction, it cannot. Step 2 runs
// without ImportStatePersist, so it executes in a throwaway working directory
// discarded at the end of that step; step 3's plan is computed against
// whatever CREATE (step 1) left, never against what import recovered. It is
// kept because criterion 1 requires the no-op plan assertion, and because it
// is real (if lesser) insurance that Create's own state stays stable under a
// same-config re-apply - not because it would start covering import if email
// ever became Optional+Computed. Steps 1 and 2 above are this test's only
// import coverage.
//
// The broader gap it guards against IS real elsewhere: 19 of the 23 files in this
// repo using ImportStateVerify stop there, and the invitation resource shipped an
// import/casing defect a passing ImportStateVerify did not see. That resource
// differs in the way that matters - it carries a plan modifier that deliberately
// rewrites the planned value, which Core permits, so Core's consistency check
// does not save it the way it saves this one.
func TestAccOrganizationUserResourceImportRoundTripPreservesCasing(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	// Deliberately mixed case, and deliberately NOT what the backend stores.
	const operatorCasing = "Alice.Import@Example.COM"

	// Seeded as an existing member so Create adopts rather than inviting, and so
	// the import has something real to find. The mock's collaborator lookup is
	// case-insensitive, matching findOrgCollaboratorByEmail's EqualFold.
	httpServer, _ := newMockOrganizationUserOriginServer(t, operatorCasing)

	const resourceName = "anyscale_organization_user.test"
	config := originTestConfig(httpServer.URL, operatorCasing)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// 1. Establish real applied state.
				Config: config,
				Check:  resource.TestCheckResourceAttr(resourceName, "email", operatorCasing),
			},
			{
				// 2. Import by the email the operator typed, casing and all.
				Config:            config,
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateId:     operatorCasing,
				ImportStateVerify: true,
				// Nothing is ignored on purpose. reinvite_if_expired has no backend
				// representation and would normally need the R8 exemption, but
				// ImportState sets it explicitly to false rather than leaving it
				// null, so it round-trips honestly and does not need silencing. If
				// this ever starts failing on that attribute, add the entry WITH a
				// comment - an unexplained entry here is indistinguishable from
				// someone hiding a real failure.
			},
			{
				// 3. Does NOT prove import-recovery correctness - see the
				// throwaway-working-directory note in the doc comment above;
				// this step cannot observe anything step 2's import wrote.
				// What it genuinely proves: against a realistic config,
				// re-applying Create's own state (step 1) is a no-op.
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionNoop),
					},
				},
				Check: resource.TestCheckResourceAttr(resourceName, "email", operatorCasing),
			},
		},
	})
}
