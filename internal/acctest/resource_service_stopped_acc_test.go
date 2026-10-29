// Mock-backed suite for anyscale_service's stopped argument (docs/decisions/service-stopped).
// The outcome mock models termination as traced: POST /terminate sets goal_state TERMINATED, the
// service reports TERMINATING before TERMINATED with its last version still primary, an apply
// while it is terminating gets a 409, and an apply after that restarts the same id with the new
// version as primary.
package acctest

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

const stoppedLine = "  stopped = true"

// stoppedOnApply answers every apply with a new version, v<n>, as primary, running the import_path
// the apply sent: the create shape, which a restart shares.
func stoppedOnApply() func(int, map[string]any) outcomeApplyResult {
	return func(n int, body map[string]any) outcomeApplyResult {
		return outcomeCreateOf(outcomeVersion{
			ID: fmt.Sprintf("svcver_v%d", n), Name: fmt.Sprintf("v%d", n), ImportPath: appliedImportPath(body),
		})
	}
}

func appliedImportPath(body map[string]any) string {
	rsc, _ := body["ray_serve_config"].(map[string]any)
	apps, _ := rsc["applications"].([]any)
	if len(apps) == 0 {
		return ""
	}
	app, _ := apps[0].(map[string]any)
	p, _ := app["import_path"].(string)
	return p
}

func expectTerminateCount(m *outcomeMock, want int) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := m.terminateCount(); got != want {
			return fmt.Errorf("POST /terminate called %d time(s), want %d", got, want)
		}
		return nil
	}
}

func expectApplyBodyField(m *outcomeMock, i int, field string, want any) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := m.applyBody(i)[field]; got != want {
			return fmt.Errorf("apply %d sent %s = %v, want %v", i+1, field, got, want)
		}
		return nil
	}
}

func checkServiceState(stopped bool, current, goal, primaryID string) resource.TestCheckFunc {
	return resource.ComposeAggregateTestCheckFunc(
		resource.TestCheckResourceAttr("anyscale_service.test", "stopped", fmt.Sprint(stopped)),
		resource.TestCheckResourceAttr("anyscale_service.test", "current_state", current),
		resource.TestCheckResourceAttr("anyscale_service.test", "goal_state", goal),
		resource.TestCheckResourceAttr("anyscale_service.test", "primary_version.id", primaryID),
	)
}

func planUpdatesStopped(want bool) []plancheck.PlanCheck {
	return []plancheck.PlanCheck{
		plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
		plancheck.ExpectKnownValue("anyscale_service.test", tfjsonpath.New("stopped"), knownvalue.Bool(want)),
		plancheck.ExpectUnknownValue("anyscale_service.test", tfjsonpath.New("current_state")),
	}
}

// TestAccServiceResource_StopAndRestartResource: stopped = true terminates the service, waits for
// TERMINATED and keeps the id; stopped = false restarts the same id with a new version through
// a standard PUT /apply. Each transition plans an in-place update.
func TestAccServiceResource_StopAndRestartResource(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "stopped-cycle"
	m, server := newOutcomeMock(t, "svc_stopped_cycle", name, stoppedOnApply())
	m.terminatingPolls = 1

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: outcomeConfig(server.URL, name, "main:app", ""),
				Check:  checkServiceState(false, "RUNNING", "RUNNING", "svcver_v1"),
			},
			{
				Config: outcomeConfig(server.URL, name, "main:app", stoppedLine),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             planUpdatesStopped(true),
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectTerminateCount(m, 1),
					expectOutcomeApplyCount(m, 1),
					// The wait saw TERMINATING first and returned only at TERMINATED.
					checkServiceState(true, "TERMINATED", "TERMINATED", "svcver_v1"),
					resource.TestCheckResourceAttr("anyscale_service.test", "id", "svc_stopped_cycle"),
					resource.TestCheckResourceAttr("anyscale_service.test", "hostname", "outcome.example.com"),
					resource.TestCheckResourceAttrSet("anyscale_service.test", "ended_at"),
				),
			},
			{
				Config: outcomeConfig(server.URL, name, "main:app", "  stopped = false"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             planUpdatesStopped(false),
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectTerminateCount(m, 1),
					expectOutcomeApplyCount(m, 2),
					expectApplyBodyField(m, 1, "rollout_strategy", "ROLLOUT"),
					checkServiceState(false, "RUNNING", "RUNNING", "svcver_v2"),
					resource.TestCheckResourceAttr("anyscale_service.test", "id", "svc_stopped_cycle"),
					resource.TestCheckResourceAttr("anyscale_service.test", "hostname", "outcome.example.com"),
					resource.TestCheckResourceAttr("anyscale_service.test", "version_name", "v2"),
				),
			},
		},
	})
}

