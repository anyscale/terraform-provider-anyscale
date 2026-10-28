package acctest

import (
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// expectApplyConfigJSON asserts apply body i's whole `config` object, compared as canonical JSON.
func expectApplyConfigJSON(m *outcomeMock, i int, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		cfg, err := m.applyConfig(i)
		if err != nil {
			return err
		}
		got, _ := json.Marshal(cfg)
		var wantObj any
		if err := json.Unmarshal([]byte(want), &wantObj); err != nil {
			return fmt.Errorf("bad want JSON: %w", err)
		}
		wantJSON, _ := json.Marshal(wantObj)
		if string(got) != string(wantJSON) {
			return fmt.Errorf("apply %d sent config %s, want %s", i+1, got, wantJSON)
		}
		return nil
	}
}

// TestAccServiceResource_GrpcServiceNamesSent_MockServer: names are sent as an enabled
// config.protocols.grpc; removing them sends gRPC disabled and rolls out; switching between null
// and [] sends the same request, so it does not deploy.
func TestAccServiceResource_GrpcServiceNamesSent_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-grpc"
	m, server := newOutcomeMock(t, "svc_parity_grpc", name, outcomeRolloutFromV1())

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: outcomeConfig(server.URL, name, "main:app", `  grpc_service_names = ["userdefinedprotobuf.UserDefinedService"]`),
				Check: expectApplyConfigJSON(m, 0, `{
					"access": {"use_bearer_token": true},
					"protocols": {"grpc": {"enabled": true, "service_names": ["userdefinedprotobuf.UserDefinedService"]}}
				}`),
			},
			{
				Config: outcomeConfig(server.URL, name, "main:app", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 2),
					expectApplyConfigJSON(m, 1, `{
						"access": {"use_bearer_token": true},
						"protocols": {"grpc": {"enabled": false, "service_names": []}}
					}`),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: outcomeConfig(server.URL, name, "main:app", `  grpc_service_names = []`),
				Check:  expectOutcomeApplyCount(m, 2),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// TestAccServiceResource_GrpcServiceNamesCapValidated: the backend's limit (one name with bearer
// auth, two without) is enforced at plan time.
func TestAccServiceResource_GrpcServiceNamesCapValidated(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-grpc-cap"
	m, server := newOutcomeMock(t, "svc_parity_grpc_cap", name, outcomeRolloutFromV1())
	const two = `  grpc_service_names = ["a.Svc", "b.Svc"]`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      outcomeConfig(server.URL, name, "main:app", two),
				ExpectError: regexp.MustCompile(`(?s)Too Many gRPC Service Names.*at most 1`),
			},
			{
				Config: outcomeConfig(server.URL, name, "main:app", two+`
  query_auth_token_enabled = false`),
				Check: expectOutcomeApplyCount(m, 1),
			},
		},
	})
}

// TestAccServiceResource_InPlaceRejectsGrpcChange: IN_PLACE ignores the service config.
func TestAccServiceResource_InPlaceRejectsGrpcChange(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-grpc-inplace"
	_, server := newOutcomeMock(t, "svc_parity_grpc_inplace", name, outcomeRolloutFromV1())
	const inPlace = `  rollout_strategy = "IN_PLACE"`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: outcomeConfig(server.URL, name, "main:app", inPlace)},
			{
				Config: outcomeConfig(server.URL, name, "main:app", inPlace+`
  grpc_service_names = ["a.Svc"]`),
				ExpectError: regexp.MustCompile(`(?s)IN_PLACE.*grpc_service_names`),
			},
		},
	})
}
