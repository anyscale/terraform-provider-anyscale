package acctest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// TestAccCloudAccessResourceImportRoundTrip is the Criterion 1 import proof for
// anyscale_cloud_access, written AHEAD of the resource per the
// before-the-reconcile ruling. It is skipped unconditionally today: all four
// CRUD methods return "Resource Not Implemented"
// (resource_cloud_access.go:258-272), there is no ImportState method, and the
// resource is not registered in provider.go. Delete the t.Skip on the first
// line the moment those three things land - everything below it is written as
// if the resource worked.
//
// WHY THIS RESOURCE NEEDS ITS OWN IMPORT PROOF, HARDER THAN ANY OTHER.
// anyscale_cloud_access is AUTHORITATIVE over a cloud's ENTIRE member list:
// anyone present on the cloud but absent from `member` is revoked on the next
// apply. That inverts the usual import hazard. Everywhere else in this
// provider, an import that fails to recover a value produces a spurious
// replace (see resource_cloud_import_storage_acc_test.go). Here, an import
// that recovers TOO MUCH - members the configuration does not declare - lands
// state whose next plan proposes REVOKING REAL HUMANS' ACCESS to a real cloud.
// The blast radius of getting import wrong on this resource is people locked
// out, not infrastructure churn.
//
// WHY ImportStateVerify ALONE CANNOT CATCH THAT. This is the trap, and it is
// why the weak "just add an ImportState step" form is not acceptable here.
// ImportStateVerify compares the imported state against the state the
// PRECEDING Create step produced. If Create and ImportState share the same
// wrong behavior - both slurping the full backend collaborator list into
// `member` - then both sides carry the same backend-echoed values and
// ImportStateVerify passes, green, having compared a bug to itself. The only
// step that can see the problem is the one that brings the CONFIGURATION back
// into the comparison: step 3's no-op plan assertion. Config declares two
// members, state carries three, and the plan proposes a revoke. That is the
// bar, and step 3 is the only thing enforcing it.
//
// The mock is built to make that failure reachable: its collaborator list
// ALWAYS contains cloudAccessMockImplicitMember, a grant that exists on the
// cloud before Terraform ever touches it (a cloud creator / org-owner grant,
// the ordinary real-world case) and that this resource did not create. So the
// backend legitimately returns MORE members than the config declares, in every
// step, without any out-of-band mutation between steps.
//
// SECOND, SEPARATE HAZARD - NOT ASSERTED HERE, AND A FUTURE TEST MUST COVER IT.
// A cloud with auto_add_user=true hard-blocks member removal: the backend
// returns 409 CONFLICT, "Users cannot be removed from clouds which have auto
// add users enabled" (product backend cloud_collaborators_service.py:540-544).
// An authoritative resource cannot function on such a cloud - it can add but
// never revoke, which is precisely the guarantee it exists to make.
// anyscale_cloud_access must REFUSE on an auto_add_user=true cloud rather
// than silently degrade to add-only, since add-only authority is authority
// in name only. No assertion here because the behavior is not implemented
// yet. Note carefully for whoever writes that test: this mock's cloud
// object carries auto_add_user=false and its revoke path never 409s, and a
// mock that IGNORES auto_add_user would pass happily against a resource
// that 409s in reality - the same mock-omission shape that let the
// mount_targets bug ship green. The auto_add_user test must set the flag
// true on the cloud AND make the revoke path 409, or it proves nothing.

const (
	cloudAccessMockCloudID = "cld_cloudaccess_import_mock"
	// cloudAccessMockImplicitMember is on the cloud before Terraform ever runs
	// and is never granted by this resource. It is what makes the backend
	// return more members than the config declares. Do not "tidy" it out of
	// the fixture - a mock whose member list exactly equals the config's is a
	// mock that cannot fail this test.
	cloudAccessMockImplicitMember = "cloud-owner@example.com"
)

type mockCloudAccessServer struct {
	mu sync.Mutex
	// members is the cloud's live collaborator list, keyed by lowercased email.
	members map[string]map[string]any
}

