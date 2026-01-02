package acctest

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccOrganizationUserDataSource_ByEmail(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationUserDataSourceByEmailConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Verify user fields are populated
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "id"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "email"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "name"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "permission_level"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "created_at"),
					// Verify email matches current user
					resource.TestCheckResourceAttrPair(
						"data.anyscale_organization_user.test", "email",
						"data.anyscale_user.current", "email",
					),
				),
			},
		},
	})
}

func TestAccOrganizationUserDataSource_ByID(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationUserDataSourceByIDConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Verify user fields are populated
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "id"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "email"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "name"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "permission_level"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "created_at"),
					// Verify ID matches from list
					resource.TestCheckResourceAttrPair(
						"data.anyscale_organization_user.test", "id",
						"data.anyscale_organization_users.all", "users.0.id",
					),
				),
			},
		},
	})
}

func TestAccOrganizationUserDataSource_ByUserID(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationUserDataSourceByUserIDConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Verify user fields are populated
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "id"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "email"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "name"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "permission_level"),
					// Verify it matches from list
					resource.TestCheckResourceAttrPair(
						"data.anyscale_organization_user.test", "user_id",
						"data.anyscale_organization_users.all", "users.0.user_id",
					),
				),
			},
		},
	})
}

func TestAccOrganizationUserDataSource_AllFieldsPopulated(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationUserDataSourceByEmailConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Verify all fields are present
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "id"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "email"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "name"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "permission_level"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "created_at"),
				),
			},
		},
	})
}

func TestAccOrganizationUserDataSource_FromListToSingle(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' is set")
		return
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationUserDataSourceFromListToSingleConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Verify we can use the users list to look up a single user
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "id"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "email"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization_user.test", "name"),
					// Verify it matches the first user from the list
					resource.TestCheckResourceAttrPair(
						"data.anyscale_organization_user.test", "id",
						"data.anyscale_organization_users.all", "users.0.id",
					),
					resource.TestCheckResourceAttrPair(
						"data.anyscale_organization_user.test", "email",
						"data.anyscale_organization_users.all", "users.0.email",
					),
				),
			},
		},
	})
}

// Configuration templates

func testAccOrganizationUserDataSourceByEmailConfig() string {
	return `
data "anyscale_user" "current" {
}

data "anyscale_organization_user" "test" {
  email = data.anyscale_user.current.email
}
`
}

func testAccOrganizationUserDataSourceByIDConfig() string {
	return `
data "anyscale_organization_users" "all" {
}

data "anyscale_organization_user" "test" {
  id = data.anyscale_organization_users.all.users[0].id
}
`
}

func testAccOrganizationUserDataSourceByUserIDConfig() string {
	return `
data "anyscale_organization_users" "all" {
}

data "anyscale_organization_user" "test" {
  user_id = data.anyscale_organization_users.all.users[0].user_id
}
`
}

func testAccOrganizationUserDataSourceFromListToSingleConfig() string {
	return `
data "anyscale_organization_users" "all" {
}

data "anyscale_organization_user" "test" {
  email = data.anyscale_organization_users.all.users[0].email
}
`
}