// TestAccServiceResource_StoppedRejectsDeployChangeResource: a plan that stops the service, or
// keeps it stopped, cannot change a deploy field; one that restarts it can, and the restart
// deploys the new config. Non-deploy inputs change freely while stopped, and a replacement cannot
// create a stopped service.
func TestAccServiceResource_StoppedRejectsDeployChangeResource(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "stopped-frozen"
	m, server := newOutcomeMock(t, "svc_stopped_frozen", name, stoppedOnApply())
	inPlace := "\n  rollout_strategy = \"IN_PLACE\""

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", "")},
			{
				Config:      outcomeConfig(server.URL, name, "main:app_v2", stoppedLine),
				PlanOnly:    true, // rejected at plan time, not at apply
				ExpectError: regexp.MustCompile(`(?s)Cannot Change a Stopped Service's Deployment.*stops the service.*ray_serve_config`),
			},
			{
				Config: outcomeConfig(server.URL, name, "main:app", stoppedLine),
				Check:  expectTerminateCount(m, 1),
			},
			{
				Config:      outcomeConfig(server.URL, name, "main:app_v2", stoppedLine),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)keeps the service stopped.*ray_serve_config`),
			},
			{
				Config:      outcomeConfig(server.URL, name+"-renamed", "main:app", stoppedLine),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`Service Cannot Be Created Stopped`),
			},
			{
				// rollout_strategy is not a deploy field: a state-only change, nothing sent.
				Config: outcomeConfig(server.URL, name, "main:app", stoppedLine+inPlace),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectTerminateCount(m, 1),
					expectOutcomeApplyCount(m, 1),
					resource.TestCheckResourceAttr("anyscale_service.test", "rollout_strategy", "IN_PLACE"),
				),
			},
			{
				// Restarting with a changed config is one PUT /apply, a standard rollout.
				Config: outcomeConfig(server.URL, name, "main:app_v2", inPlace),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             planUpdatesStopped(false),
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 2),
					expectApplyBodyField(m, 1, "rollout_strategy", "ROLLOUT"),
					func(*terraform.State) error {
						if got := appliedImportPath(m.applyBody(1)); got != "main:app_v2" {
							return fmt.Errorf("restart sent import_path %q, want main:app_v2", got)
						}
						return nil
					},
					checkServiceState(false, "RUNNING", "RUNNING", "svcver_v2"),
				),
			},
		},
	})
}

// TestAccServiceResource_CreateStoppedRejectedResource: a service cannot be created stopped; the
// plan fails before anything is sent.
func TestAccServiceResource_CreateStoppedRejectedResource(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "stopped-create"
	m, server := newOutcomeMock(t, "svc_stopped_create", name, stoppedOnApply())

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      outcomeConfig(server.URL, name, "main:app", stoppedLine),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`Service Cannot Be Created Stopped`),
			},
		},
	})
	if got := m.applyCount(); got != 0 {
		t.Errorf("PUT /apply called %d time(s), want 0", got)
	}
}

// TestAccServiceResource_StoppedServiceRestartedOutOfBandResource: a stopped service restarted
// outside Terraform refreshes stopped = false, so the plan shows false -> true and apply stops it
// again. If that restart also deployed a different config, the stop is rejected (the refreshed
// ray_serve_config differs from config): the known cost of rejecting deploy changes while stopping.
func TestAccServiceResource_StoppedServiceRestartedOutOfBandResource(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "stopped-oob"
	m, server := newOutcomeMock(t, "svc_stopped_oob", name, stoppedOnApply())
	stopped := outcomeConfig(server.URL, name, "main:app", stoppedLine)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", "")},
			{Config: stopped, Check: expectTerminateCount(m, 1)},
			{
				PreConfig: func() {
					m.restartOutOfBand(outcomeSnapshot{CurrentState: "RUNNING",
						Primary: outcomeVersion{ID: "svcver_cli", Name: "cli", ImportPath: "main:app"}})
				},
				Config: stopped,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             planUpdatesStopped(true),
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectTerminateCount(m, 2),
					expectOutcomeApplyCount(m, 1),
					checkServiceState(true, "TERMINATED", "TERMINATED", "svcver_cli"),
				),
			},
			{
				PreConfig: func() {
					m.restartOutOfBand(outcomeSnapshot{CurrentState: "RUNNING",
						Primary: outcomeVersion{ID: "svcver_cli2", Name: "cli2", ImportPath: "main:from_cli"}})
				},
				Config:      stopped,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)stops the service.*ray_serve_config`),
			},
		},
	})
}

