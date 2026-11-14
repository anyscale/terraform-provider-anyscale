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
// bindings. There, every legacy permission write answers 409 with the
// converted write detail, and the legacy collaborator reads answer 409 with
// the converted read detail - the API no longer serves the frozen rows.
// `GET .../collaborators/roles` and `/userinfo` are still readable; the mock
// keeps them working so the refusal is exactly the backend's.
//
// The mock below wraps mockCloudAccessServer (resource_cloud_access_import_acc_test.go)
// rather than editing it: one switch turns every legacy cloud/project
// collaborator write and read into its converted 409, and every refused call
// is recorded so a test can pin how many the provider attempted.

const (
	cloudAccessConvertedDetail     = "This organization uses role bindings. Manage access in Users & IAM or with the role bindings API."
	cloudAccessConvertedReadDetail = "This organization uses role bindings. See who has access in Users & IAM or with the role bindings API."
	cloudAccessConvertingDetail    = "This organization is converting to role bindings. Try again when the conversion finishes, or retry it in the migration wizard if it stopped."
	cloudAccessConvertedCloud      = cloudAccessMockCloudID
)

var (
	cloudAccessConvertedSummary = regexp.MustCompile(`Organization Uses Role Bindings`)
	// A refused write's detail must route the practitioner to the replacement
	// surface. \s+ between words because Terraform wraps diagnostic detail
	// lines.
	cloudAccessConvertedDetailWants = []*regexp.Regexp{
		regexp.MustCompile(`anyscale_iam_role_grant`),
		regexp.MustCompile(`user_group_id`),
		regexp.MustCompile(`\buser_id\b`),
		regexp.MustCompile(`rbac\.md#organization-access-mode`),
	}
	// A refused read's detail must say the resource's exit, not the
	// replacement surface's full mapping.
	cloudAccessConvertedReadDetailWants = []*regexp.Regexp{
		regexp.MustCompile(`terraform\s+state\s+rm`),
		regexp.MustCompile(`anyscale_iam_role_grant`),
		regexp.MustCompile(`rbac\.md#organization-access-mode`),
	}
	cloudAccessConvertingSummary = regexp.MustCompile(`Organization Is Converting to Role Bindings`)
	// The converting detail's guidance is to retry, never to migrate yet.
	// \s+ because Terraform wraps diagnostic detail lines.
	cloudAccessConvertingDetailWants = []*regexp.Regexp{
		regexp.MustCompile(`Retry\s+once\s+the\s+conversion\s+completes`),
		regexp.MustCompile(`migration\s+wizard`),
		regexp.MustCompile(`rbac\.md#organization-access-mode`),
	}
)

type cloudAccessConvertedMock struct {
	inner  *mockCloudAccessServer
	server *httptest.Server

	mu        sync.Mutex
	converted bool
	// converting makes every legacy WRITE answer 409 with the conversion-
	// in-progress detail while the reads keep working: the conversion still
	// runs, and until it commits the legacy rows are the truth.
	converting bool
	// convertedRolesOnly refuses only the roles PUT, as if the organization
	// converted between a member's membership POST and its role write. The
	// reads keep working, so the mocked organization is exactly mid-apply.
	convertedRolesOnly bool
	// refuseProjectReads leaves the cloud member reads working but refuses
	// the project collaborators read: an organization that converted between
	// the member list and the project-role refresh of the same Read.
	refuseProjectReads bool
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

// isLegacyMemberRead matches the cloud member reads: the collaborator
// search. The roles list (GET .../collaborators/roles) still answers in a
// converted organization, so it is not refused here.
func isLegacyMemberRead(r *http.Request) bool {
	p := r.URL.Path
	return r.Method == http.MethodPost && strings.HasPrefix(p, "/api/v2/clouds/") &&
		strings.HasSuffix(p, "/collaborators/users/search")
}

// isLegacyProjectRead matches the project collaborator read the project-role
// refresh uses.
func isLegacyProjectRead(r *http.Request) bool {
	p := r.URL.Path
	return r.Method == http.MethodGet && strings.HasPrefix(p, "/api/v2/projects/") &&
		strings.HasSuffix(p, "/collaborators/users")
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
	m.mu.Lock()
	converted := m.converted ||
		(m.convertedRolesOnly && r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/roles"))
	converting := m.converting
	refuseProjectReads := m.refuseProjectReads
	if isLegacyWrite(r) {
		m.writes = append(m.writes, r.Method+" "+r.URL.Path)
	}
	m.mu.Unlock()

	if isLegacyWrite(r) && (converted || converting) {
		detail := cloudAccessConvertedDetail
		if converting {
			detail = cloudAccessConvertingDetail
		}
		m.refuse409(w, detail)
		return
	}
	// In a converted organization the legacy reads answer 409 with the read
	// detail; a converting organization still serves them.
	if converted && isLegacyMemberRead(r) {
		m.refuse409(w, cloudAccessConvertedReadDetail)
		return
	}
	if refuseProjectReads && isLegacyProjectRead(r) {
		m.refuse409(w, cloudAccessConvertedReadDetail)
		return
	}
	m.inner.handle(w, r)
}

func (m *cloudAccessConvertedMock) refuse409(w http.ResponseWriter, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]any{"detail": detail})
}

