package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// anyscale_cloud_access in an organization that has converted to role
// bindings. Every legacy permission write there answers 409 with
// cloudAccessConvertedDetail; the legacy reads keep answering with the rows
// frozen at conversion time.
//
// The mock below wraps mockCloudAccessServer (resource_cloud_access_import_acc_test.go)
// rather than editing it: one switch turns every legacy cloud/project
// collaborator write into the converted 409, and every write is counted in
// either mode so a test can pin how many the provider attempted.

const (
	cloudAccessConvertedDetail = "This organization uses role bindings. Manage access in Users & IAM or with the role bindings API."
	cloudAccessConvertedCloud  = cloudAccessMockCloudID
)

var (
	cloudAccessConvertedSummary = regexp.MustCompile(`Organization Uses Role Bindings`)
	// The detail must route the practitioner to the replacement surface. \s+
	// between words because Terraform wraps diagnostic detail lines.
	cloudAccessConvertedDetailWants = []*regexp.Regexp{
		regexp.MustCompile(`anyscale_role_binding`),
		regexp.MustCompile(`user_group_id`),
		regexp.MustCompile(`\buser_id\b`),
		regexp.MustCompile(`rbac\.md#organization-access-mode`),
	}
)

type cloudAccessConvertedMock struct {
	inner  *mockCloudAccessServer
	server *httptest.Server

	mu        sync.Mutex
	converted bool
	// convertedRolesOnly refuses only the roles PUT, as if the organization
	// converted between a member's membership POST and its role write.
	convertedRolesOnly bool
	writes             []string
}

func newCloudAccessConvertedMock(t *testing.T) *cloudAccessConvertedMock {
	t.Helper()
	m := &cloudAccessConvertedMock{
		inner: &mockCloudAccessServer{
			// No pre-existing cloud members: nothing undeclared to revoke, so
			// the only writes an apply makes are the declared members' grants.
			members:     map[string]map[string]any{},
			unrevokable: map[string]string{},
			orgMembers: map[string]struct{ identityID, userID string }{
				"alice@example.com": {identityID: "ide_mock_alice", userID: "usr_mock_alice"},
				"bob@example.com":   {identityID: "ide_mock_bob", userID: "usr_mock_bob"},
				"carol@example.com": {identityID: "ide_mock_carol", userID: "usr_mock_carol"},
			},
			callerUserID:         "usr_mock_caller",
			callerEmail:          "caller@example.com",
			projectCollaborators: map[string]map[string]struct{ identityID, permissionLevel string }{},
		},
	}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.server.Close)
	return m
}

// isLegacyWrite matches exactly the legacy permission writes anyscale_cloud_access
// issues. Reads - including POST .../collaborators/users/search - are not writes.
func isLegacyWrite(r *http.Request) bool {
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/api/v2/clouds/") && strings.Contains(p, "/collaborators"):
		switch r.Method {
		case http.MethodPost:
			return strings.HasSuffix(p, "/collaborators/users")
		case http.MethodPut:
			return strings.Contains(p, "/collaborators/users/") && strings.HasSuffix(p, "/roles")
		case http.MethodDelete:
			return true
		}
	case strings.HasPrefix(p, "/api/v2/projects/") && strings.Contains(p, "/collaborators"):
		switch r.Method {
		case http.MethodPost:
			return strings.HasSuffix(p, "/collaborators/users/batch_create")
		case http.MethodPut, http.MethodDelete:
			return true
		}
	}
	return false
}

func (m *cloudAccessConvertedMock) handle(w http.ResponseWriter, r *http.Request) {
	if isLegacyWrite(r) {
		m.mu.Lock()
		m.writes = append(m.writes, r.Method+" "+r.URL.Path)
		converted := m.converted ||
			(m.convertedRolesOnly && r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/roles"))
		m.mu.Unlock()
		if converted {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"detail": cloudAccessConvertedDetail})
			return
		}
	}
	m.inner.handle(w, r)
}

func (m *cloudAccessConvertedMock) setConverted(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.converted = v
}

// takeWrites returns the writes recorded since the last call and resets the log.
func (m *cloudAccessConvertedMock) setConvertedRolesOnly(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.convertedRolesOnly = v
}

func (m *cloudAccessConvertedMock) takeWrites() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.writes
	m.writes = nil
	return w
}

func (m *cloudAccessConvertedMock) memberEmails() []string {
	m.inner.mu.Lock()
	defer m.inner.mu.Unlock()
	var out []string
	for e := range m.inner.members {
		out = append(out, e)
	}
	return out
}

