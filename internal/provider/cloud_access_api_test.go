package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// These tests cover the identity/roles JOIN that produces a cloud's member
// list. Every case here is one where a plausible-looking implementation
// produces a member list that is WRONG rather than one that errors - which
// matters more for this resource than for most, because its write half revokes
// whoever is not in the list it was given.

// cloudAccessTestClient points a Client at a test server.
func cloudAccessTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &Client{BaseURL: server.URL, Token: "test-token", HTTPClient: server.Client()}
}

// cloudAccessMockBackend serves the two endpoints the join reads. Both bodies
// are the real response envelopes, including the metadata/next_paging_token
// wrapper, so a fixture cannot pass against an implementation that ignores
// pagination.
type cloudAccessMockBackend struct {
	// searchPages is served one page per request, in order. More than one entry
	// exercises real pagination.
	searchPages [][]map[string]interface{}
	rolePages   [][]map[string]interface{}

	searchRequests []*http.Request
	searchBodies   []string
	roleRequests   []*http.Request
}

func (b *cloudAccessMockBackend) handler(t *testing.T, cloudID string) http.Handler {
	t.Helper()
	mux := http.NewServeMux()

	// Registered WITHOUT a trailing slash: a subtree pattern would redirect a
	// POST, and Go's client drops the body on the redirect, so the mock would
	// answer a request that no longer resembles the real one.
	mux.HandleFunc(fmt.Sprintf("/api/v2/clouds/%s/collaborators/users/search", cloudID),
		func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			b.searchRequests = append(b.searchRequests, r)
			b.searchBodies = append(b.searchBodies, string(body))
			writeCloudAccessPage(t, w, b.searchPages, len(b.searchRequests)-1)
		})

	mux.HandleFunc(fmt.Sprintf("/api/v2/clouds/%s/collaborators/roles", cloudID),
		func(w http.ResponseWriter, r *http.Request) {
			b.roleRequests = append(b.roleRequests, r)
			writeCloudAccessPage(t, w, b.rolePages, len(b.roleRequests)-1)
		})

	return mux
}

func writeCloudAccessPage(t *testing.T, w http.ResponseWriter, pages [][]map[string]interface{}, index int) {
	t.Helper()
	results := []map[string]interface{}{}
	if index < len(pages) {
		results = pages[index]
	}
	payload := map[string]interface{}{
		"results":  results,
		"metadata": map[string]interface{}{"next_paging_token": nil},
	}
	if index+1 < len(pages) {
		payload["metadata"] = map[string]interface{}{"next_paging_token": fmt.Sprintf("token-%d", index+1)}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encoding the mock response failed: %v", err)
	}
}

func cloudAccessCollaborator(identityID, userID, email string) map[string]interface{} {
	return map[string]interface{}{
		"id": identityID,
		"value": map[string]interface{}{
			"id":    userID,
			"name":  "Test User",
			"email": email,
		},
		// Present in the fixture precisely BECAUSE the implementation must not read
		// it: this endpoint's permission_level is lossy. A value here that
		// contradicts the roles endpoint below is what proves the join takes roles
		// from the roles endpoint.
		"permission_level": "readonly",
	}
}

func cloudAccessRoles(userID string, baseRoles, denyRoles []string) map[string]interface{} {
	return map[string]interface{}{
		"user_id":    userID,
		"base_roles": baseRoles,
		"deny_roles": denyRoles,
	}
}