// newMockCloudAccessServer serves the cloud-collaborator surface
// anyscale_cloud_access will reconcile against.
//
// The routes below are NOT guesses. They are the ones anyscale_cloud_user_role
// - the shipped, working resource cloud_access replaces - already calls against
// the real backend (resource_cloud_user_role.go):
//
//	GET    /api/v2/clouds/{cloud_id}/collaborators/users
//	PUT    /api/v2/clouds/{cloud_id}/collaborators/users/{user_id}/roles
//	PUT    /api/v2/clouds/{cloud_id}/collaborators/roles
//	DELETE /api/v2/clouds/{cloud_id}/collaborators/{identity_id}
//
// Pinning them matters more than it looks. A mock that answers any path
// containing "collaborator" will happily serve a resource that calls a route
// the real API does not expose, so the test would go green against code that
// 404s in production - the same shape as the mount_targets mock omission that
// let a real bug ship (see CLAUDE.md, Testing guidance). Routing is therefore
// prefix-matched on the real shapes, and anything else 404s loudly.
//
// Revisit only if cloud_access deliberately targets a different api/v2 surface
// than cloud_user_role does; that would be a design change worth noticing here
// rather than absorbing silently.
func newMockCloudAccessServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := &mockCloudAccessServer{
		members: map[string]map[string]any{
			cloudAccessMockImplicitMember: {
				"email":            cloudAccessMockImplicitMember,
				"user_id":          "usr_mock_cloud_owner",
				"permission_level": "owner",
				"deny_roles":       []string{},
			},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(server.Close)
	return server
}

func (s *mockCloudAccessServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	path := r.URL.Path

	// Pinned to the real routes cloud_user_role already uses against the live
	// backend, not to any path containing "collaborator". See the comment on
	// newMockCloudAccessServer for why the permissive version is unsafe.
	collabRoot := strings.HasPrefix(path, "/api/v2/clouds/") && strings.Contains(path, "/collaborators")
	isRolesWrite := collabRoot && strings.HasSuffix(path, "/roles")

	switch {
	case r.Method == http.MethodGet && collabRoot && strings.HasSuffix(path, "/collaborators/users"):
		s.handleList(w)
	case isRolesWrite && r.Method == http.MethodPut:
		s.handleGrant(w, r)
	case r.Method == http.MethodDelete && collabRoot:
		s.handleRevoke(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/v2/clouds/"):
		s.handleGetCloud(w, path)
	default:
		// Hard 404, not a permissive echo: an unexpected call from a
		// half-wired reconcile must fail loudly rather than be absorbed.
		http.Error(w, `{"detail":"not found"}`, http.StatusNotFound)
	}
}

func (s *mockCloudAccessServer) handleList(w http.ResponseWriter) {
	emails := make([]string, 0, len(s.members))
	for email := range s.members {
		emails = append(emails, email)
	}
	sort.Strings(emails) // deterministic ordering across steps
	results := make([]map[string]any, 0, len(emails))
	for _, email := range emails {
		results = append(results, s.members[email])
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"results":  results,
		"metadata": map[string]any{"total_count": len(results)},
	})
}

func (s *mockCloudAccessServer) handleGrant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email           string   `json:"email"`
		Identity        string   `json:"identity"`
		Role            string   `json:"role"`
		PermissionLevel string   `json:"permission_level"`
		DenyRoles       []string `json:"deny_roles"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)

	email := strings.ToLower(req.Email)
	if email == "" {
		email = strings.ToLower(req.Identity)
	}
	if email == "" {
		// Loud failure rather than a silent no-op: if the real request body
		// names the grantee differently, this mock is out of date and the
		// implementer must see that, not get a green test.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":"mock could not find a grantee email in the grant request body"}`))
		return
	}
	role := req.PermissionLevel
	if role == "" {
		role = req.Role
	}
	denyRoles := req.DenyRoles
	if denyRoles == nil {
		denyRoles = []string{}
	}
	member := map[string]any{
		"email":            email,
		"user_id":          "usr_mock_" + strings.NewReplacer("@", "_at_", ".", "_").Replace(email),
		"permission_level": role,
		"deny_roles":       denyRoles,
	}
	s.members[email] = member

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"result": member})
}

