package provider

import (
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
