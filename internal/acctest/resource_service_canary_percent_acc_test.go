package acctest

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
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
	return outcomeSnapshot{CurrentState: "ROLLING_OUT", Primary: outcomeV1, Canary: &c, Manual: true, CanaryState: "RUNNING"}
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

// State written by a provider without version tracking has no record of a held canary, so a
// canary held from outside Terraform must not be adopted on its first read either: the plan
// stays empty instead of proposing to finish someone else's rollout.
func TestAccServiceResource_CanaryPercentLegacyStateOutOfBandCanaryPlansEmpty(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-legacy-oob"
	m, server := newOutcomeMock(t, "svc_canary_legacy_oob", name, func(n int, _ map[string]any) outcomeApplyResult {
		if n > 1 {
			t.Errorf("unexpected apply %d", n)
		}
		return outcomeCreate()
	})
	base := outcomeConfig(server.URL, name, "main:app", "")
	legacy := map[string]func() (tfprotov6.ProviderServer, error){
		"anyscale": func() (tfprotov6.ProviderServer, error) {
			s, err := ProtoV6ProviderFactories["anyscale"]()
			return privateStrippingServer{s}, err
		},
	}

	resource.Test(t, resource.TestCase{
		Steps: []resource.TestStep{
			{ProtoV6ProviderFactories: legacy, Config: base},
			{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				PreConfig:                func() { m.setScript(heldCanary(10)) },
				Config:                   base,
				PlanOnly:                 true,
			},
		},
	})
}

// heldAt is v2 held at percent with the given settled share (nil: not reported yet).
func heldAt(percent int, current *int) outcomeSnapshot {
	c := outcomeV2
	c.TargetWeight, c.CurrentWeight = intPtr(percent), current
	return outcomeSnapshot{CurrentState: "ROLLING_OUT", Primary: outcomeV1, Canary: &c, Manual: true, CanaryState: "RUNNING"}
}

func (m *outcomeMock) eventLog() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.events...)
}

// canaryApplyBodyCheck checks one recorded PUT /apply body's canary_percent, import path and
// whether it named a version.
func canaryApplyBodyCheck(m *outcomeMock, i int, wantPercent any, wantImportPath string, wantVersion bool) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if m.applyCount() <= i {
			return fmt.Errorf("apply %d was not sent (%d applies)", i+1, m.applyCount())
		}
		body := m.applyBody(i)
		got, has := body["canary_percent"]
		switch {
		case wantPercent == nil && has:
			return fmt.Errorf("apply %d sent canary_percent %v, want it omitted", i+1, got)
		case wantPercent != nil && got != wantPercent:
			return fmt.Errorf("apply %d canary_percent = %v, want %v", i+1, got, wantPercent)
		}
		apps, _ := body["ray_serve_config"].(map[string]any)["applications"].([]any)
		if len(apps) != 1 || apps[0].(map[string]any)["import_path"] != wantImportPath {
			return fmt.Errorf("apply %d ray_serve_config = %v, want import_path %q", i+1, body["ray_serve_config"], wantImportPath)
		}
		if _, named := body["version"]; named != wantVersion {
			return fmt.Errorf("apply %d sent version = %t, want %t", i+1, named, wantVersion)
		}
		return nil
	}
}

