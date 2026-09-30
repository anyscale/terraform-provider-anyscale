package acctest

import (
	"fmt"
	"strconv"

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
