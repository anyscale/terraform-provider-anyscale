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
					// Data source node topology matches the resource. Confirmed
					// live that "resources" comes back null for an
					// instance_type-only head node (the server does not auto-fill
					// it), so instance_type is the meaningful assertion here.
					resource.TestCheckResourceAttr("data.anyscale_compute_config.by_name", "head_node.instance_type", "m5.large"),

					// The by-id lookup path must resolve the same config as the
					// by-name path exercised above.
					resource.TestCheckResourceAttr("data.anyscale_compute_config.by_id", "name", configName),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.by_id", "version", "1"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.by_id", "head_node.instance_type", "m5.large"),
				),
			},
		},
	})
}

// TestAccComputeConfigDataSource_WithVersions proves version enumeration
// against a real two-version history rather than a mock. The data source sends
// the "don't filter by version" sentinel and pages through results; a broken
// lookup that returns only the latest version would still show one entry, so
// this asserts exact counts: after two versions exist, versions.# is exactly 2
// and contains both 1 and 2 (sorted ascending).
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
					// Exactly 1 version exists so far.
					resource.TestCheckResourceAttr("data.anyscale_compute_config.lookup", "versions.#", "1"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.lookup", "versions.0", "1"),
				),
			},
			// Step 2: Update to create version 2
			{
				Config: testAccComputeConfigDataSourceConfig_versioned(cloudID, configName, "m5.xlarge"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "version", "2"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.lookup", "version", "2"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.lookup", "name_version", configName+":2"),
					// Both historical versions must be enumerated now, not just the latest.
					resource.TestCheckResourceAttr("data.anyscale_compute_config.lookup", "versions.#", "2"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.lookup", "versions.0", "1"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.lookup", "versions.1", "2"),
				),
			},
		},
	})
}

// TestAccComputeConfigDataSource_EnableCrossZoneScaling proves the data source
// reads enable_cross_zone_scaling from flags["allow-cross-zone-autoscaling"],
// where the API actually stores it (there is no top-level JSON key), via the
// same resolveEffectiveComputeConfig helper the resource's Read uses. Reading
// a nonexistent top-level key fails silently and reports false for every
// config, so this asserts against an explicitly-true configured value.
func TestAccComputeConfigDataSource_EnableCrossZoneScaling(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetComputeConfigCloudID(t)
	configName := UniqueName(t, "ds-compute-xzone")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name                      = %[1]q
  cloud_id                  = %[2]q
  enable_cross_zone_scaling = true

  head_node = {
    instance_type = "m5.large"
  }
}

data "anyscale_compute_config" "by_name" {
  name = anyscale_compute_config.test.name

  depends_on = [anyscale_compute_config.test]
}
`, configName, cloudID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "enable_cross_zone_scaling", "true"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.by_name", "enable_cross_zone_scaling", "true"),
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
