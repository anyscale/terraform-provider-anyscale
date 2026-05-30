package acctest

import (
	"context"
	"fmt"
	"log"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccComputeConfigResource_Basic(t *testing.T) {
	// Skip if acceptance tests are not enabled
	SkipIfNotAcceptanceTest(t)

	// Get all VM clouds for matrix testing
	// K8S clouds are skipped because compute configs use operator-defined pod shapes, not instance types
	vmClouds := GetAllVMClouds(t)
	if len(vmClouds) == 0 {
		t.Skip("No VM clouds available for compute config testing")
	}

	for _, cloud := range vmClouds {
		cloud := cloud // capture range variable
		testName := fmt.Sprintf("%s_%s", cloud.Provider, cloud.ComputeStack)
		t.Run(testName, func(t *testing.T) {
			instanceTypes := cloud.InstanceTypes()
			if !instanceTypes.IsValid() {
				t.Skipf("Skipping %s - no valid instance types (K8S clouds use operator-defined pod shapes)", testName)
			}

			configName := UniqueName(t, "compute-config-basic")
			resource.Test(t, resource.TestCase{
				PreCheck:                 func() { PreCheck(t) },
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				// Use config_id (version-specific) since Primary.ID is the name and the
				// /ext/v0/cluster_computes/ endpoint requires the versioned ID.
				CheckDestroy: NewAPIDestroyCheckByAttr("anyscale_compute_config", "config_id", "/ext/v0/cluster_computes/%s"),
				Steps: []resource.TestStep{
					// Create and Read testing
					{
						Config: testAccComputeConfigResourceConfig_basic(configName, cloud.ID, instanceTypes.Small),
						Check: resource.ComposeAggregateTestCheckFunc(
							resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "id"),
							resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "name"),
							resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "cloud_id"),
							resource.TestCheckResourceAttr("anyscale_compute_config.test", "head_node.instance_type", instanceTypes.Small),
							resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "version"),
							resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "created_at"),
							testAccCheckComputeConfigExistsInAPI("anyscale_compute_config.test"),
						),
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PostApplyPostRefresh: []plancheck.PlanCheck{
								plancheck.ExpectEmptyPlan(),
							},
						},
					},
					// ImportState testing
					{
						ResourceName:      "anyscale_compute_config.test",
						ImportState:       true,
						ImportStateVerify: true,
						// Import using config_id (version-specific API ID), not name
						ImportStateIdFunc: testAccComputeConfigImportStateIdFunc("anyscale_compute_config.test"),
						// These fields are not returned by the API read operation
						// TODO: Implement full state reconstruction from API response
						ImportStateVerifyIgnore: []string{
							"head_node",
							"worker_nodes",
							"enable_cross_zone_scaling",
							"min_resources",
							"max_resources",
							"advanced_instance_config",
							"flags",
							"zones",
						},
					},
				},
			})
		})
	}
}

func TestAccComputeConfigResource_WithWorkers(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)
	configName := UniqueName(t, "compute-config-workers")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheckByAttr("anyscale_compute_config", "config_id", "/ext/v0/cluster_computes/%s"),
		Steps: []resource.TestStep{
			{
				Config: testAccComputeConfigResourceConfig_withWorkers(configName, cloudID, "m5.large", "m5.xlarge"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "id"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "worker_nodes.#", "1"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "worker_nodes.0.instance_type", "m5.xlarge"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "worker_nodes.0.min_nodes", "0"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "worker_nodes.0.max_nodes", "10"),
					testAccCheckComputeConfigExistsInAPI("anyscale_compute_config.test"),
				),
				// FIXME(4.1): Read drops worker_nodes — apiWorkerNodeTypesToTerraform pulls
				// from API which doesn't echo back min_nodes/max_nodes/market_type defaults.
				// ExpectEmptyPlan may fail until Read round-trips worker config faithfully.
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}

func TestAccComputeConfigResource_WithCloudName(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudName := GetTestCloudName(t)
	configName := UniqueName(t, "compute-config-cloudname")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheckByAttr("anyscale_compute_config", "config_id", "/ext/v0/cluster_computes/%s"),
		Steps: []resource.TestStep{
			{
				Config: testAccComputeConfigResourceConfig_withCloudName(configName, cloudName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "id"),
					resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "cloud_id"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "head_node.instance_type", "m5.large"),
					testAccCheckComputeConfigExistsInAPI("anyscale_compute_config.test"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}

// testAccComputeConfigImportStateIdFunc returns the config_id for import (not name)
// The compute config API requires the version-specific config_id (e.g., "cpt_xxx") for lookup
func testAccComputeConfigImportStateIdFunc(resourceName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("not found: %s", resourceName)
		}

		configID := rs.Primary.Attributes["config_id"]
		if configID == "" {
			return "", fmt.Errorf("config_id is empty")
		}

		return configID, nil
	}
}