func (m *cloudAccessConvertedMock) setConverted(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.converted = v
}

func (m *cloudAccessConvertedMock) setConverting(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.converting = v
}

func (m *cloudAccessConvertedMock) setRefuseProjectReads(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refuseProjectReads = v
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

// Create in a converted org: the member-list read answers 409 before any
// write is attempted. The create must fail with the dedicated read
// diagnostic - naming the resource's exit - and leave nothing in state.
func TestAccCloudAccessResource_ConvertedOrgCreateRefusedAtMemberRead(t *testing.T) {
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
			for _, want := range cloudAccessConvertedReadDetailWants {
				if !want.MatchString(err.Error()) {
					return fmt.Errorf("converted-org read diagnostic does not mention %s: %w", want, err)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config,
			},
			{
				// Zero writes: nothing may be written before the refusal
				// surfaces, and nothing more after it.
				PreConfig: func() {
					if w := m.takeWrites(); len(w) != 0 {
						t.Errorf("converted org: want 0 legacy writes, got %d: %v", len(w), w)
					}
				},
				// Non-refresh plan against what the failed Create left: a plan
				// to CREATE proves the resource is absent from state.
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
					if w := m.takeWrites(); len(w) != 0 {
						t.Errorf("converted org: want 0 legacy writes (the member read is refused first), got %d: %v", len(w), w)
					}
					m.setConverted(false)
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

	// A refused write's diagnostic - not the read variant - because the
	// refusal this test manufactures really is a write.
	check, assertCalled := rbErrorCheck(t, cloudAccessConvertedSummary)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		ErrorCheck: func(err error) error {
			if err := check(err); err != nil {
				return err
			}
			for _, want := range cloudAccessConvertedDetailWants {
				if !want.MatchString(err.Error()) {
					return fmt.Errorf("converted-org write diagnostic does not mention %s: %w", want, err)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: original},
			{
				PreConfig: func() {
					m.takeWrites()
					m.setConvertedRolesOnly(true)
				},
				// No ExpectError here: the ErrorCheck above matches the
				// converted summary, and returning nil for a matched error is
				// exactly what lets a matched apply FAIL THE TEST when the
				// matcher is not assertCalled()-guarded - the standard
				// rbErrorCheck wiring, as the create test uses it.
				Config: readonly,
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
	assertCalled()
}

// Destroy in a converted org: the member-list read answers 409 before any
// revoke is attempted. The resource cannot be reconciled at all there, so
// destroy must WARN (not fail) and drop the resource from state, saying the
// member list could not be read and nothing was revoked.
//
// Driven through the real terraform CLI because resource.Test's reattach
// path does not surface warnings (see warning_diagnostics_acc_test.go). A
// normal destroy refreshes first, and that refresh is refused in a converted
// org (that is ConvertedOrgRefreshRefused below); the Delete reconcile path
// this test pins still runs for a state whose last good refresh predates the
// conversion, so the CLI destroyer is driven with refresh disabled.
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
	lines, out, err := run("destroy", "-auto-approve", "-json", "-refresh=false")
	if err != nil || len(errorDiags(lines)) > 0 {
		t.Fatalf("destroy in a converted org must succeed with a warning, got: %v %v\n%s", err, errorDiags(lines), out)
	}

	refusedRead := regexp.MustCompile(`member\s+list\s+could\s+not\s+be\s+read\s+and\s+nothing\s+was\s+revoked`)
	found := false
	var all []string
	for _, l := range lines {
		if l.Type != "diagnostic" {
			continue
		}
		all = append(all, l.Diagnostic.Severity+": "+l.Diagnostic.Summary)
		if l.Diagnostic.Severity == "warning" && cloudAccessConvertedSummary.MatchString(l.Diagnostic.Summary) &&
			refusedRead.MatchString(l.Diagnostic.Detail) {
			found = true
		}
	}
	if !found {
		t.Errorf("want a warning %q whose detail says %s; got diagnostics: %v", cloudAccessConvertedSummary, refusedRead, all)
	}

	_, stateOut, err := run("state", "list")
	if err != nil {
		t.Fatalf("terraform state list: %v\n%s", err, stateOut)
	}
	if strings.Contains(string(stateOut), "anyscale_cloud_access.test") {
		t.Errorf("resource still in state after destroy in a converted org:\n%s", stateOut)
	}
	// No revoke was attempted: the legacy grants are where they were.
	if got := len(m.memberEmails()); got != 3 {
		t.Errorf("converted-org mock should have refused every revoke; %d members remain, want 3", got)
	}
}

// Read in a converted org: the member-list read answers 409, so the refresh
// FAILS with the dedicated read diagnostic instead of serving the frozen rows
// or planning a no-op over stale state. The resource stays in state, but
// neither plan nor apply can pretend the member list is known.
func TestAccCloudAccessResource_ConvertedOrgRefreshRefused(t *testing.T) {
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
				Config:      config,
				ExpectError: cloudAccessConvertedSummary,
			},
			{
				PreConfig: func() {
					if w := m.takeWrites(); len(w) != 0 {
						t.Errorf("a refresh in a converted org must not write, got %v", w)
					}
					m.setConverted(false)
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// The project-collaborator read refuses while the cloud member reads still
// answer: the organization converted between the refresh's two reads. The
// refresh must fail with the dedicated read diagnostic, not silently report
// the project's roles as absent - "absent" is a revoke instruction to this
// authoritative resource.
func TestAccCloudAccessResource_ConvertedOrgProjectRolesRefreshRefused(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newCloudAccessConvertedMock(t)
	config := testAccProviderBlock(m.server.URL) +
		cloudAccessConvertedHCL(false, "alice@example.com", "carol@example.com")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig:   func() { m.setRefuseProjectReads(true) },
				Config:      config,
				ExpectError: cloudAccessConvertedSummary,
			},
			{
				PreConfig: func() { m.setRefuseProjectReads(false) },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// Create while the conversion runs: the reads still answer, and the first
// grant write is refused with the conversion-in-progress detail. The create
// must fail with the retry diagnostic - not the converted-org one, which
// would tell the practitioner to migrate before the migration even finished.
func TestAccCloudAccessResource_ConvertingOrgCreateRefusedWithRetryDiagnostic(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newCloudAccessConvertedMock(t)
	m.setConverting(true)
	config := testAccProviderBlock(m.server.URL) +
		cloudAccessConvertedHCL(false, "alice@example.com", "bob@example.com")

	check, assertCalled := rbErrorCheck(t, cloudAccessConvertingSummary)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		ErrorCheck: func(err error) error {
			if err := check(err); err != nil {
				return err
			}
			for _, want := range cloudAccessConvertingDetailWants {
				if !want.MatchString(err.Error()) {
					return fmt.Errorf("converting-org diagnostic does not mention %s: %w", want, err)
				}
			}
			if cloudAccessConvertedSummary.MatchString(err.Error()) {
				return fmt.Errorf("a conversion-in-progress refusal must not read as an already-converted org: %w", err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config},
		},
	})
	assertCalled()
	// Exactly one write was attempted before the abort.
	if w := m.takeWrites(); len(w) != 1 {
		t.Errorf("converting org: want exactly 1 legacy write before aborting, got %d: %v", len(w), w)
	}
}

// Destroy while the conversion runs: the reads still answer, every revoke is
// refused with the conversion-in-progress detail, so destroy warns with the
// retry guidance and drops the resource from state - same exit as the
// converted-org destroy, different reason. Refresh-disabled for the same
// reason as the converted-org destroy above.
func TestAccCloudAccessResource_ConvertingOrgDestroyWarnsAndDropsState(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	m := newCloudAccessConvertedMock(t)
	run := cliDevWorkspace(t, fmt.Sprintf(`
provider "anyscale" {
  api_url = %[1]q
  token   = "converting-org-fake-token"
}
%[2]s`, m.server.URL, cloudAccessConvertedHCL(false, "alice@example.com", "bob@example.com")))
	errorDiags := func(lines []tfApplyJSONLine) []string { return cliDiagnostics(lines, "error") }

	if lines, out, err := run("apply", "-auto-approve", "-json"); err != nil || len(errorDiags(lines)) > 0 {
		t.Fatalf("setup apply (unconverted) failed: %v %v\n%s", err, errorDiags(lines), out)
	}

	m.setConverting(true)
	lines, out, err := run("destroy", "-auto-approve", "-json", "-refresh=false")
	if err != nil || len(errorDiags(lines)) > 0 {
		t.Fatalf("destroy while converting must succeed with a warning, got: %v %v\n%s", err, errorDiags(lines), out)
	}

	found := false
	var all []string
	for _, l := range lines {
		if l.Type != "diagnostic" {
			continue
		}
		all = append(all, l.Diagnostic.Severity+": "+l.Diagnostic.Summary)
		if l.Diagnostic.Severity == "warning" && cloudAccessConvertingSummary.MatchString(l.Diagnostic.Summary) {
			found = true
		}
	}
	if !found {
		t.Errorf("want a warning %q on destroy-while-converting; got diagnostics: %v", cloudAccessConvertingSummary, all)
	}

	_, stateOut, err := run("state", "list")
	if err != nil {
		t.Fatalf("terraform state list: %v\n%s", err, stateOut)
	}
	if strings.Contains(string(stateOut), "anyscale_cloud_access.test") {
		t.Errorf("resource still in state after destroy while converting:\n%s", stateOut)
	}
	// The refused revokes left the members in place.
	if got := len(m.memberEmails()); got != 2 {
		t.Errorf("converting-org mock should have refused every revoke; %d members remain, want 2", got)
	}
}

// The first-apply disclosure reads the member list at plan time; in a
// converted organization that read answers 409. The disclosure must carry
// the converted-org warning with the read diagnostic's exit (state rm and
// the replacement surface), not the generic "did not run" text, and must
// not read like a transient miss: the create that follows will be refused
// for the same reason. The plan still proposes the create - the warning
// fails closed on naming, not on planning.
func TestAccCloudAccessResource_ConvertedOrgFirstApplyDisclosureRefused(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	binDir := buildProviderBinaryForCLICheck(t)

	// The converted variant of the disclosure mock: the search answers the
	// read refusal, everything else answers normally.
	convertedMux := http.NewServeMux()
	convertedMux.HandleFunc("/api/v2/clouds/"+cloudAccessDisclosureCloudID+"/collaborators/users/search", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"detail":"`+cloudAccessConvertedReadDetail+`"}`)
	})
	convertedMux.HandleFunc("/api/v2/clouds/"+cloudAccessDisclosureCloudID+"/collaborators/roles", func(w http.ResponseWriter, r *http.Request) {
		// Still readable in a converted organization.
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"results":[],"metadata":{"next_paging_token":null}}`)
	})
	convertedMux.HandleFunc("/api/v2/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"result":{"id":"usr_caller","email":"caller@example.com","name":"caller",`+
			`"username":"caller","organization_permission_level":null,"organization_ids":[],"organizations":[]}}`)
	})
	server := httptest.NewServer(convertedMux)
	t.Cleanup(server.Close)

	lines := runTerraformPlanJSON(t, binDir, server.URL, cloudAccessDisclosureResourceHCL())

	found := false
	planSucceeded := false
	var all []string
	for _, l := range lines {
		if l.Type == "diagnostic" {
			all = append(all, l.Diagnostic.Severity+": "+l.Diagnostic.Summary)
		}
		if l.Type != "diagnostic" {
			if l.Type == "planned_change" {
				planSucceeded = true
			}
			continue
		}
		if l.Diagnostic.Severity == "warning" && cloudAccessConvertedSummary.MatchString(l.Diagnostic.Summary) {
			found = true
			for _, want := range []*regexp.Regexp{
				regexp.MustCompile(`terraform\s+state\s+rm`),
				regexp.MustCompile(`anyscale_iam_role_grant`),
				regexp.MustCompile(`the\s+create\s+itself\s+will\s+be\s+refused`),
			} {
				if !want.MatchString(l.Diagnostic.Detail) {
					t.Errorf("disclosure warning detail %q does not contain %s", l.Diagnostic.Detail, want)
				}
			}
		}
	}
	if !found {
		t.Fatalf("expected the converted-org disclosure warning, got diagnostics: %v", all)
	}
	if !planSucceeded {
		t.Error("expected the plan to still propose creating the resource (warning, not error)")
	}
}