func TestListCloudAccessMembers_JoinsIdentityOntoRoles(t *testing.T) {
	const cloudID = "cld_join"

	backend := &cloudAccessMockBackend{
		searchPages: [][]map[string]interface{}{{
			cloudAccessCollaborator("idn_1", "usr_1", "alice@example.com"),
			cloudAccessCollaborator("idn_2", "usr_2", "bob@example.com"),
		}},
		rolePages: [][]map[string]interface{}{{
			cloudAccessRoles("usr_1", []string{"workload_operator"}, []string{"cloud_read_only"}),
			// usr_2 deliberately absent: a member added through the legacy
			// collaborator path has a membership row and no role row.
			// usr_ghost holds roles but is not in the search results - the shape an
			// organization admin takes, since the search endpoint filters admins out.
			cloudAccessRoles("usr_ghost", []string{"owner"}, nil),
		}},
	}

	members, err := listCloudAccessMembers(context.Background(), cloudAccessTestClient(t, backend.handler(t, cloudID)), cloudID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	byEmail := make(map[string]cloudAccessRemoteMember, len(members))
	for _, m := range members {
		byEmail[m.Email] = m
	}

	alice, ok := byEmail["alice@example.com"]
	if !ok {
		t.Fatalf("alice missing from the joined member list: %+v", members)
	}
	// The role must come from the roles endpoint, not the search endpoint's
	// permission_level - which the fixture sets to a contradicting "readonly".
	if len(alice.BaseRoles) != 1 || alice.BaseRoles[0] != "workload_operator" {
		t.Errorf("alice's base roles came out as %v, want [workload_operator]: the search endpoint's permission_level is lossy and must never be the role source", alice.BaseRoles)
	}
	if len(alice.DenyRoles) != 1 || alice.DenyRoles[0] != "cloud_read_only" {
		t.Errorf("alice's deny roles came out as %v, want [cloud_read_only]", alice.DenyRoles)
	}
	if alice.IdentityID != "idn_1" || alice.UserID != "usr_1" {
		t.Errorf("alice's ids came out as identity=%q user=%q, want idn_1/usr_1 - the two are different ids and the revoke path needs the identity one", alice.IdentityID, alice.UserID)
	}

	// A collaborator with no roles row is KEPT, with no roles. Dropping it would
	// hide an existing member from the resource whose job is to know the whole
	// set, and for an authoritative write that means revoking someone who never
	// appeared in the plan.
	bob, ok := byEmail["bob@example.com"]
	if !ok {
		t.Fatalf("bob was dropped because the roles endpoint said nothing about him; a member added through the legacy console path is exactly this shape and must still be visible: %+v", members)
	}
	if len(bob.BaseRoles) != 0 {
		t.Errorf("bob has no roles row, so his base roles must be empty rather than invented: %v", bob.BaseRoles)
	}

	if len(members) != 2 {
		t.Errorf("got %d members, want 2 - a roles entry with no matching collaborator has no email to key it by and must be dropped: %+v", len(members), members)
	}
}

// TestListCloudAccessMembers_PaginatesBothEndpoints is the test that protects
// against the failure mode with the worst consequence in this file: pagination
// on the search endpoint is a split transport (count and paging_token in the URL
// query, filter in the JSON body), and getting it wrong returns a valid first
// page with no error. For an authoritative resource, a silently truncated member
// list means revoking everyone past the page boundary.
func TestListCloudAccessMembers_PaginatesBothEndpoints(t *testing.T) {
	const cloudID = "cld_pages"

	backend := &cloudAccessMockBackend{
		searchPages: [][]map[string]interface{}{
			{cloudAccessCollaborator("idn_1", "usr_1", "page1@example.com")},
			{cloudAccessCollaborator("idn_2", "usr_2", "page2@example.com")},
		},
		rolePages: [][]map[string]interface{}{
			{cloudAccessRoles("usr_1", []string{"writer"}, nil)},
			{cloudAccessRoles("usr_2", []string{"owner"}, nil)},
		},
	}

	members, err := listCloudAccessMembers(context.Background(), cloudAccessTestClient(t, backend.handler(t, cloudID)), cloudID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("got %d members, want 2 - the second page was not fetched: %+v", len(members), members)
	}

	roles := map[string]string{}
	for _, m := range members {
		if len(m.BaseRoles) == 1 {
			roles[m.Email] = m.BaseRoles[0]
		}
	}
	if roles["page2@example.com"] != "owner" {
		t.Errorf("the second page of ROLES was not joined on: %v", roles)
	}

	if len(backend.searchRequests) != 2 {
		t.Fatalf("the search endpoint was called %d times, want 2", len(backend.searchRequests))
	}
	// Pagination in the URL query, not the body: this is the assertion that fails
	// if someone "simplifies" the helper to send count/paging_token in the JSON.
	if got := backend.searchRequests[0].URL.Query().Get("count"); got == "" {
		t.Errorf("the first search request sent no count query parameter (query %q); this endpoint takes pagination in the URL, and sending it in the body returns a truncated list with no error", backend.searchRequests[0].URL.RawQuery)
	}
	if got := backend.searchRequests[1].URL.Query().Get("paging_token"); got != "token-1" {
		t.Errorf("the second search request sent paging_token=%q, want token-1 (query %q)", got, backend.searchRequests[1].URL.RawQuery)
	}
	for i, body := range backend.searchBodies {
		if body == "" {
			t.Errorf("search request %d sent no JSON body; the endpoint is a POST and expects one", i)
		}
	}
}

func TestListCloudAccessMembers_ErrorsOnACollaboratorWithNoEmail(t *testing.T) {
	const cloudID = "cld_noemail"

	backend := &cloudAccessMockBackend{
		searchPages: [][]map[string]interface{}{{cloudAccessCollaborator("idn_1", "usr_1", "")}},
	}

	_, err := listCloudAccessMembers(context.Background(), cloudAccessTestClient(t, backend.handler(t, cloudID)), cloudID)
	if err == nil {
		t.Fatal("expected an error: a member with no email cannot be keyed, matched against config, or revoked, and silently skipping it would understate the member list an authoritative write acts on")
	}
}

// cloudAccessStateMember builds one prior-state member entry.
func cloudAccessStateMember(t *testing.T, baseRole string, denyRoles types.List, projects types.Map) types.Object {
	t.Helper()
	obj, diags := types.ObjectValue(
		map[string]attr.Type{
			"base_role":  types.StringType,
			"deny_roles": types.ListType{ElemType: types.StringType},
			"projects":   types.MapType{ElemType: types.StringType},
		},
		map[string]attr.Value{
			"base_role":  types.StringValue(baseRole),
			"deny_roles": denyRoles,
			"projects":   projects,
		},
	)
	if diags.HasError() {
		t.Fatalf("building the prior-state fixture failed: %v", diags)
	}
	return obj
}

func cloudAccessPriorMap(t *testing.T, entries map[string]attr.Value) types.Map {
	t.Helper()
	m, diags := types.MapValue(cloudAccessMemberType(), entries)
	if diags.HasError() {
		t.Fatalf("building the prior-state map failed: %v", diags)
	}
	return m
}

// cloudAccessStateEntry pulls one member back out of the converted map.
func cloudAccessStateEntry(t *testing.T, ctx context.Context, m types.Map, email string) CloudAccessMemberModel {
	t.Helper()
	entries := make(map[string]CloudAccessMemberModel, len(m.Elements()))
	if diags := m.ElementsAs(ctx, &entries, false); diags.HasError() {
		t.Fatalf("reading the converted member map failed: %v", diags)
	}
	entry, ok := entries[email]
	if !ok {
		t.Fatalf("member %q missing from the converted map (keys: %v)", email, cloudAccessMemberMapKeys(entries))
	}
	return entry
}

func cloudAccessStrPtr(s string) *string { return &s }

func cloudAccessMemberMapKeys(m map[string]CloudAccessMemberModel) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestCloudAccessMembersToState_BaseRoleCount covers the three reachable role
// counts. The distinction that matters: ZERO is an ordinary cloud (a member
// added through the console's legacy path has a membership row and no role row),
// while MORE THAN ONE is a genuine anomaly. Warning on the first would put noise
// on every refresh of a normal cloud; staying silent on the second would hide a
// real one.
//
// Both report base_role as null and neither drops the member - dropping is the
// intuitive reading of "cannot represent this" and it is the dangerous one,
// because an existing member missing from state is invisible to an authoritative
// write.
func TestCloudAccessMembersToState_BaseRoleCount(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name         string
		baseRoles    []string
		wantRole     *string
		wantWarnings int
	}{
		{
			name:      "exactly one is the normal case",
			baseRoles: []string{"writer"},
			wantRole:  cloudAccessStrPtr("writer"),
		},
		{
			name:         "none is ordinary and must not warn",
			baseRoles:    nil,
			wantRole:     nil,
			wantWarnings: 0,
		},
		{
			name:         "more than one warns and reports null",
			baseRoles:    []string{"owner", "writer"},
			wantRole:     nil,
			wantWarnings: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, diags := cloudAccessMembersToState(ctx, []cloudAccessRemoteMember{{
				Email: "member@example.com", UserID: "usr_1", IdentityID: "idn_1", BaseRoles: tc.baseRoles,
			}}, types.MapNull(cloudAccessMemberType()))
			if diags.HasError() {
				t.Fatalf("unexpected error: %v", diags)
			}
			if got.IsNull() || len(got.Elements()) != 1 {
				t.Fatalf("the member was dropped rather than represented; an existing member missing from state is invisible to an authoritative write: %v", got)
			}

			entry := cloudAccessStateEntry(t, ctx, got, "member@example.com")
			if tc.wantRole == nil {
				if !entry.BaseRole.IsNull() {
					t.Errorf("base_role came out as %q, want null - guessing one of the reported roles writes a value nobody chose", entry.BaseRole.ValueString())
				}
			} else if entry.BaseRole.ValueString() != *tc.wantRole {
				t.Errorf("base_role came out as %q, want %q", entry.BaseRole.ValueString(), *tc.wantRole)
			}

			if n := diags.WarningsCount(); n != tc.wantWarnings {
				t.Errorf("got %d warnings, want %d: %v", n, tc.wantWarnings, diags)
			}
		})
	}
}

