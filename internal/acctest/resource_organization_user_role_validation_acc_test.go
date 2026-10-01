package acctest

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// These tests cover how anyscale_organization_user_role treats values the
// backend canonicalizes (email casing, deny_roles order and duplicates) and
// values it rejects (base_role / deny_roles outside the real enums). The mock
// is backend-faithful on each: emails are stored lowercase, roles are stored as
// a set and read back in a fixed order, and the roles endpoint accepts only
// the two base roles and two additional roles.

func orgUserRoleConfig(url, email, baseRole, denyRoles string) string {
	deny := ""
	if denyRoles != "" {
		deny = "\n  deny_roles = " + denyRoles
	}
	return testAccProviderBlock(url) + fmt.Sprintf(`
resource "anyscale_organization_user_role" "mock" {
  email     = %q
  base_role = %q%s
}
`, email, baseRole, deny)
}

// An email configured with capitals must apply and then plan clean: the API
// returns it lowercased, and the configured spelling is what state keeps.
func TestAccOrganizationUserRoleResource_MixedCaseEmailPlansClean(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	httpServer, _ := newMockOrgUserRoleServer(t)
	const addr = "anyscale_organization_user_role.mock"
	mixed := "Org-Role-Mock@Example.com"
	cfg := orgUserRoleConfig(httpServer.URL, mixed, "collaborator", "")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "email", mixed),
					resource.TestCheckResourceAttr(addr, "id", mixed),
				),
			},
			{
				Config:           cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
		},
	})
}

// A deny_roles list in non-canonical order must apply and plan clean: the
// backend returns the set in its own fixed order. Positive control: the same
// roles in canonical order.
func TestAccOrganizationUserRoleResource_DenyRolesOrderPlansClean(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	for name, roles := range map[string]string{
		"canonical_order_control": `["image_reader", "image_reader_no_base_images"]`,
		"reversed_order":          `["image_reader_no_base_images", "image_reader"]`,
	} {
		t.Run(name, func(t *testing.T) {
			httpServer, mock := newMockOrgUserRoleServer(t)
			cfg := orgUserRoleConfig(httpServer.URL, mock.email, "collaborator", roles)
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{Config: cfg},
					{
						Config:           cfg,
						ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
					},
				},
			})
		})
	}
}

// When the backend's set really differs from the configured one, state takes
// the backend's value and the next plan shows the drift: only a reordering is
// kept in the configured order, never a different set.
func TestAccOrganizationUserRoleResource_DenyRolesRealDriftStillShows(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	httpServer, mock := newMockOrgUserRoleServer(t)
	cfg := orgUserRoleConfig(httpServer.URL, mock.email, "collaborator", `["image_reader_no_base_images", "image_reader"]`)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig: func() { mock.setBackend("collaborator", []string{"image_reader"}) },
				Config:    cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("anyscale_organization_user_role.mock", plancheck.ResourceActionUpdate),
				}},
			},
		},
	})
}

// Duplicates in deny_roles are a plan-time error, not an apply-time
// "inconsistent result".
func TestAccOrganizationUserRoleResource_DenyRolesDuplicateRejectedAtPlan(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	httpServer, mock := newMockOrgUserRoleServer(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      orgUserRoleConfig(httpServer.URL, mock.email, "collaborator", `["image_reader", "image_reader"]`),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`(?s)deny_roles.*(duplicate|unique)`),
		}},
	})
	if got := mock.writeCallCount(); got != 0 {
		t.Fatalf("writes = %d, want 0: the error must come from plan, before any write", got)
	}
}

// Values outside the backend's enums are plan-time errors. Controls: the valid
// values apply.
func TestAccOrganizationUserRoleResource_InvalidRolesRejectedAtPlan(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	cases := []struct {
		name, base, deny, wantErr string
	}{
		{"valid_control", "owner", "", ""},
		{"image_reader_as_base_role", "image_reader", "", `(?s)base_role.*(owner|collaborator)`},
		{"unknown_base_role", "readonly", "", `(?s)base_role.*(owner|collaborator)`},
		{"unknown_deny_role", "collaborator", `["readonly"]`, `(?s)deny_roles.*image_reader`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			httpServer, mock := newMockOrgUserRoleServer(t)
			step := resource.TestStep{Config: orgUserRoleConfig(httpServer.URL, mock.email, tc.base, tc.deny)}
			if tc.wantErr != "" {
				step.PlanOnly = true
				step.ExpectError = regexp.MustCompile(tc.wantErr)
			}
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Steps:                    []resource.TestStep{step},
			})
			if tc.wantErr != "" && mock.writeCallCount() != 0 {
				t.Fatalf("writes = %d, want 0", mock.writeCallCount())
			}
		})
	}
}

// A failed per-user lookup must be an error on every surface that reads
// additional_roles. Reporting it as null instead would present a real deny-role
// list (which restricts even organization owners) as "undetermined" on the
// strength of a transient failure. Positive control: the same configs succeed
// when the lookup works.
func TestAccOrganizationUserRoles_PerUserLookupFailureIsAnError(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	surfaces := map[string]func(url string, mock *mockOrgUserRoleServer) string{
		"resource anyscale_organization_user_role": func(url string, mock *mockOrgUserRoleServer) string {
			return orgUserRoleConfig(url, mock.email, "collaborator", "")
		},
		"data source anyscale_organization_user": func(url string, mock *mockOrgUserRoleServer) string {
			return testAccProviderBlock(url) + fmt.Sprintf(`
data "anyscale_organization_user" "u" {
  email = %q
}
`, mock.email)
		},
		"data source anyscale_organization_users": func(url string, _ *mockOrgUserRoleServer) string {
			return testAccProviderBlock(url) + `
data "anyscale_organization_users" "all" {}
`
		},
	}
	for name, config := range surfaces {
		t.Run(name, func(t *testing.T) {
			t.Run("lookup works", func(t *testing.T) {
				httpServer, mock := newMockOrgUserRoleServer(t)
				resource.Test(t, resource.TestCase{
					ProtoV6ProviderFactories: ProtoV6ProviderFactories,
					Steps:                    []resource.TestStep{{Config: config(httpServer.URL, mock)}},
				})
			})
			t.Run("lookup fails", func(t *testing.T) {
				httpServer, mock := newMockOrgUserRoleServer(t)
				mock.failSingular = true
				resource.Test(t, resource.TestCase{
					ProtoV6ProviderFactories: ProtoV6ProviderFactories,
					Steps: []resource.TestStep{{
						Config:      config(httpServer.URL, mock),
						ExpectError: regexp.MustCompile(`(?s)(HTTP|status)\s+500`),
					}},
				})
			})
		})
	}
}

// A data source looked up by email must return the configured spelling: the API
// lowercases emails, and Core rejects a result that changes a configured value.
// Positive control: the lowercase spelling, which was never affected.
func TestAccOrganizationUserDataSource_MixedCaseEmailLookup(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	for name, email := range map[string]string{
		"lowercase_control": "org-role-mock@example.com",
		"mixed_case":        "Org-Role-Mock@Example.com",
	} {
		t.Run(name, func(t *testing.T) {
			httpServer, _ := newMockOrgUserRoleServer(t)
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: testAccProviderBlock(httpServer.URL) + fmt.Sprintf(`
data "anyscale_organization_user" "u" {
  email = %q
}
`, email),
					Check: resource.TestCheckResourceAttr("data.anyscale_organization_user.u", "email", email),
				}},
			})
		})
	}
}
