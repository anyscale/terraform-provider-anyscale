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
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccCloudAccessResourceImportRoundTrip is the Criterion 1 import proof
// for anyscale_cloud_access. Originally written AHEAD of the resource on a
// premise that turned out to be BACKWARDS once the real implementation
// existed to check it against, and this comment is the corrected version -
// see the git history for what it used to claim, rather than trusting a
// paraphrase here.
//
// THE RULING (recorded here so nobody "fixes" it back): import
// recovers the FULL visible remote population, unfiltered - including
// members the practitioner never declared anywhere. There is no "declared
// set" for import to filter against in the first place: ImportState sets
// id/cloud_id/allow_empty_member_set/unmanaged_grants/ungranted_members
// directly and never touches `member` at all; the framework's automatic
// post-import Read populates it via cloudAccessMembersToState with an EMPTY
// prior, so every visible member comes back, with no way to distinguish
// "declared" from "not" because nothing has been declared yet.
//
// Filtering import to some subset would be actively unsafe, not merely
// different: state would then claim the cloud holds only the people already
// known about, the plan would show no diff for anyone else, and they would be
// revoked on the next apply with nothing to warn about it - the exact
// invisible-revoke hazard this release's plan-time disclosure work exists to
// close, except on the practitioner who did everything right and imported
// first. Unfiltered import is what makes the plan honest: state holds the
// true population, so a config that omits someone is a real, visible diff you
// see before applying, not a silent future revoke. This matches
// docs/resources/cloud_access.md's own import guidance verbatim ("import
// first, write your configuration to match what came back... terraform plan
// will show you the difference if it doesn't") - that guidance already
// assumed unfiltered import, so no doc change was needed once the code
// matched it.
//
// WHY THIS RESOURCE STILL NEEDS ITS OWN IMPORT PROOF, HARDER THAN MOST.
// anyscale_cloud_access is AUTHORITATIVE over a cloud's ENTIRE member list:
// anyone present on the cloud but absent from `member` is revoked on the next
// apply. So the failure mode this test guards against is the opposite of a
// missing-value bug - it is import recovering TOO LITTLE (silently dropping a
// real member), which would look identical to a correctly filtered import
// right up until the next apply revoked someone nobody meant to touch.
//
// THE ASSERTION LIVES IN STEP 2's ImportStateCheck, and neither neighbouring
// step can substitute for it:
//   - ImportStateVerify is deliberately OFF: it would compare imported state
//     against what Create produced, and those two are SUPPOSED to differ now
//     (Create wrote 2 declared members; import correctly recovers all 3
//     visible ones) - a verify comparing them would fail on the very
//     divergence this test exists to prove is correct.
//   - A trailing no-op plan step cannot see imported state at all.
//     terraform-plugin-testing runs an ImportState step in a THROWAWAY working
//     directory and discards it unless ImportStatePersist is set (its own doc
//     says so; testing_new_import_state.go implements it), so any later step
//     plans against what CREATE left. Do not "fix" that with
//     ImportStatePersist: true either - `terraform import` refuses an address
//     already managed in the same working directory, which it is after step 1.
//
// THE UNDECLARED MEMBER IS KEPT ALIVE DELIBERATELY, and removing that
// machinery makes this file a PLACEBO rather than breaking it. The mock seeds
// cloudAccessMockImplicitMember - a pre-existing cloud-creator/org-owner
// grant this resource never created - and Create is AUTHORITATIVE, so Create
// revokes undeclared members. A plainly-seeded member would be deleted during
// step 1, and every assertion here would STILL PASS, because none of them
// assert the backend still holds it; steps 2 and 3 would then run against a
// backend matching the config exactly, the one shape the constant below warns
// cannot fail this test. So the mock refuses that member's revoke: it
// persists legitimately, the premise survives all three steps, and the
// converge-and-record path (unmanaged_grants) gets exercised too.
//
// SECOND, SEPARATE HAZARD - NOT ASSERTED HERE, AND UNIT-COVERED BUT NOT YET
// ACCEPTANCE-COVERED. A cloud with auto_add_user=true hard-blocks member
// removal: the backend returns 409 CONFLICT, "Users cannot be removed from
// clouds which have auto add users enabled" (product backend
// cloud_collaborators_service.py:540-544). An authoritative resource cannot
// function on such a cloud - it can add but never revoke, which is precisely
// the guarantee it exists to make. The ruling is that anyscale_cloud_access
// must REFUSE on an auto_add_user=true cloud rather than silently degrade to
// add-only, since add-only authority is authority in name only.
//
// This IS implemented (RefuseWhenAutoAddUserEnabled, cloud_access_reconcile.go)
// and unit-covered by TestReconcileCloudAccess_AutoAddUserBlocksRevokes and
// TestReconcileCloudAccess_DestroyDoesNotRefuseOnAutoAddUser, which set the
// flag true on a mock cloud AND make the revoke path 409, per J.9. Unit-level
// is the current ceiling not because of any gate - the write path runs
// through the real resource in this file already - but because nobody has
// written the acceptance test yet. AC-9 requires this refusal proven through
// the real resource; that acceptance coverage is still owed and belongs in
// this file or a sibling, not assumed satisfied by the unit tests above.
//
// No assertion here because this fixture's mock cloud carries
// auto_add_user=false and its revoke path never 409s - a mock that ignores
// auto_add_user would pass happily against a resource that 409s in reality,
// the same mock-omission shape that let the mount_targets bug ship green.
// Whoever writes the AC-9 acceptance test needs a mock cloud with
// auto_add_user=true AND a revoke path that actually 409s, or it proves
// nothing.

