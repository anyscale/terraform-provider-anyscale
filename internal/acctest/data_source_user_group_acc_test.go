package acctest

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// Real-API coverage for the user group data sources, always against a group
// the test creates, never one that already exists in the org.

// TestAccUserGroupDataSource_basic reads a test-created group by ID and by
// name and checks both agree with the resource.
func TestAccUserGroupDataSource_basic(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	name := UniqueName(t, "ug-ds")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_user_group", "/api/v2/user_groups/%s"),
		Steps: []resource.TestStep{
			{
				Config: testAccUserGroupConfig(name) + `
data "anyscale_user_group" "by_id" {
  id = anyscale_user_group.test.id
}

data "anyscale_user_group" "by_name" {
  name = anyscale_user_group.test.name
}
`,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.CompareValuePairs("data.anyscale_user_group.by_id", tfjsonpath.New("id"), "anyscale_user_group.test", tfjsonpath.New("id"), compare.ValuesSame()),
					statecheck.CompareValuePairs("data.anyscale_user_group.by_name", tfjsonpath.New("id"), "anyscale_user_group.test", tfjsonpath.New("id"), compare.ValuesSame()),
					statecheck.ExpectKnownValue("data.anyscale_user_group.by_id", tfjsonpath.New("name"), knownvalue.StringExact(name)),
					statecheck.ExpectKnownValue("data.anyscale_user_group.by_id", tfjsonpath.New("source"), knownvalue.StringExact("user")),
					statecheck.ExpectKnownValue("data.anyscale_user_group.by_id", tfjsonpath.New("members"), knownvalue.ListSizeExact(0)),
					statecheck.ExpectKnownValue("data.anyscale_user_group.by_id", tfjsonpath.New("organization_role"), knownvalue.Null()),
				},
			},
		},
	})
}

// TestAccUserGroupsDataSource_basic checks a test-created group appears in the
// listing, under its own source filter and not under the other.
func TestAccUserGroupsDataSource_basic(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	name := UniqueName(t, "ug-list")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheck("anyscale_user_group", "/api/v2/user_groups/%s"),
		Steps: []resource.TestStep{
			{
				Config: testAccUserGroupConfig(name) + `
data "anyscale_user_groups" "user" {
  source     = "user"
  depends_on = [anyscale_user_group.test]
}

data "anyscale_user_groups" "scim" {
  source     = "scim"
  depends_on = [anyscale_user_group.test]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckUserGroupListed("data.anyscale_user_groups.user", true),
					testAccCheckUserGroupListed("data.anyscale_user_groups.scim", false),
				),
			},
		},
	})
}

func testAccCheckUserGroupListed(addr string, want bool) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		id := s.RootModule().Resources["anyscale_user_group.test"].Primary.ID
		ds, ok := s.RootModule().Resources[addr]
		if !ok {
			return fmt.Errorf("%s not in state", addr)
		}
		found := false
		for k, v := range ds.Primary.Attributes {
			if v == id && len(k) > len("groups.") && k[:len("groups.")] == "groups." {
				found = true
			}
		}
		if found != want {
			return fmt.Errorf("%s lists group %s = %v, want %v (groups.# = %s)", addr, id, found, want, ds.Primary.Attributes["groups.#"])
		}
		return nil
	}
}
