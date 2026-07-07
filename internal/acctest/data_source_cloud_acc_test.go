package acctest

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccCloudDataSource_ByID(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetTestCloudID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
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
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudName := GetTestCloudName(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
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
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	cloudID := GetComputeConfigCloudID(t)
	configName := UniqueName(t, "ds-cloud-compute")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudDataSourceConfig_withComputeConfig(cloudID, configName),
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

// TestAccCloudDataSource_MatchesResourceState is an acceptance-level regression
// test for change C1: data_source_cloud.go used to hardcode auto_add_user,
// enable_lineage_tracking, enable_log_ingestion, is_empty_cloud to false and
// cloud_deployment_id to null regardless of the real cloud. Unit tests can prove
// the mapping function is correct in isolation, but only a real resource+data
// source pair proves the data source's Read genuinely converges with the
// resource's own state over the real API - which is the actual acceptance
// criterion ("data source == resource state, by id AND by name").
//
// Uses the empty-cloud pattern (no aws_config) so it creates a real cloud via
// the API without requiring real AWS/GCP infra. The three booleans are set to
// true via a separate Update step (step 2), not at Create (step 1): Create()'s
// POST /clouds request does not send these fields at all today (a distinct,
// separately-reported gap - see quest chat) - only Update() does. Routing
// through Update keeps this test scoped to C1 (data-source read mapping)
// instead of also depending on that unrelated create-time gap being fixed.
func TestAccCloudDataSource_MatchesResourceState(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudName := UniqueName(t, "ds-cloud-parity")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudDestroy,
		Steps: []resource.TestStep{
			{
				// Step 1: create at defaults (all three booleans false).
				Config: testAccCloudDataSourceConfig_matchesResourceState(cloudName, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "auto_add_user", "false"),
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_id", "auto_add_user", "anyscale_cloud.test", "auto_add_user"),
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_name", "auto_add_user", "anyscale_cloud.test", "auto_add_user"),
				),
			},
			{
				// Step 2: update to non-default (true). A hardcoded-false regression
				// in the data source would pass unnoticed against a cloud left at
				// its all-false default, so this step is the one that actually
				// exercises the C1 fix.
				Config: testAccCloudDataSourceConfig_matchesResourceState(cloudName, true),
				Check: resource.ComposeAggregateTestCheckFunc(
					// Resource itself has the non-default values we configured.
					resource.TestCheckResourceAttr("anyscale_cloud.test", "auto_add_user", "true"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "enable_lineage_tracking", "true"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "enable_log_ingestion", "true"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "is_empty_cloud", "true"),
					// By-id data source converges with resource state for all 5 fields.
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_id", "auto_add_user", "anyscale_cloud.test", "auto_add_user"),
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_id", "enable_lineage_tracking", "anyscale_cloud.test", "enable_lineage_tracking"),
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_id", "enable_log_ingestion", "anyscale_cloud.test", "enable_log_ingestion"),
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_id", "is_empty_cloud", "anyscale_cloud.test", "is_empty_cloud"),
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_id", "cloud_deployment_id", "anyscale_cloud.test", "cloud_deployment_id"),
					// By-name data source converges with resource state for all 5 fields.
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_name", "auto_add_user", "anyscale_cloud.test", "auto_add_user"),
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_name", "enable_lineage_tracking", "anyscale_cloud.test", "enable_lineage_tracking"),
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_name", "enable_log_ingestion", "anyscale_cloud.test", "enable_log_ingestion"),
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_name", "is_empty_cloud", "anyscale_cloud.test", "is_empty_cloud"),
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_name", "cloud_deployment_id", "anyscale_cloud.test", "cloud_deployment_id"),
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

func testAccCloudDataSourceConfig_matchesResourceState(cloudName string, enabled bool) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name                    = "%s"
  cloud_provider          = "AWS"
  region                  = "us-east-2"
  auto_add_user           = %t
  enable_lineage_tracking = %t
  enable_log_ingestion    = %t
}

data "anyscale_cloud" "by_id" {
  id = anyscale_cloud.test.id
}

data "anyscale_cloud" "by_name" {
  name = anyscale_cloud.test.name
}
`, cloudName, enabled, enabled, enabled)
}

func testAccCloudDataSourceConfig_withComputeConfig(cloudID, configName string) string {
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