const (
	cloudAccessMockCloudID = "cld_cloudaccess_import_mock"
	// cloudAccessMockImplicitMember is on the cloud before Terraform ever runs
	// and is never granted by this resource. It is what makes the backend
	// return more members than the config declares. Do not "tidy" it out of
	// the fixture - a mock whose member list exactly equals the config's is a
	// mock that cannot fail this test.
	// Registered UNREVOKABLE in newMockCloudAccessServer, which is load-bearing
	// rather than decoration - see the header.
	cloudAccessMockImplicitMember = "cloud-owner@example.com"

	// What the mock reports when that member's revoke is attempted.
	//
	// REPRESENTATIVE, NOT GATE-1 CONFIRMED: inducing a genuinely unrevokable
	// member against the real API was not attempted, so neither this text nor
	// the status beside it is verified. No assertion may depend on either -
	// only on the behavioral chain (revoke fails, member stays, recorded in
	// unmanaged_grants).
	cloudAccessMockUnrevokableReason = "role cannot be revoked for this collaborator"
)

type mockCloudAccessServer struct {
	mu sync.Mutex
	// members is the cloud's live collaborator list, keyed by lowercased email.
	members map[string]map[string]any
	// unrevokable maps a lowercased email to the reason its revoke fails.
	// Listed members are never deleted, however often revoke is called - that
	// is what survives an authoritative Create. See the header.
	unrevokable map[string]string
	// orgMembers is the ORGANIZATION's membership, distinct from the cloud's
	// members above - resolveIdentityForEmail (grantCloudAccessMember's first
	// call) resolves against organization membership, not cloud membership,
	// since a cloud role can only be granted to someone already in the org.
	orgMembers map[string]struct{ identityID, userID string }
	// callerUserID/callerEmail back /api/v2/userinfo, which
	// fetchCloudAccessCallerIdentity requires unconditionally before any
	// reconcile - without it every apply fails at "Could Not Identify The
	// Calling Identity" before touching any cloud endpoint at all.
	callerUserID, callerEmail string
	// projectCollaborators is per-project-scoped state: projectID -> lowercased
	// email -> {identity_id, permission_level}. Distinct from members above,
	// which is cloud-scoped. Tracks the real granted permission_level, not a
	// hardcoded value, the same way s.members/s.orgMembers track what was
	// actually written rather than fabricating a constant on read.
	projectCollaborators map[string]map[string]struct{ identityID, permissionLevel string }
}

