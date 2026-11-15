package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// terraform destroy plans the configuration first, in a pass the provider
// cannot tell apart from an ordinary plan, so any plan-time check that fires
// on unchanged data blocks the destroy of a perfectly good configuration.
//
// For anyscale_cloud_access only the unchanged-member skip of the destroy-gate
// contract applies. The org-admin declaration check itself stays an ERROR:
// the reconcile deliberately treats a refused grant as non-fatal
// (ungranted_members plus a warning, to avoid tainting), so a downgraded
// plan-time warning would be a silent acceptance - an apply that appears to
// succeed while never granting the admin, retried on every plan. Apply
// cannot refuse there, so the plan must.
//
// What the skip actually fixes: a member is applied as an ordinary member,
// then made an organization admin out of band. Destroy's plan pass used to
// look every declared member up again, discover the admin, and error - with
// the skip, an unchanged declaration makes no lookup at all and the destroy
// proceeds. And the skip is what unblocks destroy, not a warning: declaring an
// admin via configuration still errors at plan and can never be applied,
// so no admin entry ever reaches state through this resource.
//
// Mutation-proof (R1): removing the unchanged-member skip from
// refuseDeclaringOrgAdmins turns DestroySurvivesOutOfBandOrgAdmin red - the
// destroy's plan pass looks the promoted member up, reads base_role "owner",
// and errors on a declaration that did not change.
//
// The admin signal is the singular organization-collaborator GET's base_role
// (owner = likely admin); this harness wraps mockCloudAccessServer and serves
// that singular route, which the base mock does not.

const cloudAccessAdminGateEmail = "gate-admin@example.com"

// cloudAccessAdminGateServer is the base mock plus the singular
// organization-collaborator read the admin check issues: base_role "owner"
// for any user promoted out of band, 404 (skipped member) for anyone else,
// and counters of singular reads served and roles writes refused.
type cloudAccessAdminGateServer struct {
	inner  *mockCloudAccessServer
	server *httptest.Server

	mu              sync.Mutex
	admins          map[string]bool
	singularGetHits int
	roleRefusals    int
}

func newCloudAccessAdminGateServer(t *testing.T) *cloudAccessAdminGateServer {
	t.Helper()
	g := &cloudAccessAdminGateServer{
		inner: &mockCloudAccessServer{
			members:     map[string]map[string]any{},
			unrevokable: map[string]string{},
			orgMembers: map[string]struct{ identityID, userID string }{
				"member@example.com":      {identityID: "ide_gate_member", userID: "usr_gate_member"},
				cloudAccessAdminGateEmail: {identityID: "ide_gateadmin", userID: "usr_gateadmin"},
				"creator@example.com":     {identityID: "ide_gate_creator", userID: "usr_gate_creator"},
			},
			callerUserID:         "usr_gate_creator",
			callerEmail:          "creator@example.com",
			projectCollaborators: map[string]map[string]struct{ identityID, permissionLevel string }{},
		},
		admins: map[string]bool{"usr_gateadmin": true},
	}
	g.server = httptest.NewServer(http.HandlerFunc(g.handle))
	t.Cleanup(g.server.Close)
	return g
}

// promoteAdmin makes a user an organization admin out of band - as the real
// console would. The member-search endpoint the resource reads never reports
// org admins, so nothing in the resource's own read path changes; only the
// singular lookup this check issues can see it.
func (g *cloudAccessAdminGateServer) promoteAdmin(userID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.admins[userID] = true
}

func (g *cloudAccessAdminGateServer) isAdmin(userID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.admins[userID]
}

func (g *cloudAccessAdminGateServer) handle(w http.ResponseWriter, r *http.Request) {
	// The roles write refuses an organization admin's role, the way the real
	// API does. Nothing in this harness's tests may reach it: the plan-time
	// check is the only refusal surface for a declaration, and a test that
	// gets this far has already failed its assertion.
	if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v2/clouds/") &&
		strings.Contains(r.URL.Path, "/collaborators/users/") && strings.HasSuffix(r.URL.Path, "/roles") {
		userID := strings.TrimSuffix(
			r.URL.Path[strings.Index(r.URL.Path, "/collaborators/users/")+len("/collaborators/users/"):], "/roles")
		if g.isAdmin(userID) {
			g.mu.Lock()
			g.roleRefusals++
			g.mu.Unlock()
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"error":{"detail":"You cannot change the role of an organization admin."}}`)
			return
		}
	}
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v2/organization_collaborators/") {
		userID := strings.TrimPrefix(r.URL.Path, "/api/v2/organization_collaborators/")
		if g.isAdmin(userID) {
			g.mu.Lock()
			g.singularGetHits++
			g.mu.Unlock()
			identityID := "ide_" + strings.TrimPrefix(userID, "usr_")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"result":{"id":"`+identityID+`","user_id":"`+userID+`","value":`+
				`{"id":"`+userID+`","name":"Org Admin"},"base_role":"owner"}}`)
			return
		}
	}
	g.inner.handle(w, r)
}

func (g *cloudAccessAdminGateServer) takeSingularGetHits() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	h := g.singularGetHits
	g.singularGetHits = 0
	return h
}

func (g *cloudAccessAdminGateServer) takeRoleRefusals() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := g.roleRefusals
	g.roleRefusals = 0
	return n
}

