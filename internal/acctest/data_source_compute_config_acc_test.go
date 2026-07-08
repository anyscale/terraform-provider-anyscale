package acctest

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccComputeConfigDataSource_Basic tests looking up a compute config by name
func TestAccComputeConfigDataSource_Basic(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	// Creating a compute config (not just reading one) needs a cloud with a
	// healthy primary cloud resource, same as TestAccComputeConfigResource_*;
	// GetTestCloudID doesn't filter for that and this test used to hard-fail
	// with a backend 500 on a degraded cloud instead of skipping cleanly.
	cloudID := GetComputeConfigCloudID(t)
	configName := UniqueName(t, "ds-compute-config")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccComputeConfigDataSourceConfig_basic(cloudID, configName),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Verify resource was created
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "name", configName),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "version", "1"),

					// Verify data source lookup by name
					resource.TestCheckResourceAttr("data.anyscale_compute_config.by_name", "name", configName),
					resource.TestCheckResourceAttrSet("data.anyscale_compute_config.by_name", "id"),
					resource.TestCheckResourceAttrSet("data.anyscale_compute_config.by_name", "config_id"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.by_name", "version", "1"),
					// Verify name_version format
					resource.TestCheckResourceAttr("data.anyscale_compute_config.by_name", "name_version", configName+":1"),
					// Verify versions list contains at least version 1
					resource.TestCheckResourceAttr("data.anyscale_compute_config.by_name", "versions.#", "1"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.by_name", "versions.0", "1"),
					// CC6: data source node topology parity with the resource.
					// Confirmed live against the real API that "resources"
					// itself comes back null from BOTH api/v2 and ext/v0 for an
					// instance_type-only head node (no client-side auto-fill
					// happens server-side despite the schema description's
					// wording) -- identical between the two endpoints, which is
					// exactly the CC5a claim this exercises, so instance_type is
					// the meaningful, verified-true assertion here.
					resource.TestCheckResourceAttr("data.anyscale_compute_config.by_name", "head_node.instance_type", "m5.large"),

					// CC5a acceptance (architect): the by-id lookup path must
					// stay green after switching Read to the shared typed
					// structs, not just the by-name path exercised above.
					resource.TestCheckResourceAttr("data.anyscale_compute_config.by_id", "name", configName),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.by_id", "version", "1"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.by_id", "head_node.instance_type", "m5.large"),
				),
			},
		},
	})
}

// TestAccComputeConfigDataSource_WithVersions tests that version-related attributes
// are populated correctly after updates to a compute config.
// Note: The Anyscale API search may not return all historical versions, so we verify
// that the current version is correctly reflected in both version and name_version.
func TestAccComputeConfigDataSource_WithVersions(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	// See TestAccComputeConfigDataSource_Basic: needs a cloud with a healthy
	// primary cloud resource to avoid a 500 on create.
	cloudID := GetComputeConfigCloudID(t)
	configName := UniqueName(t, "ds-compute-versions")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Step 1: Create initial compute config
			{
				Config: testAccComputeConfigDataSourceConfig_versioned(cloudID, configName, "m5.large"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "version", "1"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.lookup", "version", "1"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.lookup", "name_version", configName+":1"),
					// Versions list should contain at least the current version
					resource.TestCheckResourceAttrSet("data.anyscale_compute_config.lookup", "versions.#"),
				),
			},
			// Step 2: Update to create version 2
			{
				Config: testAccComputeConfigDataSourceConfig_versioned(cloudID, configName, "m5.xlarge"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "version", "2"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.lookup", "version", "2"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.lookup", "name_version", configName+":2"),
					// Versions list should contain at least the current version
					resource.TestCheckResourceAttrSet("data.anyscale_compute_config.lookup", "versions.#"),
				),
			},
		},
	})
}

func testAccComputeConfigDataSourceConfig_basic(cloudID, configName string) string {
	return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = "%s"
  cloud_id = "%s"

  head_node = {
    instance_type = "m5.large"
  }
}

data "anyscale_compute_config" "by_name" {
  name = anyscale_compute_config.test.name

  depends_on = [anyscale_compute_config.test]
}

data "anyscale_compute_config" "by_id" {
  # The data source's id input is the version-specific API id (what the
  # resource calls config_id), not the resource's own id (which is the
  # stable name) -- confusingly overlapping names for two different things.
  id = anyscale_compute_config.test.config_id

  depends_on = [anyscale_compute_config.test]
}
`, configName, cloudID)
}

func testAccComputeConfigDataSourceConfig_versioned(cloudID, configName, instanceType string) string {
	return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = "%s"
  cloud_id = "%s"

  head_node = {
    instance_type = "%s"
  }
}

data "anyscale_compute_config" "lookup" {
  name = anyscale_compute_config.test.name

  depends_on = [anyscale_compute_config.test]
}
`, configName, cloudID, instanceType)
}