// TestCloudAccessMembersToState_DenyRolesShape covers the null-vs-empty-list
// distinction. The wire cannot tell "deny_roles was never declared" from
// "deny_roles was declared as []" - both are sent and returned as an empty list
// - so reproducing one shape unconditionally puts a permanent diff against
// whichever the practitioner actually wrote.
func TestCloudAccessMembersToState_DenyRolesShape(t *testing.T) {
	ctx := context.Background()
	const email = "member@example.com"

	emptyList, diags := types.ListValueFrom(ctx, types.StringType, []string{})
	if diags.HasError() {
		t.Fatalf("building the fixture failed: %v", diags)
	}

	for _, tc := range []struct {
		name           string
		remote         []string
		priorDenyRoles types.List
		hasPrior       bool
		wantNull       bool
		wantElements   int
	}{
		{
			name:     "no remote deny roles and no prior state stays null",
			wantNull: true,
		},
		{
			name:           "no remote deny roles with a prior null stays null",
			priorDenyRoles: types.ListNull(types.StringType),
			hasPrior:       true,
			wantNull:       true,
		},
		{
			name:           "no remote deny roles with a prior empty list stays an empty list",
			priorDenyRoles: emptyList,
			hasPrior:       true,
			wantNull:       false,
			wantElements:   0,
		},
		{
			// Real current server state always wins over prior shape.
			name:           "a remote deny role overrides a prior null",
			remote:         []string{cloudAccessReadOnlyDenyRole},
			priorDenyRoles: types.ListNull(types.StringType),
			hasPrior:       true,
			wantNull:       false,
			wantElements:   1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prior := types.MapNull(cloudAccessMemberType())
			if tc.hasPrior {
				prior = cloudAccessPriorMap(t, map[string]attr.Value{
					email: cloudAccessStateMember(t, "writer", tc.priorDenyRoles, types.MapNull(types.StringType)),
				})
			}

			got, convDiags := cloudAccessMembersToState(ctx, []cloudAccessRemoteMember{{
				Email: email, UserID: "usr_1", BaseRoles: []string{"writer"}, DenyRoles: tc.remote,
			}}, prior)
			if convDiags.HasError() {
				t.Fatalf("unexpected error: %v", convDiags)
			}

			entry := cloudAccessStateEntry(t, ctx, got, email)
			if tc.wantNull {
				if !entry.DenyRoles.IsNull() {
					t.Fatalf("deny_roles came out as %v, want null - a config that omitted the attribute would otherwise show a permanent diff", entry.DenyRoles)
				}
				return
			}
			if entry.DenyRoles.IsNull() {
				t.Fatalf("deny_roles came out null, want a list of %d - a config that declared [] would otherwise show a permanent diff", tc.wantElements)
			}
			if n := len(entry.DenyRoles.Elements()); n != tc.wantElements {
				t.Errorf("deny_roles has %d elements, want %d: %v", n, tc.wantElements, entry.DenyRoles)
			}
		})
	}
}