// newMockCloudAccessServer serves the cloud-collaborator surface
// anyscale_cloud_access will reconcile against.
//
// CORRECTED after a live Gate 1 check against the real backend (both calls
// made against the pinned static fixture cloud, response shapes confirmed
// byte-for-byte, not re-derived from source alone - a prior version of this
// comment pinned four routes as "not guesses" and got two of them wrong,
// which is exactly the failure this note exists to prevent from recurring).
// The real routes (backend/server/api/product/routers/clouds_router.py):
//
//	POST   /api/v2/clouds/{cloud_id}/collaborators/users/search   - list members: paginated,
//	                                                                 returns id/value.{id,name,email}/permission_level
//	                                                                 (coarse: owner/write/readonly only - see below)
//	PUT    /api/v2/clouds/{cloud_id}/collaborators/users/{user_id}/roles - grant/set a user's role
//	DELETE /api/v2/clouds/{cloud_id}/collaborators/{identity_id}         - revoke
//
// NOT what an earlier version of this comment claimed: there is no GET on
// .../collaborators/users (that path is POST-only, for adding one user, not
// listing), and .../collaborators/roles is GET-only (a granular per-user role
// read - this resource will need it for the same reason the list route's
// permission_level is lossy, see below), never a PUT.
//
// PERMISSION_LEVEL ON THE LIST ROUTE IS LOSSY - confirmed live, not just
// theorized: the four reader-tier base roles (collaborator, project_viewer,
// compute_config_viewer, workload_operator) collapse into "write" or
// "readonly" on this endpoint, and the live check surfaced a real instance -
// a collaborator whose true base_role (per GET .../collaborators/roles) is
// "collaborator" shows permission_level "write" here. Whoever builds Read
// needs to decide whether the coarse value is sufficient or whether it must
// join both endpoints by user_id for the granular role - this comment does
// not make that call, it only pins the two routes' real, confirmed shapes.
//
// Pinning them matters more than it looks. A mock that answers any path
// containing "collaborator" will happily serve a resource that calls a route
// the real API does not expose, so the test would go green against code that
// 404s in production - the same shape as the mount_targets mock omission that
// let a real bug ship (see CLAUDE.md, Testing guidance), and the same shape
// this very comment fell into before the live check above corrected it.
// Routing is therefore matched on the real shapes, not a "collaborator"
// substring, and anything else 404s loudly.
//
// Revisit if cloud_access deliberately targets a different api/v2 surface
// than what is pinned above; that would be a design change worth noticing
// here rather than absorbing silently.
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
		// Without this entry, authoritative Create revokes the member above in
		// step 1, the backend collapses to exactly what the config declares,
		// and every assertion here passes while proving nothing. Guarded by
		// TestCloudAccessMockRefusesUnrevokableRevoke.
		unrevokable: map[string]string{
			cloudAccessMockImplicitMember: cloudAccessMockUnrevokableReason,
		},
		// alice and bob must already be organization members - a cloud role can
		// only be granted to an existing org member (grantCloudAccessMember's
		// first call, resolveIdentityForEmail, resolves against org
		// membership, not cloud membership).
		orgMembers: map[string]struct{ identityID, userID string }{
			"alice@example.com": {identityID: "ide_mock_alice", userID: "usr_mock_alice"},
			"bob@example.com":   {identityID: "ide_mock_bob", userID: "usr_mock_bob"},
		},
		// Deliberately a distinct identity from every declared/implicit member
		// above, so the caller-exclusion logic never overlaps with what this
		// test is asserting about alice/bob/the implicit member.
		callerUserID:         "usr_mock_caller",
		callerEmail:          "caller@example.com",
		projectCollaborators: map[string]map[string]struct{ identityID, permissionLevel string }{},
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

	// Pinned to the real routes, live-confirmed (see the comment on
	// newMockCloudAccessServer) - not to any path containing "collaborator".
	collabRoot := strings.HasPrefix(path, "/api/v2/clouds/") && strings.Contains(path, "/collaborators")
	// The grant/write-role route is .../collaborators/users/{user_id}/roles -
	// require the "/users/" segment too, not just a bare "/roles" suffix, so
	// this never matches the real (GET-only) .../collaborators/roles path.
	isRolesWrite := collabRoot && strings.Contains(path, "/users/") && strings.HasSuffix(path, "/roles")

	// The bootstrap membership route is POST .../collaborators/users - no
	// "/search" suffix and no user_id segment, distinct from both the list
	// route (which DOES have "/search") and the roles-write route (which has a
	// user_id and "/roles" suffix).
	isBootstrapMembership := collabRoot && r.Method == http.MethodPost && strings.HasSuffix(path, "/collaborators/users")

	// The granular per-user roles READ is GET .../collaborators/roles - no
	// "/users/" segment at all, which is exactly what distinguishes it from
	// the roles WRITE route (isRolesWrite, above) despite both ending in
	// "/roles". listCloudAccessMembers joins this onto the search results for
	// base_role/deny_roles; without it every member's roles come back empty
	// and base_role lands null on every member - confirmed the hard way, by
	// this exact gap making base_role silently vanish from imported state.
	isRolesRead := collabRoot && r.Method == http.MethodGet && strings.HasSuffix(path, "/collaborators/roles") && !strings.Contains(path, "/users/")

	switch {
	case r.Method == http.MethodGet && path == "/api/v2/userinfo":
		s.handleUserinfo(w)
	case r.Method == http.MethodGet && path == "/api/v2/organization_collaborators":
		s.handleOrgCollaborators(w, r)
	case strings.HasPrefix(path, "/api/v2/projects/"):
		s.handleProjects(w, r)
	case isRolesRead:
		s.handleRolesRead(w)
	case r.Method == http.MethodPost && collabRoot && strings.HasSuffix(path, "/collaborators/users/search"):
		s.handleList(w)
	case isBootstrapMembership:
		s.handleBootstrapMembership(w, r)
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

// handleList serves the live-confirmed CloudCollaborator shape (an "id" that
// is the identity_id for the permission, a nested "value" object carrying the
// user_id/name/email, and "metadata.total" - not "total_count") rather than
// this mock's own flat internal member representation. A response shaped
// like the mock's internal map instead of the real API's would validate a
// Read implementation that parses the wrong fields and 404s/mis-decodes in
// production - the same mock-realism gap this file's own header comment
// warns about.
// handleUserinfo backs fetchCloudAccessCallerIdentity, which every reconcile
// calls unconditionally and first - a missing handler here fails every apply
// at "Could Not Identify The Calling Identity" before any cloud endpoint is
// ever reached, regardless of what else the mock gets right.
func (s *mockCloudAccessServer) handleUserinfo(w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{
		"id":    s.callerUserID,
		"email": s.callerEmail,
	}})
}

