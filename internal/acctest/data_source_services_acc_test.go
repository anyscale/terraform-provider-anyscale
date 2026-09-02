package acctest

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// testAccCheckAllServicesHaveProjectID asserts every service in the plural data source's result
// set belongs to the given project - the narrowing-proof complement to a presence-only "services.#
// is set" placebo, which would still pass even if the project_id filter were silently ignored.
// Mirrors testAccCheckAllProjectsHaveCloudID's shape in data_source_projects_acc_test.go.
func testAccCheckAllServicesHaveProjectID(resourceName, projectID string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource not found: %s", resourceName)
		}
		count, err := strconv.Atoi(rs.Primary.Attributes["services.#"])
		if err != nil {
			return fmt.Errorf("failed to parse services.#: %w", err)
		}
		if count == 0 {
			return fmt.Errorf("expected at least one service, got 0")
		}
		for i := 0; i < count; i++ {
			got := rs.Primary.Attributes[fmt.Sprintf("services.%d.project_id", i)]
			if got != projectID {
				return fmt.Errorf("services.%d.project_id = %q, want %q (filter did not narrow to the requested project)", i, got, projectID)
			}
		}
		return nil
	}
}

// TestAccServicesDataSource_Basic lists services scoped to the known test service's own project,
// keeping the result set small and deterministic rather than listing the whole org.
func TestAccServicesDataSource_Basic(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	serviceID := GetTestServiceID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccServicesDataSourceBasicConfig(serviceID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.anyscale_services.test", "services.#"),
					resource.TestCheckResourceAttrSet("data.anyscale_services.test", "services.0.id"),
					resource.TestCheckResourceAttrSet("data.anyscale_services.test", "services.0.name"),
					resource.TestCheckResourceAttrSet("data.anyscale_services.test", "services.0.current_state"),
				),
			},
		},
	})
}

// TestAccServicesDataSource_FilterByProjectID is the narrowing-proof test (design-doc
// convention: no presence-only placebo filter tests) - it asserts every returned service's
// project_id actually matches the filter, discovered from real infrastructure via the singular
// data source rather than hardcoded, so this only tests the real filter's real behavior.
func TestAccServicesDataSource_FilterByProjectID(t *testing.T) {
	t.Parallel()
	SkipIfNotAcceptanceTest(t)

	serviceID := GetTestServiceID(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccServicesDataSourceFilterByProjectIDConfig(serviceID),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckAllServicesHaveProjectIDFromReference("data.anyscale_services.by_project", "data.anyscale_service.target"),
				),
			},
		},
	})
}

// testAccCheckAllServicesHaveProjectIDFromReference is like testAccCheckAllServicesHaveProjectID,
// but reads the expected project_id from another data source in the same state instead of a
// hardcoded value passed in from Go - needed here because the target service's project_id is
// only known once Terraform resolves it, not ahead of time in the test's Go code.
func testAccCheckAllServicesHaveProjectIDFromReference(resourceName, referenceResourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		ref, ok := s.RootModule().Resources[referenceResourceName]
		if !ok {
			return fmt.Errorf("reference resource not found: %s", referenceResourceName)
		}
		projectID := ref.Primary.Attributes["project_id"]
		if projectID == "" {
			return fmt.Errorf("reference resource %s has no project_id", referenceResourceName)
		}
		return testAccCheckAllServicesHaveProjectID(resourceName, projectID)(s)
	}
}

func testAccServicesDataSourceBasicConfig(serviceID string) string {
	return fmt.Sprintf(`
data "anyscale_service" "target" {
  id = %q
}

data "anyscale_services" "test" {
  project_id = data.anyscale_service.target.project_id
}
`, serviceID)
}

func testAccServicesDataSourceFilterByProjectIDConfig(serviceID string) string {
	return fmt.Sprintf(`
data "anyscale_service" "target" {
  id = %q
}

data "anyscale_services" "by_project" {
  project_id = data.anyscale_service.target.project_id
}
`, serviceID)
}
