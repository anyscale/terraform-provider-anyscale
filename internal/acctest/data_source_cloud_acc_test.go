package acctest

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
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
// cloud_resource_id to null regardless of the real cloud. Unit tests can prove
// the mapping function is correct in isolation, but only a real resource+data
// source pair proves the data source's Read genuinely converges with the
// resource's own state over the real API - which is the actual acceptance
// criterion ("data source == resource state, by id AND by name").
//
// Uses the empty-cloud pattern (no aws_config) so it creates a real cloud via
// the API without requiring real AWS/GCP infra. auto_add_user and
// enable_log_ingestion are set to true via a separate Update step (step 2),
// not at Create (step 1): Create()'s POST /clouds request does not send these
// fields at all today (a distinct, separately-reported gap - see quest chat)
// - only Update() does. Routing through Update keeps this test scoped to C1
// (data-source read mapping) instead of also depending on that unrelated
// create-time gap being fixed.
//
// enable_lineage_tracking is deliberately left at its false default and
// excluded from the true-value assertions: this test org's Anyscale
// organization does not have lineage tracking enabled as a feature, so
// PUT .../lineage_tracking_enabled legitimately 403s here regardless of
// provider correctness (confirmed live: "Lineage tracking is not enabled for
// your organization"). Its read-side mapping is still proven correct by
// forge's mocked unit test (TestReadCloudIntoModel_MapsFromResponseNotConstant),
// which isn't subject to this org's feature gating.
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
					resource.TestCheckResourceAttr("anyscale_cloud.test", "enable_lineage_tracking", "false"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "enable_log_ingestion", "true"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "is_empty_cloud", "true"),
					// By-id data source converges with resource state for all 5 fields.
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_id", "auto_add_user", "anyscale_cloud.test", "auto_add_user"),
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_id", "enable_lineage_tracking", "anyscale_cloud.test", "enable_lineage_tracking"),
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_id", "enable_log_ingestion", "anyscale_cloud.test", "enable_log_ingestion"),
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_id", "is_empty_cloud", "anyscale_cloud.test", "is_empty_cloud"),
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_id", "cloud_resource_id", "anyscale_cloud.test", "cloud_resource_id"),
					// By-name data source converges with resource state for all 5 fields.
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_name", "auto_add_user", "anyscale_cloud.test", "auto_add_user"),
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_name", "enable_lineage_tracking", "anyscale_cloud.test", "enable_lineage_tracking"),
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_name", "enable_log_ingestion", "anyscale_cloud.test", "enable_log_ingestion"),
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_name", "is_empty_cloud", "anyscale_cloud.test", "is_empty_cloud"),
					resource.TestCheckResourceAttrPair("data.anyscale_cloud.by_name", "cloud_resource_id", "anyscale_cloud.test", "cloud_resource_id"),
				),
			},
		},
	})
}

// TestAccCloudDataSource_C2ParityMatchesPluralDataSource is an
// acceptance-level proof for change C2's third acceptance criterion:
// "values match the same cloud in the plural data source." Forge's mocked
// unit test proves the singular data source's mapping is internally correct
// in isolation; this proves the singular and plural data sources actually
// converge on the same real cloud, which a mapping-only test can't show.
//
// Does NOT assume clouds.# == 1 or index into clouds.0: name_contains
// filtering does not reliably exclude this org's pinned default/static
// fixture cloud from plural results (confirmed live - it appears regardless
// of filter value, even a garbage string matching nothing else), the same
// reason TestAccCloudsDataSource_FindSpecificCloud avoids index-based
// assertions. Finds this test's own cloud by ID within the returned list
// instead, mirroring that existing pattern.
func TestAccCloudDataSource_C2ParityMatchesPluralDataSource(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	cloudName := UniqueName(t, "ds-cloud-c2-parity")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCloudDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccCloudDataSourceConfig_c2Parity(cloudName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.anyscale_clouds.test", "clouds.#"),
					testAccCheckCloudC2ParityFieldsMatch("anyscale_cloud.test", "data.anyscale_cloud.test", "data.anyscale_clouds.test"),
				),
			},
		},
	})
}

// testAccCheckCloudC2ParityFieldsMatch finds the entry in the plural data
// source's clouds list whose id matches resourceName's id (rather than
// assuming a specific index - see the comment on the test above), then
// asserts 4 of the 5 remaining C2 parity fields agree with the singular data
// source's own values for that same cloud. is_aioa/is_bring_your_own_resource/
// is_private_service_cloud were part of the original 8-field C2 parity set
// but were removed from both data sources (read-only, backend-internal
// classification values users could not act on) - see the
// data_source_attr_removal spec.
//
// is_default is deliberately excluded: confirmed reproducible (twice, hours
// apart, same test org) that GET /clouds/{id} and GET /clouds disagree on
// is_default for the identical, freshly-created, provably-non-default
// cloud (false vs true). Both data sources map it from the same
// CloudResult.IsDefault JSON field via the same code shape, so this is a
// backend data-consistency question between those two read paths, not a
// provider mapping bug - see commit 082e29f. Excluding it here keeps this
// test a reliable CI signal for the fields that DO agree, rather than
// permanently red over an already-reported, out-of-scope backend issue.
func testAccCheckCloudC2ParityFieldsMatch(resourceName, singularDS, pluralDS string) resource.TestCheckFunc {
	fields := []string{
		"compute_stack", "created_at", "creator_id", "is_private_cloud",
	}

	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("not found: %s", resourceName)
		}
		cloudID := rs.Primary.ID

		plural, ok := s.RootModule().Resources[pluralDS]
		if !ok {
			return fmt.Errorf("not found: %s", pluralDS)
		}
		singular, ok := s.RootModule().Resources[singularDS]
		if !ok {
			return fmt.Errorf("not found: %s", singularDS)
		}

		count, err := strconv.Atoi(plural.Primary.Attributes["clouds.#"])
		if err != nil {
			return fmt.Errorf("clouds.# is not a number: %v", err)
		}

		var foundIndex = -1
		for i := 0; i < count; i++ {
			if plural.Primary.Attributes[fmt.Sprintf("clouds.%d.id", i)] == cloudID {
				foundIndex = i
				break
			}
		}
		if foundIndex == -1 {
			return fmt.Errorf("cloud %s not found among %d entries in %s", cloudID, count, pluralDS)
		}

		for _, field := range fields {
			pluralVal := plural.Primary.Attributes[fmt.Sprintf("clouds.%d.%s", foundIndex, field)]
			singularVal := singular.Primary.Attributes[field]
			if pluralVal != singularVal {
				return fmt.Errorf("%s mismatch: singular=%q plural=%q", field, singularVal, pluralVal)
			}
		}
		return nil
	}
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
  enable_lineage_tracking = false
  enable_log_ingestion    = %t
}

data "anyscale_cloud" "by_id" {
  id = anyscale_cloud.test.id
}

data "anyscale_cloud" "by_name" {
  name = anyscale_cloud.test.name
}
`, cloudName, enabled, enabled)
}

func testAccCloudDataSourceConfig_c2Parity(cloudName string) string {
	return fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "%s"
  cloud_provider = "AWS"
  region         = "us-east-2"
}

data "anyscale_cloud" "test" {
  id = anyscale_cloud.test.id
}

data "anyscale_clouds" "test" {
  name_contains = anyscale_cloud.test.name
}
`, cloudName)
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