// TestCloudAccessMembersToState_CarriesProjectsAcrossEmailCasing covers two
// things at once, both about prior state rather than the wire.
//
// Project roles are not refreshed - they are carried across from prior state -
// so this is the test that fails if a future change starts building the member
// map from the API alone without also populating projects. That is a real
// hazard: it would silently discard every project grant on the next refresh,
// and for an authoritative write, state that has forgotten a project role is
// state that revokes it.
//
// The prior lookup is also case-INSENSITIVE, because email identity is not
// case-sensitive to Anyscale. An exact-match lookup would treat the API's
// spelling as a new member and the config's as a departed one, so the carried
// values would be lost exactly when the two spellings differ.
func TestCloudAccessMembersToState_CarriesProjectsAcrossEmailCasing(t *testing.T) {
	ctx := context.Background()

	projects, diags := types.MapValueFrom(ctx, types.StringType, map[string]string{"prj_1": "write"})
	if diags.HasError() {
		t.Fatalf("building the fixture failed: %v", diags)
	}

	prior := cloudAccessPriorMap(t, map[string]attr.Value{
		// Prior state's key differs from the API's spelling only in case.
		"Alice@Example.com": cloudAccessStateMember(t, "writer", types.ListNull(types.StringType), projects),
	})

	got, convDiags := cloudAccessMembersToState(ctx, []cloudAccessRemoteMember{{
		Email: "alice@example.com", UserID: "usr_1", BaseRoles: []string{"writer"},
	}}, prior)
	if convDiags.HasError() {
		t.Fatalf("unexpected error: %v", convDiags)
	}

	// Keyed by the API's spelling, so a refreshed state and a freshly imported
	// state are identical rather than depending on which path got there first.
	if _, ok := got.Elements()["alice@example.com"]; !ok {
		t.Fatalf("the member map is not keyed by the API's spelling of the email: %v", got.Elements())
	}

	entry := cloudAccessStateEntry(t, ctx, got, "alice@example.com")
	if entry.Projects.IsNull() {
		t.Fatalf("project roles were dropped. They are not refreshed from the API, so prior state is the only place they exist - losing them here means the next authoritative apply revokes them")
	}
	if n := len(entry.Projects.Elements()); n != 1 {
		t.Errorf("carried %d project roles, want 1: %v", n, entry.Projects)
	}
}

// A member with no prior entry has no project roles to carry, and must land
// null rather than an empty map: projects is plain Optional, and an empty map is
// a value a practitioner can deliberately write.
func TestCloudAccessMembersToState_ProjectsNullWithoutPriorState(t *testing.T) {
	ctx := context.Background()

	got, diags := cloudAccessMembersToState(ctx, []cloudAccessRemoteMember{{
		Email: "new@example.com", UserID: "usr_1", BaseRoles: []string{"writer"},
	}}, types.MapNull(cloudAccessMemberType()))
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}

	entry := cloudAccessStateEntry(t, ctx, got, "new@example.com")
	if !entry.Projects.IsNull() {
		t.Errorf("projects came out as %v, want null", entry.Projects)
	}
}
