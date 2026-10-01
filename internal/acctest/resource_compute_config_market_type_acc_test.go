package acctest

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func marketTypeConfig(serverURL, name, marketType string) string {
	return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name      = %q
  cloud_id  = "cld_mock_cc"
  head_node = { instance_type = "m5.large" }
  worker_nodes = [
    { instance_type = "m5.xlarge", market_type = %q },
  ]
}
`, name, marketType)
}

func expectCreateRequests(s *mockComputeConfigServer, want int) resource.TestCheckFunc {
	return func(*terraform.State) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if got := len(s.createRequests); got != want {
			return fmt.Errorf("create requests = %d, want %d", got, want)
		}
		return nil
	}
}

// market_type has no wire field of its own: workerNodeConfigToAPI derives
// use_spot/fallback_to_ondemand from the three known values. An unrecognized
// value (here a lowercase "spot") used to plan cleanly, create the version, and
// then fail the apply as an inconsistent result. It must be rejected at plan
// time, before anything is sent.
func TestAccComputeConfigResource_UnrecognizedMarketTypeRejectedAtPlan_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newMockComputeConfigServerWithState(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      marketTypeConfig(server.URL, "cc-market-lower", "spot"),
				ExpectError: regexp.MustCompile(`(?s)market_type.*value must be one of`),
			},
			{
				// Nothing reached the API for the rejected config.
				Config:             marketTypeConfig(server.URL, "cc-market-lower", "SPOT"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				Check:              expectCreateRequests(mock, 0),
			},
		},
	})
}

// Positive control on the same path: a recognized value applies and round-trips.
func TestAccComputeConfigResource_RecognizedMarketTypeApplies_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newMockComputeConfigServerWithState(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: marketTypeConfig(server.URL, "cc-market-spot", "SPOT"),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
			},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("anyscale_compute_config.test", "worker_nodes.0.market_type", "SPOT"),
				expectCreateRequests(mock, 1),
			),
		}},
	})
}
