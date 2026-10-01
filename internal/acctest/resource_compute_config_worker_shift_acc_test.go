package acctest

// Removing a worker group from the middle of worker_nodes shifts every later
// group to a new list index. The framework pairs nested attributes with prior
// state by index, so the shifted group must not inherit the name or resources
// of the group that used to sit at its index - and must not send them to the
// API as if configured.

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

const workerShiftAddr = "anyscale_compute_config.test"

func workerShiftConfig(serverURL, name, workers string) string {
	return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name      = %q
  cloud_id  = "cld_mock_cc"
  head_node = { instance_type = "m5.large" }
  worker_nodes = [
%s
  ]
}
`, name, workers)
}

// lastSentWorker returns worker_node_types[0] from the most recent create
// request the mock received.
func lastSentWorker(s *mockComputeConfigServer) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.createRequests) == 0 {
		return nil, fmt.Errorf("mock received no create requests")
	}
	workers, _ := s.createRequests[len(s.createRequests)-1]["worker_node_types"].([]any)
	if len(workers) == 0 {
		return nil, fmt.Errorf("last create request has no worker_node_types")
	}
	worker, _ := workers[0].(map[string]any)
	return worker, nil
}

func TestAccComputeConfigResource_RemovedWorkerDoesNotLendNameToShiftedWorker_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newMockComputeConfigServerWithState(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: workerShiftConfig(server.URL, "cc-shift-name", `
    { instance_type = "m5.large", resources = { CPU = 2 } },
    { instance_type = "m5.xlarge" },`)},
			{
				// The first (unnamed) group is removed; the m5.xlarge group moves to index 0.
				Config: workerShiftConfig(server.URL, "cc-shift-name", `
    { instance_type = "m5.xlarge" },`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectUnknownValue(workerShiftAddr, tfjsonpath.New("worker_nodes").AtSliceIndex(0).AtMapKey("name")),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(workerShiftAddr, "worker_nodes.0.name", "m5.xlarge"),
					resource.TestCheckNoResourceAttr(workerShiftAddr, "worker_nodes.0.resources.%"),
					func(*terraform.State) error {
						worker, err := lastSentWorker(mock)
						if err != nil {
							return err
						}
						if name, ok := worker["name"]; ok && name != "m5.xlarge" {
							return fmt.Errorf("sent worker name %v, want unset or m5.xlarge", name)
						}
						if res, ok := worker["resources"]; ok {
							return fmt.Errorf("sent resources %v for a worker that never configured them", res)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestAccComputeConfigResource_RemovedWorkerDoesNotLendResourcesToShiftedWorker_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newMockComputeConfigServerWithState(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: workerShiftConfig(server.URL, "cc-shift-res", `
    { name = "a", instance_type = "m5.large", resources = { CPU = 2 } },
    { name = "b", instance_type = "m5.xlarge" },`)},
			{
				Config: workerShiftConfig(server.URL, "cc-shift-res", `
    { name = "b", instance_type = "m5.xlarge" },`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectUnknownValue(workerShiftAddr, tfjsonpath.New("worker_nodes").AtSliceIndex(0).AtMapKey("resources")),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(workerShiftAddr, "worker_nodes.0.name", "b"),
					resource.TestCheckNoResourceAttr(workerShiftAddr, "worker_nodes.0.resources.%"),
					func(*terraform.State) error {
						worker, err := lastSentWorker(mock)
						if err != nil {
							return err
						}
						if res, ok := worker["resources"]; ok {
							return fmt.Errorf("sent resources %v for worker b, which never configured them", res)
						}
						return nil
					},
				),
			},
		},
	})
}

// Positive control on the same path: when the groups that remain keep their
// index, their prior values are still reused (no spurious unknowns), and an
// unchanged config plans nothing.
func TestAccComputeConfigResource_RemovingLastWorkerKeepsOthersStable_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, _ := newMockComputeConfigServerWithState(t)
	two := workerShiftConfig(server.URL, "cc-shift-last", `
    { instance_type = "m5.large", resources = { CPU = 2 } },
    { instance_type = "m5.xlarge" },`)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: two,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: workerShiftConfig(server.URL, "cc-shift-last", `
    { instance_type = "m5.large", resources = { CPU = 2 } },`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectKnownValue(workerShiftAddr, tfjsonpath.New("worker_nodes").AtSliceIndex(0).AtMapKey("name"), knownvalue.StringExact("m5.large")),
					},
				},
				Check: resource.TestCheckResourceAttr(workerShiftAddr, "worker_nodes.0.resources.CPU", "2"),
			},
		},
	})
}
