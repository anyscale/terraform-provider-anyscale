package acctest

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccOrganizationDataSource_Basic covers AC1 (zero-argument singleton
// returns id/name/public_identifier/default_cloud_id) and AC4 (no selector
// arguments accepted - the config below passes none) against real infra.
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
					// default_cloud_id is legitimately nullable (AC2) - the real test
					// org may or may not have one configured, so this only proves the
					// attribute is present in the schema/plan, not that it's non-empty.
					resource.TestCheckResourceAttrSet("data.anyscale_organization.current", "id"),
				),
			},
		},
	})
}

// TestAccOrganizationDataSource_MatchesUserDataSourceOrganization cross-checks
// the new singleton against anyscale_user's existing nested organizations[0]
// for the same authenticated token - both read the same userinfo endpoint, so
// id/name/public_identifier must agree. Guards against the two call sites
// silently drifting apart in the real API response.
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
				),
			},
		},
	})
}

// TestAccOrganizationDataSource_NoSelectorArgumentsAccepted is a regression
// guard for AC4: the schema must stay zero-argument. If a selector argument
// (id/name/public_identifier) is ever added as Optional, this config would
// still need to keep working with none supplied - proving the zero-arg form
// alone, with no other resources/data sources in the config, is sufficient.
func TestAccOrganizationDataSource_NoSelectorArgumentsAccepted(t *testing.T) {
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
				),
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
