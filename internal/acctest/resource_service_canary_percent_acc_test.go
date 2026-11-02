package acctest

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Mock acceptance tests for anyscale_service canary_percent (docs/decisions/service-canary-percent).

func intPtr(v int) *int { return &v }

// heldCanary is v2 held as a manual canary at percent, with its share settled.
func heldCanary(percent int) outcomeSnapshot {
	c := outcomeV2
	c.TargetWeight, c.CurrentWeight = intPtr(percent), intPtr(percent)
	return outcomeSnapshot{CurrentState: "ROLLING_OUT", Primary: outcomeV1, Canary: &c, Manual: true}
}

// A canary held from outside Terraform (anyscale service deploy --canary-percent) is not this
// resource's rollout: Read must not adopt its share, so a config without canary_percent plans
// empty rather than proposing to finish someone else's rollout.
func TestAccServiceResource_CanaryPercentOutOfBandCanaryPlansEmpty(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-oob"
	m, server := newOutcomeMock(t, "svc_canary_oob", name, func(n int, _ map[string]any) outcomeApplyResult {
		if n > 1 {
			t.Errorf("unexpected apply %d", n)
		}
		return outcomeCreate()
	})
	base := outcomeConfig(server.URL, name, "main:app", "")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: base},
			{
				PreConfig: func() { m.setScript(heldCanary(10)) },
				Config:    base,
				PlanOnly:  true,
			},
		},
	})
}

// Importing a service with a manually held canary recovers canary_percent from it and adopts
// the hold, so a config that declares the same share plans empty.
func TestAccServiceResource_CanaryPercentImportRecoversHeldCanary(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-import"
	m, server := newOutcomeMock(t, "svc_canary_import", name, func(int, map[string]any) outcomeApplyResult {
		t.Errorf("unexpected apply after import")
		return outcomeCreate()
	})
	m.setScript(heldCanary(25))
	held := outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 25")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "anyscale_service.test",
				ImportState:        true,
				ImportStateId:      "svc_canary_import",
				ImportStatePersist: true,
				Config:             held,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					return expectImportedAttr(states, "canary_percent", "25")
				},
			},
			{
				// rollout_strategy's default is the accepted post-import residual (see
				// TestAccServiceResource_ImportSeedsTrackingResource); applying records it locally
				// without a PUT /apply, and nothing else differs.
				Config: held,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.TestCheckResourceAttr("anyscale_service.test", "canary_percent", "25"),
			},
		},
	})
}

// canary_percent splits traffic between two versions, which an IN_PLACE upgrade never has.
func TestAccServiceResource_CanaryPercentInPlaceRejected(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-inplace"
	_, server := newOutcomeMock(t, "svc_canary_inplace", name, func(int, map[string]any) outcomeApplyResult {
		t.Errorf("unexpected apply")
		return outcomeCreate()
	})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: outcomeConfig(server.URL, name, "main:app",
				"  canary_percent    = 10\n  rollout_strategy  = \"IN_PLACE\""),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`canary_percent Requires ROLLOUT`),
		}},
	})
}

// While a canary is held, renaming it alone is rejected at plan: the next apply only re-sends
// the held version, and Anyscale keeps that version's name.
func TestAccServiceResource_CanaryPercentRenameWhileHeldRejected(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-rename"
	m, server := newOutcomeMock(t, "svc_canary_rename", name, func(int, map[string]any) outcomeApplyResult {
		t.Errorf("unexpected apply")
		return outcomeCreate()
	})
	m.setScript(heldCanary(25))
	held := outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 25")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "anyscale_service.test",
				ImportState:        true,
				ImportStateId:      "svc_canary_rename",
				ImportStatePersist: true,
				Config:             held,
			},
			{
				Config: outcomeConfig(server.URL, name, "main:app_v2",
					"  canary_percent    = 25\n  version_name      = \"renamed\""),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`Cannot Rename a Held Canary`),
			},
		},
	})
}

// expectImportedAttr checks one attribute of the single imported instance.
func expectImportedAttr(states []*terraform.InstanceState, key, want string) error {
	if len(states) != 1 {
		return fmt.Errorf("imported %d instances, want 1", len(states))
	}
	if got := states[0].Attributes[key]; got != want {
		return fmt.Errorf("imported %s = %q, want %q", key, got, want)
	}
	return nil
}
