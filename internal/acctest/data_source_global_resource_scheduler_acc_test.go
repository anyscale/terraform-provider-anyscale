//go:build grs_enabled

// GRS (Global Resource Scheduler) support is temporarily disabled pending
// backend API rework — these acceptance tests are excluded from the default
// build. Re-enable by removing this build tag (and the registrations in
// provider.go) once the APIs stabilize.

package acctest

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccGlobalResourceSchedulerDataSource_Basic(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	schedulerName := UniqueName(t, "ds-scheduler")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckGlobalResourceSchedulerDestroy,
		Steps: []resource.TestStep{
			// Create a global resource scheduler first, then read it via data source
			{
				Config: testAccGlobalResourceSchedulerDataSourceConfig(schedulerName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.anyscale_global_resource_scheduler.test", "id"),
					resource.TestCheckResourceAttr("data.anyscale_global_resource_scheduler.test", "name", schedulerName),
					resource.TestCheckResourceAttrSet("data.anyscale_global_resource_scheduler.test", "organization_id"),
					resource.TestCheckResourceAttr("data.anyscale_global_resource_scheduler.test", "enable_rootless_dataplane_config", "false"),
				),
			},
		},
	})
}

func TestAccGlobalResourceSchedulerDataSource_WithSpec(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	// Get a configured cloud and use appropriate instance types
	cloud := GetConfiguredCloud(t)
	instanceTypes := cloud.InstanceTypes()
	schedulerName := UniqueName(t, "ds-scheduler-spec")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckGlobalResourceSchedulerDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccGlobalResourceSchedulerDataSourceWithSpecConfig(schedulerName, cloud.ID, instanceTypes),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_global_resource_scheduler.test", "name", schedulerName),
					resource.TestCheckResourceAttr("data.anyscale_global_resource_scheduler.test", "spec.#", "1"),
					// kind is still returned in the data source as computed field
					resource.TestCheckResourceAttr("data.anyscale_global_resource_scheduler.test", "spec.0.kind", "ANYSCALE_MANAGED"),
				),
			},
		},
	})
}

func TestAccGlobalResourceSchedulersDataSource_Basic(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	schedulerName := UniqueName(t, "ds-schedulers")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckGlobalResourceSchedulerDestroy,
		Steps: []resource.TestStep{
			// Create a global resource scheduler first, then list all
			{
				Config: testAccGlobalResourceSchedulersDataSourceConfig(schedulerName),
				Check: resource.ComposeAggregateTestCheckFunc(
					// At least one global resource scheduler should exist
					resource.TestCheckResourceAttrSet("data.anyscale_global_resource_schedulers.all", "machine_pools.#"),
				),
			},
		},
	})
}

func TestAccGlobalResourceSchedulersDataSource_WithFilter(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	schedulerName := UniqueName(t, "ds-schedulers-filter")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckGlobalResourceSchedulerDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccGlobalResourceSchedulersDataSourceWithFilterConfig(schedulerName, schedulerName),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Should find at least the pool we created
					resource.TestCheckResourceAttrSet("data.anyscale_global_resource_schedulers.filtered", "machine_pools.#"),
				),
			},
		},
	})
}

// Configuration templates

func testAccGlobalResourceSchedulerDataSourceConfig(schedulerName string) string {
	return fmt.Sprintf(`
resource "anyscale_global_resource_scheduler" "test" {
  name = "%s"
}

data "anyscale_global_resource_scheduler" "test" {
  name = anyscale_global_resource_scheduler.test.name
}
`, schedulerName)
}

func testAccGlobalResourceSchedulerDataSourceWithSpecConfig(schedulerName, cloudID string, instanceTypes InstanceTypeSet) string {
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

data "anyscale_global_resource_scheduler" "test" {
  name = anyscale_global_resource_scheduler.test.name
}
`, schedulerName, cloudID, instanceTypes.Large)
}

func testAccGlobalResourceSchedulersDataSourceConfig(schedulerName string) string {
	return fmt.Sprintf(`
resource "anyscale_global_resource_scheduler" "test" {
  name = "%s"
}

data "anyscale_global_resource_schedulers" "all" {
  depends_on = [anyscale_global_resource_scheduler.test]
}
`, schedulerName)
}

func testAccGlobalResourceSchedulersDataSourceWithFilterConfig(schedulerName, nameContains string) string {
	return fmt.Sprintf(`
resource "anyscale_global_resource_scheduler" "test" {
  name = "%s"
}

data "anyscale_global_resource_schedulers" "filtered" {
  name_contains = "%s"
  depends_on    = [anyscale_global_resource_scheduler.test]
}
`, schedulerName, nameContains)
}
