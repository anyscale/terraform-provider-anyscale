package provider

import (
	"context"
	"fmt"
	"log"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccComputeConfigResource_Basic(t *testing.T) {
	// Skip if acceptance tests are not enabled
	skipIfNotAcceptanceTest(t)

	cloudID := getTestCloudID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccComputeConfigResourceConfig_basic(cloudID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "id"),
					resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "name"),
					resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "cloud_id"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "head_node.instance_type", "m5.large"),
					resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "version"),
					resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "created_at"),
					testAccCheckComputeConfigExistsInAPI("anyscale_compute_config.test"),
				),
			},
			// ImportState testing
			{
				ResourceName:      "anyscale_compute_config.test",
				ImportState:       true,
				ImportStateVerify: true,
				// These fields are not returned by the API read operation
				// TODO: Implement full state reconstruction from API response
				ImportStateVerifyIgnore: []string{
					"head_node",
					"worker_nodes",
					"enable_cross_zone_scaling",
					"min_resources",
					"max_resources",
					"advanced_configurations_json",
					"flags",
					"allowed_azs",
				},
			},
		},
	})
}

func TestAccComputeConfigResource_WithWorkers(t *testing.T) {
	skipIfNotAcceptanceTest(t)

	cloudID := getTestCloudID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccComputeConfigResourceConfig_withWorkers(cloudID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "id"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "worker_nodes.#", "1"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "worker_nodes.0.instance_type", "m5.xlarge"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "worker_nodes.0.min_nodes", "0"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "worker_nodes.0.max_nodes", "10"),
					testAccCheckComputeConfigExistsInAPI("anyscale_compute_config.test"),
				),
			},
		},
	})
}

func TestAccComputeConfigResource_Anonymous(t *testing.T) {
	skipIfNotAcceptanceTest(t)

	cloudID := getTestCloudID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccComputeConfigResourceConfig_anonymous(cloudID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "id"),
					resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "name"),
					testAccCheckComputeConfigExistsInAPI("anyscale_compute_config.test"),
				),
			},
		},
	})
}

func TestAccComputeConfigResource_WithCloudName(t *testing.T) {
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
				Config: testAccComputeConfigResourceConfig_withCloudName(cloudName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "id"),
					resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "cloud_id"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "head_node.instance_type", "m5.large"),
					testAccCheckComputeConfigExistsInAPI("anyscale_compute_config.test"),
				),
			},
		},
	})
}

// testAccCheckComputeConfigExistsInAPI verifies the compute config exists in the API
func testAccCheckComputeConfigExistsInAPI(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("not found: %s", resourceName)
		}

		if rs.Primary.ID == "" {
			return fmt.Errorf("no Compute Config ID is set")
		}

		// Get the API client
		client, err := getTestClient()
		if err != nil {
			return fmt.Errorf("failed to get test client: %w", err)
		}

		// Make API call to verify compute config exists
		configID := rs.Primary.ID
		resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/api/v2/compute_templates/%s", configID), nil)
		if err != nil {
			return fmt.Errorf("API request failed: %w", err)
		}
		defer func() {
			if closeErr := resp.Body.Close(); closeErr != nil {
				log.Printf("[WARN] Failed to close response body: %v", closeErr)
			}
		}()

		if resp.StatusCode == 404 {
			return fmt.Errorf("compute config %s not found in API", configID)
		}

		if resp.StatusCode != 200 {
			return fmt.Errorf("API returned status %d for compute config %s", resp.StatusCode, configID)
		}

		return nil
	}
}

// Configuration templates for tests

func testAccComputeConfigResourceConfig_basic(cloudID string) string {
	return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  # Use unique name to avoid conflicts
  name     = "tf-test-compute-config-basic-%d"
  cloud_id = "%s"

  head_node = {
    instance_type = "m5.large"
  }
}
`, os.Getpid(), cloudID)
}

func testAccComputeConfigResourceConfig_withWorkers(cloudID string) string {
	return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = "tf-test-compute-config-workers-%d"
  cloud_id = "%s"

  idle_termination_minutes = 60

  head_node = {
    instance_type = "m5.large"
  }

  worker_nodes = [
    {
      name          = "worker-group-1"
      instance_type = "m5.xlarge"
      min_nodes     = 0
      max_nodes     = 10
      market_type   = "ON_DEMAND"
    }
  ]
}
`, os.Getpid(), cloudID)
}

func testAccComputeConfigResourceConfig_anonymous(cloudID string) string {
	return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = "tf-test-compute-config-anon-%d"
  cloud_id = "%s"

  head_node = {
    instance_type = "m5.large"
  }
}
`, os.Getpid(), cloudID)
}

func testAccComputeConfigResourceConfig_withCloudName(cloudName string) string {
	configName := fmt.Sprintf("tf-test-cloudname-%d", os.Getpid())
	return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name       = "%s"
  cloud_name = "%s"

  head_node = {
    instance_type = "m5.large"
  }
}
`, configName, cloudName)
}
