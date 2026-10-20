package acctest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// An organization converted to role bindings refuses the legacy access writes
// with 409 and this detail. The provider must turn it into a diagnostic that
// points at anyscale_role_binding; any other 409 keeps the generic handling.

const convOrgConvertedDetail = "This organization uses role bindings. Manage access in Users & IAM or with the role bindings API."

// Deliberately free of the transient auto_add_user wording ("still being
// applied", "try again") so the cloud resource does not retry it.
const convOrgUnrecognizedDetail = "Conflict: another membership change for this member is in progress."

var (
	convOrgSummary = regexp.MustCompile(`Organization\s+Uses\s+Role\s+Bindings`)
	convOrgHints   = []*regexp.Regexp{
		regexp.MustCompile(`anyscale_role_binding`),
		regexp.MustCompile(`user_group_id`),
		regexp.MustCompile(`\buser_id\b`),
		regexp.MustCompile(`rbac\.md#organization-access-mode`),
	}
)

// convOrgWords matches s with any whitespace run between words, since
// Terraform wraps diagnostic detail lines.
func convOrgWords(s string) *regexp.Regexp {
	parts := strings.Fields(s)
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return regexp.MustCompile(strings.Join(parts, `\s+`))
}

// convOrgConvertedCheck requires the converted-org diagnostic: its summary and
// every pointer the detail must carry.
func convOrgConvertedCheck(t *testing.T, forbid ...*regexp.Regexp) (func(error) error, func()) {
	t.Helper()
	check, assertCalled := rbErrorCheck(t, convOrgSummary, forbid...)
	return func(err error) error {
		if e := check(err); e != nil {
			return e
		}
		for _, h := range convOrgHints {
			if !h.MatchString(err.Error()) {
				return fmt.Errorf("converted-org diagnostic is missing %s: %w", h, err)
			}
		}
		return nil
	}, assertCalled
}

// convOrgPassThroughCheck requires an unrecognized 409 to keep its own detail
// and NOT be reported as the converted-org diagnostic.
func convOrgPassThroughCheck(t *testing.T) (func(error) error, func()) {
	t.Helper()
	return rbErrorCheck(t, convOrgWords(convOrgUnrecognizedDetail), convOrgSummary)
}

// convOrgUserRoleServer wraps mockOrgUserRoleServer and answers either PUT
// with a scripted status and detail when one is set.
type convOrgUserRoleServer struct {
	*mockOrgUserRoleServer

	cmu          sync.Mutex
	rolesStatus  int
	rolesDetail  string
	legacyStatus int
	legacyDetail string
	rolesFailed  int
	legacyFailed int
}

func newConvOrgUserRoleServer(t *testing.T) (*httptest.Server, *convOrgUserRoleServer) {
	t.Helper()
	// The inner mock's own server is left idle; only its handler is used.
	_, base := newMockOrgUserRoleServer(t)
	s := &convOrgUserRoleServer{mockOrgUserRoleServer: base}
	server := httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(server.Close)
	return server, s
}

func (s *convOrgUserRoleServer) set(rolesStatus int, rolesDetail string, legacyStatus int, legacyDetail string) {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	s.rolesStatus, s.rolesDetail = rolesStatus, rolesDetail
	s.legacyStatus, s.legacyDetail = legacyStatus, legacyDetail
}

func (s *convOrgUserRoleServer) failed() (roles, legacy int) {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	return s.rolesFailed, s.legacyFailed
}

func (s *convOrgUserRoleServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		s.cmu.Lock()
		var status int
		var detail string
		switch r.URL.Path {
		case "/api/v2/organization_collaborators/" + s.userID + "/roles":
			status, detail = s.rolesStatus, s.rolesDetail
			if status != 0 {
				s.rolesFailed++
			}
		case "/api/v2/organization_collaborators/" + s.identityID:
			status, detail = s.legacyStatus, s.legacyDetail
			if status != 0 {
				s.legacyFailed++
			}
		}
		s.cmu.Unlock()
		if status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = fmt.Fprintf(w, `{"error":{"detail":%q}}`, detail)
			return
		}
	}
	s.mockOrgUserRoleServer.handle(w, r)
}

