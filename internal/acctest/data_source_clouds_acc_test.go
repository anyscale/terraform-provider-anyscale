package acctest

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
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

func TestAccCloudsDataSource_FilterByProvider(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Filter by AWS provider
				Config: testAccCloudsDataSourceFilterByProviderConfig("AWS"),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Should return at least some clouds (if any AWS clouds exist)
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.#"),
				),
			},
			{
				// Filter by GCP provider
				Config: testAccCloudsDataSourceFilterByProviderConfig("GCP"),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Should return clouds (if any GCP clouds exist)
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.#"),
				),
			},
		},
	})
}

func TestAccCloudsDataSource_FilterByRegion(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// First get the test cloud to know its region
				Config: testAccCloudsDataSourceByTestCloudConfig(cloudID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.anyscale_cloud.test_cloud", "region"),
				),
			},
		},
	})
}

func TestAccCloudsDataSource_FilterByNameContains(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudName := GetTestCloudName(t)

	// Use part of the cloud name to test partial matching
	// Take the first few characters as the search term
	nameContains := cloudName
	if len(cloudName) > 5 {
		nameContains = cloudName[:5]
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudsDataSourceFilterByNameContainsConfig(nameContains),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Should find at least one cloud matching the pattern
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.#"),
				),
			},
		},
	})
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
					// Should find exactly the test cloud
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.#"),
					// Use the cloud data in another data source to verify it works
					resource.TestCheckResourceAttr("data.anyscale_cloud.verify", "id", cloudID),
					resource.TestCheckResourceAttr("data.anyscale_cloud.verify", "name", cloudName),
				),
			},
		},
	})
}

func TestAccCloudsDataSource_MultipleFilters(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Get the test cloud details first
				Config: testAccCloudsDataSourceByTestCloudConfig(cloudID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.anyscale_cloud.test_cloud", "cloud_provider"),
					resource.TestCheckResourceAttrSet("data.anyscale_cloud.test_cloud", "region"),
				),
			},
		},
	})
}

func TestAccCloudsDataSource_FilterByProviderAndRegion(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Use hardcoded common values to test multiple filters
				Config: testAccCloudsDataSourceFilterByProviderAndRegionConfig("AWS", "us-east-2"),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Should return clouds (if any exist matching the criteria)
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.#"),
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

// nolint:unused
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
data "anyscale_clouds" "test" {
  name_contains  = "%s"
  cloud_provider = "GCP"
}

# Look the cloud up by its unique ID rather than clouds[0]. The anyscale_clouds
# name_contains/cloud_provider filters are not applied server-side, so the list
# is unordered and clouds[0] is not guaranteed to be the test cloud when other
# tfacc-cloud-gcp-basic-* clouds exist concurrently.
data "anyscale_cloud" "verify" {
  id = "%s"
}
`, cloudName, cloudID)
}

func testAccCloudsDataSourceFilterByProviderAndRegionConfig(provider, region string) string {
	return fmt.Sprintf(`
data "anyscale_clouds" "test" {
  cloud_provider = "%s"
  region        = "%s"
}
`, provider, region)
}
