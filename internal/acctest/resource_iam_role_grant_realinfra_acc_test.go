package acctest

import (
	"fmt"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Real-API coverage for the role-binding shapes TestAccIAMRoleGrantResource_RealAPI
// (group principal, cloud scope) does not reach: a user principal on project scope,
// a group on organization scope, the composite import ID for both, and the
// anyscale_iam_role and anyscale_iam_role_binding data sources. Needs a converted
// test organization with role bindings enabled; skips otherwise.
//
//	TF_ACC=1 ANYSCALE_TEST_ORG_MODE=converted ANYSCALE_TEST_ORG_NAME=<org> \
//	ANYSCALE_TEST_USER_EMAIL=<disposable member> \
//	go test ./internal/acctest/ -run '^TestAccIAMRoleGrantResource_RealAPI_' -v
//
// anyscale_iam_role_binding on cloud or organization scope is deliberately not
// exercised for real: it is authoritative over the whole scope, and the only
// cloud and organization available are the shared fixtures.

// realPolicyHas reports whether the scope's policy holds role for the principal.
func realPolicyHas(t *testing.T, scopeType, scopeID, role, principalType, principalID string) bool {
	t.Helper()
	return slices.Contains(realPolicyFacts(t, scopeType, scopeID), pmFact(role, principalType, principalID))
}

// stateAttr reads one attribute of a resource or data source from state.
func stateAttr(s *terraform.State, address, attr string) (string, error) {
	r, ok := s.RootModule().Resources[address]
	if !ok {
		return "", fmt.Errorf("%s not in state", address)
	}
	v, ok := r.Primary.Attributes[attr]
	if !ok || v == "" {
		return "", fmt.Errorf("%s.%s is empty", address, attr)
	}
	return v, nil
}

// TestAccIAMRoleGrantResource_RealAPI_UserPrincipalProject grants a disposable
// member Project reader on a throwaway project, looked up through the
// anyscale_iam_organization_user and anyscale_iam_role data sources, reads it
// back through the anyscale_iam_role_binding data source, imports it by the
// composite ID, and checks destroy revokes it.
func TestAccIAMRoleGrantResource_RealAPI_UserPrincipalProject(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	skipUnlessTestOrgMode(t, testOrgModeConverted, "role binding writes answer 409 until the organization converts")
	PreCheck(t)
	skipIfRoleBindingsDisabled(t)
	email := requireRealInfraTestUser(t)

	projectID, _, err := CreateEphemeralTestProjectForCloud(t, GetTestCloudID(t))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	var userID, roleID string

	cfg := fmt.Sprintf(`
data "anyscale_iam_organization_user" "test" {
  email = %q
}

data "anyscale_iam_role" "test" {
  name = "Project reader"
}

resource "anyscale_iam_role_grant" "test" {
  user_id    = data.anyscale_iam_organization_user.test.user_id
  role_id    = data.anyscale_iam_role.test.id
  project_id = %q
}

data "anyscale_iam_role_binding" "test" {
  project_id = anyscale_iam_role_grant.test.project_id
}
`, email, projectID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		ErrorCheck:               orgModeErrorCheck(t),
		CheckDestroy: func(*terraform.State) error {
			if realPolicyHas(t, "project", projectID, roleID, "user", userID) {
				return fmt.Errorf("user %s still holds %s on %s after destroy", userID, roleID, projectID)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_iam_role.test", "built_in", "true"),
					resource.TestCheckResourceAttrPair("anyscale_iam_role_grant.test", "user_id", "data.anyscale_iam_organization_user.test", "user_id"),
					resource.TestCheckNoResourceAttr("anyscale_iam_role_grant.test", "user_group_id"),
					func(s *terraform.State) error {
						var err error
						if userID, err = stateAttr(s, "anyscale_iam_role_grant.test", "user_id"); err != nil {
							return err
						}
						if roleID, err = stateAttr(s, "anyscale_iam_role_grant.test", "role_id"); err != nil {
							return err
						}
						if !realPolicyHas(t, "project", projectID, roleID, "user", userID) {
							return fmt.Errorf("API does not list %s holding %s on %s", userID, roleID, projectID)
						}
						// The data source reads the grant back by role ID.
						return resource.TestCheckTypeSetElemAttr("data.anyscale_iam_role_binding.test",
							fmt.Sprintf("roles.%s.user_ids.*", roleID), userID)(s)
					},
				),
			},
			{
				ResourceName:      "anyscale_iam_role_grant.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) {
					return fmt.Sprintf("project/%s/user/%s/%s", projectID, userID, roleID), nil
				},
			},
		},
	})
}

// TestAccIAMRoleGrantResource_RealAPI_OrganizationScopeGroup grants an empty
// group a role on the organization, so no one gains access, imports it by the
// composite ID, and checks that destroying the grant alone revokes it.
func TestAccIAMRoleGrantResource_RealAPI_OrganizationScopeGroup(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	skipUnlessTestOrgMode(t, testOrgModeConverted, "role binding writes answer 409 until the organization converts")
	PreCheck(t)
	skipIfRoleBindingsDisabled(t)

	groupName := UniqueName(t, "rborg")
	var orgID, groupID, roleID string

	groupOnly := fmt.Sprintf(`
data "anyscale_organization" "current" {}

data "anyscale_iam_role" "test" {
  name = "Billing/consumption reader"
}

resource "anyscale_iam_user_group" "test" {
  name = %q
}
`, groupName)
	cfg := groupOnly + `
resource "anyscale_iam_role_grant" "test" {
  user_group_id   = anyscale_iam_user_group.test.id
  role_id         = data.anyscale_iam_role.test.id
  organization_id = data.anyscale_organization.current.id
}
`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		ErrorCheck:               orgModeErrorCheck(t),
		CheckDestroy: func(*terraform.State) error {
			if orgID != "" && realPolicyHas(t, "organization", orgID, roleID, "user_group", groupID) {
				return fmt.Errorf("group %s still holds %s on %s after destroy", groupID, roleID, orgID)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("anyscale_iam_role_grant.test", "cloud_id"),
					resource.TestCheckNoResourceAttr("anyscale_iam_role_grant.test", "project_id"),
					func(s *terraform.State) error {
						var err error
						if orgID, err = stateAttr(s, "anyscale_iam_role_grant.test", "organization_id"); err != nil {
							return err
						}
						if groupID, err = stateAttr(s, "anyscale_iam_role_grant.test", "user_group_id"); err != nil {
							return err
						}
						if roleID, err = stateAttr(s, "anyscale_iam_role_grant.test", "role_id"); err != nil {
							return err
						}
						if !realPolicyHas(t, "organization", orgID, roleID, "user_group", groupID) {
							return fmt.Errorf("API does not list %s holding %s on %s", groupID, roleID, orgID)
						}
						return nil
					},
				),
			},
			{
				ResourceName:      "anyscale_iam_role_grant.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) {
					return fmt.Sprintf("organization/%s/user_group/%s/%s", orgID, groupID, roleID), nil
				},
			},
			{
				// Destroy only the grant while the group still exists, so the
				// revoke is the provider's own Delete, not a group cascade.
				Config: groupOnly,
				Check: func(*terraform.State) error {
					if realPolicyHas(t, "organization", orgID, roleID, "user_group", groupID) {
						return fmt.Errorf("group %s still holds %s on %s after the grant was destroyed", groupID, roleID, orgID)
					}
					return nil
				},
			},
		},
	})
}