// handleOrgCollaborators backs resolveIdentityForEmail, which
// grantCloudAccessMember calls before any cloud-scoped write - a cloud role
// can only be granted to someone who is already an organization member.
// Real shape: OrganizationCollaboratorsListResponse, matching
// listAllOrganizationCollaborators' pagination contract (results + metadata).
func (s *mockCloudAccessServer) handleOrgCollaborators(w http.ResponseWriter, r *http.Request) {
	emailFilter := strings.ToLower(r.URL.Query().Get("email"))
	type result struct {
		ID              string   `json:"id"`
		UserID          *string  `json:"user_id"`
		Email           string   `json:"email"`
		PermissionLevel string   `json:"permission_level"`
		BaseRole        string   `json:"base_role"`
		AdditionalRoles []string `json:"additional_roles"`
	}
	var results []result
	for email, ids := range s.orgMembers {
		if emailFilter != "" && strings.ToLower(email) != emailFilter {
			continue
		}
		userID := ids.userID
		results = append(results, result{
			ID: ids.identityID, UserID: &userID, Email: email,
			PermissionLevel: "collaborator", BaseRole: "collaborator", AdditionalRoles: []string{},
		})
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"results":  results,
		"metadata": map[string]any{"total": len(results), "next_paging_token": nil},
	})
}

