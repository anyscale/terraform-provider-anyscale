package acctest

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// initial_cluster_config_id was removed: the API rejected every create that
// set it. A configuration that still declares it must fail at plan with
// Terraform's own unsupported-argument error, before any request is made.
func TestProjectResource_RemovedInitialClusterConfigIDFailsPlan(t *testing.T) {
	server := newMockProjectServer(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: testAccProviderBlock(server.URL) + `
resource "anyscale_project" "test" {
  name                      = "removed-attribute"
  cloud_id                  = "cld_mock_removed"
  initial_cluster_config_id = "ccfg_removed"
}
`,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`(?s)Unsupported argument.*initial_cluster_config_id`),
		}},
	})
}