func cloudAccessAdminGateHCL(apiURL string, members ...string) string {
	var b strings.Builder
	for _, e := range members {
		fmt.Fprintf(&b, "    %q = {\n      base_role = \"writer\"\n    }\n", e)
	}
	return testAccProviderBlock(apiURL) + fmt.Sprintf(`
resource "anyscale_cloud_access" "test" {
  cloud_id = %q
  member = {
%s  }
}
`, cloudAccessMockCloudID, b.String())
}

// A member applied as an ordinary user, promoted to organization admin out of
// band, then a normal lifecycle end: the unchanged declaration makes no
// singular lookup in any plan pass (the skip), so the destroy proceeds even
// though a lookup in that pass would read "owner" and error.
func TestAccCloudAccessResource_DestroySurvivesOutOfBandOrgAdmin_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	g := newCloudAccessAdminGateServer(t)

	plain := cloudAccessAdminGateHCL(g.server.URL, "member@example.com")
	run := cliDevWorkspace(t, plain)

	if lines, out, err := run("apply", "-auto-approve", "-json"); err != nil || len(cliDiagnostics(lines, "error")) > 0 {
		t.Fatalf("setup apply failed: %v %v\n%s", err, cliDiagnostics(lines, "error"), out)
	}

	// The member becomes an organization admin out of band, after the state
	// that says otherwise was written. Nothing applied changes.
	g.promoteAdmin("usr_gate_member")
	g.takeSingularGetHits()
	g.takeRoleRefusals()

	// An unchanged re-plan makes no admin lookup at all: the skip. The
	// resource's own standing deprecation warning is always present and is
	// not this check's business.
	expectedWarnings := func(lines []tfApplyJSONLine) []string {
		var out []string
		for _, w := range pmProviderWarnings(lines) {
			if !strings.HasPrefix(w, "warning: Deprecated") {
				out = append(out, w)
			}
		}
		return out
	}
	planLines := pmRunOK(t, run, "plan", "-json")
	if unexpected := expectedWarnings(planLines); len(unexpected) != 0 {
		t.Errorf("an unchanged re-plan must not warn beyond the standing deprecation; got %v", unexpected)
	}
	if got := g.takeSingularGetHits(); got != 0 {
		t.Errorf("an unchanged re-plan made %d singular member reads, want 0", got)
	}

	if lines, out, err := run("destroy", "-auto-approve", "-json"); err != nil || len(cliDiagnostics(lines, "error")) > 0 {
		t.Fatalf("terraform destroy must survive an out-of-band admin promotion; it failed: %v %v\n%s",
			err, cliDiagnostics(lines, "error"), out)
	}
	if got := g.takeSingularGetHits(); got != 0 {
		t.Errorf("destroy's plan pass made %d singular member reads, want 0: without the unchanged-member skip, the promoted member would have blocked the destroy", got)
	}
	if _, stateOut, err := run("state", "list"); err != nil {
		t.Fatalf("terraform state list: %v\n%s", err, stateOut)
	} else if strings.Contains(stateOut, "anyscale_cloud_access.test") {
		t.Errorf("resource still in state after destroy:\n%s", stateOut)
	}
}

// A declaration that CHANGED to name an org admin still errors at plan, with
// no forecast wording: the reconcile cannot refuse at apply (a refused grant
// is recorded in ungranted_members and the apply converges, by design), so a
// downgrade here would be a silent acceptance. The error is the refusal.
func TestAccCloudAccessResource_ChangedAdminDeclarationStillErrors_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	g := newCloudAccessAdminGateServer(t)

	config := cloudAccessAdminGateHCL(g.server.URL, cloudAccessAdminGateEmail)
	run := cliDevWorkspace(t, config)

	lines, out, err := run("plan", "-json")
	if err == nil {
		t.Fatalf("declaring an organization admin must error at plan; it did not:\n%s", out)
	}
	found := false
	for _, l := range lines {
		if l.Type == "diagnostic" && l.Diagnostic.Severity == "error" &&
			l.Diagnostic.Summary == "This Member Appears To Be An Organization Admin" {
			found = true
			if !strings.Contains(l.Diagnostic.Detail, cloudAccessAdminGateEmail) {
				t.Errorf("the error must name the admin; got: %s", l.Diagnostic.Detail)
			}
			for _, forbid := range []string{"If applied, this change fails.", "the destroy itself is not affected"} {
				if strings.Contains(l.Diagnostic.Detail, forbid) {
					t.Errorf("this check deliberately keeps erroring (apply cannot refuse), so no forecast wording belongs here; found %q", forbid)
				}
			}
		}
	}
	if !found {
		t.Errorf("the plan must refuse the admin declaration outright; got: %v\n%s", cliDiagnostics(lines, ""), out)
	}
	// The check really ran (it looked the admin up and read "owner"), and the
	// roles write was never attempted: the same refusal the old apply-path
	// proved, now caught before any write could exist.
	if got := g.takeSingularGetHits(); got == 0 {
		t.Error("the admin check never looked the declared member up; the error came from nowhere")
	}
	if got := g.takeRoleRefusals(); got != 0 {
		t.Errorf("the roles write was attempted (and refused) %d times at plan; plan must refuse before any write", got)
	}
}
