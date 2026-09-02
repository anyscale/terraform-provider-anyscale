package acctest

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccServiceDataSource_ByID looks up a real, pre-existing service by ID. Unlike most other
// acctests, this cannot create its own fixture: there is no CreateEphemeralTestService, so
// GetTestServiceID resolves an externally-created service via ANYSCALE_TEST_SERVICE_ID or
// auto-discovery, and skips cleanly (not silently) if the test org has none.
func TestAccServiceDataSource_ByID(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	serviceID := GetTestServiceID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccServiceDataSourceByIDConfig(serviceID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.anyscale_service.test", "id", serviceID),
					resource.TestCheckResourceAttrSet("data.anyscale_service.test", "name"),
					resource.TestCheckResourceAttrSet("data.anyscale_service.test", "project_id"),
					resource.TestCheckResourceAttrSet("data.anyscale_service.test", "cloud_id"),
					resource.TestCheckResourceAttrSet("data.anyscale_service.test", "creator_id"),
					resource.TestCheckResourceAttrSet("data.anyscale_service.test", "created_at"),
					resource.TestCheckResourceAttrSet("data.anyscale_service.test", "hostname"),
					resource.TestCheckResourceAttrSet("data.anyscale_service.test", "base_url"),
					resource.TestCheckResourceAttrSet("data.anyscale_service.test", "current_state"),
					resource.TestCheckResourceAttrSet("data.anyscale_service.test", "goal_state"),
					resource.TestCheckResourceAttrSet("data.anyscale_service.test", "primary_version.id"),
					resource.TestCheckResourceAttrSet("data.anyscale_service.test", "primary_version.version"),
					resource.TestCheckResourceAttrSet("data.anyscale_service.test", "primary_version.current_state"),
					resource.TestCheckResourceAttrSet("data.anyscale_service.test", "primary_version.build_id"),
					resource.TestCheckResourceAttrSet("data.anyscale_service.test", "primary_version.compute_config_id"),
					// ray_serve_config is required upstream (AC-5) - always present, unlike the
					// genuinely-nullable dashboard URLs and canary_version this test does not
					// assert on, since a real service may legitimately have neither set.
					resource.TestCheckResourceAttrSet("data.anyscale_service.test", "primary_version.ray_serve_config"),
				),
			},
		},
	})
}

// TestAccServiceDataSource_ByName looks up the same real service by name, scoped by the
// project_id the by-ID lookup above reports - proving the by-name resolver's happy path (a
// single exact match) against real infrastructure, not just the mocked unit tests.
func TestAccServiceDataSource_ByName(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	serviceID := GetTestServiceID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccServiceDataSourceByNameConfig(serviceID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(
						"data.anyscale_service.by_name", "id",
						"data.anyscale_service.by_id", "id",
					),
					resource.TestCheckResourceAttrPair(
						"data.anyscale_service.by_name", "name",
						"data.anyscale_service.by_id", "name",
					),
				),
			},
		},
	})
}

func testAccServiceDataSourceByIDConfig(serviceID string) string {
	return fmt.Sprintf(`
data "anyscale_service" "test" {
  id = %q
}
`, serviceID)
}

func testAccServiceDataSourceByNameConfig(serviceID string) string {
	return fmt.Sprintf(`
data "anyscale_service" "by_id" {
  id = %q
}

data "anyscale_service" "by_name" {
  name       = data.anyscale_service.by_id.name
  project_id = data.anyscale_service.by_id.project_id
}
`, serviceID)
}
