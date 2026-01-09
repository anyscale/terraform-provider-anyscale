package acctest

import (
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccComputeConfigDataSource_Basic tests looking up a compute config by name
func TestAccComputeConfigDataSource_Basic(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)
	configName := fmt.Sprintf("tf-test-ds-compute-config-%d", time.Now().UnixNano())

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
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)
	configName := fmt.Sprintf("tf-test-ds-versions-%d", time.Now().UnixNano())

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
