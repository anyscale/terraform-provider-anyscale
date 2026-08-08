package acctest

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccOrganizationUsersDataSource_Basic previously asserted only that
// "users.#" was set, which holds even for an empty list - a data source that
// returned zero rows, or rows with every field left empty, would pass
// identically to a correctly working one. Replaced with a falsifiable
// assertion per the RBAC test-gap review: every returned row must have a
// non-empty id and email, which fails if the data source returns rows it did
// not actually populate.
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
					resource.TestCheckResourceAttrSet("data.anyscale_organization_users.test", "users.#"),
					testAccCheckAllOrgUsersHaveIDAndEmail("data.anyscale_organization_users.test"),
				),
			},
		},
	})
}

// orgUsersRowCount parses and validates usersResourceName's "users.#", shared
// by every check below that scans the list by index - all of them need the
// same resource lookup, count parse, and non-empty guard before their loop.
func orgUsersRowCount(s *terraform.State, usersResourceName string) (*terraform.ResourceState, int, error) {
	rs, ok := s.RootModule().Resources[usersResourceName]
	if !ok {
		return nil, 0, fmt.Errorf("resource not found: %s", usersResourceName)
	}
	count, err := strconv.Atoi(rs.Primary.Attributes["users.#"])
	if err != nil {
		return nil, 0, fmt.Errorf("failed to parse users.#: %w", err)
	}
	if count == 0 {
		return nil, 0, fmt.Errorf("expected at least one user (the current user), got 0")
	}
	return rs, count, nil
}

// testAccCheckAllOrgUsersHaveIDAndEmail asserts every row in usersResourceName's
// "users" list has a non-empty id and email - falsifiable against a data
// source that returns unpopulated rows, unlike a bare users.# presence check.
func testAccCheckAllOrgUsersHaveIDAndEmail(usersResourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, count, err := orgUsersRowCount(s, usersResourceName)
		if err != nil {
			return err
		}
		for i := 0; i < count; i++ {
			id := rs.Primary.Attributes[fmt.Sprintf("users.%d.id", i)]
			email := rs.Primary.Attributes[fmt.Sprintf("users.%d.email", i)]
			if id == "" {
				return fmt.Errorf("users.%d.id is empty - row was returned but not populated", i)
			}
			if email == "" {
				return fmt.Errorf("users.%d.email is empty - row was returned but not populated", i)
			}
		}
		return nil
	}
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
// CORRECTED: the original version of this test asserted unfiltered count ==
// (true count) + (false count), which is not the real contract and fails on
// any org with at least one user - confirmed with a live, read-only backend
// trace, not assumed. Omitting is_service_account is NOT "return everyone";
// the backend (and this data source's own schema description) documents it
// as shorthand for is_service_account=false. So the correct, contract-true
// invariant is unfiltered == false, not true+false == unfiltered.
//
// That alone would not catch a filter that silently ignores its value: a
// no-op implementation that always behaves as "false" would still satisfy
// unfiltered == false. So this also asserts true != unfiltered whenever the
// org has at least one user - the moment is_service_account=true silently
// behaves like the false/no-filter case, this fails, whether or not the org
// happens to have any real service accounts today. Neither assertion depends
// on the org's actual service-account population, unlike the version this
// replaces.
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

		rs, count, err := orgUsersRowCount(s, usersResourceName)
		if err != nil {
			return err
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

		rs, count, err := orgUsersRowCount(s, usersResourceName)
		if err != nil {
			return err
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

// testAccCheckOrgUsersServiceAccountPartitionSumsToTotal asserts the real,
// live-confirmed is_service_account contract: omitting the filter behaves
// exactly like is_service_account=false (not "return everyone" - see the
// CORRECTED note on the calling test), and is_service_account=true is not
// silently ignored in favor of that same default. Two checks, neither
// dependent on how many real service accounts the org happens to have today:
//
//  1. len(all) == len(regular_users) - the documented default. If a future
//     regression made "no filter" start returning everyone (service accounts
//     included), this catches it without needing any service accounts to
//     exist.
//  2. len(service_accounts) != len(all) whenever the org has at least one
//     user - if is_service_account=true silently behaved like false/omitted
//     instead of genuinely filtering, it would return the exact same count
//     as the unfiltered/false case. This does not require the org to have
//     any real service accounts: a working filter legitimately returning 0
//     still satisfies 0 != total (given total > 0), while a no-op filter
//     returning the full list would equal total and correctly fail here.
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
			return fmt.Errorf("expected at least one user in the org, got 0 - cannot prove the filter's behavior against an empty list")
		}

		serviceAccounts, err := countOf(s, "data.anyscale_organization_users.service_accounts")
		if err != nil {
			return err
		}
		regularUsers, err := countOf(s, "data.anyscale_organization_users.regular_users")
		if err != nil {
			return err
		}

		if regularUsers != total {
			return fmt.Errorf(
				"is_service_account=false (%d) != unfiltered total (%d) - omitting the filter no longer matches its documented default of is_service_account=false",
				regularUsers, total,
			)
		}
		if serviceAccounts == total {
			return fmt.Errorf(
				"is_service_account=true (%d) equals the unfiltered/false total (%d) - the filter looks like it is being silently ignored and defaulting to false instead of genuinely filtering",
				serviceAccounts, total,
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
