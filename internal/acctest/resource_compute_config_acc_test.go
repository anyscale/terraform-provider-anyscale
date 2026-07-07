package acctest

import (
	"context"
	"fmt"
	"io"
	"log"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccComputeConfigResource_Basic(t *testing.T) {
	t.Parallel()
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
				// Compute configs ARCHIVE (not delete) on destroy: the resource's Delete
				// calls /api/v2/compute_templates/{id}/archive, which sets archived_at
				// but leaves the row 200-fetchable. So verify the archived marker, not a
				// 404 — else CheckDestroy false-positives ("still returns 200"). Same
				// wrong-check-type class as the F4 container-image fix; confirmed live:
				// an archived config returns archived_at set + deleted_at null here.
				CheckDestroy: NewAPIArchivedDestroyCheckByAttr("anyscale_compute_config", "config_id", "/ext/v0/cluster_computes/%s", "result.archived_at"),
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
						ImportStateVerifyIgnore: []string{
							"head_node",                 // nested attrs auto-filled from instance_type by API; mask-vs-prior logic in Read cannot recover original null markers on import
							"worker_nodes",              // same as head_node: API normalizes resources/physical_resources and import has no prior state to mask against
							"enable_cross_zone_scaling", // serialized into flags["allow-cross-zone-autoscaling"]; default false matches null on configs that omit it
							"min_resources",             // serialized into flags["min_resources"]; null on Basic test config but API returns whatever it normalized
							"max_resources",             // serialized into flags["max_resources"]; null on Basic test config but API returns whatever it normalized
							"advanced_instance_config",  // Dynamic type: API may return null vs empty maps differently; preserved-as-configured by Read
							"flags",                     // Dynamic type at top level: user flags preserved-as-configured to avoid representation drift
							"zones",                     // API replaces empty with ["any"]; preserved-as-configured by Read
						},
					},
				},
			})
		})
	}
}

func TestAccComputeConfigResource_WithWorkers(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetComputeConfigCloudID(t)
	configName := UniqueName(t, "compute-config-workers")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIArchivedDestroyCheckByAttr("anyscale_compute_config", "config_id", "/ext/v0/cluster_computes/%s", "result.archived_at"), // compute configs archive (not delete) -> poll archived_at, not 404
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
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		},
	})
}

