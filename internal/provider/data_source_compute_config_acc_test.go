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

	// Use timestamp to ensure unique name for each test run
	configName := fmt.Sprintf("tf-test-datasource-by-name-%d", os.Getpid())

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
					resource.TestCheckResourceAttrSet("anyscale_compute_config.base", "name"),
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

func TestAccComputeConfigDataSource_ByNameWithCloudName(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC not set, skipping acceptance test")
	}

	cloudID := os.Getenv("ANYSCALE_TEST_CLOUD_ID")
	cloudName := os.Getenv("ANYSCALE_TEST_CLOUD_NAME")
	if cloudID == "" || cloudName == "" {
		t.Skip("ANYSCALE_TEST_CLOUD_ID and ANYSCALE_TEST_CLOUD_NAME must be set for this test")
	}

	configName := fmt.Sprintf("tf-test-datasource-cloudname-%d", os.Getpid())

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
			// Then look it up by name and cloud_name (not cloud_id)
			{
				Config: testAccComputeConfigDataSourceConfig_byNameAndCloudName(cloudID, configName, cloudName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_compute_config.test", "name", configName),
					resource.TestCheckResourceAttrSet("data.anyscale_compute_config.test", "id"),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.test", "cloud_id", cloudID),
					resource.TestCheckResourceAttr("data.anyscale_compute_config.test", "cloud_name", cloudName),
					resource.TestCheckResourceAttrSet("data.anyscale_compute_config.test", "region"),
				),
			},
		},
	})
}

// Configuration templates

func testAccComputeConfigDataSourceConfig_createConfig(cloudID string) string {
	configName := fmt.Sprintf("tf-test-datasource-config-%d", os.Getpid())
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

func testAccComputeConfigDataSourceConfig_byID(cloudID string) string {
	configName := fmt.Sprintf("tf-test-datasource-config-%d", os.Getpid())
	return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = "%s"
  cloud_id = "%s"

  head_node = {
    instance_type = "m5.large"
  }
}

data "anyscale_compute_config" "test" {
  id = anyscale_compute_config.test.id
}
`, configName, cloudID)
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
	baseName := fmt.Sprintf("tf-test-datasource-base-%d", os.Getpid())
	derivedName := fmt.Sprintf("tf-test-datasource-derived-%d", os.Getpid())
	return fmt.Sprintf(`
resource "anyscale_compute_config" "base" {
  name                     = "%s"
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
  name                     = "%s"
  cloud_id                 = data.anyscale_compute_config.template.cloud_id
  region                   = data.anyscale_compute_config.template.region
  idle_termination_minutes = 60

  head_node = {
    instance_type = "m5.xlarge"
  }
}
`, baseName, cloudID, derivedName)
}

func testAccComputeConfigDataSourceConfig_byNameAndCloudName(cloudID, configName, cloudName string) string {
	return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = "%s"
  cloud_id = "%s"

  head_node = {
    instance_type = "m5.large"
  }
}

data "anyscale_compute_config" "test" {
  name       = anyscale_compute_config.test.name
  cloud_name = "%s"
}
`, configName, cloudID, cloudName)
}