// Every create write path answers the converted-org 409 with the role-bindings
// diagnostic: the roles PUT (deny_roles declared, and base_role alone) and the
// legacy permission_level PUT reached when the roles API answers 501.
func TestAccOrganizationUserRoleResource_ConvertedOrgCreateErrors(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	cases := []struct {
		name                  string
		deny                  string
		rolesStatus           int
		rolesDetail           string
		legacyStatus          int
		wantRoles, wantLegacy int
	}{
		{name: "roles_path_deny_roles_declared", deny: `["image_reader"]`, rolesStatus: http.StatusConflict, rolesDetail: convOrgConvertedDetail, wantRoles: 1},
		{name: "roles_path_base_role_only", rolesStatus: http.StatusConflict, rolesDetail: convOrgConvertedDetail, wantRoles: 1},
		{name: "legacy_path_after_roles_501", rolesStatus: http.StatusNotImplemented, rolesDetail: "Organization roles are not enabled.", legacyStatus: http.StatusConflict, wantRoles: 1, wantLegacy: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, mock := newConvOrgUserRoleServer(t)
			mock.set(tc.rolesStatus, tc.rolesDetail, tc.legacyStatus, convOrgConvertedDetail)
			check, assertCalled := convOrgConvertedCheck(t, regexp.MustCompile(`Could\s+Not\s+Set\s+Organization`))
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				ErrorCheck:               check,
				Steps:                    []resource.TestStep{{Config: orgUserRoleConfig(server.URL, mock.email, "collaborator", tc.deny)}},
			})
			assertCalled()
			if roles, legacy := mock.failed(); roles != tc.wantRoles || legacy != tc.wantLegacy {
				t.Errorf("refused PUTs: roles=%d legacy=%d, want roles=%d legacy=%d", roles, legacy, tc.wantRoles, tc.wantLegacy)
			}
		})
	}
}

// Destroy clears declared deny_roles through the roles PUT. In a converted
// organization that write is refused; destroy must warn that the role was
// left in place and drop the resource from state, never error, or the
// resource could not be destroyed without terraform state rm. Driven through
// the terraform CLI because resource.Test does not surface warnings.
func TestAccOrganizationUserRoleResource_ConvertedOrgDestroyWarnsAndDropsState(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newConvOrgUserRoleServer(t)
	run := cliDevWorkspace(t, orgUserRoleConfig(server.URL, mock.email, "collaborator", `["image_reader"]`))

	if lines, out, err := run("apply", "-auto-approve", "-json"); err != nil || len(cliDiagnostics(lines, "error")) > 0 {
		t.Fatalf("setup apply (unconverted) failed: %v %v\n%s", err, cliDiagnostics(lines, "error"), out)
	}

	mock.set(http.StatusConflict, convOrgConvertedDetail, http.StatusConflict, convOrgConvertedDetail)
	lines, out, err := run("destroy", "-auto-approve", "-json")
	if err != nil || len(cliDiagnostics(lines, "error")) > 0 {
		t.Fatalf("destroy in a converted org must succeed with a warning, got: %v %v\n%s", err, cliDiagnostics(lines, "error"), out)
	}
	leftInPlace := regexp.MustCompile(`left\s+in\s+place`)
	found := false
	for _, l := range lines {
		if l.Type == "diagnostic" && l.Diagnostic.Severity == "warning" &&
			convOrgSummary.MatchString(l.Diagnostic.Summary) && leftInPlace.MatchString(l.Diagnostic.Detail) {
			found = true
		}
	}
	if !found {
		t.Errorf("want a warning %q whose detail says %s; got diagnostics: %v", convOrgSummary, leftInPlace, cliDiagnostics(lines, ""))
	}
	if roles, legacy := mock.failed(); roles+legacy == 0 {
		t.Errorf("destroy made no refused write; the converted-org path was not exercised")
	}

	_, stateOut, err := run("state", "list")
	if err != nil {
		t.Fatalf("terraform state list: %v\n%s", err, stateOut)
	}
	if strings.Contains(stateOut, "anyscale_organization_user_role.mock") {
		t.Errorf("resource still in state after destroy in a converted org:\n%s", stateOut)
	}
}

// A 409 with any other detail keeps the generic handling on both write paths.
// This is also the control for the converted-org tests: the same mock and
// configs, only the detail differs.
func TestAccOrganizationUserRoleResource_ConvertedOrgUnrecognizedConflictPassesThrough(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	cases := []struct {
		name                     string
		rolesStatus, legacyCode  int
		rolesDetail              string
		wantRoles, wantLegacyPut int
	}{
		{name: "roles_path", rolesStatus: http.StatusConflict, rolesDetail: convOrgUnrecognizedDetail, wantRoles: 1},
		{name: "legacy_path_after_roles_501", rolesStatus: http.StatusNotImplemented, rolesDetail: "Organization roles are not enabled.", legacyCode: http.StatusConflict, wantRoles: 1, wantLegacyPut: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, mock := newConvOrgUserRoleServer(t)
			mock.set(tc.rolesStatus, tc.rolesDetail, tc.legacyCode, convOrgUnrecognizedDetail)
			check, assertCalled := convOrgPassThroughCheck(t)
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				ErrorCheck:               check,
				Steps:                    []resource.TestStep{{Config: orgUserRoleConfig(server.URL, mock.email, "collaborator", "")}},
			})
			assertCalled()
			if roles, legacy := mock.failed(); roles != tc.wantRoles || legacy != tc.wantLegacyPut {
				t.Errorf("refused PUTs: roles=%d legacy=%d, want roles=%d legacy=%d", roles, legacy, tc.wantRoles, tc.wantLegacyPut)
			}
		})
	}
}
