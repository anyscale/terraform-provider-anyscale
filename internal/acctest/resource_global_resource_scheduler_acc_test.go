//go:build grs_enabled

// GRS (Global Resource Scheduler) support is temporarily disabled pending
// backend API rework — these acceptance tests are excluded from the default
// build. Re-enable by removing this build tag (and the registrations in
// provider.go) once the APIs stabilize.

package acctest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccGlobalResourceSchedulerResource_Basic(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	schedulerName := UniqueName(t, "scheduler-basic")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckGlobalResourceSchedulerDestroy,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccGlobalResourceSchedulerResourceBasicConfig(schedulerName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_global_resource_scheduler.test", "id"),
					resource.TestCheckResourceAttr("anyscale_global_resource_scheduler.test", "name", schedulerName),
					resource.TestCheckResourceAttr("anyscale_global_resource_scheduler.test", "enable_rootless_dataplane_config", "false"),
					resource.TestCheckResourceAttrSet("anyscale_global_resource_scheduler.test", "organization_id"),
					testAccCheckGlobalResourceSchedulerExistsInAPI("anyscale_global_resource_scheduler.test"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// ImportState testing
			{
				ResourceName:      "anyscale_global_resource_scheduler.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateId:     schedulerName,
			},
		},
	})
}

