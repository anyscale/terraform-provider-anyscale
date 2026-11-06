package acctest

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// Mock acceptance tests for data anyscale_iam_role, introduced by the
// v0.38.0 rename, covering the Read paths the role-grant lifecycle tests do
// not: a custom role with no description, archived roles being excluded, a
// name that matches several roles, and role bindings being disabled for the
// org. They run through resource.Test against roleBindingMockServer and need
// no credentials. Query behavior (name matching, built_in defaults) is
// exercised end to end: the mock server applies the same filters the API
// would, so these tests would pass against a provider that queries correctly
// and fail against one that drops or skews a query parameter.
func iamRoleDsConfig(m *roleBindingMockServer, name string) string {
	return m.providerBlock() + fmt.Sprintf(`
data "anyscale_iam_role" "test" {
  name = %q
}
`, name)
}

// A custom (non-built-in) role with no description resolves to id + nulls.
func TestAccIAMRoleDataSource_MockOrgRole(t *testing.T) {
	m := newRoleBindingMockServer(t)
	m.addRole(roleBindingMockRole{ID: "rol_orgrole000000000000000000", Name: "custom_viewer", BuiltIn: false})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: iamRoleDsConfig(m, "custom_viewer"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.anyscale_iam_role.test", tfjsonpath.New("id"), knownvalue.StringExact("rol_orgrole000000000000000000")),
					statecheck.ExpectKnownValue("data.anyscale_iam_role.test", tfjsonpath.New("name"), knownvalue.StringExact("custom_viewer")),
					statecheck.ExpectKnownValue("data.anyscale_iam_role.test", tfjsonpath.New("built_in"), knownvalue.Bool(false)),
					statecheck.ExpectKnownValue("data.anyscale_iam_role.test", tfjsonpath.New("description"), knownvalue.Null()),
				},
			},
			{
				// Re-read is stable.
				Config: iamRoleDsConfig(m, "custom_viewer"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// An archived role is never found: the roles listing is what excludes it,
// and the data source must not widen the query to include it.
func TestAccIAMRoleDataSource_MockArchivedExcluded(t *testing.T) {
	m := newRoleBindingMockServer(t)
	m.addRole(roleBindingMockRole{ID: "rol_archived000000000000000000", Name: "retired", Archived: true})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      iamRoleDsConfig(m, "retired"),
				ExpectError: regexp.MustCompile(`(?s)Role Not Found.*No assignable role is named "retired"\. Archived roles are not listed\.`),
			},
		},
	})
}

// Two roles with one name cannot resolve to a single id.
func TestAccIAMRoleDataSource_MockMultipleMatches(t *testing.T) {
	m := newRoleBindingMockServer(t)
	m.addRole(roleBindingMockRole{ID: "rol_dup0000000000000000000a", Name: "clashing"})
	m.addRole(roleBindingMockRole{ID: "rol_dup0000000000000000000b", Name: "clashing"})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      iamRoleDsConfig(m, "clashing"),
				ExpectError: regexp.MustCompile(`(?s)Multiple Roles Found.*2 roles are named "clashing"`),
			},
		},
	})
}

// A 404 from the roles listing means role bindings are disabled for the
// org, and must surface as that, not as a listing error.
func TestAccIAMRoleDataSource_MockFeatureDisabled(t *testing.T) {
	m := newRoleBindingMockServer(t)
	m.setFlagOff(true)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      iamRoleDsConfig(m, "anything"),
				ExpectError: regexp.MustCompile(`(?s)Role bindings are not enabled.*This organization does not have role bindings enabled`),
			},
		},
	})
}
