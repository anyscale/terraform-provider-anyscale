package acctest

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// Mock acceptance test for data anyscale_iam_role, introduced by the
// v0.38.0 rename. The other Read paths — exact-match among "contains"
// results, null description, case-mismatch hint, archived-exclusion,
// walking past one page, role bindings disabled — are covered in
// resource_iam_role_grant_readcontract_mock_acc_test.go; only a name that
// matches several roles had no test. This one runs through resource.Test
// against roleBindingMockServer and needs no credentials.
func iamRoleDsConfig(m *roleBindingMockServer, name string) string {
	return m.providerBlock() + fmt.Sprintf(`
data "anyscale_iam_role" "test" {
  name = %q
}
`, name)
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

// The built-in owner role is renamed "Organization owner" -> "Admin" per
// environment, so both names are live at once. Either config name must resolve
// the built-in in either state, keeping the configured name in state.
func TestAccIAMRoleDataSource_MockOwnerRoleRename(t *testing.T) {
	const ownerID = "rol_zdti5dxw7jy7bsekx179sfe13b"
	for _, tc := range []struct{ stored, configured string }{
		{"Organization owner", "Admin"},              // not yet renamed (prod at time of writing)
		{"Admin", "Organization owner"},              // renamed; old config keeps working
		{"Admin", "Admin"},                           // renamed; exact match
		{"Organization owner", "Organization owner"}, // not renamed; exact match
	} {
		t.Run(tc.stored+"->"+tc.configured, func(t *testing.T) {
			m := newRoleBindingMockServer(t)
			m.addRole(roleBindingMockRole{ID: ownerID, Name: tc.stored, BuiltIn: true})
			m.addRole(roleBindingMockRole{ID: "rol_00000000000000000000000020", Name: "Organization collaborator", BuiltIn: true})
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: iamRoleDsConfig(m, tc.configured),
					ConfigStateChecks: []statecheck.StateCheck{
						statecheck.ExpectKnownValue("data.anyscale_iam_role.test", tfjsonpath.New("id"), knownvalue.StringExact(ownerID)),
						statecheck.ExpectKnownValue("data.anyscale_iam_role.test", tfjsonpath.New("name"), knownvalue.StringExact(tc.configured)),
					},
				}},
			})
		})
	}
}

// An organization's own role named "Admin" wins over the rename fallback: the
// name means that role there (the backend also refuses the rename then).
func TestAccIAMRoleDataSource_MockCustomAdminWins(t *testing.T) {
	m := newRoleBindingMockServer(t)
	m.addRole(roleBindingMockRole{ID: "rol_zdti5dxw7jy7bsekx179sfe13b", Name: "Organization owner", BuiltIn: true})
	m.addRole(roleBindingMockRole{ID: "rol_custom00000000000000000a", Name: "Admin"})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: iamRoleDsConfig(m, "Admin"),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("data.anyscale_iam_role.test", tfjsonpath.New("id"), knownvalue.StringExact("rol_custom00000000000000000a")),
			},
		}},
	})
}

// The fallback resolves only the built-in: a custom role that happens to carry
// the other name is not substituted.
func TestAccIAMRoleDataSource_MockRenameFallbackBuiltInOnly(t *testing.T) {
	m := newRoleBindingMockServer(t)
	m.addRole(roleBindingMockRole{ID: "rol_custom00000000000000000b", Name: "Organization owner"})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      iamRoleDsConfig(m, "Admin"),
			ExpectError: regexp.MustCompile(`Role Not Found`),
		}},
	})
}
