package acctest

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccUserGroupDataSource_ByID(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	groupID := os.Getenv("ANYSCALE_TEST_USER_GROUP_ID")
	if groupID == "" {
		t.Skip("ANYSCALE_TEST_USER_GROUP_ID not set, skipping test")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccUserGroupDataSourceByIDConfig(groupID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_user_group.test", "id", groupID),
					resource.TestCheckResourceAttrSet("data.anyscale_user_group.test", "name"),
					resource.TestCheckResourceAttrSet("data.anyscale_user_group.test", "org_id"),
					resource.TestCheckResourceAttrSet("data.anyscale_user_group.test", "created_at"),
					resource.TestCheckResourceAttrSet("data.anyscale_user_group.test", "updated_at"),
				),
			},
		},
	})
}

func TestAccUserGroupDataSource_ByName(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	groupName := os.Getenv("ANYSCALE_TEST_USER_GROUP_NAME")
	if groupName == "" {
		t.Skip("ANYSCALE_TEST_USER_GROUP_NAME not set, skipping test")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccUserGroupDataSourceByNameConfig(groupName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_user_group.test", "name", groupName),
					resource.TestCheckResourceAttrSet("data.anyscale_user_group.test", "id"),
					resource.TestCheckResourceAttrSet("data.anyscale_user_group.test", "org_id"),
					resource.TestCheckResourceAttrSet("data.anyscale_user_group.test", "created_at"),
					resource.TestCheckResourceAttrSet("data.anyscale_user_group.test", "updated_at"),
				),
			},
		},
	})
}

func TestAccUserGroupDataSource_FromListToSingle(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			PreCheck(t)
			// This test requires at least one user group to exist
			t.Skip("Skipping test - requires user groups to be configured")
		},
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccUserGroupDataSourceFromListToSingleConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Verify we can use the groups list to look up a single group
					resource.TestCheckResourceAttrSet("data.anyscale_user_group.test", "id"),
					resource.TestCheckResourceAttrSet("data.anyscale_user_group.test", "name"),
				),
			},
		},
	})
}

// Configuration templates

func testAccUserGroupDataSourceByIDConfig(groupID string) string {
	return fmt.Sprintf(`
data "anyscale_user_group" "test" {
  id = "%s"
}
`, groupID)
}

func testAccUserGroupDataSourceByNameConfig(groupName string) string {
	return fmt.Sprintf(`
data "anyscale_user_group" "test" {
  name = "%s"
}
`, groupName)
}

func testAccUserGroupDataSourceFromListToSingleConfig() string {
	return `
data "anyscale_user_groups" "all" {
}

data "anyscale_user_group" "test" {
  id = length(data.anyscale_user_groups.all.groups) > 0 ? data.anyscale_user_groups.all.groups[0].id : "skip"
}
`
}