// The canary workflow: deploy held at 0, shift to 25, then remove canary_percent to finish. The
// shift re-sends the held version's config without a name (Anyscale matches it by config, so the
// canary moves), and every step plans empty afterwards. The first hold reports a null
// current_weight before settling: null is not settled, even at 0.
func TestAccServiceResource_CanaryPercentHoldShiftComplete(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-flow"
	settledV2 := outcomeSnapshot{CurrentState: "RUNNING", Primary: outcomeV2}
	m, server := newOutcomeMock(t, "svc_canary_flow", name, func(n int, _ map[string]any) outcomeApplyResult {
		switch n {
		case 1:
			return outcomeCreate()
		case 2:
			return outcomeApplyResult{Response: heldAt(0, nil), Script: []outcomeSnapshot{heldAt(0, nil), heldAt(0, intPtr(0))}}
		case 3:
			return outcomeApplyResult{Response: heldAt(25, intPtr(0)), Script: []outcomeSnapshot{heldAt(25, intPtr(25))}}
		default:
			return outcomeApplyResult{Response: heldAt(25, intPtr(25)), Script: []outcomeSnapshot{settledV2}}
		}
	})
	empty := resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", "")},
			{
				Config:           outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 0"),
				ConfigPlanChecks: empty,
				Check: resource.ComposeAggregateTestCheckFunc(
					canaryApplyBodyCheck(m, 1, float64(0), "main:app_v2", false),
					resource.TestCheckResourceAttr("anyscale_service.test", "canary_version.current_weight", "0"),
				),
			},
			{
				Config:           outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 25"),
				ConfigPlanChecks: empty,
				Check: resource.ComposeAggregateTestCheckFunc(
					canaryApplyBodyCheck(m, 2, float64(25), "main:app_v2", false),
					resource.TestCheckResourceAttr("anyscale_service.test", "canary_percent", "25"),
				),
			},
			{
				Config:           outcomeConfig(server.URL, name, "main:app_v2", ""),
				ConfigPlanChecks: empty,
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 4),
					canaryApplyBodyCheck(m, 3, nil, "main:app_v2", false),
					resource.TestCheckResourceAttr("anyscale_service.test", "primary_version.id", "svcver_v2"),
				),
			},
		},
	})
}

// With no canary present, a canary_percent change has nothing to split: it is recorded in state
// with no API write and takes effect on the next deploy.
func TestAccServiceResource_CanaryPercentNoCanaryIsStateOnly(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-state-only"
	m, server := newOutcomeMock(t, "svc_canary_state_only", name, func(n int, _ map[string]any) outcomeApplyResult {
		if n > 1 {
			t.Errorf("unexpected apply %d", n)
		}
		return outcomeCreate()
	})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", "")},
			{
				Config: outcomeConfig(server.URL, name, "main:app", "  canary_percent    = 25"),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 1),
					resource.TestCheckResourceAttr("anyscale_service.test", "canary_percent", "25"),
				),
			},
		},
	})
	if got := m.eventLog(); len(got) != 1 {
		t.Errorf("events = %v, want only the create apply", got)
	}
}

