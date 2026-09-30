package acctest

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccOrganizationDataSource_Basic proves the zero-argument singleton
// returns id/name/public_identifier against real infra. default_cloud_id is
// legitimately nullable (the org may have no default cloud), so it is checked
// against anyscale_user in
// TestAccOrganizationDataSource_MatchesUserDataSourceOrganization instead.
func TestAccOrganizationDataSource_Basic(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationDataSourceConfig_basic(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.anyscale_organization.current", "id"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization.current", "name"),
					resource.TestCheckResourceAttrSet("data.anyscale_organization.current", "public_identifier"),
				),
			},
		},
	})
}

// TestAccOrganizationDataSource_MatchesUserDataSourceOrganization cross-checks
// the new singleton against anyscale_user's existing nested organizations[0]
// for the same authenticated token - both read the same userinfo endpoint, so
// id/name/public_identifier/default_cloud_id must agree. The default_cloud_id
// pair also holds when the org has no default cloud (both sides null). Guards
// against the two call sites silently drifting apart.
func TestAccOrganizationDataSource_MatchesUserDataSourceOrganization(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationDataSourceConfig_withUserComparison(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(
						"data.anyscale_organization.current", "id",
						"data.anyscale_user.test", "organizations.0.id",
					),
					resource.TestCheckResourceAttrPair(
						"data.anyscale_organization.current", "name",
						"data.anyscale_user.test", "organizations.0.name",
					),
					resource.TestCheckResourceAttrPair(
						"data.anyscale_organization.current", "public_identifier",
						"data.anyscale_user.test", "organizations.0.public_identifier",
					),
					resource.TestCheckResourceAttrPair(
						"data.anyscale_organization.current", "default_cloud_id",
						"data.anyscale_user.test", "organizations.0.default_cloud_id",
					),
				),
			},
		},
	})
}

// TestAccOrganizationDataSource_NoSelectorArgumentsAccepted guards that the
// schema stays zero-argument: supplying id (a would-be selector) must be
// rejected as a read-only attribute. If a selector argument is ever added as
// Optional, this test fails.
func TestAccOrganizationDataSource_NoSelectorArgumentsAccepted(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
data "anyscale_organization" "current" {
  id = "org_selector_not_accepted"
}
`,
				ExpectError: regexp.MustCompile(`Invalid Configuration for Read-Only Attribute`),
			},
		},
	})
}

func testAccOrganizationDataSourceConfig_basic() string {
	return `
data "anyscale_organization" "current" {
}
`
}

func testAccOrganizationDataSourceConfig_withUserComparison() string {
	return `
data "anyscale_organization" "current" {
}

data "anyscale_user" "test" {
}
`
}