// cloudAccessConvertedHCL declares the given members (writer role); carol, if
// present, also carries a project role so the project phase has work to do.
func cloudAccessConvertedHCL(allowEmpty bool, emails ...string) string {
	var b strings.Builder
	for _, e := range emails {
		if e == "carol@example.com" {
			fmt.Fprintf(&b, "    %q = {\n      base_role = \"writer\"\n      projects = {\n        \"prj_converted_mock\" = \"readonly\"\n      }\n    }\n", e)
			continue
		}
		fmt.Fprintf(&b, "    %q = {\n      base_role = \"writer\"\n    }\n", e)
	}
	allow := ""
	if allowEmpty {
		allow = "  allow_empty_member_set = true\n"
	}
	return fmt.Sprintf(`
resource "anyscale_cloud_access" "test" {
  cloud_id = %[1]q
%[2]s
  member = {
%[3]s  }
}
`, cloudAccessConvertedCloud, allow, b.String())
}

// Create in a converted org: the first grant write answers 409 converted and
// the provider must stop there - no further grants, no revokes, no project
// writes - fail with the dedicated diagnostic, and leave nothing in state.
func TestAccCloudAccessResource_ConvertedOrgCreateAbortsAfterOneWrite(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newCloudAccessConvertedMock(t)
	m.setConverted(true)
	resourceName := "anyscale_cloud_access.test"
	config := testAccProviderBlock(m.server.URL) +
		cloudAccessConvertedHCL(false, "alice@example.com", "bob@example.com", "carol@example.com")

	check, assertCalled := rbErrorCheck(t, cloudAccessConvertedSummary)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		ErrorCheck: func(err error) error {
			if err := check(err); err != nil {
				return err
			}
			for _, want := range cloudAccessConvertedDetailWants {
				if !want.MatchString(err.Error()) {
					return fmt.Errorf("converted-org diagnostic does not mention %s: %w", want, err)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config},
			{
				// Exactly one write: the first grant, refused. Anything more means
				// the grant loop kept going or reached the revoke/project phase.
				PreConfig: func() {
					if w := m.takeWrites(); len(w) != 1 {
						t.Errorf("converted org: want exactly 1 legacy write before aborting, got %d: %v", len(w), w)
					}
				},
				// Non-refresh plan against what the failed Create left: a plan to
				// CREATE proves the resource is absent from state. A tainted
				// resource would plan a replace instead.
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPreRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionCreate),
					},
				},
			},
		},
	})
	assertCalled()
}

// Update in a converted org: the first grant answers 409 converted, the
// provider aborts after that one write, and state is left as it was before
// the apply.
//
// allow_empty_member_set is the state discriminator: it has no API
// representation and Read preserves it, so a state that took the new plan
// (true) shows as an update against the original config, whereas `member` would
// be rebuilt from the frozen remote rows and mask the difference. Step 3 is a
// PlanOnly step, whose first plan is a NON-refresh plan, so `member` is
// compared against raw state too.
func TestAccCloudAccessResource_ConvertedOrgUpdateKeepsPriorState(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newCloudAccessConvertedMock(t)
	original := testAccProviderBlock(m.server.URL) +
		cloudAccessConvertedHCL(false, "alice@example.com", "bob@example.com")
	updated := testAccProviderBlock(m.server.URL) +
		cloudAccessConvertedHCL(true, "alice@example.com", "bob@example.com", "carol@example.com")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: original},
			{
				PreConfig: func() {
					m.takeWrites()
					m.setConverted(true)
				},
				Config:      updated,
				ExpectError: cloudAccessConvertedSummary,
			},
			{
				PreConfig: func() {
					if w := m.takeWrites(); len(w) != 1 {
						t.Errorf("converted org: want exactly 1 legacy write before aborting the update, got %d: %v", len(w), w)
					}
				},
				Config:   original,
				PlanOnly: true,
			},
		},
	})
}

// The organization converts between the membership POST and the roles PUT
// for an existing member: the POST answers "already a member" and the roles
// PUT is the first refused write. That refusal must abort the update the same
// way: the converted-org error, no further writes, and the prior state kept.
func TestAccCloudAccessResource_ConvertedOrgBetweenMembershipAndRoleAborts(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newCloudAccessConvertedMock(t)
	original := testAccProviderBlock(m.server.URL) +
		cloudAccessConvertedHCL(true, "alice@example.com", "bob@example.com")
	readonly := strings.ReplaceAll(original, `"writer"`, `"readonly"`)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: original},
			{
				PreConfig: func() {
					m.takeWrites()
					m.setConvertedRolesOnly(true)
				},
				Config:      readonly,
				ExpectError: cloudAccessConvertedSummary,
			},
			{
				PreConfig: func() {
					var roles []string
					for _, w := range m.takeWrites() {
						if strings.HasPrefix(w, http.MethodPut) && strings.HasSuffix(w, "/roles") {
							roles = append(roles, w)
						}
					}
					if len(roles) != 1 {
						t.Errorf("want exactly 1 refused roles PUT before aborting, got %d: %v", len(roles), roles)
					}
				},
				Config:   original,
				PlanOnly: true,
			},
		},
	})
}

