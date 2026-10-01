package acctest

// maximum_uptime_minutes and node resources have no server default: the API
// stores what was sent and returns null when unset. So removing either from
// configuration must clear it - plan an update to null, then send a version
// without it - and a configuration that never sets it, or keeps it, must plan
// nothing.

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func optionalOnlyConfig(serverURL, name, topLevel, headAttrs, workerAttrs string) string {
	return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = %q
  cloud_id = "cld_mock_cc"
%s
  head_node = {
    instance_type = "m5.large"
%s
  }
  worker_nodes = [
    {
      name          = "w"
      instance_type = "m5.xlarge"
%s
    },
  ]
}
`, name, topLevel, headAttrs, workerAttrs)
}

// lastCreateRequest returns the config object of the most recent create
// request the mock received.
func lastCreateRequest(s *mockComputeConfigServer) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.createRequests) == 0 {
		return nil, fmt.Errorf("mock received no create requests")
	}
	return s.createRequests[len(s.createRequests)-1], nil
}

func TestAccComputeConfigResource_RemovingOptionalOnlyFieldsClearsThem_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newMockComputeConfigServerWithState(t)
	const addr = "anyscale_compute_config.test"
	set := optionalOnlyConfig(server.URL, "cc-optional-only", "  maximum_uptime_minutes = 60",
		"    resources = { CPU = 4 }", "      resources = { CPU = 2 }")
	removed := optionalOnlyConfig(server.URL, "cc-optional-only", "", "", "")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Set and kept: no diff.
				Config: set,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: removed,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
						plancheck.ExpectKnownValue(addr, tfjsonpath.New("maximum_uptime_minutes"), knownvalue.Null()),
						plancheck.ExpectKnownValue(addr, tfjsonpath.New("head_node").AtMapKey("resources"), knownvalue.Null()),
						plancheck.ExpectKnownValue(addr, tfjsonpath.New("worker_nodes").AtSliceIndex(0).AtMapKey("resources"), knownvalue.Null()),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(addr, "maximum_uptime_minutes"),
					resource.TestCheckNoResourceAttr(addr, "head_node.resources.%"),
					resource.TestCheckNoResourceAttr(addr, "worker_nodes.0.resources.%"),
					func(*terraform.State) error {
						req, err := lastCreateRequest(mock)
						if err != nil {
							return err
						}
						if v, ok := req["maximum_uptime_minutes"]; ok && v != nil {
							return fmt.Errorf("new version still sent maximum_uptime_minutes = %v", v)
						}
						head, _ := req["head_node_type"].(map[string]any)
						if v, ok := head["resources"]; ok && v != nil {
							return fmt.Errorf("new version still sent head_node resources = %v", v)
						}
						return nil
					},
				),
			},
		},
	})
}

// Control: never set plans nothing on re-apply.
func TestAccComputeConfigResource_OptionalOnlyFieldsNeverSetPlanEmpty_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, _ := newMockComputeConfigServerWithState(t)
	config := optionalOnlyConfig(server.URL, "cc-optional-never", "", "", "")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}
