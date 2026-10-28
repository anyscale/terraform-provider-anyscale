package acctest

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestAccServiceResource_EnvVarsSentAsServiceConfig_MockServer: env_vars goes to config.env_vars
// only (never into an application's runtime_env), a change rolls out, and removing it sends no
// env_vars; switching between null and {} does not deploy.
func TestAccServiceResource_EnvVarsSentAsServiceConfig_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-env"
	m, server := newOutcomeMock(t, "svc_parity_env", name, outcomeRolloutFromV1())
	noRuntimeEnv := func(i int) resource.TestCheckFunc {
		return func(*terraform.State) error {
			rsc, _ := m.applyBody(i)["ray_serve_config"].(map[string]any)
			apps, _ := rsc["applications"].([]any)
			for _, a := range apps {
				if _, ok := a.(map[string]any)["runtime_env"]; ok {
					return fmt.Errorf("apply %d added a runtime_env to an application: %v", i+1, a)
				}
			}
			return nil
		}
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: outcomeConfig(server.URL, name, "main:app", `  env_vars = { RAY_SERVE_LOG_LEVEL = "DEBUG" }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectApplyConfigJSON(m, 0, `{
						"access": {"use_bearer_token": true},
						"protocols": {"grpc": {"enabled": false, "service_names": []}},
						"env_vars": {"RAY_SERVE_LOG_LEVEL": "DEBUG"}
					}`),
					noRuntimeEnv(0),
				),
			},
			{
				Config: outcomeConfig(server.URL, name, "main:app", `  env_vars = { RAY_SERVE_LOG_LEVEL = "INFO" }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 2),
					expectApplyConfigJSON(m, 1, `{
						"access": {"use_bearer_token": true},
						"protocols": {"grpc": {"enabled": false, "service_names": []}},
						"env_vars": {"RAY_SERVE_LOG_LEVEL": "INFO"}
					}`),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: outcomeConfig(server.URL, name, "main:app", `  env_vars = {}`),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 3),
					expectApplyConfigJSON(m, 2, `{
						"access": {"use_bearer_token": true},
						"protocols": {"grpc": {"enabled": false, "service_names": []}}
					}`),
				),
			},
			{
				Config: outcomeConfig(server.URL, name, "main:app", ""),
				Check:  expectOutcomeApplyCount(m, 3),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// TestAccServiceResource_InPlaceRejectsEnvVarsChange: IN_PLACE ignores the service config.
func TestAccServiceResource_InPlaceRejectsEnvVarsChange(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-env-inplace"
	_, server := newOutcomeMock(t, "svc_parity_env_inplace", name, outcomeRolloutFromV1())
	const inPlace = `  rollout_strategy = "IN_PLACE"`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", inPlace)},
			{
				Config: outcomeConfig(server.URL, name, "main:app", inPlace+`
  env_vars = { A = "1" }`),
				ExpectError: regexp.MustCompile(`(?s)IN_PLACE.*env_vars`),
			},
		},
	})
}