// handleRevoke deletes by trailing path segment or ?email=, matching either
// the stored email or the stored user_id. auto_add_user is false on this
// mock's cloud, so this path never 409s - see the second-hazard note in the
// file header before reusing this fixture for the auto_add_user case.
func (s *mockCloudAccessServer) handleRevoke(w http.ResponseWriter, r *http.Request) {
	key := strings.ToLower(r.URL.Query().Get("email"))
	if key == "" {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		key = strings.ToLower(parts[len(parts)-1])
	}
	for email, member := range s.members {
		if email == key || strings.EqualFold(fmt.Sprint(member["user_id"]), key) {
			delete(s.members, email)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"detail":"collaborator not found"}`))
}

func (s *mockCloudAccessServer) handleGetCloud(w http.ResponseWriter, path string) {
	id := strings.SplitN(strings.TrimPrefix(path, "/api/v2/clouds/"), "/", 2)[0]
	if id != cloudAccessMockCloudID {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Cloud not found"}`))
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{
		"id":       id,
		"name":     "cloudaccess-import-mock",
		"provider": "AWS",
		"region":   "us-east-2",
		"state":    "ACTIVE",
		"status":   "ready",
		// FALSE deliberately. A true value hard-blocks every revoke with a 409
		// (see the second-hazard note in the file header); this test covers the
		// ordinary cloud, and the auto_add_user refusal needs its own fixture.
		"auto_add_user": false,
	}})
}

func TestAccCloudAccessResourceImportRoundTrip(t *testing.T) {
	t.Skip("anyscale_cloud_access is not yet registered or implemented (CRUD returns Not Implemented; no ImportState). This test is written ahead of that work per the before-the-reconcile ruling and will run as soon as the resource is wired.")

	SkipIfNotAcceptanceTest(t)

	server := newMockCloudAccessServer(t)
	resourceName := "anyscale_cloud_access.test"

	// Two members declared, explicitly and completely. The backend also carries
	// cloudAccessMockImplicitMember, which this config deliberately does NOT
	// declare - that gap is the whole experiment.
	//
	// allow_empty_member_set is intentionally left unset (schema Default:
	// false). It must not be set to true here: import can never recover it, so
	// it always lands false, and a config asking for true would make step 3 a
	// real false -> true update rather than the no-op this test asserts. That
	// documented diff is a separate case needing its own test.
	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_cloud_access" "test" {
  cloud_id = %[1]q

  member = {
    "alice@example.com" = {
      base_role = "writer"
    }
    "bob@example.com" = {
      base_role  = "collaborator"
      deny_roles = ["cloud_read_only"]
      projects = {
        "prj_cloudaccess_mock" = "readonly"
      }
    }
  }
}
`, cloudAccessMockCloudID)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Step 1: create the member set the config declares, and
				// establish the real applied state that step 2 imports against.
				// member.% must be 2 - the implicit backend member is not this
				// resource's to hold in its authoritative map.
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "cloud_id", cloudAccessMockCloudID),
					resource.TestCheckResourceAttr(resourceName, "allow_empty_member_set", "false"),
					resource.TestCheckResourceAttr(resourceName, "member.%", "2"),
					resource.TestCheckResourceAttr(resourceName, "member.alice@example.com.base_role", "writer"),
					resource.TestCheckResourceAttr(resourceName, "member.bob@example.com.base_role", "collaborator"),
					resource.TestCheckResourceAttr(resourceName, "member.bob@example.com.deny_roles.0", "cloud_read_only"),
					resource.TestCheckResourceAttr(resourceName, "member.bob@example.com.projects.prj_cloudaccess_mock", "readonly"),
					resource.TestCheckNoResourceAttr(resourceName, "member."+cloudAccessMockImplicitMember+".base_role"),
				),
				ExpectNonEmptyPlan: false,
			},
			{
				// Step 2: import against a backend whose collaborator list has
				// MORE members than the config declared. Necessary but NOT
				// sufficient - see the file header: if Create and ImportState
				// are wrong the same way, this step compares a bug to itself
				// and passes. It is here to catch the asymmetric failure (one
				// path recovers a field the other leaves null), which is the
				// only class it can catch.
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateId:     cloudAccessMockCloudID,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					// R8 exemption, and the only permitted entry in this list.
					// allow_empty_member_set has no backend representation at
					// all - it is a provider-side safety interlock carrying a
					// schema Default - so import can never recover it and it
					// always lands at the default. Every other attribute,
					// `member` above all, must round-trip exactly; adding one
					// here would silence the thing this test exists to prove.
					"allow_empty_member_set",
				},
			},
			{
				// Step 3: THE assertion. Re-plan the same configuration against
				// the imported state and require a no-op. This is the only step
				// that puts the configuration back into the comparison, and so
				// the only one that can see state carrying a member the config
				// never declared. Any non-noop action here means the next real
				// `terraform apply` after an import proposes revoking a real
				// person's access to a real cloud.
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
}
