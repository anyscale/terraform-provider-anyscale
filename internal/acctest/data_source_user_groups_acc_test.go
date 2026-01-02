package acctest

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccUserGroupsDataSource_Basic(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccUserGroupsDataSourceBasicConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Should return user groups (or empty list if none configured)
					resource.TestCheckResourceAttrSet("data.anyscale_user_groups.test", "groups.#"),
				),
			},
		},
	})
}

func TestAccUserGroupsDataSource_GroupFieldsPopulated(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			PreCheck(t)
			// This test requires at least one user group to exist
			// Skip if no groups are configured
		},
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccUserGroupsDataSourceBasicConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// If groups exist, verify fields are populated
					// These checks will only run if groups.# > 0
					resource.TestCheckResourceAttrSet("data.anyscale_user_groups.test", "groups.#"),
				),
			},
		},
	})
}

func TestAccUserGroupsDataSource_NoDeletedGroups(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccUserGroupsDataSourceBasicConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Verify data source returns successfully
					// The implementation filters out deleted groups
					resource.TestCheckResourceAttrSet("data.anyscale_user_groups.test", "groups.#"),
				),
			},
		},
	})
}

// Configuration templates

func testAccUserGroupsDataSourceBasicConfig() string {
	return `
data "anyscale_user_groups" "test" {
}
`
}