// handleBootstrapMembership backs the unconditional bootstrap POST
// grantCloudAccessMember always issues before the roles PUT - see that
// function's own comment for why it runs first and unconditionally. Answers
// the real "already a member" signal
// (cloudAccessAlreadyHasPermissionsSubstring) when the target already has a
// cloud membership row, exactly as the real API does, so the reconcile's
// "tolerate already-a-member" branch is genuinely exercised rather than
// assumed.
func (s *mockCloudAccessServer) handleBootstrapMembership(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email           string `json:"email"`
		PermissionLevel string `json:"permission_level"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)
	email := strings.ToLower(req.Email)

	if _, exists := s.members[email]; exists {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"detail": fmt.Sprintf("%s already has permissions for this cloud", email)})
		return
	}

	ids, ok := s.orgMembers[email]
	userID := "usr_mock_" + strings.NewReplacer("@", "_at_", ".", "_").Replace(email)
	if ok {
		userID = ids.userID
	}
	s.members[email] = map[string]any{
		"email":            email,
		"user_id":          userID,
		"permission_level": req.PermissionLevel,
		"deny_roles":       []string{},
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"result": s.members[email]})
}

// handleRolesRead backs GET /api/v2/clouds/{cloud_id}/collaborators/roles,
// the granular per-user role read listCloudAccessMembers joins onto the
// search results. Real shape: cloudUserRolesListResponse
// ({results: [{user_id, base_roles, deny_roles}], metadata}).
func (s *mockCloudAccessServer) handleRolesRead(w http.ResponseWriter) {
	results := make([]map[string]any, 0, len(s.members))
	for _, m := range s.members {
		denyRoles, _ := m["deny_roles"].([]string)
		results = append(results, map[string]any{
			"user_id":    m["user_id"],
			"base_roles": []string{fmt.Sprint(m["permission_level"])},
			"deny_roles": denyRoles,
		})
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"results":  results,
		"metadata": map[string]any{"next_paging_token": nil},
	})
}

func (s *mockCloudAccessServer) handleList(w http.ResponseWriter) {
	emails := make([]string, 0, len(s.members))
	for email := range s.members {
		emails = append(emails, email)
	}
	sort.Strings(emails) // deterministic ordering across steps
	results := make([]map[string]any, 0, len(emails))
	for _, email := range emails {
		m := s.members[email]
		userID := fmt.Sprint(m["user_id"])
		results = append(results, map[string]any{
			"id": "ide_mock_" + userID,
			"value": map[string]any{
				"id":    userID,
				"name":  m["email"],
				"email": m["email"],
			},
			"permission_level": m["permission_level"],
		})
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"results":  results,
		"metadata": map[string]any{"total": len(results), "next_paging_token": nil},
	})
}

// handleGrant serves the roles PUT, which carries no grantee identifier in
// its body at all - SetCloudRolesRequest is only {base_role, deny_roles}. The
// grantee is the user_id path segment (.../collaborators/users/{user_id}/roles),
// which the caller already established via the bootstrap POST
// (handleBootstrapMembership) before this call ever runs.
func (s *mockCloudAccessServer) handleGrant(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// .../clouds/{cloud_id}/collaborators/users/{user_id}/roles
	userID := parts[len(parts)-2]

	var req struct {
		BaseRole  string   `json:"base_role"`
		DenyRoles []string `json:"deny_roles"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)

	for email, m := range s.members {
		if fmt.Sprint(m["user_id"]) == userID {
			m["permission_level"] = req.BaseRole
			denyRoles := req.DenyRoles
			if denyRoles == nil {
				denyRoles = []string{}
			}
			m["deny_roles"] = denyRoles
			s.members[email] = m
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": m})
			return
		}
	}
	// Loud failure rather than a silent no-op: a roles PUT against a user_id
	// with no prior membership row means the bootstrap step didn't run (or
	// this mock is out of date), and the real implementer must see that.
	w.WriteHeader(http.StatusBadRequest)
	_, _ = fmt.Fprintf(w, `{"detail":"mock has no membership row for user_id %s - the bootstrap POST should have created one first"}`, userID)
}

