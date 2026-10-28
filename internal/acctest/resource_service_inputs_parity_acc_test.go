// Mock-backed suite for the anyscale_service inputs added for CLI parity: the service config
// (query_auth_token_enabled, grpc_service_names, env_vars), image_uri and version_name. Design and
// gate results: docs/decisions/service-inputs-parity. Uses the scriptable outcomeMock
// (resource_service_rollout_outcome_acc_test.go), which records every apply body and tracks the
// bearer token the way the backend does.
package acctest

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// applyConfig returns apply body i's `config` object.
func (m *outcomeMock) applyConfig(i int) (map[string]any, error) {
	cfg, ok := m.applyBody(i)["config"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("apply %d sent no config object: %v", i+1, m.applyBody(i)["config"])
	}
	return cfg, nil
}

// expectApplyBearer asserts apply body i's config.access.use_bearer_token.
func expectApplyBearer(m *outcomeMock, i int, want bool) resource.TestCheckFunc {
	return func(*terraform.State) error {
		cfg, err := m.applyConfig(i)
		if err != nil {
			return err
		}
		access, _ := cfg["access"].(map[string]any)
		if got, ok := access["use_bearer_token"].(bool); !ok || got != want {
			return fmt.Errorf("apply %d sent config.access %v, want use_bearer_token = %v", i+1, cfg["access"], want)
		}
		return nil
	}
}

func outcomeRolloutFromV1() func(int, map[string]any) outcomeApplyResult {
	return func(n int, _ map[string]any) outcomeApplyResult {
		if n == 1 {
			return outcomeCreate()
		}
		return outcomeRollout(outcomeV1, outcomeV2)
	}
}

// TestAccServiceResource_AuthDisabledSendsAccessConfig_MockServer: false is sent as
// config.access.use_bearer_token, recorded in state from the (absent) token, and plans clean.
func TestAccServiceResource_AuthDisabledSendsAccessConfig_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-auth-off"
	m, server := newOutcomeMock(t, "svc_parity_auth_off", name, outcomeRolloutFromV1())
	cfg := outcomeConfig(server.URL, name, "main:app", `  query_auth_token_enabled = false`)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					expectApplyBearer(m, 0, false),
					resource.TestCheckResourceAttr("anyscale_service.test", "query_auth_token_enabled", "false"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// TestAccServiceResource_AuthToggleRollsOut_MockServer: toggling query_auth_token_enabled alone is
// a deploy (one apply plus a rollout wait), and true is sent explicitly.
func TestAccServiceResource_AuthToggleRollsOut_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-auth-toggle"
	m, server := newOutcomeMock(t, "svc_parity_auth_toggle", name, outcomeRolloutFromV1())

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: outcomeConfig(server.URL, name, "main:app", ""),
				Check:  expectApplyBearer(m, 0, true),
			},
			{
				Config: outcomeConfig(server.URL, name, "main:app", `  query_auth_token_enabled = false`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 2),
					expectApplyBearer(m, 1, false),
					resource.TestCheckResourceAttr("anyscale_service.test", "query_auth_token_enabled", "false"),
					resource.TestCheckResourceAttr("anyscale_service.test", "primary_version.id", "svcver_v2"),
				),
			},
		},
	})
}

// TestAccServiceResource_AuthDriftDetected_MockServer: auth turned off outside Terraform (the
// token disappears from GET) plans an update back to the configured value.
func TestAccServiceResource_AuthDriftDetected_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-auth-drift"
	m, server := newOutcomeMock(t, "svc_parity_auth_drift", name, outcomeRolloutFromV1())
	cfg := outcomeConfig(server.URL, name, "main:app", "")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig:          func() { m.setToken(nil) },
				Config:             cfg,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})
}

// TestAccServiceResource_AuthTokenNotIssuedErrors_MockServer: when Anyscale does not issue a
// token although true was requested (a flag can force tokens off), the update fails naming the
// cause, and state records the observed false (Gate 2: Core keeps state returned with an error).
func TestAccServiceResource_AuthTokenNotIssuedErrors_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-auth-forced-off"
	m, server := newOutcomeMock(t, "svc_parity_auth_forced_off", name, outcomeRolloutFromV1())

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", `  query_auth_token_enabled = false`)},
			{
				PreConfig:   func() { m.noTokens = true },
				Config:      outcomeConfig(server.URL, name, "main:app", ""),
				ExpectError: regexp.MustCompile(`(?s)Bearer Token Not Issued.*query_auth_token_enabled = false`),
			},
			{
				// The service has no token, so the configured true still plans an update.
				Config:             outcomeConfig(server.URL, name, "main:app", ""),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_service.test", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				Config: outcomeConfig(server.URL, name, "main:app", `  query_auth_token_enabled = false`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_service.test", "query_auth_token_enabled", "false"),
					expectOutcomeApplyCount(m, 2),
				),
			},
		},
	})
}

// TestAccServiceResource_InPlaceRejectsAuthChange: IN_PLACE ignores the service config, so a
// toggle under it is rejected at plan time.
func TestAccServiceResource_InPlaceRejectsAuthChange(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-auth-inplace"
	m, server := newOutcomeMock(t, "svc_parity_auth_inplace", name, outcomeRolloutFromV1())
	const inPlace = `  rollout_strategy = "IN_PLACE"`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", inPlace)},
			{
				Config: outcomeConfig(server.URL, name, "main:app", inPlace+`
  query_auth_token_enabled = false`),
				ExpectError: regexp.MustCompile(`(?s)IN_PLACE.*query_auth_token_enabled`),
			},
		},
	})
	if got := m.applyCount(); got != 1 {
		t.Errorf("apply called %d times, want 1", got)
	}
}

// TestAccServiceResource_ImportRecoversAuthDisabled (Test A): a cold import of a service with
// auth off records false, so a config that sets false plans clean.
func TestAccServiceResource_ImportRecoversAuthDisabled(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-auth-import"
	m, server := newOutcomeMock(t, "svc_parity_auth_import", name, func(int, map[string]any) outcomeApplyResult {
		t.Errorf("unexpected apply after import")
		return outcomeCreate()
	})
	m.setScript(outcomeSnapshot{CurrentState: "RUNNING", Primary: outcomeV1})
	m.setToken(nil)
	cfg := outcomeConfig(server.URL, name, "main:app", `  query_auth_token_enabled = false`)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				ResourceName:       "anyscale_service.test",
				ImportState:        true,
				ImportStateId:      "svc_parity_auth_import",
				ImportStatePersist: true,
				Config:             cfg,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if got := states[0].Attributes["query_auth_token_enabled"]; got != "false" {
						return fmt.Errorf("imported query_auth_token_enabled = %q, want false", got)
					}
					return nil
				},
			},
			{
				// rollout_strategy's default is the accepted post-import residual; nothing deploys.
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}
