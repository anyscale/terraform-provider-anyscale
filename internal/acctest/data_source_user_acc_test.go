package acctest

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccUserDataSource_Basic(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccUserDataSourceConfig_basic(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Check basic user fields are populated
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "id"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "email"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "name"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "username"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "organization_permission_level"),
				),
			},
		},
	})
}

func TestAccUserDataSource_OrganizationData(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccUserDataSourceConfig_basic(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Check organization IDs list exists
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "organization_ids.#"),
					// Check organizations list exists
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "organizations.#"),
					// Check first organization has required fields (if at least one org exists)
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "organizations.0.id"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "organizations.0.name"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "organizations.0.public_identifier"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "organizations.0.default_cloud_id"),
				),
			},
		},
	})
}

func TestAccUserDataSource_CloudAccess(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccUserDataSourceConfig_basic(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Check cloud_ids list exists (should have at least one if test cloud is set)
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "cloud_ids.#"),
				),
			},
		},
	})
}

func TestAccUserDataSource_UserGroups(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccUserDataSourceConfig_basic(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Check user_group_ids list exists (may be empty as feature is not fully implemented)
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "user_group_ids.#"),
				),
			},
		},
	})
}

func TestAccUserDataSource_WithCloudReference(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccUserDataSourceConfig_withCloudReference(cloudID),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Check user data is populated
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "id"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "email"),
					// Check cloud data is populated
					resource.TestCheckResourceAttr("data.anyscale_cloud.test", "id", cloudID),
					resource.TestCheckResourceAttrSet("data.anyscale_cloud.test", "name"),
				),
			},
		},
	})
}

func TestAccUserDataSource_MultipleFields(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccUserDataSourceConfig_basic(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Comprehensive check of all fields
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "id"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "email"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "name"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "username"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "organization_permission_level"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "organization_ids.#"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "organizations.#"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "cloud_ids.#"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "user_group_ids.#"),
				),
			},
		},
	})
}

func TestAccUserDataSource_OutputsWork(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccUserDataSourceConfig_withOutputs(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Check user data is populated
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "id"),
					resource.TestCheckResourceAttrSet("data.anyscale_user.test", "email"),
					// Outputs should be set (verified by Terraform planning successfully)
				),
			},
		},
	})
}

// Configuration templates

func testAccUserDataSourceConfig_basic() string {
	return `
data "anyscale_user" "test" {
}
`
}

func testAccUserDataSourceConfig_withCloudReference(cloudID string) string {
	return `
data "anyscale_user" "test" {
}

data "anyscale_cloud" "test" {
  id = "` + cloudID + `"
}
`
}

func testAccUserDataSourceConfig_withOutputs() string {
	return `
data "anyscale_user" "test" {
}

output "user_id" {
  value = data.anyscale_user.test.id
}

output "user_email" {
  value = data.anyscale_user.test.email
}

output "user_org_count" {
  value = length(data.anyscale_user.test.organization_ids)
}

output "user_cloud_count" {
  value = length(data.anyscale_user.test.cloud_ids)
}

output "user_permission_level" {
  value = data.anyscale_user.test.organization_permission_level
}
`
}
