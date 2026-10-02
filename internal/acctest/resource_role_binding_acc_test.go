package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Real-API coverage for anyscale_role_binding and anyscale_role. Role
// bindings are behind a feature flag; these tests skip when the test org does
// not have it, and run unchanged once it does. They bind a role to a fresh
// group with no members, so the grant reaches no one.
//
//	TF_ACC=1 go test ./internal/acctest/ -run '^TestAccRoleBindingResource_RealAPI$' -v

// skipIfRoleBindingsDisabled skips when the org does not have role bindings,
// which every role_bindings and roles route reports as a 404. Any other
// failure is a test failure, not a skip.
func skipIfRoleBindingsDisabled(t *testing.T) {
	t.Helper()
	status, body := userGroupAPI(t, "GET", "/api/v2/roles/?count=1", nil)
	switch status {
	case http.StatusOK:
	case http.StatusNotFound:
		t.Skip("role bindings are not enabled for the test organization (GET /api/v2/roles/ answered 404)")
	default:
		t.Fatalf("check role bindings enabled: status %d: %s", status, truncateBody(string(body), 256))
	}
}

// firstBuiltInRoleName returns the name of a built-in role to bind. Which
// built-ins exist is data, not code, so the test picks one at run time.
func firstBuiltInRoleName(t *testing.T) string {
	t.Helper()
	status, body := userGroupAPI(t, "GET", "/api/v2/roles/?"+url.Values{"count": {"50"}, "include_built_in": {"true"}}.Encode(), nil)
	if status != http.StatusOK {
		t.Fatalf("list roles: status %d: %s", status, truncateBody(string(body), 256))
	}
	var resp struct {
		Results []struct {
			Name    string `json:"name"`
			BuiltIn bool   `json:"built_in"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("parse roles: %v", err)
	}
	for _, r := range resp.Results {
		if r.BuiltIn {
			return r.Name
		}
	}
	t.Fatalf("no built-in role in the first page of %d roles", len(resp.Results))
	return ""
}

// roleBindingIDsForGroup lists the binding IDs a group holds on a cloud,
// outside Terraform.
func roleBindingIDsForGroup(t *testing.T, cloudID, groupID string) []string {
	t.Helper()
	status, body := userGroupAPI(t, "GET", fmt.Sprintf("/api/v2/role_bindings/cloud/%s/principals/user_group/%s?count=50", cloudID, groupID), nil)
	if status != http.StatusOK {
		t.Fatalf("list bindings: status %d: %s", status, truncateBody(string(body), 256))
	}
	var resp struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("parse bindings: %v", err)
	}
	ids := make([]string, 0, len(resp.Results))
	for _, r := range resp.Results {
		ids = append(ids, r.ID)
	}
	return ids
}

func testAccRoleBindingConfig(groupName, roleName, cloudID string, withBinding bool) string {
	cfg := fmt.Sprintf(`
resource "anyscale_user_group" "test" {
  name = %q
}

data "anyscale_role" "test" {
  name = %q
}
`, groupName, roleName)
	if withBinding {
		cfg += fmt.Sprintf(`
resource "anyscale_role_binding" "test" {
  user_group_id = anyscale_user_group.test.id
  role_id       = data.anyscale_role.test.id
  cloud_id      = %q
}
`, cloudID)
	}
	return cfg
}

// TestAccRoleBindingResource_RealAPI binds a built-in role to an empty group
// on the test cloud, imports it, recreates it after an out-of-band revoke, and
// destroys only the binding while the group still exists, so the revoke is
// observed through the provider's own Delete rather than a group cascade.
func TestAccRoleBindingResource_RealAPI(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	PreCheck(t)
	skipIfRoleBindingsDisabled(t)

	cloudID := GetTestCloudID(t)
	roleName := firstBuiltInRoleName(t)
	groupName := UniqueName(t, "rb")
	var groupID, bindingID string

	inAPI := func(want bool) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			g, ok := s.RootModule().Resources["anyscale_user_group.test"]
			if !ok {
				return fmt.Errorf("anyscale_user_group.test not in state")
			}
			groupID = g.Primary.ID
			ids := roleBindingIDsForGroup(t, cloudID, groupID)
			if !want {
				if len(ids) != 0 {
					return fmt.Errorf("group %s still holds %v on %s after the binding was destroyed", groupID, ids, cloudID)
				}
				return nil
			}
			b, ok := s.RootModule().Resources["anyscale_role_binding.test"]
			if !ok {
				return fmt.Errorf("anyscale_role_binding.test not in state")
			}
			bindingID = b.Primary.ID
			for _, id := range ids {
				if id == bindingID {
					return nil
				}
			}
			return fmt.Errorf("binding %s not listed for group %s on %s (listed %v)", bindingID, groupID, cloudID, ids)
		}
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccRoleBindingConfig(groupName, roleName, cloudID, true),
				Check:  inAPI(true),
			},
			{
				ResourceName:      "anyscale_role_binding.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				PreConfig: func() {
					if status, body := userGroupAPI(t, "DELETE", "/api/v2/role_bindings/"+bindingID, nil); status != http.StatusNoContent {
						t.Fatalf("revoke %s out of band: status %d: %s", bindingID, status, truncateBody(string(body), 256))
					}
				},
				Config: testAccRoleBindingConfig(groupName, roleName, cloudID, true),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_role_binding.test", plancheck.ResourceActionCreate),
					},
				},
				Check: inAPI(true),
			},
			{
				Config: testAccRoleBindingConfig(groupName, roleName, cloudID, false),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_role_binding.test", plancheck.ResourceActionDestroy),
					},
				},
				Check: inAPI(false),
			},
		},
	})
}

// The real-API test's skip must key on the flag-off 404 and nothing else:
// skipping on a working org would make the test vacuous once the flag is on.
func TestSkipIfRoleBindingsDisabled(t *testing.T) {
	for _, tc := range []struct {
		name     string
		flagOff  bool
		wantSkip bool
	}{{"flag off skips", true, true}, {"flag on runs", false, false}} {
		m := newRoleBindingMockServer(t)
		m.setFlagOff(tc.flagOff)
		t.Setenv("ANYSCALE_API_URL", m.URL)
		t.Setenv("ANYSCALE_CLI_TOKEN", "test-token")
		reached := false
		t.Run(tc.name, func(t *testing.T) {
			skipIfRoleBindingsDisabled(t)
			reached = true
		})
		if reached == tc.wantSkip {
			t.Errorf("%s: reached past the skip = %v, want %v", tc.name, reached, !tc.wantSkip)
		}
	}
}
