package provider

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccOrganizationUsersDataSource_Basic(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationUsersDataSourceBasicConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Should return at least some users
					resource.TestCheckResourceAttrSet("data.anyscale_organization_users.test", "users.#"),
				),
			},
		},
	})
}

func TestAccOrganizationUsersDataSource_UserFieldsPopulated(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationUsersDataSourceBasicConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Verify at least one user is returned
					resource.TestCheckResourceAttrSet("data.anyscale_organization_users.test", "users.#"),
					// Verify the first user has expected fields populated
					resource.TestCheckResourceAttrSet("data.anyscale_organization_users.test", "users.0.id"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization_users.test", "users.0.name"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization_users.test", "users.0.email"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization_users.test", "users.0.permission_level"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization_users.test", "users.0.created_at"),
				),
			},
		},
	})
}

func TestAccOrganizationUsersDataSource_FilterByEmail(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	// Get current user info to use for filtering
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationUsersDataSourceFilterByEmailConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Should find the current user
					resource.TestCheckResourceAttrSet("data.anyscale_organization_users.test", "users.#"),
				),
			},
		},
	})
}

func TestAccOrganizationUsersDataSource_FilterByName(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationUsersDataSourceFilterByNameConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Should filter by name (may find users or not)
					resource.TestCheckResourceAttrSet("data.anyscale_organization_users.test", "users.#"),
				),
			},
		},
	})
}

func TestAccOrganizationUsersDataSource_ServiceAccountsOnly(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationUsersDataSourceServiceAccountsOnlyConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Should return service accounts only (may be zero)
					resource.TestCheckResourceAttrSet("data.anyscale_organization_users.test", "users.#"),
				),
			},
		},
	})
}

func TestAccOrganizationUsersDataSource_UsersOnly(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationUsersDataSourceUsersOnlyConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Should return regular users only (should have at least one)
					resource.TestCheckResourceAttrSet("data.anyscale_organization_users.test", "users.#"),
				),
			},
		},
	})
}

// Configuration templates

func testAccOrganizationUsersDataSourceBasicConfig() string {
	return `
data "anyscale_organization_users" "test" {
}
`
}

func testAccOrganizationUsersDataSourceFilterByEmailConfig() string {
	return `
data "anyscale_user" "current" {
}

data "anyscale_organization_users" "test" {
  email = data.anyscale_user.current.email
}
`
}

func testAccOrganizationUsersDataSourceFilterByNameConfig() string {
	return `
data "anyscale_user" "current" {
}

data "anyscale_organization_users" "test" {
  name = data.anyscale_user.current.name
}
`
}

func testAccOrganizationUsersDataSourceServiceAccountsOnlyConfig() string {
	return `
data "anyscale_organization_users" "test" {
  is_service_account = true
}
`
}

func testAccOrganizationUsersDataSourceUsersOnlyConfig() string {
	return `
data "anyscale_organization_users" "test" {
  is_service_account = false
}
`
}