// TestAccServiceResource_PreStoppedStateRestartsResource (Gate 2): state written before stopped
// existed holds null. The first refresh fills it from goal_state: a running service plans empty,
// and a terminated one plans stopped true -> false and restarts, as it did before this argument.
func TestAccServiceResource_PreStoppedStateRestartsResource(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "stopped-legacy"
	m, server := newOutcomeMock(t, "svc_stopped_legacy", name, stoppedOnApply())
	cfg := outcomeConfig(server.URL, name, "main:app", "")
	old := map[string]func() (tfprotov6.ProviderServer, error){
		"anyscale": func() (tfprotov6.ProviderServer, error) {
			s, err := ProtoV6ProviderFactories["anyscale"]()
			return preParityServer{ProviderServer: s, attrs: []string{"stopped"}}, err
		},
	}

	// The simulation still plans the stopped default against the stripped null.
	oldStep := resource.TestStep{
		ProtoV6ProviderFactories: old,
		Config:                   cfg,
		Check:                    resource.TestCheckNoResourceAttr("anyscale_service.test", "stopped"),
		ExpectNonEmptyPlan:       true,
	}

	resource.Test(t, resource.TestCase{
		Steps: []resource.TestStep{
			oldStep,
			{
				// Running: the refresh fills stopped = false, so nothing is planned or sent.
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Config:                   cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 1),
					resource.TestCheckResourceAttr("anyscale_service.test", "stopped", "false"),
				),
			},
			oldStep, // back to a null stopped in state
			{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				PreConfig: func() {
					m.setScript(outcomeSnapshot{CurrentState: "TERMINATED", GoalState: "TERMINATED",
						Primary: outcomeVersion{ID: "svcver_v1", Name: "v1", ImportPath: "main:app"}})
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             planUpdatesStopped(false),
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 2),
					checkServiceState(false, "RUNNING", "RUNNING", "svcver_v2"),
				),
			},
		},
	})
}

// TestAccServiceResource_ImportStoppedServiceResource: importing a terminated service records
// stopped = true (Test A, in the import step), and a config that says so plans no stop or restart
// against the imported state (Test B: the persisted import is what the next step plans against).
func TestAccServiceResource_ImportStoppedServiceResource(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "stopped-import"
	m, server := newOutcomeMock(t, "svc_stopped_import", name, func(int, map[string]any) outcomeApplyResult {
		t.Errorf("unexpected apply after import")
		return outcomeCreate()
	})
	m.setScript(outcomeSnapshot{CurrentState: "TERMINATED", GoalState: "TERMINATED", Primary: outcomeV1})
	cfg := outcomeConfig(server.URL, name, "main:app", stoppedLine)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "anyscale_service.test",
				ImportState:        true,
				ImportStateId:      "svc_stopped_import",
				ImportStatePersist: true,
				Config:             cfg,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if got := states[0].Attributes["stopped"]; got != "true" {
						return fmt.Errorf("imported stopped = %q, want true", got)
					}
					return nil
				},
			},
			{
				// rollout_strategy's default is the accepted post-import residual.
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectTerminateCount(m, 0),
					expectOutcomeApplyCount(m, 0),
					resource.TestCheckResourceAttr("anyscale_service.test", "stopped", "true"),
				),
			},
		},
	})
}

// TestAccServiceResource_StoppedRestartFailsResource: a restart whose new version fails fails the
// apply like a failed create. The failed version is primary and there is no previous running
// version to roll back to, so state keeps the configured values and the next plan is empty; if
// Anyscale then terminates the service, the refresh plans another restart.
func TestAccServiceResource_StoppedRestartFailsResource(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "stopped-restart-fails"
	v2 := outcomeVersion{ID: "svcver_v2", Name: "v2", ImportPath: "main:app"}
	m, server := newOutcomeMock(t, "svc_stopped_restart_fails", name, func(n int, body map[string]any) outcomeApplyResult {
		if n == 1 {
			return outcomeCreate()
		}
		return outcomeApplyResult{
			Response: outcomeSnapshot{CurrentState: "STARTING", Primary: v2},
			Script: []outcomeSnapshot{
				{CurrentState: "STARTING", Primary: v2},
				{CurrentState: "USER_ERROR_FAILURE", Primary: v2},
			},
		}
	})
	running := outcomeConfig(server.URL, name, "main:app", "")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: running},
			{Config: outcomeConfig(server.URL, name, "main:app", stoppedLine)},
			{
				Config:      running,
				ExpectError: regexp.MustCompile(`service entered USER_ERROR_FAILURE state`),
			},
			{
				Config:   running,
				PlanOnly: true,
			},
			{
				PreConfig: func() {
					m.setScript(outcomeSnapshot{CurrentState: "TERMINATED", GoalState: "TERMINATED", Primary: v2})
				},
				Config:             running,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: planUpdatesStopped(false),
				},
			},
		},
	})
	if got := m.applyCount(); got != 2 {
		t.Errorf("PUT /apply called %d time(s), want 2 (create, restart)", got)
	}
}
