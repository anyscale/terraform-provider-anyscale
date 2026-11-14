package acctest

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// A conversion to role bindings in progress refuses every legacy access write
// with a dedicted 409: not "use role bindings instead" but "retry", because the
// conversion that justifies the new surface has not landed yet. On
// anyscale_organization_user_role and anyscale_cloud.auto_add_user that
// refusal must surface as the converting diagnostic - never the converted-org
// one, whose pointers at the replacement surface would tell a practitioner to
// migrate access that is one wizard step away from migrating itself.

const convOrgConvertingDetail = "This organization is converting to role bindings. Try again when the conversion finishes, or retry it in the migration wizard if it stopped."

var (
	convOrgConvertingSummary = regexp.MustCompile(`Organization\s+Is\s+Converting\s+to\s+Role\s+Bindings`)
	// The converting diagnostic must say retry, and must not borrow the
	// converted-org guidance (\s+ wrappings: Terraform folds detail lines).
	convOrgConvertingHints = []*regexp.Regexp{
		regexp.MustCompile(`Retry\s+once\s+the\s+conversion\s+completes`),
		regexp.MustCompile(`migration\s+wizard`),
		regexp.MustCompile(`rbac\.md#organization-access-mode`),
	}
)

// convOrgConvertingCheck requires the converting diagnostic: its summary and
// the retry guidance, and forbids the converted-org diagnostic.
func convOrgConvertingCheck(t *testing.T) (func(error) error, func()) {
	t.Helper()
	check, assertCalled := rbErrorCheck(t, convOrgConvertingSummary, convOrgSummary)
	return func(err error) error {
		if e := check(err); e != nil {
			return e
		}
		for _, h := range convOrgConvertingHints {
			if !h.MatchString(err.Error()) {
				return fmt.Errorf("converting diagnostic is missing %s: %w", h, err)
			}
		}
		return nil
	}, assertCalled
}

// ─── anyscale_organization_user_role ───────────────────────────────────────

// Every create write path answers the converting 409 with the retry
// diagnostic: both PUTs (the roles path and the legacy permission_level path
// after a 501), and the deny_roles-declared roles PUT.
func TestAccOrganizationUserRoleResource_ConvertingOrgCreateErrors(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	cases := []struct {
		name                  string
		deny                  string
		rolesStatus           int
		rolesDetail           string
		legacyStatus          int
		wantRoles, wantLegacy int
	}{
		{name: "roles_path_deny_roles_declared", deny: `["image_reader"]`, rolesStatus: http.StatusConflict, rolesDetail: convOrgConvertingDetail, wantRoles: 1},
		{name: "roles_path_base_role_only", rolesStatus: http.StatusConflict, rolesDetail: convOrgConvertingDetail, wantRoles: 1},
		{name: "legacy_path_after_roles_501", rolesStatus: http.StatusNotImplemented, rolesDetail: "Organization roles are not enabled.", legacyStatus: http.StatusConflict, wantRoles: 1, wantLegacy: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, mock := newConvOrgUserRoleServer(t)
			mock.set(tc.rolesStatus, tc.rolesDetail, tc.legacyStatus, convOrgConvertingDetail)
			check, assertCalled := convOrgConvertingCheck(t)
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

// Destroy while the conversion runs: the deny-roles PUT is refused, and the
// destroy must FAIL - the legacy role this resource clears is about to be
// migrated, so dropping the resource now would silently surrender management
// of it - with the retry diagnostic and the resource left in state. After the
// conversion completes, a retry lands in the converted branch and warns and
// drops instead (its own test above). Driven through the terraform CLI, same
// reason as the converted-org destroy.
func TestAccOrganizationUserRoleResource_ConvertingOrgDestroyKeepsState(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newConvOrgUserRoleServer(t)
	run := cliDevWorkspace(t, orgUserRoleConfig(server.URL, mock.email, "collaborator", `["image_reader"]`))

	if lines, out, err := run("apply", "-auto-approve", "-json"); err != nil || len(cliDiagnostics(lines, "error")) > 0 {
		t.Fatalf("setup apply (unconverted) failed: %v %v\n%s", err, cliDiagnostics(lines, "error"), out)
	}

	mock.set(http.StatusConflict, convOrgConvertingDetail, http.StatusConflict, convOrgConvertingDetail)
	lines, out, err := run("destroy", "-auto-approve", "-json")
	if err == nil {
		t.Fatalf("destroy while converting must FAIL - the retry guidance is only honest with the resource still in state - got success:\n%s", out)
	}

	found := false
	var all []string
	for _, l := range lines {
		if l.Type != "diagnostic" {
			continue
		}
		all = append(all, l.Diagnostic.Severity+": "+l.Diagnostic.Summary)
		if l.Diagnostic.Severity == "error" && convOrgConvertingSummary.MatchString(l.Diagnostic.Summary) &&
			regexp.MustCompile(`stays\s+in\s+state`).MatchString(l.Diagnostic.Detail) {
			found = true
		}
	}
	if !found {
		t.Errorf("want an error %q whose detail says the resource stays in state; got diagnostics: %v", convOrgConvertingSummary, all)
	}
	if roles, legacy := mock.failed(); roles+legacy == 0 {
		t.Error("destroy made no refused write; the converting-org path was not exercised")
	}

	if _, stateOut, err := run("state", "list"); err != nil {
		t.Fatalf("terraform state list: %v\n%s", err, stateOut)
	} else if !strings.Contains(stateOut, "anyscale_organization_user_role.mock") {
		t.Errorf("destroy while converting must keep the resource in state:\n%s", stateOut)
	}
}

// ─── anyscale_cloud.auto_add_user ───────────────────────────────────────────

// The converting 409 on the auto_add_user PUT at UPDATE time: the retry
// diagnostic, not the converted-org one, and no retry of the PUT.
func TestAccCloudResource_ConvertingOrgAutoAddUserUpdateErrors(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	detail := convOrgConvertingDetail
	server, mock := newConvOrgCloudServer(t)
	check, assertCalled := convOrgConvertingCheck(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		ErrorCheck:               check,
		Steps: []resource.TestStep{
			{Config: convOrgCloudConfig(server.URL, false)},
			{
				PreConfig: func() { mock.set(http.StatusConflict, detail) },
				Config:    convOrgCloudConfig(server.URL, true),
			},
		},
	})
	assertCalled()
	if got := mock.puts(); got != 1 {
		t.Errorf("auto_add_user PUTs = %d, want 1: the converting 409 must not be retried", got)
	}
}

// The same refusal at create time: the cloud the create just made is deleted
// and removed from state (identical rollback to the converted-org refusal),
// but the diagnostic says to wait out the conversion instead of pointing at
// the replacement surface.
func TestAccCloudResource_ConvertingOrgAutoAddUserCreateDeletesCloud(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	deletes := convOrgCreateThenPlan(t, convOrgConvertingDetail, 0,
		regexp.MustCompile(`(?s)Organization\s+Is\s+Converting\s+to\s+Role\s+Bindings.*The\s+cloud\s+was\s+not\s+created`),
		plancheck.ResourceActionCreate)
	if deletes != 1 {
		t.Errorf("cloud DELETEs during the failed create = %d, want 1", deletes)
	}
}
