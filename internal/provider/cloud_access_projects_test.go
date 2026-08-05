package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These tests cover planProjectRoles, which decides which project roles get
// written and which get revoked. It is pure, so every case here is about the
// DECISION rather than the request - and the decisions are where this resource's
// authority boundary actually lives.

func projectMember(email string, projects map[string]string) cloudAccessDesiredMember {
	return cloudAccessDesiredMember{Email: email, BaseRole: "writer", Projects: projects}
}

func TestPlanProjectRoles(t *testing.T) {
	const (
		email  = "member@example.com"
		folded = "member@example.com"
		userID = "usr_1"
	)
	identity := map[string]cloudAccessRemoteMember{
		folded: {Email: email, UserID: userID, IdentityID: "idn_1"},
	}

	for _, tc := range []struct {
		name        string
		desired     map[string]cloudAccessDesiredMember
		prior       map[string]map[string]string
		current     map[string]map[string]string
		cascaded    map[string]bool
		wantGrants  []cloudAccessProjectGrant
		wantRevokes []cloudAccessProjectGrant
	}{
		{
			name:       "a newly declared role is granted",
			desired:    map[string]cloudAccessDesiredMember{folded: projectMember(email, map[string]string{"prj_1": "write"})},
			current:    map[string]map[string]string{"prj_1": {}},
			wantGrants: []cloudAccessProjectGrant{{ProjectID: "prj_1", Email: email, Level: "write"}},
		},
		{
			// A no-op apply must issue no writes at all. Rewriting an already-correct
			// role is harmless against the API but makes every apply look like it
			// changed something, which is how a real change stops being noticeable.
			name:    "a role already at the declared level is left alone",
			desired: map[string]cloudAccessDesiredMember{folded: projectMember(email, map[string]string{"prj_1": "write"})},
			current: map[string]map[string]string{"prj_1": {userID: "write"}},
		},
		{
			name:       "a role at a different level is rewritten",
			desired:    map[string]cloudAccessDesiredMember{folded: projectMember(email, map[string]string{"prj_1": "write"})},
			current:    map[string]map[string]string{"prj_1": {userID: "readonly"}},
			wantGrants: []cloudAccessProjectGrant{{ProjectID: "prj_1", Email: email, Level: "write"}},
		},
		{
			// The revoke half. Dropping a project from the configuration removes the
			// role - without this, Terraform could grant a project role and never take
			// one away.
			name:        "a previously declared role that was dropped is revoked",
			desired:     map[string]cloudAccessDesiredMember{folded: projectMember(email, map[string]string{})},
			prior:       map[string]map[string]string{folded: {"prj_1": "write"}},
			current:     map[string]map[string]string{"prj_1": {userID: "write"}},
			wantRevokes: []cloudAccessProjectGrant{{ProjectID: "prj_1", Email: folded}},
		},
		{
			// THE AUTHORITY BOUNDARY. A role this resource never declared is not its
			// to remove, even on a project it does manage for somebody else. Revoking
			// it would mean adopting the resource silently took ownership of roles
			// nobody mentioned.
			name:    "a role nobody declared is not revoked even on an in-scope project",
			desired: map[string]cloudAccessDesiredMember{folded: projectMember(email, map[string]string{"prj_1": "write"})},
			current: map[string]map[string]string{"prj_1": {userID: "write", "usr_stranger": "owner"}},
		},
		{
			// THE CASCADE. Removing a cloud collaborator recursively revokes their
			// project permissions, so a member dropped at cloud scope needs no
			// per-project revokes - attempting them produces 404s this resource would
			// have to report as failures against access that is already gone.
			name:     "no project revokes for a member being removed from the cloud",
			desired:  map[string]cloudAccessDesiredMember{},
			prior:    map[string]map[string]string{folded: {"prj_1": "write"}},
			current:  map[string]map[string]string{"prj_1": {userID: "write"}},
			cascaded: map[string]bool{folded: true},
		},
		{
			// The same member dropped from the configuration WITHOUT being removed from
			// the cloud: no cascade happens, so the project revoke must be issued. This
			// is the pair that proves the cascade skip is conditional rather than
			// blanket.
			name:        "a project revoke IS issued when the member stays on the cloud",
			desired:     map[string]cloudAccessDesiredMember{folded: projectMember(email, map[string]string{})},
			prior:       map[string]map[string]string{folded: {"prj_1": "write"}},
			current:     map[string]map[string]string{"prj_1": {userID: "write"}},
			cascaded:    map[string]bool{},
			wantRevokes: []cloudAccessProjectGrant{{ProjectID: "prj_1", Email: folded}},
		},
		{
			// A project that was not read yields a grant rather than a skip: without a
			// current value there is no basis for concluding the role is already right,
			// and re-granting a correct role is harmless where skipping a wrong one is
			// not.
			name:       "an unread project is granted rather than assumed correct",
			desired:    map[string]cloudAccessDesiredMember{folded: projectMember(email, map[string]string{"prj_1": "write"})},
			current:    map[string]map[string]string{},
			wantGrants: []cloudAccessProjectGrant{{ProjectID: "prj_1", Email: email, Level: "write"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := planProjectRoles(tc.desired, tc.prior, tc.current, identity, tc.cascaded)

			assertGrants(t, "grants", got.Grants, tc.wantGrants)
			assertGrants(t, "revokes", got.Revokes, tc.wantRevokes)
		})
	}
}

func assertGrants(t *testing.T, what string, got, want []cloudAccessProjectGrant) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d %s %+v, want %d %+v", len(got), what, got, len(want), want)
	}
	for i := range want {
		if got[i].ProjectID != want[i].ProjectID || got[i].Email != want[i].Email || got[i].Level != want[i].Level {
			t.Errorf("%s[%d] = %+v, want %+v", what, i, got[i], want[i])
		}
	}
}