// TestAccComputeConfigResource_InconsistentResultRegressions is a regression
// test for tasks 451e2845 and 1f2d592f: worker_nodes[].name and resource-map
// keys (per-node resources, and top-level min_resources) used to trip
// Terraform's "provider produced inconsistent result after apply" check -
// resourceMapToAPI canonicalizes well-known resource keys to lowercase before
// sending, so a configured "CPU" used to come back as "cpu", and a
// server-assigned worker name used to come back non-null when the config left
// it unset. Step 1 exercises both at Create time. Step 2 adds a second,
// brand-new nameless worker group via Update - the case populateNodesFromResponse
// exists for, since UseStateForUnknown has no prior list element to fall back
// to for a worker group that didn't exist before this update.
func TestAccComputeConfigResource_InconsistentResultRegressions(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetComputeConfigCloudID(t)
	configName := UniqueName(t, "compute-config-inconsistent")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIArchivedDestroyCheckByAttr("anyscale_compute_config", "config_id", "/ext/v0/cluster_computes/%s", "result.archived_at"),
		Steps: []resource.TestStep{
			{
				Config: testAccComputeConfigResourceConfig_inconsistentResultRegressions(configName, cloudID, "m5.large"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_compute_config.test", "id"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "worker_nodes.#", "1"),
					// Configured uppercase keys must round-trip with their original
					// casing, not the API's lowercased canonical form.
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "worker_nodes.0.resources.CPU", "2"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "min_resources.CPU", "1"),
					testAccCheckComputeConfigExistsInAPI("anyscale_compute_config.test"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// regression test for task 1f2d592f: adding a second, brand-new
			// nameless worker group via Update (not Create) must not trip the
			// inconsistent-result check either - Update() now resolves Computed
			// sub-attributes from the response the same way Create() does.
			{
				Config: testAccComputeConfigResourceConfig_inconsistentResultUpdateAddWorker(configName, cloudID, "m5.large"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "worker_nodes.#", "2"),
					resource.TestCheckResourceAttr("anyscale_compute_config.test", "worker_nodes.1.resources.GPU", "1"),
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

func TestAccComputeConfigResource_WithCloudName(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudName := GetComputeConfigCloudName(t)
	configName := UniqueName(t, "compute-config-cloudname")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIArchivedDestroyCheckByAttr("anyscale_compute_config", "config_id", "/ext/v0/cluster_computes/%s", "result.archived_at"), // compute configs archive (not delete) -> poll archived_at, not 404
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

func testAccComputeConfigResourceConfig_inconsistentResultRegressions(name, cloudID, workerInstanceType string) string {
	return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = "%s"
  cloud_id = "%s"

  head_node = {
    instance_type = "%s"
  }

  worker_nodes = [
    {
      # name intentionally omitted: the API assigns one from the instance type.
      instance_type = "%s"
      min_nodes     = 0
      max_nodes     = 1
      market_type   = "ON_DEMAND"
      resources = {
        CPU = 2
      }
    }
  ]

  min_resources = {
    CPU = 1
  }
}
`, name, cloudID, workerInstanceType, workerInstanceType)
}

func testAccComputeConfigResourceConfig_inconsistentResultUpdateAddWorker(name, cloudID, workerInstanceType string) string {
	return fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = "%s"
  cloud_id = "%s"

  head_node = {
    instance_type = "%s"
  }

  worker_nodes = [
    {
      # name intentionally omitted: the API assigns one from the instance type.
      instance_type = "%s"
      min_nodes     = 0
      max_nodes     = 1
      market_type   = "ON_DEMAND"
      resources = {
        CPU = 2
      }
    },
    {
      # Second worker group, brand new in this update, also nameless -
      # UseStateForUnknown has no prior list element to fall back to for it.
      instance_type = "%s"
      min_nodes     = 0
      max_nodes     = 1
      market_type   = "ON_DEMAND"
      resources = {
        GPU = 1
      }
    }
  ]

  min_resources = {
    CPU = 1
  }
}
`, name, cloudID, workerInstanceType, workerInstanceType, workerInstanceType)
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
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetComputeConfigCloudID(t)
	configName := UniqueName(t, "compute-config-update")
	var initialConfigID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIArchivedDestroyCheckByAttr("anyscale_compute_config", "config_id", "/ext/v0/cluster_computes/%s", "result.archived_at"), // compute configs archive (not delete) -> poll archived_at, not 404
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

// TestAccComputeConfigResource_Disappears verifies that an out-of-band archive
// of the compute config is detected by the next plan as drift.
func TestAccComputeConfigResource_Disappears(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	// K8S clouds use operator-defined pod shapes, not the basic instance_type
	// shape used here. Pick the first VM cloud, mirroring TestAccComputeConfigResource_Basic.
	vmClouds := GetAllVMClouds(t)
	if len(vmClouds) == 0 {
		t.Skip("No VM clouds available for compute config testing")
	}
	cloud := vmClouds[0]
	instanceTypes := cloud.InstanceTypes()
	if !instanceTypes.IsValid() {
		t.Skipf("Skipping %s - no valid instance types (K8S clouds use operator-defined pod shapes)", cloud.Provider)
	}

	configName := UniqueName(t, "compute-config-disappears")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             NewAPIArchivedDestroyCheckByAttr("anyscale_compute_config", "config_id", "/ext/v0/cluster_computes/%s", "result.archived_at"), // compute configs archive (not delete) -> poll archived_at, not 404
		Steps: []resource.TestStep{
			{
				Config: testAccComputeConfigResourceConfig_basic(configName, cloud.ID, instanceTypes.Small),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckComputeConfigExistsInAPI("anyscale_compute_config.test"),
					testAccDeleteComputeConfigViaAPI("anyscale_compute_config.test"),
				),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// testAccDeleteComputeConfigViaAPI archives the compute config directly via the
// Anyscale API so the next plan must observe drift. Uses the same archive
// endpoint as Delete and the sweeper. 200/202/204/404 all count as success.
func testAccDeleteComputeConfigViaAPI(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("not found: %s", resourceName)
		}

		// config_id is the version-specific ID expected by the archive endpoint.
		configID := rs.Primary.Attributes["config_id"]
		if configID == "" {
			return fmt.Errorf("no config_id attribute set for %s", resourceName)
		}

		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("failed to get test client: %w", err)
		}

		resp, err := client.DoRequest(context.Background(), "POST", fmt.Sprintf("/api/v2/compute_templates/%s/archive", configID), nil)
		if err != nil {
			return fmt.Errorf("failed to archive compute config %s via API: %w", configID, err)
		}
		defer func() {
			if closeErr := resp.Body.Close(); closeErr != nil {
				log.Printf("[WARN] Failed to close response body: %v", closeErr)
			}
		}()

		switch resp.StatusCode {
		case 200, 202, 204, 404:
			return nil
		default:
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("unexpected status %d archiving compute config %s: %s", resp.StatusCode, configID, truncateBody(string(body), 256))
		}
	}
}