// A deploy while a canary is held rolls the canary back first, then deploys. Deploying back to
// the config that was live before the hold stops after the rollback: re-sending it would start a
// new version.
func TestAccServiceResource_CanaryPercentDeployWhileHeldRollsBack(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-redeploy"
	m, server := newOutcomeMock(t, "svc_canary_redeploy", name, func(n int, _ map[string]any) outcomeApplyResult {
		switch n {
		case 1:
			return outcomeCreate()
		case 2, 3:
			return outcomeApplyResult{Response: heldAt(10, intPtr(10)), Script: []outcomeSnapshot{heldAt(10, intPtr(10))}}
		default:
			t.Errorf("unexpected apply %d", n)
			return outcomeCreate()
		}
	})
	m.rollbackScript = []outcomeSnapshot{{CurrentState: "RUNNING", Primary: outcomeV1}}
	held := outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 10")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", "")},
			{Config: held},
			{
				// New config while held: rollback, then a new hold. The mock answers with v2 again,
				// which is enough to see the order.
				Config: outcomeConfig(server.URL, name, "main:app_v3", "  canary_percent    = 10"),
				Check: func(*terraform.State) error {
					want := []string{"apply", "apply", "rollback", "apply"}
					if got := m.eventLog(); fmt.Sprint(got) != fmt.Sprint(want) {
						return fmt.Errorf("events = %v, want %v", got, want)
					}
					return canaryApplyBodyCheck(m, 2, float64(10), "main:app_v3", false)(nil)
				},
			},
			{
				// Back to the pre-canary config: the rollback restores it, and nothing is re-sent.
				Config: outcomeConfig(server.URL, name, "main:app", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 3),
					resource.TestCheckResourceAttr("anyscale_service.test", "primary_version.id", "svcver_v1"),
					resource.TestCheckNoResourceAttr("anyscale_service.test", "canary_version.id"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// A held canary that fails is never rolled back by Anyscale. The apply holding it rolls it back
// itself, fails with Service Rollout Rolled Back, and the next plan proposes the change again.
func TestAccServiceResource_CanaryPercentFailedHoldRollsBack(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-failed"
	failed := heldAt(10, intPtr(0))
	failed.CanaryState, failed.CurrentState = "UNHEALTHY", "UNHEALTHY"
	m, server := newOutcomeMock(t, "svc_canary_failed", name, func(n int, _ map[string]any) outcomeApplyResult {
		if n == 1 {
			return outcomeCreate()
		}
		return outcomeApplyResult{Response: heldAt(10, nil), Script: []outcomeSnapshot{failed}}
	})
	m.rollbackScript = []outcomeSnapshot{{CurrentState: "RUNNING", Primary: outcomeV1}}
	held := outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 10")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", "")},
			{Config: held, ExpectError: regexp.MustCompile(`Service Rollout Rolled Back`)},
			{
				PreConfig: func() {
					if got := m.eventLog(); fmt.Sprint(got) != fmt.Sprint([]string{"apply", "apply", "rollback"}) {
						t.Errorf("events = %v, want the failed hold rolled back", got)
					}
				},
				Config:             held,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// Deploying back to the pre-canary config with a new version_name is not a revert: the
// rollback restores the old version under its old name, so the new name needs the PUT.
func TestAccServiceResource_CanaryPercentRevertWithNewNameDeploys(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-revert-name"
	renamed := outcomeVersion{ID: "svcver_v4", Name: "v1-again", ImportPath: "main:app"}
	m, server := newOutcomeMock(t, "svc_canary_revert_name", name, func(n int, _ map[string]any) outcomeApplyResult {
		switch n {
		case 1:
			return outcomeCreate()
		case 2:
			return outcomeApplyResult{Response: heldAt(10, intPtr(10)), Script: []outcomeSnapshot{heldAt(10, intPtr(10))}}
		default:
			return outcomeRollout(outcomeV1, renamed)
		}
	})
	m.rollbackScript = []outcomeSnapshot{{CurrentState: "RUNNING", Primary: outcomeV1}}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", "")},
			{Config: outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 10")},
			{
				Config: outcomeConfig(server.URL, name, "main:app", "  version_name      = \"v1-again\""),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 3),
					resource.TestCheckResourceAttr("anyscale_service.test", "version_name", "v1-again"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// canary_percent = 100 cuts all traffic over at once; where Anyscale completes the rollout the
// apply waits for it and ends on the new version.
func TestAccServiceResource_CanaryPercentCutoverCompletes(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "canary-cutover"
	m, server := newOutcomeMock(t, "svc_canary_cutover", name, func(n int, _ map[string]any) outcomeApplyResult {
		switch n {
		case 1:
			return outcomeCreate()
		case 2:
			return outcomeApplyResult{Response: heldAt(10, intPtr(10)), Script: []outcomeSnapshot{heldAt(10, intPtr(10))}}
		default:
			return outcomeApplyResult{Response: heldAt(100, intPtr(10)), Script: []outcomeSnapshot{
				heldAt(100, intPtr(100)), {CurrentState: "RUNNING", Primary: outcomeV2},
			}}
		}
	})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", "")},
			{Config: outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 10")},
			{
				Config: outcomeConfig(server.URL, name, "main:app_v2", "  canary_percent    = 100"),
				Check: resource.ComposeAggregateTestCheckFunc(
					canaryApplyBodyCheck(m, 2, float64(100), "main:app_v2", false),
					resource.TestCheckResourceAttr("anyscale_service.test", "primary_version.id", "svcver_v2"),
				),
			},
		},
	})
}
