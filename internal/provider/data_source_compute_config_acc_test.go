package provider

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccComputeConfigDataSource_ByID(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC not set, skipping acceptance test")
	}

	cloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID")
	if cloudID == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_ID not set, skipping test")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// First create a compute config
			{
				Config: testAccComputeConfigDataSourceConfig_createConfig(cloudID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "id"),
				),
			},
			// Then look it up by ID
			{
				Config: testAccComputeConfigDataSourceConfig_byID(cloudID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.anyscale_compute_config.test", "id"),
					resource.TestCheckResourceAttrSet("data.anyscale_compute_config.test", "name"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.test", "cloud_id", cloudID),
					resource.TestCheckResourceAttrSet("data.anyscale_compute_config.test", "region"),
					resource.TestCheckResourceAttrSet("data.anyscale_compute_config.test", "idle_termination_minutes"),
					resource.TestCheckResourceAttrSet("data.anyscale_compute_config.test", "version"),
					resource.TestCheckResourceAttrSet("data.anyscale_compute_config.test", "created_at"),
				),
			},
		},
	})
}

func TestAccComputeConfigDataSource_ByName(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC not set, skipping acceptance test")
	}

	cloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID")
	if cloudID == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_ID not set, skipping test")
	}

	configName := "tf-test-datasource-by-name"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// First create a named compute config
			{
				Config: testAccComputeConfigDataSourceConfig_createNamedConfig(cloudID, configName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "id"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "name", configName),
				),
			},
			// Then look it up by name
			{
				Config: testAccComputeConfigDataSourceConfig_byName(cloudID, configName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_compute_config.test", "name", configName),
					resource.TestCheckResourceAttrSet("data.anyscale_compute_config.test", "id"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.test", "cloud_id", cloudID),
					resource.TestCheckResourceAttrSet("data.anyscale_compute_config.test", "region"),
				),
			},
		},
	})
}

func TestAccComputeConfigDataSource_AsTemplate(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC not set, skipping acceptance test")
	}

	cloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID")
	if cloudID == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_ID not set, skipping test")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create a base config and use it as template for another
			{
				Config: testAccComputeConfigDataSourceConfig_asTemplate(cloudID),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Check base config
					resource.TestCheckResourceAttrSet("anyscale_compute_config.base", "id"),
					resource.TestCheckResourceAttr("anyscale_compute_config.base", "name", "tf-test-datasource-base"),
					// Check data source
					resource.TestCheckResourceAttrSet("data.anyscale_compute_config.template", "id"),
					// Check derived config uses same cloud
					resource.TestCheckResourceAttrSet("anyscale_compute_config.derived", "id"),
					resource.TestCheckResourceAttr("anyscale_compute_config.derived", "cloud_id", cloudID),
				),
			},
		},
	})
}

// Configuration templates

func testAccComputeConfigDataSourceConfig_createConfig(cloudID string) string {
	return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = "tf-test-datasource-config"
  cloud_id = "%s"

  head_node = {
    instance_type = "m5.large"
  }
}
`, cloudID)
}

func testAccComputeConfigDataSourceConfig_byID(cloudID string) string {
	return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = "tf-test-datasource-config"
  cloud_id = "%s"

  head_node = {
    instance_type = "m5.large"
  }
}

data "anyscale_compute_config" "test" {
  id = anyscale_compute_config.test.id
}
`, cloudID)
}

func testAccComputeConfigDataSourceConfig_createNamedConfig(cloudID, configName string) string {
	return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = "%s"
  cloud_id = "%s"

  head_node = {
    instance_type = "m5.large"
  }
}
`, configName, cloudID)
}

func testAccComputeConfigDataSourceConfig_byName(cloudID, configName string) string {
	return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = "%s"
  cloud_id = "%s"

  head_node = {
    instance_type = "m5.large"
  }
}

data "anyscale_compute_config" "test" {
  name = anyscale_compute_config.test.name
}
`, configName, cloudID)
}

func testAccComputeConfigDataSourceConfig_asTemplate(cloudID string) string {
	return fmt.Sprintf(`
resource "anyscale_compute_config" "base" {
  name                     = "tf-test-datasource-base"
  cloud_id                 = "%s"
  idle_termination_minutes = 120

  head_node = {
    instance_type = "m5.large"
  }
}

data "anyscale_compute_config" "template" {
  id = anyscale_compute_config.base.id
}

resource "anyscale_compute_config" "derived" {
  name                     = "tf-test-datasource-derived"
  cloud_id                 = data.anyscale_compute_config.template.cloud_id
  region                   = data.anyscale_compute_config.template.region
  idle_termination_minutes = 60

  head_node = {
    instance_type = "m5.xlarge"
  }
}
`, cloudID)
}