func TestAccGlobalResourceSchedulerResource_WithSpec(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	// Get a configured cloud and use appropriate instance types
	cloud := GetConfiguredCloud(t)
	instanceTypes := cloud.InstanceTypes()
	schedulerName := UniqueName(t, "scheduler-spec")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckGlobalResourceSchedulerDestroy,
		Steps: []resource.TestStep{
			// Create with spec (requires cloud attachment for ANYSCALE_MANAGED)
			{
				Config: testAccGlobalResourceSchedulerResourceWithSpecConfig(schedulerName, cloud.ID, instanceTypes),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_global_resource_scheduler.test", "id"),
					resource.TestCheckResourceAttr("anyscale_global_resource_scheduler.test", "name", schedulerName),
					resource.TestCheckResourceAttr("anyscale_global_resource_scheduler.test", "spec.#", "1"),
					resource.TestCheckResourceAttr("anyscale_global_resource_scheduler.test", "spec.0.machine_type.#", "1"),
					resource.TestCheckResourceAttr("anyscale_global_resource_scheduler.test", "spec.0.machine_type.0.name", "RES-8CPU-32GB"),
					testAccCheckGlobalResourceSchedulerExistsInAPI("anyscale_global_resource_scheduler.test"),
				),
				// cloud_attachment is a block stored as a slice on the model; Read does
				// not overwrite it, so the user-supplied values round-trip via prior state.
				// CloudIDs (computed) is refreshed separately from the API response.
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
			// Update spec
			{
				Config: testAccGlobalResourceSchedulerResourceWithUpdatedSpecConfig(schedulerName, cloud.ID, instanceTypes),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_global_resource_scheduler.test", "spec.0.machine_type.#", "2"),
					testAccCheckGlobalResourceSchedulerExistsInAPI("anyscale_global_resource_scheduler.test"),
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

func TestAccGlobalResourceSchedulerResource_WithCloudAttachment(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	// This test requires a cloud with cloud resources configured
	cloud := GetConfiguredCloud(t)
	schedulerName := UniqueName(t, "scheduler-cloud")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckGlobalResourceSchedulerDestroy,
		Steps: []resource.TestStep{
			// Create with cloud attachment
			{
				Config: testAccGlobalResourceSchedulerResourceWithCloudAttachmentConfig(schedulerName, cloud.ID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_global_resource_scheduler.test", "id"),
					resource.TestCheckResourceAttr("anyscale_global_resource_scheduler.test", "name", schedulerName),
					resource.TestCheckResourceAttr("anyscale_global_resource_scheduler.test", "cloud_attachment.#", "1"),
					resource.TestCheckResourceAttr("anyscale_global_resource_scheduler.test", "cloud_attachment.0.cloud_id", cloud.ID),
					testAccCheckGlobalResourceSchedulerExistsInAPI("anyscale_global_resource_scheduler.test"),
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

func TestAccGlobalResourceSchedulerResource_WithCloudName(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudName := GetTestCloudName(t)

	schedulerName := UniqueName(t, "scheduler-cloudname")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckGlobalResourceSchedulerDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccGlobalResourceSchedulerResourceWithCloudNameConfig(schedulerName, cloudName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_global_resource_scheduler.test", "id"),
					resource.TestCheckResourceAttr("anyscale_global_resource_scheduler.test", "name", schedulerName),
					resource.TestCheckResourceAttr("anyscale_global_resource_scheduler.test", "cloud_attachment.#", "1"),
					resource.TestCheckResourceAttr("anyscale_global_resource_scheduler.test", "cloud_attachment.0.cloud_name", cloudName),
					testAccCheckGlobalResourceSchedulerExistsInAPI("anyscale_global_resource_scheduler.test"),
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

func TestAccGlobalResourceSchedulerResource_Full(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	// Get a configured cloud and use appropriate instance types
	cloud := GetConfiguredCloud(t)
	instanceTypes := cloud.InstanceTypes()
	schedulerName := UniqueName(t, "scheduler-full")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckGlobalResourceSchedulerDestroy,
		Steps: []resource.TestStep{
			// Create with full configuration
			{
				Config: testAccGlobalResourceSchedulerResourceFullConfig(schedulerName, cloud.ID, instanceTypes),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("anyscale_global_resource_scheduler.test", "id"),
					resource.TestCheckResourceAttr("anyscale_global_resource_scheduler.test", "name", schedulerName),
					resource.TestCheckResourceAttr("anyscale_global_resource_scheduler.test", "enable_rootless_dataplane_config", "false"),
					resource.TestCheckResourceAttr("anyscale_global_resource_scheduler.test", "cloud_attachment.#", "1"),
					resource.TestCheckResourceAttr("anyscale_global_resource_scheduler.test", "spec.#", "1"),
					resource.TestCheckResourceAttr("anyscale_global_resource_scheduler.test", "spec.0.machine_type.#", "1"),
					resource.TestCheckResourceAttr("anyscale_global_resource_scheduler.test", "spec.0.machine_type.0.partition.#", "1"),
					testAccCheckGlobalResourceSchedulerExistsInAPI("anyscale_global_resource_scheduler.test"),
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

// Helper functions

func testAccCheckGlobalResourceSchedulerExistsInAPI(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resourceName)
		}

		schedulerName := rs.Primary.Attributes["name"]
		if schedulerName == "" {
			return fmt.Errorf("global resource scheduler name not set")
		}

		client, err := GetTestClient()
		if err != nil {
			return fmt.Errorf("failed to get test client: %w", err)
		}

		pools, err := listMachinePoolsForTest(context.Background(), client)
		if err != nil {
			return fmt.Errorf("failed to list global resource schedulers: %w", err)
		}

		for _, pool := range pools {
			if pool.MachinePoolName == schedulerName {
				return nil
			}
		}

		return fmt.Errorf("global resource scheduler %s not found in machine_pools list (%d entries)", schedulerName, len(pools))
	}
}

func testAccCheckGlobalResourceSchedulerDestroy(s *terraform.State) error {
	client, err := GetTestClient()
	if err != nil {
		return fmt.Errorf("failed to get test client: %w", err)
	}

	var names []string
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "anyscale_global_resource_scheduler" {
			continue
		}
		if name := rs.Primary.Attributes["name"]; name != "" {
			names = append(names, name)
		}
	}

	if len(names) == 0 {
		return nil
	}

	pools, err := listMachinePoolsForTest(context.Background(), client)
	if err != nil {
		return fmt.Errorf("failed to list global resource schedulers: %w", err)
	}

	for _, schedulerName := range names {
		for _, pool := range pools {
			if pool.MachinePoolName == schedulerName {
				return fmt.Errorf("global resource scheduler %s still present in machine_pools list after destroy", schedulerName)
			}
		}
	}

	return nil
}

// listMachinePoolsForTest lists every global resource scheduler (machine
// pool), mirroring GlobalResourceSchedulerResource.readMachinePool in
// internal/provider (an unexported method there, so not reusable across
// packages). Not paginated - ListMachinePoolsResponse has no paging token,
// matching how the real resource's own Read path treats this endpoint.
func listMachinePoolsForTest(ctx context.Context, client *provider.Client) ([]provider.MachinePoolResult, error) {
	listResp, err := provider.DoRequestAndParse[provider.ListMachinePoolsResponse](
		ctx, client, "GET", "/api/v2/machine_pools/", nil, http.StatusOK,
	)
	if err != nil {
		return nil, err
	}
	return listResp.Result.MachinePools, nil
}

// schedulerState hand-builds the exact terraform.State shape the GRS
// exists/destroy checks see, matching the pattern established in
// helpers_checkdestroy_test.go: these TestCheckFuncs can be called directly
// against a fake state and a mock server, no real resource.Test apply needed.
// TEST-ONLY: this and the tests below exercise nothing but the acctest
// package's own CheckFunc wiring against a local mock - no provider
// GRS/machine-pool logic is touched, matching the standing GRSv2 deferral.
func schedulerState(schedulerName string) *terraform.State {
	return &terraform.State{
		Modules: []*terraform.ModuleState{
			{
				Path: []string{"root"},
				Resources: map[string]*terraform.ResourceState{
					"anyscale_global_resource_scheduler.test": {
						Type: "anyscale_global_resource_scheduler",
						Primary: &terraform.InstanceState{
							ID:         schedulerName,
							Attributes: map[string]string{"name": schedulerName},
						},
					},
				},
			},
		},
	}
}

// machinePoolsListServer starts a mock /api/v2/machine_pools/ endpoint
// returning exactly the given pools (single page - the real endpoint has no
// pagination either, see listMachinePoolsForTest).
func machinePoolsListServer(t *testing.T, pools []provider.MachinePoolResult) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		resp := provider.ListMachinePoolsResponse{}
		resp.Result.MachinePools = pools
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)
	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "fake-token-grs-checks")
	return server
}

// TestGRSExistsInAPI_SucceedsWhenPresent is the positive control: the
// scheduler IS in the mocked list, so the check must pass.
func TestGRSExistsInAPI_SucceedsWhenPresent(t *testing.T) {
	const schedulerName = "scheduler-present"
	machinePoolsListServer(t, []provider.MachinePoolResult{
		{MachinePoolID: "mp_1", MachinePoolName: schedulerName},
	})

	if err := testAccCheckGlobalResourceSchedulerExistsInAPI("anyscale_global_resource_scheduler.test")(schedulerState(schedulerName)); err != nil {
		t.Fatalf("expected success for a scheduler present in the API list, got: %v", err)
	}
}

// TestGRSExistsInAPI_FailsWhenAbsent is the mutation proof this check is no
// longer a placebo: before the fix, this exact scenario (the scheduler is
// genuinely absent from the API) still returned nil because the old code
// never parsed the response at all. It must now fail loudly.
func TestGRSExistsInAPI_FailsWhenAbsent(t *testing.T) {
	const schedulerName = "scheduler-absent"
	machinePoolsListServer(t, []provider.MachinePoolResult{
		{MachinePoolID: "mp_2", MachinePoolName: "some-other-scheduler"},
	})

	err := testAccCheckGlobalResourceSchedulerExistsInAPI("anyscale_global_resource_scheduler.test")(schedulerState(schedulerName))
	if err == nil {
		t.Fatal("expected an error when the scheduler is absent from the API list, got nil (this is the exact placebo behavior being fixed)")
	}
}

// TestGRSDestroy_SucceedsWhenAbsent is the positive control for the
// post-destroy check: the scheduler is genuinely gone, so it must pass.
func TestGRSDestroy_SucceedsWhenAbsent(t *testing.T) {
	const schedulerName = "scheduler-destroyed"
	machinePoolsListServer(t, []provider.MachinePoolResult{
		{MachinePoolID: "mp_3", MachinePoolName: "some-other-scheduler"},
	})

	if err := testAccCheckGlobalResourceSchedulerDestroy(schedulerState(schedulerName)); err != nil {
		t.Fatalf("expected success when the scheduler is genuinely absent, got: %v", err)
	}
}

// TestGRSDestroy_FailsWhenPresent is the mutation proof for the post-destroy
// check: before the fix, a scheduler that was NOT actually removed (still
// present in the API) still passed silently. It must now fail.
func TestGRSDestroy_FailsWhenPresent(t *testing.T) {
	const schedulerName = "scheduler-leaked"
	machinePoolsListServer(t, []provider.MachinePoolResult{
		{MachinePoolID: "mp_4", MachinePoolName: schedulerName},
	})

	err := testAccCheckGlobalResourceSchedulerDestroy(schedulerState(schedulerName))
	if err == nil {
		t.Fatal("expected an error when the scheduler is still present after destroy, got nil (this is the exact placebo behavior being fixed)")
	}
}

// Configuration templates

func testAccGlobalResourceSchedulerResourceBasicConfig(schedulerName string) string {
	return fmt.Sprintf(`
resource "anyscale_global_resource_scheduler" "test" {
  name = "%s"
}
`, schedulerName)
}

func testAccGlobalResourceSchedulerResourceWithSpecConfig(schedulerName, cloudID string, instanceTypes InstanceTypeSet) string {
	return fmt.Sprintf(`
resource "anyscale_global_resource_scheduler" "test" {
  name = "%s"

  cloud_attachment {
    cloud_id = "%s"
  }

  spec {
    machine_type {
      name = "RES-8CPU-32GB"

      launch_template {
        instance_type = "%s"
        market_type   = "ON_DEMAND"
      }

      partition {
        name = "default"
        size = 5

        rule {
          selector = "workload-type in (job)"
          priority = 100
        }
      }
    }
  }
}
`, schedulerName, cloudID, instanceTypes.Large)
}

func testAccGlobalResourceSchedulerResourceWithUpdatedSpecConfig(schedulerName, cloudID string, instanceTypes InstanceTypeSet) string {
	return fmt.Sprintf(`
resource "anyscale_global_resource_scheduler" "test" {
  name = "%s"

  cloud_attachment {
    cloud_id = "%s"
  }

  spec {
    machine_type {
      name = "RES-8CPU-32GB"

      launch_template {
        instance_type = "%s"
        market_type   = "ON_DEMAND"
      }

      partition {
        name = "default"
        size = 5

        rule {
          selector = "workload-type in (job)"
          priority = 100
        }
      }
    }

    machine_type {
      name = "RES-4CPU-16GB"

      launch_template {
        instance_type = "%s"
        market_type   = "SPOT"
      }

      partition {
        name = "spot-partition"
        size = 10

        rule {
          selector = "workload-type in (job)"
          priority = 50
        }
      }
    }
  }
}
`, schedulerName, cloudID, instanceTypes.Large, instanceTypes.Medium)
}

func testAccGlobalResourceSchedulerResourceWithCloudAttachmentConfig(schedulerName, cloudID string) string {
	return fmt.Sprintf(`
resource "anyscale_global_resource_scheduler" "test" {
  name = "%s"

  cloud_attachment {
    cloud_id = "%s"
  }
}
`, schedulerName, cloudID)
}

func testAccGlobalResourceSchedulerResourceWithCloudNameConfig(schedulerName, cloudName string) string {
	return fmt.Sprintf(`
resource "anyscale_global_resource_scheduler" "test" {
  name = "%s"

  cloud_attachment {
    cloud_name = "%s"
  }
}
`, schedulerName, cloudName)
}

func testAccGlobalResourceSchedulerResourceFullConfig(schedulerName, cloudID string, instanceTypes InstanceTypeSet) string {
	return fmt.Sprintf(`
resource "anyscale_global_resource_scheduler" "test" {
  name                              = "%s"
  enable_rootless_dataplane_config = false

  cloud_attachment {
    cloud_id = "%s"
  }

  spec {
    machine_type {
      name = "RES-8CPU-32GB"

      launch_template {
        instance_type = "%s"
        market_type   = "ON_DEMAND"
        zones         = ["%s", "%s"]
      }

      recycle_policy {
        max_workloads     = 100
        rotation_interval = "24h"
        max_idle_duration = "60m"
      }

      partition {
        name = "default"
        size = 10

        rule {
          selector = "workload-type in (job)"
          priority = 100
        }

        rule {
          selector = "workload-type in (service)"
          priority = 200
          quota    = 5
        }
      }
    }
  }
}
`, schedulerName, cloudID, instanceTypes.Large, instanceTypes.Zones[0], instanceTypes.Zones[1])
}
