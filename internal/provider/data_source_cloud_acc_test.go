package provider

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccCloudDataSource_ByID(t *testing.T) {
	skipIfNotAcceptanceTest(t)

	cloudID := getTestCloudID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudDataSourceConfig_byID(cloudID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_cloud.test", "id", cloudID),
					resource.TestCheckResourceAttrSet("data.anyscale_cloud.test", "name"),
					resource.TestCheckResourceAttrSet("data.anyscale_cloud.test", "cloud_provider"),
					resource.TestCheckResourceAttrSet("data.anyscale_cloud.test", "region"),
					resource.TestCheckResourceAttrSet("data.anyscale_cloud.test", "status"),
					resource.TestCheckResourceAttrSet("data.anyscale_cloud.test", "state"),
				),
			},
		},
	})
}

func TestAccCloudDataSource_ByName(t *testing.T) {
	skipIfNotAcceptanceTest(t)

	cloudName := os.Getenv("ANYSCALE_TEST_CLOUD_NAME")
	if cloudName == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_NAME not set, skipping test")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudDataSourceConfig_byName(cloudName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_cloud.test", "name", cloudName),
					resource.TestCheckResourceAttrSet("data.anyscale_cloud.test", "id"),
					resource.TestCheckResourceAttrSet("data.anyscale_cloud.test", "cloud_provider"),
					resource.TestCheckResourceAttrSet("data.anyscale_cloud.test", "region"),
				),
			},
		},
	})
}

func TestAccCloudDataSource_WithComputeConfig(t *testing.T) {
	skipIfNotAcceptanceTest(t)

	cloudID := getTestCloudID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudDataSourceConfig_withComputeConfig(cloudID),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Check data source
					resource.TestCheckResourceAttr("data.anyscale_cloud.test", "id", cloudID),
					resource.TestCheckResourceAttrSet("data.anyscale_cloud.test", "name"),
					// Check compute config uses the data source
					resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "id"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "cloud_id", cloudID),
				),
			},
		},
	})
}

// Configuration templates

func testAccCloudDataSourceConfig_byID(cloudID string) string {
	return fmt.Sprintf(`
data "anyscale_cloud" "test" {
  id = "%s"
}
`, cloudID)
}

func testAccCloudDataSourceConfig_byName(cloudName string) string {
	return fmt.Sprintf(`
data "anyscale_cloud" "test" {
  name = "%s"
}
`, cloudName)
}

func testAccCloudDataSourceConfig_withComputeConfig(cloudID string) string {
	configName := fmt.Sprintf("tf-test-datasource-compute-%d", os.Getpid())
	return fmt.Sprintf(`
data "anyscale_cloud" "test" {
  id = "%s"
}

resource "anyscale_compute_config" "test" {
  name     = "%s"
  cloud_id = data.anyscale_cloud.test.id

  head_node = {
    instance_type = "m5.large"
  }
}
`, cloudID, configName)
}
