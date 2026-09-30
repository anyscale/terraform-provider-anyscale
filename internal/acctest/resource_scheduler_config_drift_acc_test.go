package acctest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// ApplyOutOfBand simulates a writer other than Terraform (the CLI, the
// console) applying document: like a real apply it mints a new version and
// becomes what GET returns, but it is not counted as a provider write. Later
// provider applies still echo their own posted document.
func (s *schedulerConfigServer) ApplyOutOfBand(document string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version++
	s.readConfig = document
}

// TestAccSchedulerConfigResourceOutOfBandDrift_MockServer: a config changed
// outside Terraform is picked up by refresh and shows as a planned Update, and
// the corrective apply posts the declared document back.
//
// The two halves fail independently. A Read that returned prior state instead
// of re-reading would leave the refreshed flavor unchanged and the plan empty.
// An Update that detected the drift but sent the wrong document would pass the
// plan check and fail the corrective-write assertions.
func TestAccSchedulerConfigResourceOutOfBandDrift_MockServer(t *testing.T) {
	server, mock := newSchedulerConfigServer(t, schedulerConfigServerOpts{EchoAppliedConfig: true})
	defer server.Close()

	const declaredFlavor = "tfacc-drift-declared"
	const driftedFlavor = "tfacc-drift-out-of-band"
	const resourceName = "anyscale_scheduler_config.test"

	config := testAccProviderBlock(server.URL) + fmt.Sprintf(`
resource "anyscale_scheduler_config" "test" {
  resource_flavors = [
    { name = %q },
  ]
}
`, declaredFlavor)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "version", "1"),
					resource.TestCheckResourceAttr(resourceName, "resource_flavors.0.name", declaredFlavor),
				),
			},
			{
				// Out-of-band write, then refresh only: state must now carry
				// the drifted document, and the plan against config is
				// non-empty.
				PreConfig: func() {
					mock.ApplyOutOfBand(fmt.Sprintf(`{"resource_flavors":[{"name":%q}]}`, driftedFlavor))
				},
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "resource_flavors.0.name", driftedFlavor),
					resource.TestCheckResourceAttr(resourceName, "version", "2"),
				),
			},
			{
				// The unchanged config plans an in-place Update that restores
				// the declared document.
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "resource_flavors.0.name", declaredFlavor),
					resource.TestCheckResourceAttr(resourceName, "version", "3"),
					func(*terraform.State) error {
						if got := mock.WriteCount(); got != 2 {
							return fmt.Errorf("expected 2 provider writes (create + correction), got %d", got)
						}
						body := string(mock.LastApplyBody())
						if !strings.Contains(body, declaredFlavor) || strings.Contains(body, driftedFlavor) {
							return fmt.Errorf("corrective write did not post the declared document: %s", body)
						}
						return nil
					},
				),
			},
		},
	})
}