// Destroy in a converted org: the revoke answers 409 converted. The legacy
// grants cannot be removed by this resource any more, so destroy must WARN
// (not fail) and drop the resource from state.
//
// Driven through the real terraform CLI: resource.Test's reattach path does
// not surface warnings (see warning_diagnostics_acc_test.go), and the
// observable outcome alone - destroy succeeds, state empty - is the same
// with or without the dedicated warning.
func TestAccCloudAccessResource_ConvertedOrgDestroyWarnsAndDropsState(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newCloudAccessConvertedMock(t)
	run := cliDevWorkspace(t, fmt.Sprintf(`
provider "anyscale" {
  api_url = %[1]q
  token   = "converted-org-fake-token"
}
%[2]s`, m.server.URL, cloudAccessConvertedHCL(false, "alice@example.com", "bob@example.com", "carol@example.com")))
	errorDiags := func(lines []tfApplyJSONLine) []string { return cliDiagnostics(lines, "error") }

	if lines, out, err := run("apply", "-auto-approve", "-json"); err != nil || len(errorDiags(lines)) > 0 {
		t.Fatalf("setup apply (unconverted) failed: %v %v\n%s", err, errorDiags(lines), out)
	}
	if got := len(m.memberEmails()); got != 3 {
		t.Fatalf("setup apply granted %d members, want 3", got)
	}

	m.setConverted(true)
	lines, out, err := run("destroy", "-auto-approve", "-json")
	if err != nil || len(errorDiags(lines)) > 0 {
		t.Fatalf("destroy in a converted org must succeed with a warning, got: %v %v\n%s", err, errorDiags(lines), out)
	}

	leftInPlace := regexp.MustCompile(`legacy\s+grants\s+could\s+not\s+be\s+revoked\s+and\s+were\s+left\s+in\s+place`)
	found := false
	var all []string
	for _, l := range lines {
		if l.Type != "diagnostic" {
			continue
		}
		all = append(all, l.Diagnostic.Severity+": "+l.Diagnostic.Summary)
		if l.Diagnostic.Severity == "warning" && cloudAccessConvertedSummary.MatchString(l.Diagnostic.Summary) &&
			leftInPlace.MatchString(l.Diagnostic.Detail) {
			found = true
		}
	}
	if !found {
		t.Errorf("want a warning %q whose detail says %s; got diagnostics: %v", cloudAccessConvertedSummary, leftInPlace, all)
	}

	_, stateOut, err := run("state", "list")
	if err != nil {
		t.Fatalf("terraform state list: %v\n%s", err, stateOut)
	}
	if strings.Contains(string(stateOut), "anyscale_cloud_access.test") {
		t.Errorf("resource still in state after destroy in a converted org:\n%s", stateOut)
	}
	// The refused revokes left the legacy grants where they were.
	if got := len(m.memberEmails()); got != 3 {
		t.Errorf("converted-org mock should have refused every revoke; %d members remain, want 3", got)
	}
}

// Read in a converted org returns the legacy rows frozen at conversion time,
// so a refresh against an unchanged config plans a no-op and makes no write.
//
// This pins a known blind spot rather than a desired property: the frozen rows
// no longer describe anyone's real access (role bindings do), but nothing in
// these read responses says the org converted. Without a mode signal on the
// read path, that drift is not detectable here, and this test exists so a
// future change that adds one updates it deliberately.
func TestAccCloudAccessResource_ConvertedOrgFrozenReadPlansNoop(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newCloudAccessConvertedMock(t)
	config := testAccProviderBlock(m.server.URL) +
		cloudAccessConvertedHCL(false, "alice@example.com", "bob@example.com", "carol@example.com")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					m.takeWrites()
					m.setConverted(true)
				},
				Config:   config,
				PlanOnly: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				PreConfig: func() {
					if w := m.takeWrites(); len(w) != 0 {
						t.Errorf("a refresh/plan in a converted org must not write, got %v", w)
					}
				},
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}