// The in-scope set is the UNION of config-named and previously-named projects.
// Config alone would make a dropped project unobservable, and therefore
// unrevokable - the read would never look at it, so nothing would ever notice the
// role still exists.
func TestCloudAccessInScopeProjectIDs_UnionOfConfigAndPrior(t *testing.T) {
	desired := map[string]cloudAccessDesiredMember{
		"a@example.com": projectMember("a@example.com", map[string]string{"prj_config": "write"}),
	}
	prior := map[string]map[string]string{
		"a@example.com": {"prj_dropped": "write"},
	}

	got := cloudAccessInScopeProjectIDs(desired, prior)
	if len(got) != 2 {
		t.Fatalf("got %v, want both prj_config and prj_dropped: a project dropped from config must stay in scope or its role can never be observed, let alone removed", got)
	}
	// Sorted, so an identical apply reads the same projects in the same order.
	if got[0] != "prj_config" || got[1] != "prj_dropped" {
		t.Errorf("got %v, want [prj_config prj_dropped] in sorted order", got)
	}
}

// applyProjectRolesTestMock is a minimal backend for applyProjectRoles: the
// batch-create grant endpoint, the per-collaborator PUT/DELETE, and the
// project collaborator listing findProjectCollaboratorIdentity needs.
type applyProjectRolesTestMock struct {
	// collaborators maps projectID -> email -> identityID, the project's
	// current membership.
	collaborators map[string]map[string]string
	failGrantFor  map[string]bool // keyed by email
}

