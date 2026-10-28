package acctest

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// serviceInputsAddedForParity are the anyscale_service attributes a provider version before
// them never wrote.
var serviceInputsAddedForParity = []string{
	"query_auth_token_enabled", "grpc_service_names", "env_vars",
	"image_uri", "registry_login_secret", "version_name",
}

// preParityServer simulates a provider version that predates attrs (default
// serviceInputsAddedForParity): every planned, applied and refreshed anyscale_service state
// carries them as null.
type preParityServer struct {
	tfprotov6.ProviderServer
	attrs []string
}

func (s preParityServer) strip(ctx context.Context, dv *tfprotov6.DynamicValue) *tfprotov6.DynamicValue {
	if dv == nil {
		return nil
	}
	schemas, err := s.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		return dv
	}
	typ := schemas.ResourceSchemas["anyscale_service"].ValueType()
	val, err := dv.Unmarshal(typ)
	if err != nil || val.IsNull() || !val.IsKnown() {
		return dv
	}
	attrs := map[string]tftypes.Value{}
	if err := val.As(&attrs); err != nil {
		return dv
	}
	objType := typ.(tftypes.Object)
	names := s.attrs
	if names == nil {
		names = serviceInputsAddedForParity
	}
	for _, name := range names {
		attrs[name] = tftypes.NewValue(objType.AttributeTypes[name], nil)
	}
	out, err := tfprotov6.NewDynamicValue(typ, tftypes.NewValue(typ, attrs))
	if err != nil {
		return dv
	}
	return &out
}

func (s preParityServer) PlanResourceChange(ctx context.Context, req *tfprotov6.PlanResourceChangeRequest) (*tfprotov6.PlanResourceChangeResponse, error) {
	resp, err := s.ProviderServer.PlanResourceChange(ctx, req)
	if resp != nil {
		resp.PlannedState = s.strip(ctx, resp.PlannedState)
	}
	return resp, err
}

func (s preParityServer) ApplyResourceChange(ctx context.Context, req *tfprotov6.ApplyResourceChangeRequest) (*tfprotov6.ApplyResourceChangeResponse, error) {
	resp, err := s.ProviderServer.ApplyResourceChange(ctx, req)
	if resp != nil {
		resp.NewState = s.strip(ctx, resp.NewState)
	}
	return resp, err
}

func (s preParityServer) ReadResource(ctx context.Context, req *tfprotov6.ReadResourceRequest) (*tfprotov6.ReadResourceResponse, error) {
	resp, err := s.ProviderServer.ReadResource(ctx, req)
	if resp != nil {
		resp.NewState = s.strip(ctx, resp.NewState)
	}
	return resp, err
}

// TestAccServiceResource_PreParityStateRefreshesClean_MockServer (Gate 2): state written before
// these attributes existed refreshes to populated values with an empty plan and no deploy.
func TestAccServiceResource_PreParityStateRefreshesClean_MockServer(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const name = "parity-transition"
	m, server := newOutcomeMock(t, "svc_parity_transition", name, outcomeRolloutFromV1())
	cfg := outcomeConfig(server.URL, name, "main:app", "")
	old := map[string]func() (tfprotov6.ProviderServer, error){
		"anyscale": func() (tfprotov6.ProviderServer, error) {
			s, err := ProtoV6ProviderFactories["anyscale"]()
			return preParityServer{ProviderServer: s}, err
		},
	}

	resource.Test(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ProtoV6ProviderFactories: old,
				Config:                   cfg,
				Check:                    resource.TestCheckNoResourceAttr("anyscale_service.test", "version_name"),
				// The simulation is not a whole old provider: the current one still plans the
				// query_auth_token_enabled default against the stripped null.
				ExpectNonEmptyPlan: true,
			},
			{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Config:                   cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					expectOutcomeApplyCount(m, 1),
					resource.TestCheckResourceAttr("anyscale_service.test", "query_auth_token_enabled", "true"),
					resource.TestCheckResourceAttr("anyscale_service.test", "version_name", "v1"),
				),
			},
		},
	})
}
