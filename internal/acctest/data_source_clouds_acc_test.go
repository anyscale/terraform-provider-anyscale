package acctest

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccCloudsDataSource_NoFilters(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudsDataSourceNoFiltersConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Should return at least some clouds
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.#"),
				),
			},
		},
	})
}

// TestAccCloudsDataSource_FilterByProvider is DS-CLOUD-1's mutation-proof
// acceptance guard for cloud_provider. The old version only asserted clouds.#
// was set for both an "AWS" and a "GCP" query, which passes even if the filter
// is a complete no-op (this org's one real cloud is AWS - a no-op returns it
// for both queries, and clouds.# is "1" either way, still "set"). This asserts
// exclusion instead: a non-matching provider must return zero clouds, and a
// matching provider must return a list that includes the known test cloud.
func TestAccCloudsDataSource_FilterByProvider(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// The known test cloud is AWS - GCP must exclude it entirely.
				Config: testAccCloudsDataSourceFilterByProviderConfig("GCP"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_clouds.test", "clouds.#", "0"),
				),
			},
			{
				// AWS must include the known test cloud.
				Config: testAccCloudsDataSourceFilterByProviderConfig("AWS"),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckCloudsListIncludesID("data.anyscale_clouds.test", cloudID),
				),
			},
		},
	})
}

// TestAccCloudsDataSource_FilterByRegion is DS-CLOUD-1's mutation-proof
// acceptance guard for region - same shape as FilterByProvider above. The old
// version was worse than presence-only: its body never queried anyscale_clouds
// with a region filter at all, it looked up the singular anyscale_cloud by ID
// and checked an unrelated attribute, so it never exercised this code path.
func TestAccCloudsDataSource_FilterByRegion(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// The known test cloud is us-east-2 - a different region must exclude it.
				Config: testAccCloudsDataSourceFilterByRegionConfig("us-west-2"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_clouds.test", "clouds.#", "0"),
				),
			},
			{
				// The matching region must include the known test cloud.
				Config: testAccCloudsDataSourceFilterByRegionConfig("us-east-2"),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckCloudsListIncludesID("data.anyscale_clouds.test", cloudID),
				),
			},
		},
	})
}

// TestAccCloudsDataSource_FilterByNameContains is DS-CLOUD-1's mutation-proof
// acceptance guard for name_contains - same shape as the two above, using a
// substring that cannot match the real test cloud's name for the negative case.
func TestAccCloudsDataSource_FilterByNameContains(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)
	cloudName := GetTestCloudName(t)

	nameContains := cloudName
	if len(cloudName) > 5 {
		nameContains = cloudName[:5]
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// A substring guaranteed absent from the real cloud's name must exclude it.
				Config: testAccCloudsDataSourceFilterByNameContainsConfig("definitely-not-a-real-cloud-name-zzz"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_clouds.test", "clouds.#", "0"),
				),
			},
			{
				// A real substring of the test cloud's name must include it.
				Config: testAccCloudsDataSourceFilterByNameContainsConfig(nameContains),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckCloudsListIncludesID("data.anyscale_clouds.test", cloudID),
				),
			},
		},
	})
}

// testAccCheckCloudsListIncludesID asserts the given cloud ID is present
// somewhere in resourceName's "clouds" list, rather than assuming a specific
// count or index - robust to the org gaining more clouds later.
func testAccCheckCloudsListIncludesID(resourceName, cloudID string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resourceName)
		}
		count, err := strconv.Atoi(rs.Primary.Attributes["clouds.#"])
		if err != nil {
			return fmt.Errorf("failed to parse clouds.#: %w", err)
		}
		for i := 0; i < count; i++ {
			if rs.Primary.Attributes[fmt.Sprintf("clouds.%d.id", i)] == cloudID {
				return nil
			}
		}
		return fmt.Errorf("expected cloud %s to be present in %s's %d result(s), but it was not - the filter incorrectly excluded a matching cloud", cloudID, resourceName, count)
	}
}

func TestAccCloudsDataSource_CloudFieldsPopulated(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	// This test requires at least one cloud to exist in the account
	// Skip if no clouds are available
	_ = GetAnyCloudID(t) // Will skip the test if no clouds exist

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudsDataSourceNoFiltersConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Verify at least one cloud is returned
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.#"),
					// Verify the first cloud has expected fields populated
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.0.id"),
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.0.name"),
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.0.cloud_provider"),
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.0.compute_stack"),
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.0.region"),
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.0.status"),
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.0.state"),
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.0.created_at"),
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.0.creator_id"),
					// Boolean fields should be present (even if false)
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.0.is_default"),
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.0.is_k8s"),
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.0.auto_add_user"),
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.0.lineage_tracking_enabled"),
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.0.is_aggregated_logs_enabled"),
				),
			},
		},
	})
}

