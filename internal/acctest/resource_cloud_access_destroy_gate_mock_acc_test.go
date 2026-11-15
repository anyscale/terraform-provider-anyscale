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
// cannot tell apart from an ordinary plan; the org-admin plan-time check used
// to error there, blocking destroy. These tests pin the contract for
// anyscale_cloud_access: an unchanged member declaration makes no plan-time
// admin lookup (so an unchanged resource - what nearly every destroy plans -
// skips the check), a declaration that CHANGED to name an org admin forecasts
// the refusal at plan ("If applied, this change fails") and the destroy
// proceeds, while the reconcile's own role write is what refuses an admin at
// apply, with no acceptable mid-apply recovery.
//
// The admin signal is the singular organization-collaborator GET's base_role
// (owner = likely admin); this harness wraps mockCloudAccessServer and serves
// that singular route, which the base mock does not.

const cloudAccessAdminGateEmail = "gate-admin@example.com"

// cloudAccessAdminGateServer is the base mock plus the singular
// organization-collaborator read the admin check issues: base_role "owner"
// for the gate admin, 404 (skipped member) for anyone else, and a counter of
// singular reads served.
type cloudAccessAdminGateServer struct {
	inner  *mockCloudAccessServer
	server *httptest.Server

	mu              sync.Mutex
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
	}
	g.server = httptest.NewServer(http.HandlerFunc(g.handle))
	t.Cleanup(g.server.Close)
	return g
}

func (g *cloudAccessAdminGateServer) handle(w http.ResponseWriter, r *http.Request) {
	// The roles write refuses an organization admin's role, the way the real
	// API does: the plan-time check forecasts it, the apply-path write IS it.
	if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v2/clouds/") &&
		strings.HasSuffix(r.URL.Path, "/collaborators/users/usr_gateadmin/roles") {
		g.mu.Lock()
		g.roleRefusals++
		g.mu.Unlock()
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"error":{"detail":"You cannot change the role of an organization admin."}}`)
		return
	}
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v2/organization_collaborators/") &&
		strings.TrimPrefix(r.URL.Path, "/api/v2/organization_collaborators/") == "usr_gateadmin" {
		g.mu.Lock()
		g.singularGetHits++
		g.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"result":{"id":"ide_gateadmin","user_id":"usr_gateadmin","value":`+
			`{"id":"usr_gateadmin","name":"Gate Admin","email":"`+cloudAccessAdminGateEmail+`"},"base_role":"owner"}}`)
		return
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

// Declare an org admin AFTER a clean apply, then destroy: the pre-destroy
// plan pass sees a changed member map, forecasts the admin refusal as a
// warning, and the destroy proceeds.
func TestAccCloudAccessResource_DestroySurvivesOrgAdminDeclaration_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	g := newCloudAccessAdminGateServer(t)

	plain := cloudAccessAdminGateHCL(g.server.URL, "member@example.com")
	withAdmin := cloudAccessAdminGateHCL(g.server.URL, "member@example.com", cloudAccessAdminGateEmail)
	run, rewrite := cliDevWorkspaceEditable(t, plain)

	if lines, out, err := run("apply", "-auto-approve", "-json"); err != nil || len(cliDiagnostics(lines, "error")) > 0 {
		t.Fatalf("setup apply failed: %v %v\n%s", err, cliDiagnostics(lines, "error"), out)
	}

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

	rewrite(withAdmin)
	planLines = pmRunOK(t, run, "plan", "-json")
	found := false
	for _, l := range planLines {
		if l.Type == "diagnostic" && l.Diagnostic.Severity == "warning" &&
			l.Diagnostic.Summary == "This Member Appears To Be An Organization Admin" {
			found = true
			for _, want := range []string{"If applied, this change fails.", cloudAccessAdminGateEmail, "the destroy itself is not affected"} {
				if !strings.Contains(l.Diagnostic.Detail, want) {
					t.Errorf("forecast detail lacks %q: %s", want, l.Diagnostic.Detail)
				}
			}
		}
	}
	if !found {
		t.Errorf("the plan must forecast the admin refusal; got diagnostics: %v", cliDiagnostics(planLines, ""))
	}

	if lines, out, err := run("destroy", "-auto-approve", "-json"); err != nil || len(cliDiagnostics(lines, "error")) > 0 {
		t.Fatalf("terraform destroy must survive an org-admin plan forecast; it failed: %v %v\n%s",
			err, cliDiagnostics(lines, "error"), out)
	}
	if _, stateOut, err := run("state", "list"); err != nil {
		t.Fatalf("terraform state list: %v\n%s", err, stateOut)
	} else if strings.Contains(stateOut, "anyscale_cloud_access.test") {
		t.Errorf("resource still in state after destroy:\n%s", stateOut)
	}
}

// The apply never accepts an admin declaration silently: the role write is
// refused by the API, the refusal is recorded in ungranted_members with its
// reason, the warning says so, and the grant does not land. This resource
// deliberately converges rather than erroring here - an error after the
// rollback would leave the resource tainted, and the next apply would revoke
// everyone this one granted - so what R3 pins is the recorded refusal, not an
// error exit.
func TestAccCloudAccessResource_ApplyStillRefusesOrgAdmin_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	g := newCloudAccessAdminGateServer(t)

	config := cloudAccessAdminGateHCL(g.server.URL, cloudAccessAdminGateEmail)
	run := cliDevWorkspace(t, config)

	lines, out, err := run("apply", "-auto-approve", "-json")
	if err != nil {
		t.Fatalf("the apply converges on a refused admin grant rather than erroring; it failed: %v\n%s",
			cliDiagnostics(lines, "error"), out)
	}
	found := false
	for _, l := range lines {
		if l.Type == "diagnostic" && l.Diagnostic.Severity == "warning" &&
			strings.Contains(l.Diagnostic.Summary, "Some Members Could Not Be Granted") &&
			strings.Contains(l.Diagnostic.Detail, "ungranted_members") &&
			!strings.Contains(l.Diagnostic.Detail, "If applied") {
			found = true
		}
	}
	if !found {
		t.Errorf("the apply must surface the real refusal (no forecast wording), naming the admin; got: %v\n%s",
			cliDiagnostics(lines, ""), out)
	}
	// The role write was refused by the API itself - the real refusal the
	// forecast pointed at - before the grant could land.
	g.mu.Lock()
	refusals := g.roleRefusals
	g.mu.Unlock()
	if refusals == 0 {
		t.Error("the roles write was never attempted or never refused; the recorded refusal came from nowhere")
	}
}
