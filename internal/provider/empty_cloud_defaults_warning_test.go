package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

const emptyCloudDefaultsSummary = "Empty Cloud Uses Placeholder Settings"

// emptyCloudConfig is an empty cloud's config with cloud_provider, region, credentials and
// compute_stack all set; each case nulls what it leaves unset.
func emptyCloudConfig(t *testing.T) CloudResourceModel {
	t.Helper()
	c := preflightPlan(t, "tfacc-empty", "GCP", "VM", "us-central1")
	c.Credentials = types.StringValue(`{"type":"service_account"}`)
	c.ID = types.StringNull()
	c.IsEmptyCloud = types.BoolNull()
	c.CloudResourceID = types.StringNull()
	return c
}

func TestWarnEmptyCloudDefaults(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CloudResourceModel)
		want   []string // substrings of the warning; nil = no warning
	}{
		{"all set", func(*CloudResourceModel) {}, nil},
		{"provider unset", func(c *CloudResourceModel) { c.CloudProvider = types.StringNull() },
			[]string{"cloud_provider (defaults to AWS)"}},
		{"region unset", func(c *CloudResourceModel) { c.Region = types.StringNull() },
			[]string{"region (defaults to us-east-1)"}},
		{"credentials unset", func(c *CloudResourceModel) { c.Credentials = types.StringNull() },
			[]string{"credentials (a placeholder role is used)"}},
		{"all unset, compute_stack unset", func(c *CloudResourceModel) {
			c.CloudProvider, c.Region, c.Credentials, c.ComputeStack = types.StringNull(), types.StringNull(), types.StringNull(), types.StringNull()
		}, []string{"cloud_provider (defaults to AWS), region (defaults to us-east-1), credentials (a placeholder role is used)", "Services on this cloud cannot create load balancers"}},
		{"unknown values count as set", func(c *CloudResourceModel) {
			c.CloudProvider, c.Region, c.Credentials = types.StringUnknown(), types.StringUnknown(), types.StringUnknown()
		}, nil},
		{"K8S stack is not warned", func(c *CloudResourceModel) {
			c.CloudProvider, c.ComputeStack = types.StringNull(), types.StringValue("K8S")
		}, nil},
		{"unknown compute_stack is not warned", func(c *CloudResourceModel) {
			c.CloudProvider, c.ComputeStack = types.StringNull(), types.StringUnknown()
		}, nil},
		{"embedded config is not warned", func(c *CloudResourceModel) {
			c.CloudProvider = types.StringNull()
			c.AWSConfig = awsConfigWithExternalID(t, "org_abcd-1234")
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := emptyCloudConfig(t)
			tc.mutate(&c)
			diags := warnEmptyCloudDefaults(&c)
			if diags.HasError() {
				t.Fatalf("want no error, got %v", diags)
			}
			if tc.want == nil {
				if diagsContainSummary(diags, emptyCloudDefaultsSummary) {
					t.Fatalf("want no warning, got %v", diags)
				}
				return
			}
			if !diagsContainSummary(diags, emptyCloudDefaultsSummary) {
				t.Fatalf("want the warning, got %v", diags)
			}
			for _, w := range tc.want {
				if !strings.Contains(diags[0].Detail(), w) {
					t.Errorf("warning %q does not contain %q", diags[0].Detail(), w)
				}
			}
		})
	}
}

// ModifyPlan itself must surface the warning on create and stay silent on update.
func TestCloudResourceModifyPlan_EmptyCloudDefaultsWarning(t *testing.T) {
	ctx := context.Background()
	r := &CloudResource{}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)

	run := func(t *testing.T, create bool) diag.Diagnostics {
		c := emptyCloudConfig(t)
		c.CloudProvider, c.Region, c.Credentials = types.StringNull(), types.StringNull(), types.StringNull()
		cfg := tfsdk.Plan{Schema: schemaResp.Schema}
		if d := cfg.Set(ctx, &c); d.HasError() {
			t.Fatalf("config fixture: %v", d)
		}
		state := tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)}
		if !create {
			c.ID = types.StringValue("cld_existing")
			st := tfsdk.Plan{Schema: schemaResp.Schema}
			if d := st.Set(ctx, &c); d.HasError() {
				t.Fatalf("state fixture: %v", d)
			}
			state.Raw = st.Raw
		}
		req := resource.ModifyPlanRequest{
			Plan:   cfg,
			Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: cfg.Raw},
			State:  state,
		}
		resp := &resource.ModifyPlanResponse{Plan: cfg}
		r.ModifyPlan(ctx, req, resp)
		return resp.Diagnostics
	}

	if diags := run(t, true); !diagsContainSummary(diags, emptyCloudDefaultsSummary) {
		t.Fatalf("create plan: want the warning, got %v", diags)
	}
	if diags := run(t, false); diagsContainSummary(diags, emptyCloudDefaultsSummary) {
		t.Fatalf("update plan: want no warning, got %v", diags)
	}
}