func TestAccCloudsDataSource_FindSpecificCloud(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)
	cloudName := GetTestCloudName(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudsDataSourceFindSpecificCloudConfig(cloudName, cloudID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_cloud.verify", "id", cloudID),
					resource.TestCheckResourceAttr("data.anyscale_cloud.verify", "name", cloudName),
					// Should find exactly the test cloud via the combined filter.
					testAccCheckCloudsListIncludesID("data.anyscale_clouds.test", cloudID),
				),
			},
		},
	})
}

// TestAccCloudsDataSource_MultipleFilters exercises cloud_provider and region
// together using the test cloud's own live values (rather than the hardcoded
// literals FilterByProviderAndRegion below uses), and now actually asserts on
// the anyscale_clouds block the config builds. The old version built that
// block in HCL but its Check function never referenced it at all - only the
// unrelated singular anyscale_cloud lookup - so it never verified anything
// about combining two filters.
func TestAccCloudsDataSource_MultipleFilters(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudsDataSourceByTestCloudConfig(cloudID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.anyscale_cloud.test_cloud", "cloud_provider"),
					resource.TestCheckResourceAttrSet("data.anyscale_cloud.test_cloud", "region"),
					// Filtering by the test cloud's own provider+region must include it.
					testAccCheckCloudsListIncludesID("data.anyscale_clouds.test", cloudID),
				),
			},
		},
	})
}

// TestAccCloudsDataSource_FilterByProviderAndRegion is DS-CLOUD-1's
// mutation-proof acceptance guard for combining two filters at once - proves
// AND semantics, not just that each filter works in isolation. A matching
// provider with a non-matching region must still exclude the test cloud.
func TestAccCloudsDataSource_FilterByProviderAndRegion(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Correct provider, wrong region - AND semantics must still exclude it.
				Config: testAccCloudsDataSourceFilterByProviderAndRegionConfig("AWS", "us-west-2"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_clouds.test", "clouds.#", "0"),
				),
			},
			{
				// Both correct - must include the test cloud.
				Config: testAccCloudsDataSourceFilterByProviderAndRegionConfig("AWS", "us-east-2"),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckCloudsListIncludesID("data.anyscale_clouds.test", cloudID),
				),
			},
		},
	})
}

// Configuration templates

func testAccCloudsDataSourceNoFiltersConfig() string {
	return `
data "anyscale_clouds" "test" {
}
`
}

func testAccCloudsDataSourceFilterByProviderConfig(provider string) string {
	return fmt.Sprintf(`
data "anyscale_clouds" "test" {
  cloud_provider = "%s"
}
`, provider)
}

func testAccCloudsDataSourceFilterByRegionConfig(region string) string {
	return fmt.Sprintf(`
data "anyscale_clouds" "test" {
  region = "%s"
}
`, region)
}

func testAccCloudsDataSourceFilterByNameContainsConfig(nameContains string) string {
	return fmt.Sprintf(`
data "anyscale_clouds" "test" {
  name_contains = "%s"
}
`, nameContains)
}

func testAccCloudsDataSourceByTestCloudConfig(cloudID string) string {
	return fmt.Sprintf(`
data "anyscale_cloud" "test_cloud" {
  id = "%s"
}

data "anyscale_clouds" "test" {
  cloud_provider = data.anyscale_cloud.test_cloud.cloud_provider
  region        = data.anyscale_cloud.test_cloud.region
}
`, cloudID)
}

func testAccCloudsDataSourceFindSpecificCloudConfig(cloudName, cloudID string) string {
	return fmt.Sprintf(`
data "anyscale_cloud" "verify" {
  id = "%s"
}

# name_contains + cloud_provider together, provider sourced dynamically from
# the test cloud itself (rather than a hardcoded literal) so this keeps
# matching the real test cloud regardless of which provider it happens to be
# on. Post DS-CLOUD-1 both filters narrow server/client-side for real, so
# testAccCheckCloudsListIncludesID below is a real assertion, not a placebo.
data "anyscale_clouds" "test" {
  name_contains  = "%s"
  cloud_provider = data.anyscale_cloud.verify.cloud_provider
}
`, cloudID, cloudName)
}

func testAccCloudsDataSourceFilterByProviderAndRegionConfig(provider, region string) string {
	return fmt.Sprintf(`
data "anyscale_clouds" "test" {
  cloud_provider = "%s"
  region        = "%s"
}
`, provider, region)
}