func (m *applyProjectRolesTestMock) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, payload interface{}) {
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Fatalf("encoding mock response failed: %v", err)
		}
	}

	mux.HandleFunc("/api/v2/projects/", func(w http.ResponseWriter, r *http.Request) {
		trailing := strings.TrimPrefix(r.URL.Path, "/api/v2/projects/")
		parts := strings.SplitN(trailing, "/", 2)
		projectID := parts[0]
		rest := ""
		if len(parts) > 1 {
			rest = parts[1]
		}

		switch {
		case rest == "collaborators/users/batch_create" && r.Method == http.MethodPost:
			var entries []projectCollaboratorEntry
			if err := json.NewDecoder(r.Body).Decode(&entries); err != nil {
				t.Fatalf("undecodable batch_create body: %v", err)
			}
			for _, e := range entries {
				if m.failGrantFor[e.Value.Email] {
					w.WriteHeader(http.StatusInternalServerError)
					writeJSON(w, map[string]interface{}{"error": map[string]interface{}{"detail": "grant exploded"}})
					return
				}
				if m.collaborators[projectID] == nil {
					m.collaborators[projectID] = map[string]string{}
				}
				m.collaborators[projectID][e.Value.Email] = "idn_" + e.Value.Email
			}
			w.WriteHeader(http.StatusNoContent)
		case rest == "collaborators/users" && r.Method == http.MethodGet:
			results := []map[string]interface{}{}
			for email, id := range m.collaborators[projectID] {
				results = append(results, map[string]interface{}{
					"id": id, "value": map[string]interface{}{"id": "usr_" + email, "email": email},
					"permission_level": "write",
				})
			}
			writeJSON(w, map[string]interface{}{"results": results, "metadata": map[string]interface{}{"next_paging_token": nil}})
		case strings.HasPrefix(rest, "collaborators/") && r.Method == http.MethodDelete:
			identityID := strings.TrimPrefix(rest, "collaborators/")
			for email, id := range m.collaborators[projectID] {
				if id == identityID {
					delete(m.collaborators[projectID], email)
				}
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	return mux
}

// TestApplyProjectRoles_GrantFailureSkipsRevokesButNotOtherGrants covers the
// project-scope half of AC-35, the same rule cloud_access_reconcile.go follows
// for cloud-scope grants: continue attempting every grant, but skip every
// revoke - recording it, not silently dropping it - once any grant in this
// pass has failed.
func TestApplyProjectRoles_GrantFailureSkipsRevokesButNotOtherGrants(t *testing.T) {
	mock := &applyProjectRolesTestMock{
		collaborators: map[string]map[string]string{
			"prj_1": {"toremove@example.com": "idn_toremove"},
		},
		failGrantFor: map[string]bool{"alice@example.com": true},
	}
	server := httptest.NewServer(mock.handler(t))
	t.Cleanup(server.Close)
	client := &Client{BaseURL: server.URL, Token: "test-token", HTTPClient: server.Client()}

	plan := cloudAccessProjectPlan{
		Grants: []cloudAccessProjectGrant{
			{ProjectID: "prj_1", Email: "alice@example.com", Level: "write"},
			{ProjectID: "prj_1", Email: "bob@example.com", Level: "write"},
		},
		Revokes: []cloudAccessProjectGrant{
			{ProjectID: "prj_1", Email: "toremove@example.com"},
		},
	}
	identityByFoldedEmail := map[string]cloudAccessRemoteMember{
		"toremove@example.com": {Email: "toremove@example.com", UserID: "usr_toremove@example.com"},
	}

	unmanaged, ungranted, diags := applyProjectRoles(context.Background(), client, plan, identityByFoldedEmail, false)
	if diags.HasError() {
		t.Fatalf("a project grant failure must not error: %v", diags)
	}
	if len(ungranted) != 1 || ungranted[0].Email.ValueString() != "alice@example.com" {
		t.Fatalf("expected alice@example.com in ungranted, got: %+v", ungranted)
	}
	if _, ok := mock.collaborators["prj_1"]["bob@example.com"]; !ok {
		t.Error("bob's grant was not attempted after alice's failed")
	}
	if _, ok := mock.collaborators["prj_1"]["toremove@example.com"]; !ok {
		t.Error("the revoke ran despite a grant failure - the destructive half must be suppressed")
	}
	foundSkip := false
	for _, u := range unmanaged {
		if u.Email.ValueString() == "toremove@example.com" && strings.Contains(u.Reason.ValueString(), "skipped") {
			foundSkip = true
		}
	}
	if !foundSkip {
		t.Errorf("expected toremove@example.com recorded in unmanaged with a skipped reason: %+v", unmanaged)
	}
}

// TestApplyProjectRoles_PriorCloudGrantFailureSkipsProjectRevokesToo covers the
// cross-phase half: a CLOUD-scope grant failure earlier in the same reconcile
// must suppress PROJECT-scope revokes too, even when every project grant in
// this pass succeeds - a grant failure anywhere suppresses every destructive
// action in the apply, not only the ones downstream of its own phase.
func TestApplyProjectRoles_PriorCloudGrantFailureSkipsProjectRevokesToo(t *testing.T) {
	mock := &applyProjectRolesTestMock{
		collaborators: map[string]map[string]string{
			"prj_1": {"toremove@example.com": "idn_toremove"},
		},
	}
	server := httptest.NewServer(mock.handler(t))
	t.Cleanup(server.Close)
	client := &Client{BaseURL: server.URL, Token: "test-token", HTTPClient: server.Client()}

	plan := cloudAccessProjectPlan{
		Revokes: []cloudAccessProjectGrant{{ProjectID: "prj_1", Email: "toremove@example.com"}},
	}
	identityByFoldedEmail := map[string]cloudAccessRemoteMember{
		"toremove@example.com": {Email: "toremove@example.com", UserID: "usr_toremove@example.com"},
	}

	unmanaged, ungranted, diags := applyProjectRoles(context.Background(), client, plan, identityByFoldedEmail, true)
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if len(ungranted) != 0 {
		t.Errorf("expected no ungranted entries from this pass alone: %+v", ungranted)
	}
	if _, ok := mock.collaborators["prj_1"]["toremove@example.com"]; !ok {
		t.Error("the project revoke ran despite a prior cloud-scope grant failure")
	}
	if len(unmanaged) != 1 || !strings.Contains(unmanaged[0].Reason.ValueString(), "skipped") {
		t.Errorf("expected the revoke recorded as skipped: %+v", unmanaged)
	}
}

// TestCloudAccessUnplannedProjectShortfall covers the safe fallback when the
// project pass could not even be planned (a read failure before
// planProjectRoles could run, most likely J.13's own timeout firing after
// cloud-scope grants already succeeded). It must over-report rather than
// under-report: every declared project role becomes ungranted, and every
// previously-declared-but-now-dropped one becomes unmanaged, with no overlap
// between the two channels for the same (email, project) pair.
func TestCloudAccessUnplannedProjectShortfall(t *testing.T) {
	desired := map[string]cloudAccessDesiredMember{
		"alice@example.com": projectMember("alice@example.com", map[string]string{"prj_keep": "write"}),
	}
	prior := map[string]map[string]string{
		"alice@example.com": {"prj_keep": "write", "prj_dropped": "write"},
		"bob@example.com":   {"prj_bob": "readonly"},
	}
	cascaded := map[string]bool{"bob@example.com": true}

	unmanaged, ungranted := cloudAccessUnplannedProjectShortfall(desired, prior, cascaded, "read timed out")

	if len(ungranted) != 1 || ungranted[0].Email.ValueString() != "alice@example.com" ||
		ungranted[0].ProjectID.ValueString() != "prj_keep" {
		t.Fatalf("expected exactly one ungranted entry (alice/prj_keep), got: %+v", ungranted)
	}
	if len(unmanaged) != 1 || unmanaged[0].Email.ValueString() != "alice@example.com" ||
		unmanaged[0].ProjectID.ValueString() != "prj_dropped" {
		t.Fatalf("expected exactly one unmanaged entry (alice/prj_dropped), got: %+v", unmanaged)
	}
	// bob is cascaded (his cloud-scope revoke already handles his project roles),
	// so he must appear in NEITHER channel.
	for _, e := range append(append([]CloudAccessUnmanagedGrantModel{}, unmanaged...), ungranted...) {
		if e.Email.ValueString() == "bob@example.com" {
			t.Errorf("cascaded member bob@example.com must not appear in either channel: %+v / %+v", unmanaged, ungranted)
		}
	}
	if !strings.Contains(ungranted[0].Reason.ValueString(), "read timed out") {
		t.Errorf("expected the reason to be carried through: %s", ungranted[0].Reason.ValueString())
	}
}