// testAccCheckComputeConfigExistsInAPI verifies the compute config exists in the API
func testAccCheckComputeConfigExistsInAPI(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("not found: %s", resourceName)
		}

		// Use config_id for API lookup (version-specific ID)
		configID := rs.Primary.Attributes["config_id"]
		if configID == "" {
			// Fallback to primary ID for backwards compatibility
			configID = rs.Primary.ID
		}

		if configID == "" {
			return fmt.Errorf("no Compute Config ID is set")
		}

		// Get the API client
		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("failed to get test client: %w", err)
		}

		// Make API call to verify compute config exists
		resp, err := client.DoRequest(context.Background(), "GET", fmt.Sprintf("/ext/v0/cluster_computes/%s", configID), nil)
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

func testAccComputeConfigResourceConfig_basic(name, cloudID, instanceType string) string {
	return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = "%s"
  cloud_id = "%s"

  head_node = {
    instance_type = "%s"
  }
}
`, name, cloudID, instanceType)
}

func testAccComputeConfigResourceConfig_withWorkers(name, cloudID, headInstanceType, workerInstanceType string) string {
	return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = "%s"
  cloud_id = "%s"

  head_node = {
    instance_type = "%s"
  }

  worker_nodes = [
    {
      name          = "worker-group-1"
      instance_type = "%s"
      min_nodes     = 0
      max_nodes     = 10
      market_type   = "ON_DEMAND"
    }
  ]
}
`, name, cloudID, headInstanceType, workerInstanceType)
}

func testAccComputeConfigResourceConfig_withCloudName(name, cloudName string) string {
	return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name       = "%s"
  cloud_name = "%s"

  head_node = {
    instance_type = "m5.large"
  }
}
`, name, cloudName)
}

// TestAccComputeConfigResource_Update tests that updating a compute config
// creates a new version with the updated configuration.
func TestAccComputeConfigResource_Update(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)
	configName := UniqueName(t, "compute-config-update")
	var initialConfigID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIDestroyCheckByAttr("anyscale_compute_config", "config_id", "/ext/v0/cluster_computes/%s"),
		Steps: []resource.TestStep{
			// Create initial compute config with small instance
			{
				Config: testAccComputeConfigResourceConfig_update(cloudID, configName, "m5.large"),
				Check: resource.ComposeAggregateTestCheckFunc(
					// ID should be the name (stable across versions)
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "id", configName),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "name", configName),
					resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "config_id"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "head_node.instance_type", "m5.large"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "version", "1"),
					// Verify name_version format
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "name_version", configName+":1"),
					testAccCheckComputeConfigExistsInAPI("anyscale_compute_config.test"),
					// Capture initial config_id for comparison
					testAccCaptureComputeConfigID("anyscale_compute_config.test", &initialConfigID),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// Update to larger instance - should create a new version
			{
				Config: testAccComputeConfigResourceConfig_update(cloudID, configName, "m5.xlarge"),
				Check: resource.ComposeAggregateTestCheckFunc(
					// ID should still be the name (stable)
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "id", configName),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "name", configName),
					resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "config_id"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "head_node.instance_type", "m5.xlarge"),
					// Version should be incremented
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "version", "2"),
					// Verify name_version is updated
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "name_version", configName+":2"),
					testAccCheckComputeConfigExistsInAPI("anyscale_compute_config.test"),
					// Verify config_id changed (new version = new config_id)
					testAccCheckComputeConfigIDChanged("anyscale_compute_config.test", &initialConfigID),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}

// testAccCaptureComputeConfigID captures the config_id for later comparison
func testAccCaptureComputeConfigID(resourceName string, configID *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("not found: %s", resourceName)
		}

		*configID = rs.Primary.Attributes["config_id"]
		return nil
	}
}

// testAccCheckComputeConfigIDChanged verifies that config_id has changed from the initial value
func testAccCheckComputeConfigIDChanged(resourceName string, initialConfigID *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("not found: %s", resourceName)
		}

		currentConfigID := rs.Primary.Attributes["config_id"]
		if currentConfigID == *initialConfigID {
			return fmt.Errorf("compute config config_id should have changed after update, but still is %s", currentConfigID)
		}

		return nil
	}
}

func testAccComputeConfigResourceConfig_update(cloudID, configName, instanceType string) string {
	return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = "%s"
  cloud_id = "%s"

  head_node = {
    instance_type = "%s"
  }
}
`, configName, cloudID, instanceType)
}
