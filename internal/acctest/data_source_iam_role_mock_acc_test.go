package acctest

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
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
