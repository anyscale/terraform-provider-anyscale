package acctest

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccOrganizationUsersDataSource_Basic(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
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
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
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

// TestAccOrganizationUsersDataSource_FilterByEmail is DS-OU-6's mutation-proof
// guard for the email filter. The old version only asserted users.# was set,
// which passes even if the filter is a silent no-op (the current user is
// always in the unfiltered list too). This asserts every returned user's email
// actually contains the filter substring, per the schema's own "case-insensitive
// partial match" contract.
func TestAccOrganizationUsersDataSource_FilterByEmail(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationUsersDataSourceFilterByEmailConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.anyscale_organization_users.test", "users.#"),
					testAccCheckAllOrgUsersEmailContains("data.anyscale_organization_users.test", "data.anyscale_user.current", "email"),
				),
			},
		},
	})
}

// TestAccOrganizationUsersDataSource_FilterByName is DS-OU-6's mutation-proof
// guard for the name filter - same shape as FilterByEmail above.
func TestAccOrganizationUsersDataSource_FilterByName(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationUsersDataSourceFilterByNameConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.anyscale_organization_users.test", "users.#"),
					testAccCheckAllOrgUsersNameContains("data.anyscale_organization_users.test", "data.anyscale_user.current", "name"),
				),
			},
		},
	})
}

// TestAccOrganizationUsersDataSource_ServiceAccountFilterPartitionsUsers is
// DS-OU-6's mutation-proof guard for is_service_account, replacing the old
// _ServiceAccountsOnly and _UsersOnly tests. Those only asserted users.# was
// set, which is unfalsifiable in this test org: it currently has zero service
// accounts, so a real is_service_account=true filter and a completely broken
// no-op filter both "pass" (0 and 2 users respectively both count as "set").
// There is also no anyscale_service_account resource in this provider to stand
// up a real fixture, and is_service_account is filter-input only - it is not
// surfaced as a per-item output attribute, so no per-item assertion is
// possible either.
//
// Instead this proves the filter actually partitions the full user list:
// unfiltered count must equal (service-accounts-only count) + (regular-users-only
// count). A no-op filter would make both the true and false variants return the
// SAME full list, so their counts would sum to 2x the total instead of exactly
// the total - this fails today whenever the org has at least one user (it has
// two), independent of whether any service accounts currently exist.
func TestAccOrganizationUsersDataSource_ServiceAccountFilterPartitionsUsers(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationUsersDataSourcePartitionConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.anyscale_organization_users.all", "users.#"),
					testAccCheckOrgUsersServiceAccountPartitionSumsToTotal(),
				),
			},
		},
	})
}

// testAccCheckAllOrgUsersEmailContains asserts every user in usersResourceName's
// "users" list has an email containing the value of filterResourceName's
// filterAttr, matching the plural DS's own "case-insensitive partial match"
// contract for the email filter.
func testAccCheckAllOrgUsersEmailContains(usersResourceName, filterResourceName, filterAttr string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		filterRS, ok := s.RootModule().Resources[filterResourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", filterResourceName)
		}
		filterValue := strings.ToLower(filterRS.Primary.Attributes[filterAttr])

		rs, ok := s.RootModule().Resources[usersResourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", usersResourceName)
		}
		count, err := strconv.Atoi(rs.Primary.Attributes["users.#"])
		if err != nil {
			return fmt.Errorf("failed to parse users.#: %w", err)
		}
		if count == 0 {
			return fmt.Errorf("expected at least one user (the current user), got 0")
		}
		for i := 0; i < count; i++ {
			email := strings.ToLower(rs.Primary.Attributes[fmt.Sprintf("users.%d.email", i)])
			if !strings.Contains(email, filterValue) {
				return fmt.Errorf("users.%d.email = %q, want it to contain %q (email filter did not narrow the result)", i, email, filterValue)
			}
		}
		return nil
	}
}

// testAccCheckAllOrgUsersNameContains is the name-filter sibling of
// testAccCheckAllOrgUsersEmailContains above.
func testAccCheckAllOrgUsersNameContains(usersResourceName, filterResourceName, filterAttr string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		filterRS, ok := s.RootModule().Resources[filterResourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", filterResourceName)
		}
		filterValue := strings.ToLower(filterRS.Primary.Attributes[filterAttr])

		rs, ok := s.RootModule().Resources[usersResourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", usersResourceName)
		}
		count, err := strconv.Atoi(rs.Primary.Attributes["users.#"])
		if err != nil {
			return fmt.Errorf("failed to parse users.#: %w", err)
		}
		if count == 0 {
			return fmt.Errorf("expected at least one user (the current user), got 0")
		}
		for i := 0; i < count; i++ {
			name := strings.ToLower(rs.Primary.Attributes[fmt.Sprintf("users.%d.name", i)])
			if !strings.Contains(name, filterValue) {
				return fmt.Errorf("users.%d.name = %q, want it to contain %q (name filter did not narrow the result)", i, name, filterValue)
			}
		}
		return nil
	}
}

// testAccCheckOrgUsersServiceAccountPartitionSumsToTotal asserts
// len(service_accounts) + len(regular_users) == len(all), proving
// is_service_account genuinely partitions the full list rather than being a
// no-op that returns everything regardless of its value.
func testAccCheckOrgUsersServiceAccountPartitionSumsToTotal() resource.TestCheckFunc {
	countOf := func(s *terraform.State, resourceName string) (int, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return 0, fmt.Errorf("resource not found: %s", resourceName)
		}
		count, err := strconv.Atoi(rs.Primary.Attributes["users.#"])
		if err != nil {
			return 0, fmt.Errorf("failed to parse users.# on %s: %w", resourceName, err)
		}
		return count, nil
	}

	return func(s *terraform.State) error {
		total, err := countOf(s, "data.anyscale_organization_users.all")
		if err != nil {
			return err
		}
		if total == 0 {
			return fmt.Errorf("expected at least one user in the org, got 0 - cannot prove partitioning against an empty list")
		}

		serviceAccounts, err := countOf(s, "data.anyscale_organization_users.service_accounts")
		if err != nil {
			return err
		}
		regularUsers, err := countOf(s, "data.anyscale_organization_users.regular_users")
		if err != nil {
			return err
		}

		if serviceAccounts+regularUsers != total {
			return fmt.Errorf(
				"is_service_account=true (%d) + is_service_account=false (%d) = %d, want exactly %d (unfiltered total) - the filter is not partitioning the list, likely a no-op returning everything regardless of value",
				serviceAccounts, regularUsers, serviceAccounts+regularUsers, total,
			)
		}
		return nil
	}
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

func testAccOrganizationUsersDataSourcePartitionConfig() string {
	return `
data "anyscale_organization_users" "all" {
}

data "anyscale_organization_users" "service_accounts" {
  is_service_account = true
}

data "anyscale_organization_users" "regular_users" {
  is_service_account = false
}
`
}