// handleRevoke deletes by trailing path segment or ?email=, matching either
// the stored email or the stored user_id. A member in s.unrevokable is never
// deleted and answers non-2xx instead - see the header.
//
// NOT the auto_add_user case, which is a separate cloud-wide 409 scoped to its
// own future fixture (second-hazard note in the header). This cloud carries
// auto_add_user=false, and the status below is deliberately not 409 so the two
// cannot be confused.
func (s *mockCloudAccessServer) handleRevoke(w http.ResponseWriter, r *http.Request) {
	key := strings.ToLower(r.URL.Query().Get("email"))
	if key == "" {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		key = strings.ToLower(parts[len(parts)-1])
	}
	for email, member := range s.members {
		// The real identity_id this resource reads off the search response is
		// "ide_mock_" + user_id (see handleList) - match on that form too, not
		// just a bare email or user_id, since revokeCloudAccessMember is always
		// called with the identity_id.
		if email == key || strings.EqualFold(fmt.Sprint(member["user_id"]), key) ||
			strings.EqualFold("ide_mock_"+fmt.Sprint(member["user_id"]), key) {
			if reason, blocked := s.unrevokable[email]; blocked {
				// Status and body are REPRESENTATIVE, not Gate-1 confirmed -
				// see cloudAccessMockUnrevokableReason. Assert the behavioral
				// chain (not deleted, recorded in unmanaged_grants), never
				// this status or this text.
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"detail": reason})
				return
			}
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

// handleProjects backs project-scoped role reconcile
// (cloud_access_projects.go): reading a project's collaborators, granting via
// batch_create, and revoking. Real routes and shapes confirmed against
// cloud_access_projects_test.go's own mock in the provider package.
func (s *mockCloudAccessServer) handleProjects(w http.ResponseWriter, r *http.Request) {
	trailing := strings.TrimPrefix(r.URL.Path, "/api/v2/projects/")
	parts := strings.SplitN(trailing, "/", 2)
	projectID := parts[0]
	rest := ""
	if len(parts) > 1 {
		rest = parts[1]
	}

	switch {
	case rest == "collaborators/users" && r.Method == http.MethodGet:
		results := []map[string]any{}
		for email, c := range s.projectCollaborators[projectID] {
			results = append(results, map[string]any{
				"id":               c.identityID,
				"value":            map[string]any{"id": "usr_" + email, "email": email},
				"permission_level": c.permissionLevel,
			})
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results, "metadata": map[string]any{"next_paging_token": nil}})
	case rest == "collaborators/users/batch_create" && r.Method == http.MethodPost:
		var entries []struct {
			Value struct {
				Email string `json:"email"`
			} `json:"value"`
			PermissionLevel string `json:"permission_level"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &entries)
		if s.projectCollaborators[projectID] == nil {
			s.projectCollaborators[projectID] = map[string]struct{ identityID, permissionLevel string }{}
		}
		for _, e := range entries {
			email := strings.ToLower(e.Value.Email)
			s.projectCollaborators[projectID][email] = struct{ identityID, permissionLevel string }{
				identityID: "ide_mock_" + email, permissionLevel: e.PermissionLevel,
			}
		}
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(rest, "collaborators/") && r.Method == http.MethodDelete:
		identityID := strings.TrimPrefix(rest, "collaborators/")
		for email, c := range s.projectCollaborators[projectID] {
			if c.identityID == identityID {
				delete(s.projectCollaborators[projectID], email)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(rest, "collaborators/") && r.Method == http.MethodPut:
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, `{"detail":"not found"}`, http.StatusNotFound)
	}
}

func TestAccCloudAccessResourceImportRoundTrip(t *testing.T) {
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
				//
				// Create is AUTHORITATIVE, so it also attempts to revoke the
				// undeclared implicit member. The mock refuses, so the apply
				// must CONVERGE (not fail - unmanaged_grants' documented
				// contract) and record it. That assertion is what proves the
				// member is still on the backend going into step 2 rather
				// than leaving it assumed.
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
					// The converge-and-record path. Exactly one entry, naming
					// the member whose revoke failed. `reason` is asserted
					// only as SET, never by value - the mock's text is
					// representative, not a confirmed wire shape.
					resource.TestCheckResourceAttr(resourceName, "unmanaged_grants.#", "1"),
					resource.TestCheckResourceAttr(resourceName, "unmanaged_grants.0.email", cloudAccessMockImplicitMember),
					resource.TestCheckResourceAttrSet(resourceName, "unmanaged_grants.0.reason"),
				),
				// TRUE, not false, and confirmed as the correct expectation rather
				// than assumed: Create itself writes `member` as exactly the
				// declared set (alice, bob), matching the checks above, but
				// resource.Test's own automatic post-apply refresh calls Read,
				// which rebuilds `member` from every VISIBLE remote member -
				// including cloud-owner, since its revoke never actually
				// succeeds and it remains on the backend. That refresh therefore
				// surfaces a real, permanent diff proposing to revoke cloud-owner
				// again, which is correct: "the next apply retries everything
				// still outstanding" is this resource's own documented contract
				// for unmanaged_grants, and an unrevokable member means that
				// retry can never converge. A false expectation here was this
				// test's own bug, not evidence of one in the resource.
				ExpectNonEmptyPlan: true,
			},
			{
				// Step 2: import against a backend holding MORE members than
				// the config declared - the implicit member survived because
				// step 1's revoke of it was refused.
				//
				// THIS STEP CARRIES THE REAL ASSERTION, in ImportStateCheck
				// below: it is the only hook that sees what import actually
				// recovered, since this step's state is discarded when it
				// ends. See the header for why neither ImportStateVerify nor a
				// later plan step can stand in.
				//
				// ImportStateVerify is deliberately OFF, not an oversight: it
				// compares imported state against step 1's Create state, and
				// those two are now SUPPOSED to differ - Create wrote 2
				// members, import correctly recovers all 3 visible ones. A
				// verify comparing them would fail on the very divergence this
				// test exists to prove is correct, not catch a regression.
				ResourceName:  resourceName,
				ImportState:   true,
				ImportStateId: cloudAccessMockCloudID,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported instance state, got %d", len(states))
					}
					attrs := states[0].Attributes

					// See the file header for the ruling this enforces: import
					// recovers the full visible population, unfiltered.
					if got := attrs["member.%"]; got != "3" {
						return fmt.Errorf("imported member.%% = %q, want \"3\" - import must recover every visible member, including the undeclared backend grant (%s), not just what a prior config happened to declare", got, cloudAccessMockImplicitMember)
					}
					for _, want := range []string{"alice@example.com", "bob@example.com", cloudAccessMockImplicitMember} {
						if _, ok := attrs["member."+want+".base_role"]; !ok {
							return fmt.Errorf("imported state is missing member %s", want)
						}
					}
					if got := attrs["member."+cloudAccessMockImplicitMember+".base_role"]; got != "owner" {
						return fmt.Errorf("imported member.%s.base_role = %q, want \"owner\"", cloudAccessMockImplicitMember, got)
					}
					// DELIBERATE GAP: unmanaged_grants is not asserted here.
					// Import performs no revoke, so whether a fresh import
					// reports a pre-existing unrevoked grant is a design
					// question the reconcile has not answered yet. Asserting
					// either way now would be guessing at a shape. Fill this
					// in with the PR-B implementation, not before.
					return nil
				},
			},
			{
				// Step 3: NOT the import assertion. Step 2's state is
				// discarded when step 2 ends, so this plan is computed
				// against what step 1's CREATE left - it cannot see imported
				// state at all (see the header). The import claim belongs to
				// step 2's ImportStateCheck.
				//
				// NOT a no-op, and confirmed as the correct expectation
				// rather than assumed (same lesson as step 1's
				// ExpectNonEmptyPlan): this plan's own refresh calls Read,
				// which reports cloud-owner back into `member` since their
				// revoke never succeeds, and config still doesn't declare
				// them - a real, standing diff proposing to revoke them
				// again. J.12 in the consolidation record: unmanaged_grants
				// is diagnostics only and never influences planning, and
				// every plan re-attempts everything still undone. What this
				// step actually proves is narrower and still real: the
				// SHAPE of that diff is an in-place update (attempting the
				// revoke again), never a replace or a destroy - a resource
				// that turned an unrevokable member into resource
				// replacement would be caught here.
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate),
					},
				},
				ExpectNonEmptyPlan: true,
			},
		},
	})
}
