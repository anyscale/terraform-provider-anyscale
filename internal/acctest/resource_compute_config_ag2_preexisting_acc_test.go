package acctest

// Every existing compute_config import test seeds its own fixture inside
// the same test function that then
// imports it - none proves import of a compute config that is genuinely
// pre-existing, created entirely outside that test's own Terraform lifecycle.
// This closes that gap using CreateEphemeralComputeConfig, which hits the raw
// API directly and never touches the anyscale_compute_config resource.

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccComputeConfigResource_ImportPreExistingOutOfBandFixture(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	PreCheck(t)
	cloudID := GetComputeConfigCloudID(t)

	const instanceType = "m5.large"
	fixture, err := CreateEphemeralComputeConfig(t, cloudID, instanceType)
	if err != nil {
		t.Fatalf("failed to create out-of-band compute config fixture: %v", err)
	}

	config := fmt.Sprintf(`
resource "anyscale_compute_config" "test" {
  name     = %[1]q
  cloud_id = %[2]q

  head_node = {
    instance_type = %[3]q
  }
}
`, fixture.Name, cloudID, instanceType)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Cold import: this test's own Terraform lifecycle never
				// created fixture.ConfigID - CreateEphemeralComputeConfig did,
				// entirely out of band. No preceding Create step exists, so
				// per this repo's own testing guidance this uses
				// ImportStatePersist + ImportStateCheck, not
				// ImportStateVerify (which requires a prior apply to diff
				// against).
				ResourceName:       "anyscale_compute_config.test",
				ImportState:        true,
				ImportStateId:      fixture.ConfigID,
				ImportStatePersist: true,
				Config:             config,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported instance state, got %d", len(states))
					}
					attrs := states[0].Attributes
					if got := attrs["config_id"]; got != fixture.ConfigID {
						return fmt.Errorf("config_id = %q, want the real pre-existing id %q", got, fixture.ConfigID)
					}
					if got := attrs["id"]; got != fixture.Name {
						return fmt.Errorf("id (name) = %q, want %q", got, fixture.Name)
					}
					if got := attrs["head_node.instance_type"]; got != instanceType {
						return fmt.Errorf("head_node.instance_type = %q, want %q - a pre-existing config's real fields must be recovered, not left blank", got, instanceType)
					}
					return nil
				},
			},
			{
				// The self-heal bar: an equivalent config plans clean after
				// importing something Terraform never created.
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}
